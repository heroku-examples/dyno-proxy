package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/heroku-examples/dyno-proxy/internal/engine"
	"github.com/heroku-examples/dyno-proxy/internal/supervisor"
)

// sentinelCmd is the internal `sentinel` subcommand, started by the background
// daemon. It terminates --app-pid (the dyno's main process) if --daemon-pid
// disappears, so killing the daemon outright still restarts the dyno. It exits
// quietly once the app is gone.
func sentinelCmd(args []string) error {
	fs := flag.NewFlagSet("sentinel", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	daemonPID := fs.Int("daemon-pid", 0, "")
	appPID := fs.Int("app-pid", 0, "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("sentinel: %w", err)
	}
	if *daemonPID <= 0 || *appPID <= 0 {
		return fmt.Errorf("sentinel: --daemon-pid and --app-pid are required")
	}
	return supervisor.RunSentinel(*daemonPID, *appPID, 0, 0, engine.EnvLogger())
}
