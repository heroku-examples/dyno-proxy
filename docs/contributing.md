# Contributing

Requires Go (version pinned in `go.mod`). `traefik` on `PATH` is optional; without it the live-traffic conformance tests skip.

## Run locally

```bash
go run ./cmd/dyno-proxy run -- ruby bin/test-app.rb      # supervise a web command
go run ./cmd/dyno-proxy run -f other.toml -- <cmd>        # use another project.toml-format file
go run ./cmd/dyno-proxy config                            # print the rendered config and exit
DYNO_PROXY_LOG_LEVEL=DEBUG go run ./cmd/dyno-proxy run -- sh -c 'sleep 3600'
```

`run` needs the `--` separator before the web command. It writes the rendered
`*.dyno-proxy-static.toml` and `*.dyno-proxy-dynamic.toml` files next to
`project.toml`, so you can inspect the routing it generated. `config` prints the
same files to stdout and needs no web command or `project.toml`.

## Test

```bash
make test        # plugin tests + go test ./... (what CI runs)
make test-e2e    # only the conformance and golden render tests
make golden      # regenerate golden files after an intentional rendering change
go test ./internal/config/...   # a single package
```

CI (`.github/workflows/test.yml`) runs `make test` with the Traefik version from
`internal/traefik/traefik.go` installed so the live-traffic tests run. Tests that
need a missing tool (`traefik`, `bash`, `awk`, `tar`) skip rather than pass
silently; use `-v` to see why.

| Package | Covers |
| --- | --- |
| `internal/config` | `project.toml` parsing into the route model: percentage routing, `set`, `catch`/`body` plugins, middleware, `$VAR` expansion in `to` |
| `internal/traefik` | router rules from matchers, log and access-log format |
| `internal/conformance` | the routing contract: each scenario is a route model plus a request and the upstream it must reach, rendered to config and (if `traefik` is on `PATH`) run against real Traefik; plus byte-for-byte golden render files in `testdata/golden/` |
| `internal/engine` | default `APP_PORT`, plugin linking into `plugins-local`, `dyno-proxy config` output, logger |
| `internal/supervisor` | the process lifecycle with real child processes: either child exiting stops the other, signals and reloads, the LB readiness gate, and the restart contract for `run` and `background` (`--app-pid`) |
| `cmd/dyno-proxy` | argument parsing, the `DISABLE_DYNO_PROXY` switch, `run` execing directly when disabled or already fronted, the start hook, `background` readiness |
| `buildpack` | the real `bin/build` (CNB `exec.d`) and `bin/compile` (classic `.profile.d`) scripts against temp apps: Procfile untouched, plugin bundling, web-dyno matching, the off switch, `APP_PORT` selection, `PORT` left alone on failure |

The buildpack tests need a binary: `buildpack/bin/dyno-proxy-linux-amd64` for
`build_test.go`, and `dyno-proxy-<os>-<arch>` for this machine for
`compile_test.go` (`make build`).

### Plugin tests

Each plugin under `internal/plugins/` (`body`, `catch`) is its own Go module, so
`go test ./...` from the root skips them. `make test-plugins` runs each one, and
`make test` includes it.

### Heroku end-to-end matrix

`e2e/run.sh` deploys the buildpack to real Heroku apps and checks, from outside,
that the proxy is on or off as expected and that the dyno recovers from crashes.
It force-pushes to and crashes the target apps, so only point it at dedicated
test apps. It is not run in CI. See [e2e/README.md](../e2e/README.md).

## Build

```bash
make build       # bin/dyno-proxy-<os>-<arch> for this machine
make build-all   # darwin amd64/arm64, linux amd64/arm64/arm
```
