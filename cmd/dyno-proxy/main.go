// Command dyno-proxy is the supervising parent of a Heroku dyno's process tree.
//
// Control is inverted relative to the legacy design: instead of the platform
// exec-ing into the web command with dyno-proxy backgrounded, the launcher execs
// into dyno-proxy, which renders the load-balancer config, then launches BOTH the
// load balancer and the web command as its own children and supervises them (see
// internal/supervisor). This is a single real supervising parent, always present.
//
// Primary contract:
//
//	dyno-proxy run -- <web command...>
//
// The buildpack never rewrites the web command; `run` is for local experiments
// and testing (e.g. `dyno-proxy run -- rails s`) or a Procfile the user writes
// as `web: dyno-proxy run -- <cmd>`.
//
// Buildpack contract (classic .profile.d script and CNB exec.d hook, on every
// web launch):
//
//	dyno-proxy background [--app-pid <pid>]
//
// sourced from .profile.d (or run from exec.d): it starts a detached dyno-proxy
// that runs only the load balancer on $PORT and returns immediately; the caller
// then points PORT at APP_PORT so the web command binds the app port behind it.
//
// The subcommands live in run.go, background.go and hook.go (the optional
// startup hook); usage.go holds the help text.
package main

import (
	"fmt"
	"os"

	"github.com/heroku-examples/dyno-proxy/internal/engine"
)

// Version is set via ldflags at build time.
var Version = "dev"

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("dyno-proxy version %s\n", Version)
	case "run":
		exitOnError(runCmd(args[1:]))
	case "background", "--background":
		exitOnError(backgroundCmd(args[1:]))
	case "sentinel": // internal: started by the background daemon
		exitOnError(sentinelCmd(args[1:]))
	case "config":
		exitOnError(configCmd(args[1:]))
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "dyno-proxy: unknown command %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

// exitOnError prints err and exits 1; it does nothing for a nil error.
func exitOnError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "dyno-proxy: %v\n", err)
		os.Exit(1)
	}
}

// configCmd renders the load-balancer config and prints it to stdout instead of
// starting anything. No web command is required; without a project.toml the
// config is rendered from the environment / defaults alone.
func configCmd(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("config: unexpected argument %q (usage: dyno-proxy config)", args[0])
	}
	return engine.DumpConfig(engine.Config{}, os.Stdout)
}
