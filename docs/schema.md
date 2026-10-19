# Configuration Schema

A single reference for every field you can use to configure Dyno Proxy. Each
section shows the field, what it does, and a runnable example.

- **Where config lives** → [Configuration file](#configuration-file)
- **The file layout** → [project.toml structure](#projecttoml-structure)
- **Matching requests** → [Matchers](#matchers)
- **Choosing an upstream** → [Targets](#targets)
- **Splitting & mirroring traffic** → [Traffic control](#traffic-control)
- **Rewriting the request** → [Request modifiers](#request-modifiers)
- **Middleware & plugins** → [Middleware and plugins](#middleware-and-plugins)
- **Rules that gate rules** → [Nested routes](#nested-routes)
- **Everything at a glance** → [Field reference](#field-reference)

---

## Configuration file

All routing configuration lives in `project.toml` in the app's working
directory. It coexists with the CNB buildpack tables: one file both installs
dyno-proxy and configures its routes. When it changes, routes reload without a
restart.

### Environment variables

| Variable | Purpose |
| --- | --- |
| `DISABLE_DYNO_PROXY` | The proxy is on by default once the buildpack is installed; set to `true`, `TRUE` or `1` to disable it (the web command runs directly, no proxy or supervision). |
| `APP_PORT` | Port the local app listens on. Defaults to the first port above `PORT` with no listener (normally `PORT + 1`), or `8889` when `PORT` is unset. |
| `PORT` | Port the proxy itself listens on (default `8888`). |
| `DYNO_PROXY_LOG_LEVEL` | `TRACE`, `DEBUG`, `INFO`, `WARN`, `ERROR`. Defaults to `ERROR`. |
| `DYNO_PROXY_LOG_FORMAT` | Log output format for the proxy and the load balancer. Defaults to text (`key=value` for the proxy, `common` for Traefik); set to `json` for JSON output everywhere. |
| `DYNO_PROXY_ADMIN_USER`, `DYNO_PROXY_ADMIN_PASSWORD` | Basic-auth credentials for the Traefik dashboard and the Prometheus metrics endpoint. Both must be set; otherwise neither is served. |
| `DYNO_PROXY_ENCRYPTION_KEY` | AES-256-GCM key used by `set … encrypt` and `catch … decrypt`. |

> The dashboard (`/.dyno-proxy/traefik`) and Prometheus metrics endpoint
> (`/.dyno-proxy/metrics`) are only served when the admin credentials are set.
> Traefik's version check and anonymous usage
> reporting are disabled.

---

## project.toml structure

Routes are a `[[routes]]` array-of-tables. Nested child rules use a `routes`
array too (see [Nested routes](#nested-routes)).

```toml
[_]
schema-version = "0.2"

[[io.buildpacks.group]]
uri = "https://my-buildpack-staging.herokuapp.com/buildpack-v1alpha1.tgz"

[[routes]]
path = "/api"
to = "https://api.example.com/"

[[routes]]
path = "/blog*"
to = "https://blogger.googleblog.com"
methods = ["GET", "POST"]
```

### Top-level fields

| Field | Type | Description |
| --- | --- | --- |
| `routes` | array of [rules](#route) | Ordered routing rules. |

### Route

Every entry in `routes` is a rule. A request must satisfy **all** of a rule's
matchers to be routed by it.

```toml
[[routes]]
path = "/api/v2"
methods = ["GET", "POST"]
to = "https://api-v2-beta.example.com/"
  [routes.header]
  x-api-version = "2.0"
  [routes.query]
  beta = "true"
```

| Field | Type | Category | |
| --- | --- | --- | --- |
| `routeID` | string | name | [Route ID](#route-id) |
| `path` | string | matcher | [Path](#path) |
| `header` | object | matcher | [Header](#header) |
| `query` | object | matcher | [Query](#query) |
| `methods` | array | matcher | [Methods](#methods) |
| `body` | object | matcher | [Body](#body) |
| `to` | string | target | [Targets](#targets) |
| `percentage` | number | traffic | [Percentage](#percentage-weighted-deployments) |
| `mirror` | bool | traffic | [Mirror](#mirror) |
| `set` | array | modifier | [Set](#set--rewrite-the-request) |
| `catch` | array | modifier | [Catch](#catch--decrypt-forwarded-secrets) |
| `middleware` | array | middleware | [Raw middleware](#raw-traefik-middleware) |
| `plugins` | array | middleware | [Plugins](#plugins) |
| `routes` | array | nesting | [Nested rules](#nested-routes) |

### Route ID

An optional `routeID` is appended to the names of the Traefik router and service
generated for the route (for example `route-0-api`, or
`percentage-0-0-experiment-canary`), which makes them easy to find in the
dashboard, metrics and logs. Characters other than letters, digits, `_` and `-`
become `-`. A nested child without its own `routeID` uses its parent's. IDs are
not required to be unique, since the generated names keep their position
prefix.

```toml
[[routes]]
routeID = "api"
path = "/api"
to = "https://api.example.com/"
```

---

## Matchers

Populated matchers are ANDed together. Leave a matcher out to ignore that
dimension.

### Path

```toml
[[routes]]
path = "/api"
to = "https://api.example.com/"
```

Supports prefixes, wildcards, and regex:

| Pattern | Matches |
| --- | --- |
| `/api` | exactly `/api` |
| `/app/123*` | any path with prefix `/app/123` |
| `/products/.*` | regex (any `.*` in the value triggers regex mode) |
| `^/products/(shoes\|socks)/[0-9]+$` | full regex |

### Header

```toml
[[routes]]
to = "https://example.com/admin"
  [routes.header]
  x-proxy-header = "admin"
```

### Query

```toml
[[routes]]
to = "https://v2.example.com/"
  [routes.query]
  x-proxy-query = "v2"
```

An empty value matches the parameter being present with any value —
`canary = ""` under `[routes.query]` matches `example.com?canary`. A value containing `*`
becomes a regex match.

### Methods

```toml
[[routes]]
path = "/api/users"
methods = ["GET"]
to = "https://read.example.com/"
```

Multiple methods match if the request uses **any** of them:

```toml
[[routes]]
path = "/api/users"
methods = ["POST", "PUT", "DELETE"]
to = "https://write.example.com/"
```

Method names are upper-cased automatically.

### Body

Route on fields inside a JSON request body.

```toml
[[routes]]
to = "https://org-123.example.com/"
  [routes.body]
  organization_id = "org-123"
```

Nested fields use dot paths (with or without a leading `$.` / `.`), and values
containing `*` or `.*` are treated as regex:

```toml
[[routes]]
to = "https://premium.example.com/"
  [routes.body]
  "user.subscription.tier" = "premium"

[[routes]]
to = "https://orgs.example.com/"
  [routes.body]
  "organization.id" = "org-*"

[[routes]]
to = "https://pricing.example.com/"
  [routes.body]
  "total.profit" = '.*\.(99|50)'
```

Body matching implicitly enables body field extraction (the fields are promoted
to `X-Body-*` headers for the downstream service).

---

## Targets

The upstream is:

| Field | Result |
| --- | --- |
| `to` | Used verbatim; `$VAR` / `${VAR}` are expanded from the environment. |
| *(omitted)* | Falls back to the local app service (`http://localhost:$APP_PORT`). |

```toml
[[routes]]
path = "/blog"
to = "https://blog.example.com"

[[routes]]
path = "/admin"
to = "http://$REVIEW_APP.herokuapp.com"

[[routes]]
path = "/local"
```

---

## Traffic control

### Percentage (weighted deployments)

Send a share of traffic to an alternate upstream — canary / progressive rollout.

```toml
[[routes]]
percentage = 10
to = "https://canary.example.com/"
```

### Mirror

Copy matching traffic to a second upstream while still serving the local app's
response. Useful for shadow testing.

```toml
[[routes]]
path = "/api/v1"
to = "https://canary.example.com"
mirror = true
```

---

## Request modifiers

### Set — rewrite the request

An array of directives applied in order. Each is either a **path** rewrite or a
**header** set.

Rewrite the path:

```toml
[[routes]]
path = "/blog"
to = "https://blogger.googleblog.com"
  [[routes.set]]
  path = "/2005"
```

Set a header. `value` is executed in a shell on startup, so it can read env vars
(`$DATABASE_URL`) or run commands (`$(date +%s)`, `$(cat /certs/private.key)`):

```toml
[[routes]]
path = "/date"
to = "http://localhost:9999"
  [[routes.set]]
  header = "X-Timestamp"
  value = "$(date +%s)"
```

Encrypt the value (AES-256-GCM) so it can be safely forwarded and decrypted
downstream with the same key:

```toml
[[routes]]
path = "/admin"
to = "http://review-app.herokuapp.com"
  [[routes.set]]
  header = "x-forwarded-database-url"
  value = "$DATABASE_URL"
  encrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

| `set` item field | Description |
| --- | --- |
| `path` | Replacement request path (path-rewrite directive). |
| `header` | Header name to set (header directive). |
| `value` | Header value; shell/env expression evaluated at startup. |
| `encrypt` | Optional key spec; encrypts `value` with AES-256-GCM. |

### Catch — decrypt forwarded secrets

The downstream side of `set … encrypt`. Catches named headers and decrypts them
with the shared key so the app receives plaintext values.

```toml
[[routes]]
path = "/admin"
  [[routes.catch]]
  header = "x-forwarded-database-url"
  decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

| `catch` item field | Description |
| --- | --- |
| `header` | Header name to catch. |
| `decrypt` | Key spec used to decrypt the value. |

---

## Middleware and plugins

### Raw Traefik middleware

The `middleware` array passes Traefik middleware blocks through verbatim, in
order.

```toml
[[routes]]
path = "/"
to = "https://example.com"

  [[routes.middleware]]
  [routes.middleware.rateLimit]
  average = 4
  period = "1m"

  [[routes.middleware]]
  [routes.middleware.inFlightReq]
  amount = 10
  [routes.middleware.inFlightReq.sourceCriterion.ipStrategy]
  depth = 2
```

### Plugins

The `plugins` array references Yaegi/Traefik plugins. `source` is a URL, git
repo, or local path; `config` is passed through verbatim.

```toml
[[routes]]
path = "/decrypted"
  [[routes.plugins]]
  source = "plugins/catch"
    [[routes.plugins.config.catch]]
    header = "x-forwarded-database-url"
    decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

---

## Nested routes

A rule can gate a set of child rules via a nested `routes` array. The parent's
matchers and modifiers apply, then each child adds its own — so you can route on
the request *after* a modifier or plugin has altered it.

```toml
[[routes]]
path = "/hello"
  [[routes.set]]
  header = "x-hello"
  value = "canary"
  [[routes.routes]]
  to = "https://canary.herokuapp.com"
    [routes.routes.header]
    x-hello = "canary"
```

A child that defines no target falls back to the default app service (it does
**not** inherit the parent's `to`). Child matchers overlay the parent's: scalar
`path` and `methods` are replaced when set; header/query/body maps are unioned
with the child winning on key collisions.

---

## Field reference

Everything a single route accepts, in one place (shown in project.toml format):

```toml
[[routes]]
routeID = "api"                           # string; appended to generated router/service names

# ── Matchers (all populated matchers must pass) ─────────────
path = "/api/*"                           # string; supports prefix (*) and regex (.*)
methods = ["GET", "POST"]                 # array; matches any listed method

  [routes.header]
  x-env = "staging"                       # name -> expected value

  [routes.query]
  beta = "true"                           # name -> value ("" = present, * = regex)

  [routes.body]
  "user.tier" = "premium"                 # JSON field path -> value (* / .* = regex)

# ── Target ──────────────────────────────────────────────────
to = "https://svc.example.com/"           # explicit URL; $VAR expanded
#     (omitted) -> local app on http://localhost:$APP_PORT

# ── Traffic control ─────────────────────────────────────────
percentage = 10                           # send this % to the target
mirror = true                             # copy traffic; serve local response

# ── Request modifiers ───────────────────────────────────────
  [[routes.set]]
  path = "/v2"                            # rewrite path

  [[routes.set]]
  header = "x-key"
  value = "$SECRET"
  encrypt = "$DYNO_PROXY_ENCRYPTION_KEY"

  [[routes.catch]]
  header = "x-key"
  decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"

# ── Middleware & plugins ────────────────────────────────────
  [[routes.middleware]]
  [routes.middleware.rateLimit]
  average = 4
  period = "1m"

  [[routes.plugins]]
  source = "plugins/body"
  config = {}

# ── Nesting ─────────────────────────────────────────────────
  [[routes.routes]]
  to = "https://child.example.com"
    [routes.routes.header]
    x-key = "v"
```
