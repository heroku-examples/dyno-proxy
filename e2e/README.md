# Heroku end-to-end tests

`run.sh` deploys the dyno-proxy buildpack to real Heroku apps and checks, from
outside, that the proxy is on or off as expected and that the dyno recovers from
crashes. `app/` is the small Rack/Puma app it deploys.

> **Warning:** it force-pushes to the target apps, changes their buildpacks and
> config vars, clears their build cache, and scales and crashes their dynos. Only
> point it at apps dedicated to this test.

Needs the Heroku CLI (`heroku login`), `jq`, `git`, `curl`.

## Quick start

```bash
e2e/run.sh my-test-app                      # every variant and state on one app
e2e/run.sh my-cnb-app my-classic-app        # several apps, in parallel
```

To test your own build of the buildpack (for example a branch), point the
buildpack variables at it. For CNB apps (`CNB_BUILDPACK_URL`, written to the
app's `project.toml`) and classic apps (`CLASSIC_BUILDPACK_URL`):

```bash
CNB_BUILDPACK_URL=docker://ghcr.io/heroku-examples/dyno-proxy-buildpack:<tag> \
CLASSIC_BUILDPACK_URL=https://github.com/heroku-examples/dyno-proxy/releases/latest/download/dyno-proxy-buildpack.tgz \
  e2e/run.sh my-test-app
```

## What it tests

Each app gets `app/` deployed in 4 variants, with the build cache
cleared before every deploy:

- the dyno-proxy buildpack **first** or **last** relative to `heroku/ruby`
- the app's Procfile **present** or **absent**

Each variant runs the states in `STATES` (extra states only run once `default`
passed):

| State | Setup | Pass condition |
| --- | --- | --- |
| `default` | no config vars | proxy active |
| `disable` | `DISABLE_DYNO_PROXY=true` | proxy inactive |
| `dyno-proxy-crash` | `GET /crash/dyno-proxy` | dyno restarts, proxy active again |
| `app-crash` | `GET /crash/app` | dyno restarts, proxy active again |

Apps run in parallel; variants and states within an app run in sequence.

**Proxy active?** The generated `project.toml` routes
`/anything/dyno-proxy-e2e*` to httpbin.org. Through the proxy that returns
httpbin's JSON echo; without it the app answers 404
`dyno-proxy-e2e-app: not found`. `GET /` must return the app page.

**Restarted?** `GET /boot` returns a boot ID that is new per process start. A
crash cell reads it, triggers the crash, then polls until it changes and the
proxy is active again. Each crash cell starts with `ps:scale web=0` then
`web=1`, because the Common Runtime's crash cool-off is only reset by scaling.

## Running it

```bash
STATES="default disable" e2e/run.sh my-test-app
ORDERS=last PROCFILES=absent STATES="default dyno-proxy-crash" e2e/run.sh my-test-app
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `CNB_BUILDPACK_URL` | `docker://ghcr.io/heroku-examples/dyno-proxy-buildpack` | buildpack for CNB apps |
| `CLASSIC_BUILDPACK_URL` | `https://github.com/heroku-examples/dyno-proxy/releases/latest/download/dyno-proxy-buildpack.tgz` | buildpack for classic apps |
| `APP_DIR` | `e2e/app` | app to deploy |
| `ORDERS` | `first last` | buildpack positions |
| `PROCFILES` | `present absent` | Procfile variants |
| `STATES` | `default disable dyno-proxy-crash app-crash` | states to test |
| `TIMEOUT` | `90` | seconds to wait for a release or crash recovery |
| `WORK_DIR` | `tmp/e2e` | logs and response bodies |

Ctrl+C stops the whole run.

## Results

One `PASS`/`FAIL` line per cell (`<order>/<procfile> <state>: <observed>`) and a
final count; the exit status is non-zero if any cell failed. Logs are in
`$WORK_DIR/<timestamp>/<app>/<order>-<procfile>/` (`build.log`,
`clear-cache.log`, response bodies) and `$WORK_DIR/<timestamp>/<app>.log`. Each
app is left on its last variant with the proxy config vars unset.

## The test app

| Endpoint | Behaviour |
| --- | --- |
| `GET /` | 200 with `dyno-proxy e2e app` and the boot ID |
| `GET /boot` | JSON `{boot_id, pid, started_at, dyno, port}` |
| `GET /crash/app` | 202, then the server hard-exits (`exit!` 137) after 0.5s |
| `GET /crash/dyno-proxy` | 202, then SIGKILLs the dyno-proxy PIDs found in `/proc` (409 if none) |
| anything else | 404 `dyno-proxy-e2e-app: not found` |
