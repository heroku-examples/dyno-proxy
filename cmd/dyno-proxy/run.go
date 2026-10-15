package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/heroku-examples/dyno-proxy/internal/engine"
)

// runArgs is the parsed form of the `run` subcommand arguments.
type runArgs struct {
	ProjectPath string
	AppCommand  []string
}

// parseRunArgs parses `run [--filename <path>|-f <path>|--filename=<path>] -- <web command...>`.
// Everything after the "--" separator is the web command argv; a bare first
// token with no separator starts the web command for ergonomics (`run <cmd...>`).
func parseRunArgs(args []string) (runArgs, error) {
	var out runArgs

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			out.AppCommand = args[i+1:]
			i = len(args)
		case a == "--filename" || a == "-f":
			if i+1 >= len(args) {
				return runArgs{}, fmt.Errorf("run: %s requires a value", a)
			}
			out.ProjectPath = args[i+1]
			i += 2
		case strings.HasPrefix(a, "--filename="):
			out.ProjectPath = strings.TrimPrefix(a, "--filename=")
			i++
		default:
			// First bare token with no "--" separator: treat the rest as the
			// web command for ergonomics (`run <cmd...>`).
			out.AppCommand = args[i:]
			i = len(args)
		}
	}

	if len(out.AppCommand) == 0 {
		return runArgs{}, fmt.Errorf("run: no web command provided (usage: dyno-proxy run -- <command...>)")
	}
	return out, nil
}

// proxyDisabled reports whether the runtime opt-out switch is engaged. The
// proxy is ON by default; ONLY DISABLE_DYNO_PROXY set to exactly
// true, TRUE or 1 disables it. This mirrors
// the opt-out gates in buildpack/bin/build and buildpack/bin/compile; the
// comparison is intentionally strict (no case-folding / trimming) so the
// polarity is unambiguous and cannot silently drift.
func proxyDisabled(getenv func(string) string) bool {
	switch getenv("DISABLE_DYNO_PROXY") {
	case "true", "TRUE", "1":
		return true
	}
	return false
}

// runCmd parses the `run` subcommand arguments and drives the engine.
func runCmd(args []string) error {
	parsed, err := parseRunArgs(args)
	if err != nil {
		return err
	}

	// The buildpack (CNB exec.d hook or classic .profile.d script) has already
	// started the LB on the router port via `background` (which ran the startup
	// hook) and pointed PORT at APP_PORT. Become the app in place so there is no
	// second LB.
	if os.Getenv("DYNO_PROXY_FRONTED") != "" {
		return execApp(parsed.AppCommand, "already fronted by the buildpack")
	}

	// Run the optional startup hook synchronously before anything else. If a
	// `dyno-proxy-start-hook` executable sits alongside the running dyno-proxy
	// binary, we run it directly (any executable, not just a shell script) and
	// block until it completes, then continue.
	if err := runStartupHook(); err != nil {
		return err
	}

	// Runtime on/off switch. The proxy is ON by default; set
	// DISABLE_DYNO_PROXY=true|TRUE|1 to opt out. When disabled we
	// transparently become the child in-place: exec the web command with the
	// current environment untouched (PORT stays the real router port), which is
	// equivalent to running the app without dyno-proxy — no traefik, no
	// supervision.
	if proxyDisabled(os.Getenv) {
		return execApp(parsed.AppCommand, "DISABLE_DYNO_PROXY set")
	}

	return engine.Run(context.Background(), engine.Config{
		AppCommand:  parsed.AppCommand,
		ProjectPath: parsed.ProjectPath,
	})
}

// execApp replaces this process with argv and the current environment
// untouched. syscall.Exec only returns on error.
func execApp(argv []string, why string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("run: %s, cannot exec %q: %w", why, argv[0], err)
	}
	return syscall.Exec(path, argv, os.Environ())
}
