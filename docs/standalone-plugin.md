# Standalone plugin buildpacks

A plugin can be its own repo, packaged
as a buildpack that installs dyno-proxy as a dependency, ships the plugin, and
configures the proxy for the app when the dyno boots.

For instance, [heroku-examples/xp](https://github.com/heroku-examples/xp),
whose startup hook generates experiment routes from the `HEROKU_XP` config var.
XP ships no Traefik plugin of its own, only the buildpack and the hook; a
buildpack that also ships a plugin adds the plugin parts described below.

A standalone plugin has three parts:

1. a **composite buildpack** that runs dyno-proxy first and the plugin's
   buildpack second,
2. the **plugin source** (a Yaegi/Traefik plugin), installed next to dyno-proxy,
3. an optional **startup hook** that edits `project.toml` from the environment
   on every boot.

## Layout

```
buildpack/
  buildpack.toml                  # the plugin's own CNB buildpack (id: heroku/xp-src)
  bin/
    detect                        # always passes
    build                         # CNB: install plugin + hook into $DYNO_PROXY_DIR
    compile                       # classic: download dyno-proxy, then do the same
    buildpack.toml                # CNB: composite: order = dyno-proxy, then xp-src
    package.toml                  # CNB: what `pack buildpack package` bundles
  plugins/<name>/                 # the plugin: .traefik.yml, go.mod, *.go
```

## Composite buildpack

Users add one buildpack, and it brings dyno-proxy with it. `bin/buildpack.toml`
orders two groups, dyno-proxy first:

```toml
[[order]]
  [[order.group]]
  id = "heroku/dyno-proxy"
  version = "0.0.1"

  [[order.group]]
  id = "heroku/xp-src"
  version = "0.0.1"
```

`bin/package.toml` lists the dependencies that get bundled, so the ids above
resolve:

```toml
[buildpack]
uri = "."

[[dependencies]]
uri = "docker://ghcr.io/heroku-examples/dyno-proxy-buildpack"

[[dependencies]]
uri = "../"
```

Order matters. dyno-proxy's build installs the binary into a layer and exports
`DYNO_PROXY_DIR` pointing at it. The plugin's `bin/build` runs afterward and
installs into that directory; it should fail if `DYNO_PROXY_DIR` is unset.

For classic buildpacks, `bin/compile` downloads the dyno-proxy classic tarball,
runs its `bin/compile` (which installs into `$BUILD_DIR/vendor/dyno-proxy`), and
then does the same installs from there.

## Installing the plugin

Traefik loads a local plugin whose `moduleName` is `M` from
`plugins-local/src/M`. dyno-proxy links `$DYNO_PROXY_DIR/plugins` there, and
derives the plugin's name from the last path segment of `source`. So:

- the plugin's `go.mod` module path, its `.traefik.yml` `import:`, and the
  `source` used in `project.toml` must all be the same string, such as
  for example `github.com/acme/my-buildpack/myplugin`;
- the buildpack copies the plugin to `$DYNO_PROXY_DIR/plugins/<module path>/`.

`bin/build` reads the module path out of `go.mod` rather than hard-coding it:

```bash
module="$(awk '$1=="module"{print $2; exit}' plugins/<name>/go.mod)"
mkdir -p "${DYNO_PROXY_DIR}/plugins/${module}"
cp -R plugins/<name>/. "${DYNO_PROXY_DIR}/plugins/${module}/"
```

Plugins run under Yaegi: standard library only. See [plugins.md](plugins.md).

## The startup hook

If an executable named `dyno-proxy-start-hook` sits in the same directory as the
dyno-proxy binary (that is, `$DYNO_PROXY_DIR`), then `dyno-proxy run` and
`dyno-proxy background` run it before they start the load balancer.

- It runs on **every boot**, so config can follow the environment (config vars
  change without a rebuild).
- It runs synchronously, in the app's working directory, with the dyno's
  environment, and its stdout and stderr go to the dyno log.
- A **non-zero exit aborts startup**. With `background` (the buildpack path)
  that leaves `PORT` unchanged, so the app serves directly with no proxy.
- It can be a script or a compiled binary. Compiled Go avoids needing a runtime
  on the dyno; build one binary per architecture
  (`GOOS=linux GOARCH=amd64|arm64`, `CGO_ENABLED=0`) and have `bin/build` copy
  the one matching `CNB_TARGET_ARCH` to `${DYNO_PROXY_DIR}/dyno-proxy-start-hook`.
  Fail the build if the binary is missing, so you never ship a proxy that
  silently skips your configuration.

### Customizing configuration

The hook's usual job is to turn environment (config vars) into `[[routes]]` in
`project.toml`. dyno-proxy only reads `project.toml`; the hook is what makes it
dynamic. XP's hook, for example, reads `HEROKU_XP` and writes this:

```toml
# >>> heroku-xp (managed by dyno-proxy-start-hook) >>>
[[routes]]
  path = "/api"
  to = "https://exp-a.herokuapp.com"
  percentage = 25
  mirror = true
# <<< heroku-xp <<<
```

The pattern XP's hook follows:

1. **Managed block.** Everything the hook writes sits between begin and end
   marker comments. On each run, strip the old block, then append a fresh one.
   The rest of the app's `project.toml` is never touched, so the app's own
   routes and the buildpack's `[[io.buildpacks.group]]` entries survive.
2. **Drop when unconfigured.** If the config var is unset, write no block. The
   app then runs exactly as if the buildpack were absent.
3. **Write in place, only on change.** dyno-proxy watches `project.toml` and
   reloads on change. Use `os.WriteFile` on the existing path (a rename would
   drop the file watch), and skip the write when the content is identical so
   boot does not trigger a spurious reload.
4. **Validate before writing.** Parse the existing file, and parse the result.
   If the user's file is broken, exit non-zero with a message. If the *input*
   (the config var) is broken, prefer to warn, drop the block, and exit 0, so a
   bad value does not take the app down.
5. **Routes shape.** The block is `[[routes]]` tables. If the user declared
   `routes` as an inline array, the append is invalid TOML, so the hook rewrites
   the user's routes as `[[routes]]` tables once (and warns that comments were
   not kept).
