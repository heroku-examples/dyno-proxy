package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/heroku-examples/dyno-proxy/internal/engine"
)

// backgroundArgs is the parsed form of the `background` subcommand arguments.
type backgroundArgs struct {
	// Foreground runs the proxy in the current process instead of spawning a
	// detached copy. The launcher passes it to the copy it spawns.
	Foreground bool
	// AppPID is the dyno's main process (the one that runs the web command;
	// 0 = unknown). When set, the daemon SIGTERMs (then SIGKILLs) it if the
	// load balancer dies, so the platform restarts the dyno, and stops the load
	// balancer when it dies.
	AppPID int
}

// parseBackgroundArgs parses
// `background [--app-pid <pid>] [--foreground]`.
func parseBackgroundArgs(args []string) (backgroundArgs, error) {
	var out backgroundArgs

	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--foreground":
			out.Foreground = true
			i++
		case a == "--app-pid" || strings.HasPrefix(a, "--app-pid="):
			v, ok := strings.CutPrefix(a, "--app-pid=")
			if !ok {
				if i+1 >= len(args) {
					return backgroundArgs{}, fmt.Errorf("background: %s requires a value", a)
				}
				v = args[i+1]
				i++
			}
			pid, err := strconv.Atoi(v)
			if err != nil || pid <= 0 {
				return backgroundArgs{}, fmt.Errorf("background: --app-pid must be a positive pid, got %q", v)
			}
			out.AppPID = pid
			i++
		default:
			return backgroundArgs{}, fmt.Errorf("background: unexpected argument %q (usage: dyno-proxy background [--app-pid <pid>])", a)
		}
	}
	return out, nil
}

// daemonArgs is the argv (after the executable) for the detached copy the
// launcher spawns: the same subcommand in --foreground mode.
func daemonArgs(parsed backgroundArgs) []string {
	out := []string{"background", "--foreground"}
	if parsed.AppPID > 0 {
		out = append(out, "--app-pid", strconv.Itoa(parsed.AppPID))
	}
	return out
}

// backgroundCmd is how the classic buildpack starts dyno-proxy on every web
// launch. It is called from a SOURCED .profile.d script, so the launcher must
// return immediately: it spawns a copy of itself (--foreground) in a new
// session with stdin from /dev/null and stdout/stderr inherited (so logs still
// reach the dyno log stream), then exits 0.
//
// The detached copy runs the normal engine with no app child: the load balancer
// binds $PORT and routes to $APP_PORT, derived exactly as `run` does (the first
// free port above PORT, normally PORT+1, unless APP_PORT is set; the .profile.d
// script and the exec.d hook export it before calling us so both sides agree).
//
// The daemon is not the dyno's main process, so it cannot make the platform
// restart the dyno by exiting. With --app-pid (the main process that runs the
// web command) it ties the two together: if the load balancer dies it SIGTERMs
// that pid (SIGKILL after a grace period) so the dyno exits and is restarted,
// and once that pid is gone it stops the load balancer and exits. Without
// --app-pid it falls back to stopping the load balancer once the app on
// APP_PORT has come up and gone away. SIGTERM/SIGINT stop the load balancer.
//
// The optional dyno-proxy-start-hook runs in the launcher, before the daemon is
// spawned, never in the --foreground copy.
//
// The CNB exec.d hook (buildpack/bin/build) calls this too, for every web
// launch. There the caller must not hand us the exec.d stdout/stderr capture
// pipes (the launcher waits for their EOF); any other inherited fd >= 3 is
// marked close-on-exec before the daemon is spawned.
//
// DISABLE_DYNO_PROXY=true makes this a no-op.
func backgroundCmd(args []string) error {
	parsed, err := parseBackgroundArgs(args)
	if err != nil {
		return err
	}
	if proxyDisabled(os.Getenv) {
		return nil
	}

	if !parsed.Foreground {
		// Run the optional startup hook here, synchronously, before spawning the
		// daemon (same contract as `run`): the default web command must not start
		// until it finishes, and a failing hook returns non-zero so the sourcing
		// script leaves PORT unchanged.
		if err := runStartupHook(); err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("background: locating dyno-proxy executable: %w", err)
		}
		// Don't leak inherited descriptors (e.g. the CNB exec.d fd 3 pipe) into
		// the long-lived daemon: the launcher would block on their EOF.
		closeExtraFDsOnExec()
		cmd := exec.Command(exe, daemonArgs(parsed)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Stdin stays nil (/dev/null). Setsid detaches from the sourcing shell's
		// session and process group; deliberately no Pdeathsig, since this
		// launcher exits right away.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("background: starting detached dyno-proxy: %w", err)
		}
		// Succeed only once the LB serves $PORT: the caller moves the app off
		// $PORT on success, so a daemon that dies first (bad load balancer config or
		// project.toml, render error) must fail here and leave the app on $PORT.
		if err := waitDaemonReady(cmd, os.Getenv("PORT"), backgroundReadyTimeout); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "dyno-proxy: started background proxy (pid %d)\n", cmd.Process.Pid)
		return nil
	}

	return engine.Run(context.Background(), engine.Config{
		Background: true,
		AppPID:     parsed.AppPID,
	})
}

// backgroundReadyTimeout bounds how long `background` waits for the detached
// LB to accept connections on $PORT.
const backgroundReadyTimeout = 30 * time.Second

// waitDaemonReady waits until something accepts connections on port, or fails
// if the started daemon cmd exits first or the timeout passes (the daemon, its
// own session leader, is then killed with its process group so it cannot take
// $PORT later). An empty port skips the wait.
func waitDaemonReady(cmd *exec.Cmd, port string, timeout time.Duration) error {
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if port == "" {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		if conn, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", port), 200*time.Millisecond); err == nil {
			conn.Close()
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("background: proxy exited before serving port %s: %v", port, err)
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return fmt.Errorf("background: proxy not serving port %s within %v", port, timeout)
		}
	}
}

// closeExtraFDsOnExec marks every descriptor >= 3 close-on-exec so a spawned
// child gets only stdin/stdout/stderr (plus any ExtraFiles). Go opens its own
// files close-on-exec already; this catches descriptors inherited from our
// parent, such as the CNB launcher's exec.d fd 3 pipe. Errors (unused fds) are
// ignored.
func closeExtraFDsOnExec() {
	for fd := 3; fd < 1024; fd++ {
		syscall.CloseOnExec(fd)
	}
}
