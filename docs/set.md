## Set

Modify a request before it's passed to the downstream service.

For example, the `path` can be rewritten:
```toml
[[routes]]
path = "/blog"
to = "https://blogger.googleblog.com"
  [[routes.set]]
  path = "/2005"
```

## Encrypt

Pass encrypted metadata in a header.

```term
$ DYNO_PROXY_ENCRYPTION_KEY=$(openssl rand -hex 32)
```

```toml
[[routes]]
path = "/admin"
to = "http://<review-app>.herokuapp.com"
  [[routes.set]]
  header = "x-forwarded-database-url"
  value = "$DATABASE_URL"
  encrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

On startup, the value is executed in the shell. Here it gets the environment variable `$DATABASE_URL`, but could also read a file like `$(cat /certs/private.key)` or `$(env | base64)` to get all environment variables.

Encrypted (AES-256-GCM) value is written to the proxy configuration and reused on matching requests.

## Catch

Downstream services can `catch` and decrypt secrets using the same key.

```toml
[[routes]]
path = "/admin"
  [[routes.catch]]
  header = "x-forwarded-database-url"
  decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
```

The headers received by downstream clients will have decrypted values.
