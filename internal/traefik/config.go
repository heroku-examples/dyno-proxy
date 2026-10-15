package traefik

import "encoding/json"

// This file ports the Traefik configuration structs from the legacy flat
// package (shared.go:263+). They model the subset of Traefik's dynamic and
// static configuration that dyno-proxy emits.

// traefikConfig is the root of the Traefik dynamic configuration.
type traefikConfig struct {
	HTTP *httpConfig `json:"http,omitempty" toml:"http,omitempty"`
}

// httpConfig holds the routers, services, and middlewares.
type httpConfig struct {
	Routers     map[string]*router     `json:"routers,omitempty" toml:"routers,omitempty"`
	Services    map[string]*service    `json:"services,omitempty" toml:"services,omitempty"`
	Middlewares map[string]*middleware `json:"middlewares,omitempty" toml:"middlewares,omitempty"`
}

// router defines a Traefik router.
type router struct {
	Rule        string   `json:"rule,omitempty" toml:"rule,omitempty"`
	Service     string   `json:"service,omitempty" toml:"service,omitempty"`
	EntryPoints []string `json:"entryPoints,omitempty" toml:"entryPoints,omitempty"`
	Priority    *int     `json:"priority,omitempty" toml:"priority,omitempty"`
	Middlewares []string `json:"middlewares,omitempty" toml:"middlewares,omitempty"`
	ParentRefs  []string `json:"parentRefs,omitempty" toml:"parentRefs,omitempty"`
}

// service defines a Traefik service.
type service struct {
	LoadBalancer *loadBalancer `json:"loadBalancer,omitempty" toml:"loadBalancer,omitempty"`
	Mirroring    *mirroring    `json:"mirroring,omitempty" toml:"mirroring,omitempty"`
}

// loadBalancer defines the load balancer for a service.
type loadBalancer struct {
	PassHostHeader bool     `json:"passHostHeader,omitempty" toml:"passHostHeader"`
	Strategy       string   `json:"strategy,omitempty" toml:"strategy,omitempty"`
	Servers        []server `json:"servers,omitempty" toml:"servers,omitempty"`
}

// mirroring defines a Traefik mirroring service.
type mirroring struct {
	Service string          `json:"service,omitempty" toml:"service,omitempty"`
	Mirrors []mirrorService `json:"mirrors,omitempty" toml:"mirrors,omitempty"`
}

// mirrorService defines a mirror target service.
type mirrorService struct {
	Name    string `json:"name,omitempty" toml:"name,omitempty"`
	Percent int    `json:"percent,omitempty" toml:"percent,omitempty"`
}

// server defines a backend server.
type server struct {
	URL    string `json:"url,omitempty" toml:"url,omitempty"`
	Weight *int   `json:"weight,omitempty" toml:"weight,omitempty"`
}

// middleware defines a Traefik middleware. It supports typed middlewares
// (ReplacePath, Headers, BasicAuth) and arbitrary middlewares via Raw, which
// passes configuration directly to Traefik.
type middleware struct {
	Plugin      map[string]interface{} `json:"plugin,omitempty" toml:"plugin,omitempty"`
	ReplacePath *replacePath           `json:"replacePath,omitempty" toml:"replacePath,omitempty"`
	Headers     *headersMiddleware     `json:"headers,omitempty" toml:"headers,omitempty"`
	BasicAuth   *basicAuthMiddleware   `json:"basicAuth,omitempty" toml:"basicAuth,omitempty"`
	// Raw holds arbitrary middleware configurations passed directly to Traefik.
	// The key is the middleware type name (e.g. "rateLimit", "stripPrefix").
	Raw map[string]interface{} `json:"-" toml:"-"`
}

// MarshalJSON merges the Raw field with the typed fields.
func (m middleware) MarshalJSON() ([]byte, error) {
	result := make(map[string]interface{})
	if m.Plugin != nil {
		result["plugin"] = m.Plugin
	}
	if m.ReplacePath != nil {
		result["replacePath"] = m.ReplacePath
	}
	if m.Headers != nil {
		result["headers"] = m.Headers
	}
	if m.BasicAuth != nil {
		result["basicAuth"] = m.BasicAuth
	}
	for key, value := range m.Raw {
		result[key] = value
	}
	return json.Marshal(result)
}

// replacePath defines a Traefik ReplacePath middleware.
type replacePath struct {
	Path string `json:"path,omitempty" toml:"path,omitempty"`
}

// headersMiddleware defines a Traefik Headers middleware.
type headersMiddleware struct {
	CustomRequestHeaders map[string]string `json:"customRequestHeaders,omitempty" toml:"customRequestHeaders,omitempty"`
}

// basicAuthMiddleware defines a Traefik BasicAuth middleware.
type basicAuthMiddleware struct {
	Users []string `json:"users,omitempty" toml:"users,omitempty"`
}

// expandMiddlewaresForTOML converts middleware structs to map[string]interface{}
// by expanding Raw fields, making them properly marshalable to TOML (which has
// no MarshalJSON escape hatch). Ported from main.go:87.
func expandMiddlewaresForTOML(middlewares map[string]*middleware) map[string]interface{} {
	result := make(map[string]interface{})
	for name, mw := range middlewares {
		expanded := make(map[string]interface{})
		if mw.Plugin != nil {
			expanded["plugin"] = mw.Plugin
		}
		if mw.ReplacePath != nil {
			expanded["replacePath"] = mw.ReplacePath
		}
		if mw.Headers != nil {
			expanded["headers"] = mw.Headers
		}
		if mw.BasicAuth != nil {
			expanded["basicAuth"] = mw.BasicAuth
		}
		for key, value := range mw.Raw {
			expanded[key] = value
		}
		result[name] = expanded
	}
	return result
}

func intPtr(i int) *int { return &i }
