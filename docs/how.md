# How dyno-proxy works

Dyno Proxy does two things:

- generates load-balancer (Traefik) configuration from `project.toml`
- runs the load balancer in front of your web command: on Heroku as a background load balancer tied to the dyno's main process, or with `dyno-proxy run` as a single supervised process tree

## On Heroku: autorun

Add the buildpack (CNB: `project.toml`; classic: `heroku buildpacks:add`) and
optionally configure `[[routes]]`. That's all. The proxy is **on by default**,
the `Procfile` is never read or modified, buildpack order doesn't matter, and it
works with or without a Procfile.

A hook the platform runs before **every** process does this on a web dyno:

1. derives `APP_PORT`,
2. runs `dyno-proxy background --app-pid <main pid>`, which runs the optional
   `dyno-proxy-start-hook`, starts a detached dyno-proxy running only the load
   balancer on the router `$PORT` (routing to `APP_PORT`), and returns once it
   is serving,
3. only if that succeeded, sets `PORT=$APP_PORT`, `APP_PORT` and
   `DYNO_PROXY_FRONTED=1` for the web command.

The web command binds `$PORT` as usual, which is now the app port behind the
load balancer. It is never changed.

| | CNB (`buildpack/bin/build`) | Classic (`buildpack/bin/compile`) |
| --- | --- | --- |
| Hook | `exec.d/dyno-proxy` in the launch layer | `.profile.d/dyno-proxy-defaults.sh` |
| Run by | the CNB launcher, as a child, before each process | the dyno's shell, **sourced** before the command |
| Env handoff | TOML assignments on fd 3 | `export` in the sourcing shell |
| `--app-pid` | `$PPID` (the launcher, which then execs the web command) | `$$` (the sourcing shell) |

Details that apply to both:

- **Web dynos only.** An exact match on `$DYNO`: `web.<n>` or `web-<hash>-<id>`
  (Fir). `worker.1`, `webhooks.1`, `web-api.1` and `run.N` are left alone.
- **`APP_PORT`** defaults to the first port above `PORT` with no listener
  (checked on `127.0.0.1` and `::1`, up to `PORT+20`), normally `PORT+1`. On
  Fir the bind shim holds `PORT+1`/`PORT+2`, so the app gets `PORT+3`. `run` uses
  the same rule (`engine.defaultAppPort`).
- **Fallback.** If `background` fails (bad config, failing start hook, or no
  listener on `$PORT` within 30s), `PORT` is left unchanged and the app binds the
  router port directly: unproxied but reachable.
- **`DYNO_PROXY_FRONTED=1`** marks that a load balancer is already on the router
  port. Both hooks skip starting another, and `dyno-proxy run` execs the app in
  place, so an explicit `web: dyno-proxy run -- ...` Procfile line never starts a
  second one.
- **CNB fd rules.** The launcher waits for EOF on every copy of the hook's fd 1,
  2 and 3, so the daemon inherits none of them: stdin is `/dev/null`, fd 3 is
  closed, and output goes to `/proc/$PPID/fd/1,2` so logs still reach the dyno log
  stream. The hook always exits 0, since a failing exec.d aborts launch.
- **Classic** `.profile.d` is sourced, so it only sets env and returns, never
  execs or blocks, and must not leak helper variables into the shell.

### The dyno restarts if either process dies

Heroku restarts a dyno when its main process exits, but the daemon is not the
main process. So `--app-pid` tells it which pid is:

