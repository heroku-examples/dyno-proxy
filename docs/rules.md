## Configuration

Configure routing in `project.toml` with `[[routes]]`, in the app's working directory. If the file changes, the proxy is updated without restarting.

#### Path-based Routing
```toml
[[routes]]
path = "/api"
to = "https://api.example.com/"
```

#### Header-based Routing
```toml
[[routes]]
to = "https://example.com/admin"
  [routes.header]
  x-proxy-header = "admin"
```

#### Query Parameter Routing
```toml
[[routes]]
to = "https://v2.example.com/"
  [routes.query]
  x-proxy-query = "v2"
```

Empty query values will match requests with only the param, eg `canary = ""` will match `example.com?canary`.

#### HTTP Method Routing
```toml
[[routes]]
path = "/api/users"
methods = ["GET"]
to = "https://read-service.example.com/"
```

Multiple methods:
```toml
[[routes]]
path = "/api/users"
methods = ["POST", "PUT", "DELETE"]
to = "https://write-service.example.com/"
```

#### Request Mirroring
```toml
[[routes]]
path = "/api/v1"
to = "https://canary.example.com"
mirror = true
```

Sends traffic to both local app and proxy URL. Local app response returned.


#### Percentage-based Routing (Weighted Deployments)
```toml
[[routes]]
percentage = 10
to = "https://canary.example.com/"
```

## Prefix, Wildcards and Regex

`path: app/123*` match any paths with prefix `/app/123`

`path: (?)/123*` match any paths containing `123`

`path: ^/products/(shoes|socks)/[0-9]+$` paths beginning with products/ - shoes or socks - integer

```toml
[[routes]]
path = "^/products/(shoes|socks)/[0-9]+$"
to = "https://products.example.com/"
```

#### Request Body Routing

Route based on JSON fields in the request body.

```toml
[[routes]]
to = "https://org-123.example.com/"
  [routes.body]
  organization_id = "org-123"
```

**Nested field extraction:**
```toml
[[routes]]
to = "https://premium.example.com/"
  [routes.body]
  "user.subscription.tier" = "premium"
```

**Multi-tenant routing by organization:**
```toml
[[routes]]
to = "https://acme-corp.example.com/"
  [routes.body]
  organization_id = "acme-corp"

[[routes]]
to = "https://startup-inc.example.com/"
  [routes.body]
  organization_id = "startup-inc"

[[routes]]
to = "https://default.example.com/"
```


#### Complex Routing Rules

Combine multiple conditions in single rule

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

Define multiple rules

```toml
[[routes]]
path = "/api"
to = "https://api.example.com/"

[[routes]]
to = "https://example.com/admin"
  [routes.header]
  x-proxy-header = "admin"

[[routes]]
to = "https://v2.example.com/"
  [routes.query]
  x-proxy-query = "v2"

[[routes]]
percentage = 10
to = "https://canary.example.com/"
```

#### Nested Routes

Routes or plugins may alter the request. To route based on the altered request, nest a `routes` key inside a rule:
```toml
[[routes]]
path = "/hello"
  [[routes.set]]
  header = "x-hello"
  value = "canary"

  [[routes.routes]]
  to = "https://my-canary-app.herokuapp.com"
    [routes.routes.header]
    x-hello = "canary"
```