6. **Target.** A route without `to` forwards to the local app
   (`http://localhost:$APP_PORT`). XP sets `to` to the experiment's URL; a
   plugin that wraps the app leaves it out and attaches `[[routes.plugins]]`
   instead.
7. **Secrets.** `${VAR}` is not expanded inside a plugin's `config`, and
   anything written to `project.toml` lands in the app's filesystem. If you ship
   a plugin, prefer having it read secrets from its environment at request time
   over writing them into the file.

The file edited is `./project.toml` in the working directory, or `$PROJECT_TOML`
if set (XP's hook honors this for tests).

## Building and publishing

XP uses this pipeline (`Makefile` and
`.github/workflows/build-release.yml`):

- `make hook`: run the plugin and hook tests, then cross-compile the hook to
  `buildpack/bin/dyno-proxy-start-hook-linux-{amd64,arm64}`. These are build
  output and gitignored.
- `make buildpack-package`: `pack buildpack package <image> --config
  buildpack/bin/package.toml --publish`, producing the composite CNB image on
  ghcr.io. CI tags every commit `<commit count>.<short sha>` and moves `latest`
  on main with `crane tag`.
- `make buildpack-tarball`: the classic buildpack archive (excluding the hook's
  Go source), attached to a GitHub Release so
  `.../releases/latest/download/xp-buildpack.tgz` works with
  `heroku buildpacks:add`.

## Checklist for a new plugin

1. Copy the layout from [xp](https://github.com/heroku-examples/xp).
2. Pick the module path and use it in `go.mod`, `.traefik.yml` (`import:`), the
   hook, and the docs.
3. Rename the buildpack ids (`heroku/xp`, `heroku/xp-src`) for your buildpack.
4. Rewrite the hook's `run` for your env vars; keep the managed-block helpers.
5. Test locally: run `bin/build` with a scratch `DYNO_PROXY_DIR`, run the hook in
   a scratch directory, then `DYNO_PROXY_DIR=... dyno-proxy config` and check
   that `experimental.localPlugins` and the plugin middleware are rendered.
