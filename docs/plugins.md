## Plugins

dyno-proxy runs [Traefik](https://traefik.io) as its load balancer, so **any [Traefik plugin](https://plugins.traefik.io/plugins) should work**: anything from the plugin catalog, your own, or one bundled in a buildpack. Plugins run under [Yaegi](https://github.com/traefik/yaegi), Traefik's Go interpreter, so a plugin must stick to the Go standard library and be a Traefik middleware plugin.

Plugins are configured per route. `source` is a remote GitHub repo (the plugin's Go module path), or a local directory:

```toml
[[routes]]
path = "/admin"
  [[routes.plugins]]
  source = "github.com/chap/heroku-oauth"
```

Plugins can accept a `config` object, passed to the plugin as-is:

```toml
[[routes]]
path = "/hello"
  [[routes.plugins]]
  source = "github.com/example/hello-plugin"
    [routes.plugins.config]
    greeting = "hi"
```

Several plugins can be listed on one route.

See the [`body`](../internal/plugins/body) and [`catch`](../internal/plugins/catch) plugins in this repo for working examples of plugin code.

View the [heroku-oauth-buildpack](https://github.com/chap/heroku-oauth-buildpack) as an example of a plugin that installs Dyno Proxy as a dependency.

To ship a plugin in its own repo, as a buildpack that installs dyno-proxy and configures it at boot, see [standalone plugins](standalone-plugin.md).

## Traefik middleware

Every [Traefik HTTP middleware](https://doc.traefik.io/traefik/middlewares/http/overview/) (`rateLimit`, `retry`, `headers`, `basicAuth`, `compress`, `circuitBreaker`, `stripPrefix`, `inFlightReq`, and the rest) can be used without writing a plugin. Add a `[[routes.middleware]]` entry and put the middleware's name and options under it, using the same keys as Traefik's [dynamic configuration](https://doc.traefik.io/traefik/middlewares/http/overview/). The block is passed to Traefik verbatim:

```toml
[[routes]]
path = "/api"
to = "https://example.com"

  [[routes.middleware]]
  [routes.middleware.rateLimit]
  average = 100
  period = "1m"

  [[routes.middleware]]
  [routes.middleware.headers.customResponseHeaders]
  X-Served-By = "dyno-proxy"
```

- Each `[[routes.middleware]]` entry is one middleware. Use a separate entry for each so options aren't mixed up.
- Middleware run in the order listed, before any `plugins`, and apply only to requests that match the route.
- Options are not validated by dyno-proxy. A bad name or option is reported by Traefik when it loads the config (run with `DYNO_PROXY_LOG_LEVEL=DEBUG`, or `dyno-proxy config` to see what was rendered).
- Plugins from the Traefik catalog are also middleware; they go under `[[routes.plugins]]` (see above) because they need a `source`.

## Internal Plugins

Internal plugins are built-in plugins that can be used directly in routes without explicitly specifying the plugin source. These attributes are automatically transformed into the plugin format when project.toml is loaded.

### Body Parser Internal Plugin

The `body` internal plugin parses json or x-www-form-urlencoded body parameters and adds them as headers to the requests.

**Using the internal plugin syntax:**

```toml
[[routes]]
path = "/submit"
to = "https://my-canary-app.herokuapp.com"
  [routes.body]
  name = "canary"
  version = "v2"
```

This is automatically expanded to:

```toml
[[routes]]
path = "/submit"
  [[routes.plugins]]
  source = "plugins/body"
    [routes.plugins.config]
    fields = ["name", "version"]

  [[routes.routes]]
  to = "https://my-canary-app.herokuapp.com"
    [routes.routes.header]
    x-body-name = "canary"
    x-body-version = "v2"
```

**Key features:**
- The `body` attribute is automatically converted to a plugin with `source: "plugins/body"`
- The `plugins` array is created automatically if it doesn't exist
- Existing plugins in the `plugins` array are preserved
- Only adds matching fields from the body as headers
- Automatically creates a nested route

### Catch Internal Plugin

The `catch` internal plugin allows you to decrypt encrypted headers in incoming requests. This is useful for securely passing sensitive information like database URLs.

**Using the internal plugin syntax:**

```toml
[[routes]]
path = "/decrypted"
  [[routes.catch]]
  header = "x-forwarded-database-url"
  decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

This is automatically expanded to:

```toml
[[routes]]
path = "/decrypted"
  [[routes.plugins]]
  source = "plugins/catch"
    [[routes.plugins.config.catch]]
    header = "x-forwarded-database-url"
    decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

**Key features:**
- The `catch` attribute is automatically converted to a plugin with `source: "plugins/catch"`
- The `plugins` array is created automatically if it doesn't exist
- Existing plugins in the `plugins` array are preserved
