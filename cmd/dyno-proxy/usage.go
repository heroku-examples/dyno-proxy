package main

import (
	"fmt"
	"os"
)

// usageText is the help text; %[1]s is the program name.
const usageText = `Usage: %[1]s run [--filename <path>] -- <web command...>
       %[1]s background [--app-pid <pid>]
       %[1]s config
       %[1]s version

dyno-proxy supervises the load balancer and the given web command as its
children, rendering the LB config from project.toml.

The 'background' subcommand starts a detached dyno-proxy that runs only the LB
on $PORT, routing to $APP_PORT (default: first free port above PORT), and returns immediately. It is
for the buildpack's .profile.d / exec.d hooks; the caller must then set
PORT=$APP_PORT for the web command. --app-pid names the dyno's main process: it
is terminated if the LB dies (so the dyno restarts), and the LB stops once it
exits. Without it the LB stops once the app on APP_PORT goes away.
--foreground runs it in the current process instead (used internally). The
daemon also starts an internal 'sentinel' process that terminates --app-pid if
the daemon itself is killed.

The 'config' subcommand renders the LB config and prints it to stdout, then
exits without starting anything (no web command required).

Environment:
  DISABLE_DYNO_PROXY      Runtime on/off switch. On by default; set to true, TRUE
                          or 1 to exec the web command directly (no proxy, no
                          supervision); 'background' becomes a no-op
  DYNO_PROXY_LOG_LEVEL    Log level (TRACE, DEBUG, INFO, WARN, ERROR)
  DYNO_PROXY_LOG_FORMAT   Log format: key=value (default) or json

Examples:
  %[1]s run -- ruby bin/test-app.rb
  %[1]s config
`

func usage() {
	fmt.Fprintf(os.Stderr, usageText, os.Args[0])
}
