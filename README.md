# Dyno Proxy

A lightweight load balancer for Heroku apps.

Add buildpack and rebuild app.
```
# Classic buildpack
$ heroku buildpacks:add https://github.com/heroku-examples/dyno-proxy/releases/latest/download/dyno-proxy-buildpack.tgz
```
```
# Cloud native buildpack (CNB) added in project.toml
[[io.buildpacks.group]]
uri = "docker://ghcr.io/heroku-examples/dyno-proxy-buildpack"
```

Configure routes in `project.toml` (both Classic and CNB buildpacks)
```
[[routes]]
path = "/anything"
to = "https://httpbin.org"
```

The proxy is active by default. To disable, `heroku config:set DISABLE_DYNO_PROXY=true` or remove buildpack and rebuild.

More info on
- [Configure routing rules](docs/rules.md)
- [How it works](docs/how.md)
- [Full configuration schema](docs/schema.md)
- [Monitoring and logs](docs/how.md#monitoring-and-troubleshooting)
- [Plugins and middleware](docs/plugins.md)
- [Edit request and pass data to downstream services](docs/set.md)

[Contributions](docs/contributing.md) welcome!
