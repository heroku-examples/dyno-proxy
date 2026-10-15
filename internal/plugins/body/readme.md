# Body Parser Plugin

Traefik plugin to parse JSON or `x-www-form-urlencoded` request bodies and expose
selected fields as request headers so they can be used in routing rules.

## Overview

The Body Parser plugin reads incoming request bodies, extracts the configured
fields, and adds each one as a header with an `X-Body-` prefix. This lets Traefik
make routing decisions based on request body content without the downstream
service having to parse the body.

The plugin only **extracts** values. The actual matching — exact value or regex —
is performed downstream by Traefik's `Header` / `HeaderRegexp` router rules against
the `X-Body-*` headers this plugin sets.

**Key features:**
- Supports JSON (`application/json`) and form-encoded (`application/x-www-form-urlencoded`) bodies
- Exact-match and nested-key field extraction (dot notation)
- Automatic filtering of large fields (>1KB) to skip file uploads
- Preserves the original request body for downstream handlers

## Configuration

The plugin takes a single `fields` array of body field names to extract:

```yaml
fields:
  - name
  - user_id
  - organization.id
```

Each field is looked up in the body and added as `X-Body-<field>`:

```
X-Body-name: <value of "name">
X-Body-user_id: <value of "user_id">
X-Body-organization.id: <value of organization.id>
```

If `fields` is empty or omitted, **all** top-level fields are extracted.

### Field names

- **Top-level:** `name`, `user_id`
- **Nested (dot notation):** `organization.id`, `total.profit`
- A leading `$.` or `.` is stripped, so JSONPath-style names (`$.user.id`) also work
  and produce the same header as `user.id`.

Field names may only contain letters, digits, `.`, `-`, `_`, and `$`; any other
character causes the field to be skipped (prevents header injection).

## Matching in Traefik

Matching happens in the router rule, not in this plugin. Given the extracted
`X-Body-*` headers, use:

- `Header(...)` for an **exact** match
- `HeaderRegexp(...)` for a **regex** match

```yaml
http:
  routers:
    exact-router:
      rule: "Path(`/api`) && Header(`X-Body-name`, `canary`)"
      service: canary-service

    regex-router:
      rule: "Path(`/api`) && HeaderRegexp(`X-Body-version`, `^v2.*`)"
      service: v2-service
```

In dyno-proxy's manifest format, this is generated automatically: an exact value
becomes `Header(...)` and a value containing `*` becomes `HeaderRegexp(...)`.

## Value serialization

Values are converted to strings based on their JSON type:

| JSON Type | Serialization | Example Input | Example Output |
|-----------|---------------|---------------|----------------|
| String | Direct value | `"hello"` | `hello` |
| Number (integer) | Integer format | `123` | `123` |
| Number (float) | Float format | `123.45` | `123.45` |
| Boolean | Lowercase string | `true` | `true` |
| Null | Empty string | `null` | `` |

## Large field handling

Fields larger than **1024 bytes** are skipped to avoid HTTP header size limits
(typically 8KB total per request), skip file uploads / binary data, and prevent
performance issues. The whole body is capped at 1MB.

## Content type support

- **`application/json`** — fields extracted by name (top-level and nested).
- **`application/x-www-form-urlencoded`** — fields extracted by name. Form data is
  flat, so only top-level names apply.
- Any other content type is passed through unchanged.

Charset suffixes are fine: `application/json; charset=utf-8` is recognized.

## Dyno Proxy integration

In the manifest format, the `body` attribute maps field names to the values you
want to match. It expands to the body plugin (extracting the field names) plus the
matching router rule:

```json
{
  "routes": [
    {
      "path": "/submit",
      "body": {
        "name": "canary",
        "version": "v2"
      },
      "to": "https://example.herokuapp.com"
    }
  ]
}
```

expands the plugin config to:

```json
{
  "source": "plugins/body",
  "config": { "fields": ["name", "version"] }
}
```

and generates `Header("X-Body-name", "canary")` and `Header("X-Body-version", "v2")`
router predicates (or `HeaderRegexp` when a value contains `*`).

## Limitations

1. Only `application/json` and `application/x-www-form-urlencoded` bodies are parsed.
2. Fields larger than 1KB are skipped; the body is read up to 1MB.
3. Form-encoded bodies are flat — only top-level field names apply.
4. All headers use the `X-Body-` prefix and cannot be customized.
5. The plugin only extracts fields; it never modifies the request body.

## Development

```bash
cd internal/plugins/body
go test -v      # run tests
make test       # via Makefile
```

## Further reading

- [Traefik Plugins Documentation](https://doc.traefik.io/traefik/plugins/overview/)
- [Traefik Router Rules (Header / HeaderRegexp)](https://doc.traefik.io/traefik/routing/routers/#rule)
- [Dyno Proxy Documentation](../../../docs/plugins.md)