- If the load balancer dies (including a reload that can't relaunch it), the
  daemon SIGTERMs that pid, then SIGKILLs it after 10s, and the dyno restarts.
  If the pid is a PID 1 with no SIGTERM handler (Fir), neither signal can reach it,
  so the daemon relaunches the load balancer in place instead.
- If the pid exits (the app died), the daemon stops the load balancer and exits.
  The pid is what counts, not the port, so a brief listener gap is harmless.
- If the daemon itself is killed, a small `dyno-proxy sentinel` process the
  daemon started (its own session, restarted by the daemon if it dies) sees it
  is gone and terminates that pid the same way. The load balancer dies with the
  daemon, so nothing would otherwise restart the dyno.

### On/off switch

The hook is installed unconditionally (config vars aren't reliably present at CNB
build time) and the switch is read at **runtime**, by the hook and the binary.
Set `DISABLE_DYNO_PROXY` to exactly `true`, `TRUE` or `1` to disable it: the hook
starts nothing, and `run` `syscall.Exec`s straight into the web command with the
environment untouched (`PORT` stays the real router port). The result is
equivalent to running without dyno-proxy. No rebuild is needed.

```bash
heroku config:set DISABLE_DYNO_PROXY=true -a <my-app>
```

### Verified behavior

Checked by the e2e matrix ([contributing.md](./contributing.md#heroku-end-to-end-matrix)) on Common Runtime, Cedar
Private Spaces and Fir, classic and CNB, buildpack first or last, Procfile
present or absent:

| Scenario | Result |
| --- | --- |
| default | Proxy active: traefik on the real `$PORT`, app on `APP_PORT` |
| `DISABLE_DYNO_PROXY=true` | Passthrough: no traefik, app on the real `$PORT` |
| dyno-proxy killed | Dyno restarts and the proxy is active again |
| app crashes | Dyno restarts and the proxy is active again |

## `dyno-proxy run`: the supervised process tree

For local experiments, tests or an explicit Procfile line, `run` makes
dyno-proxy the supervising parent. It renders the config, then launches **both**
the load balancer and the web command as its own children. Implementation:
[internal/supervisor](../internal/supervisor).

```bash
dyno-proxy run [--filename <path>] -- <web command...>
# e.g.
dyno-proxy run -- rails s
```

1. **Ports.** `PORT` is the entrypoint the load balancer binds (default `8888`).
   `APP_PORT` is where your web command listens (derived as above); both are set
   in the child's environment.
2. **Startup.** The load balancer starts first; the supervisor waits for it to
   accept a TCP connection on `PORT` (a bounded readiness probe, no fixed sleep),
   then starts the web command.
3. **Lifecycle loop.** A single loop selects over the load balancer exiting, the
   web command exiting, `SIGINT`/`SIGTERM`, a reload trigger and context
   cancellation.
   - Load balancer exits: both children are torn down and `run` returns an error.
   - Web command exits: both are torn down and `run` returns the app's exit
     status, giving normal dyno restart behavior.
   - Signal: graceful shutdown of both.
   - Reload: the load balancer is hot-reloaded without stopping the app.
4. **Graceful shutdown.** The app gets `SIGTERM` first (it holds client
   connections), within a budget kept under 30s so the platform `SIGKILL` never
   beats it; then the load balancer. Anything overrunning its budget is
   `SIGKILL`ed. On Linux the supervisor is a subreaper, so orphaned grandchildren
   are reaped.

### Live config reload

If a `project.toml` is present, `dyno-proxy` watches it. On change it re-renders
the config and reloads the load balancer, with no restart of the dyno or the web
command.

## Monitoring and troubleshooting

### Traefik dashboard and metrics

The Traefik dashboard (`/.dyno-proxy/traefik`) and Prometheus metrics
(`/.dyno-proxy/metrics`) are **off by default**. Set both variables to serve them,
protected by HTTP Basic Authentication:

```bash
heroku config:set DYNO_PROXY_ADMIN_USER=your-username DYNO_PROXY_ADMIN_PASSWORD=your-password -a <my-app>
```

### Logging

`DYNO_PROXY_LOG_LEVEL` (`TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`; default
`ERROR`) controls dyno-proxy's own logs, Traefik's log level and access-log
verbosity (`DYNO_PROXY_LOG_FORMAT` picks `key=value` or `json`):

| Level | Access log | Traefik internal logs |
| --- | --- | --- |
| `DEBUG` | Everything, all headers as-is except sensitive ones (`Authorization`, `X-API-Key`) | DEBUG (shows startup messages) |
| `INFO` | RequestPath, RequestHost, RequestMethod, RequestURI, ResponseCode, ResponseSize, ServiceURL, Duration; keeps only `X-Request-ID` and `Host` headers | WARN |
| `WARN`/`ERROR` | RequestPath, RequestMethod, ResponseCode, Duration; no headers | ERROR (critical only) |

### Crashes

When the load balancer or the app dies, the dyno crashes as a unit and the
platform restarts it (see [the dyno restarts if either process dies](#the-dyno-restarts-if-either-process-dies)):

```
heroku-runtime[web]: Dyno web for app <id> - status: crashed
heroku-runtime[web]: Dyno web for app <id> - status: starting
```

### Load balancer not ready

If `background` never sees the load balancer serving `PORT`, the app binds the
router port directly (under `run`, the supervisor logs a warning after the
timeout). Check that:

1. `traefik` is available in `PATH`.
2. `PORT` is not already in use.
3. The rendered config is valid: run with `DYNO_PROXY_LOG_LEVEL=DEBUG`, or use `dyno-proxy config`.

### App not reachable

1. Your app must bind `APP_PORT` (the first free port above `PORT`, normally `PORT + 1`). Read it from `$PORT` in your web command.
2. It must accept TCP connections on that port, not just HTTP.
