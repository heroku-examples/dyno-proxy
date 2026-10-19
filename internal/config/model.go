package config

// RouteModel is the intermediate representation (IR) built from project.toml
// and rendered into Traefik config by package traefik.
type RouteModel struct {
	// Routes is the ordered set of routing rules. Order is significant: it is
	// declaration order. Earlier routes are higher priority.
	Routes []Route

	// DefaultServiceURL is the fallback upstream for requests that match no
	// route's own target (http://localhost:APP_PORT).
	DefaultServiceURL string

	// Dashboard describes the reserved admin dashboard route
	// (/.dyno-proxy/traefik).
	Dashboard DashboardRoute

	// Metrics describes the reserved Prometheus metrics route
	// (/.dyno-proxy/metrics).
	Metrics MetricsRoute
}

// Route is a single routing rule: a set of matchers plus one or
// more upstream targets and an ordered middleware chain. Generalized from the
// source-level route without any Traefik coupling.
type Route struct {
	// ID is the optional `routeID` from project.toml. It is appended to the
	// generated router and service names. A nested child without its own routeID
	// inherits its parent's.
	ID string

	// Match holds the request-matching predicates. A request must satisfy all
	// populated matchers.
	Match Matcher

	// Targets are the upstream destinations. A single target is a plain proxy.
	Targets []Upstream

	// Percentage is the traffic percentage (0-100) directed at this route /
	// its primary target. Zero means unset (treat as 100 / no split).
	Percentage int

	// Mirror, when true, marks this route's target(s) as mirror destinations:
	// traffic is copied to them rather than served from them.
	Mirror bool

	// Middlewares is the ordered middleware chain applied to matched requests.
	Middlewares []Middleware
}

// Matcher holds the request-matching predicates for a Route. An empty field
// means "do not match on this dimension".
type Matcher struct {
	// Path is the request path pattern. A trailing "*" or an embedded ".*"
	// signals a prefix / regex match respectively.
	Path string

	// Headers maps header name -> expected value.
	Headers map[string]string

	// Query maps query-parameter name -> expected value. Empty value means
	// "parameter present with any value".
	Query map[string]string

	// Methods restricts matching to these HTTP methods (upper-case).
	Methods []string

	// Body maps JSON body field name -> expected value, for body-based routing.
	Body map[string]string
}

// Upstream is a single proxy target.
type Upstream struct {
	// URL is the resolved upstream URL (e.g. http://localhost:8889 or
	// https://app.herokuapp.com). Resolution of "to"/default happens when
	// project.toml is loaded, so this is always concrete.
	URL string
}

// Middleware is one entry in a Route's middleware chain. Kind selects the
// behavior; Config carries its parameters. The expected Config keys for each
// Kind are documented on the Cap constants.
type Middleware struct {
	Kind   Capability
	Config map[string]any
}

// DashboardRoute describes the reserved admin dashboard route.
type DashboardRoute struct {
	// PathPrefix is the path prefix the dashboard is served under
	// (/.dyno-proxy/traefik).
	PathPrefix string

	// User and HashedPassword are the basic-auth credentials guarding it.
	User           string
	HashedPassword string
}

// MetricsRoute describes the reserved Prometheus metrics route.
type MetricsRoute struct {
	// PathPrefix is the path prefix metrics are served under
	// (/.dyno-proxy/metrics).
	PathPrefix string
}

// Capability names a Middleware's kind. Rendering an unknown kind fails at
// config-generation time.
type Capability string

const (
	// CapSetHeader sets a request header before proxying. Config keys:
	//   "header" string  - header name
	//   "value"  string  - value or shell/env expression ($VAR / $(cmd))
	//   "encrypt" string - optional key spec; when set, value is AES-256-GCM encrypted
	CapSetHeader Capability = "SetHeader"

	// CapSetPath rewrites the request path before proxying. Config keys:
	//   "path" string - the replacement path
	CapSetPath Capability = "SetPath"

	// CapDecrypt catches named request headers and optionally decrypts them
	// (the "catch" internal plugin). Config keys:
	//   "headers" map[string]string - header name -> value source expression
	//   "decrypt" string            - optional AES key spec used to decrypt
	CapDecrypt Capability = "Decrypt"

	// CapBodyToHeader extracts named JSON body fields so they can be matched on
	// and/or promoted to headers (the "body" internal plugin). Config keys:
	//   "fields" []string - body field names
	CapBodyToHeader Capability = "BodyToHeader"

	// CapRawTraefikPlugin is the escape hatch for any Traefik/Yaegi plugin or raw
	// middleware. Config keys:
	//   "source" string         - plugin source (URL, git repo, or local path), optional for raw middlewares
	//   "config" map[string]any - plugin/middleware configuration, passed through verbatim
	//   "raw"    map[string]any - a raw Traefik middleware block, passed through verbatim
	CapRawTraefikPlugin Capability = "RawTraefikPlugin"
)
