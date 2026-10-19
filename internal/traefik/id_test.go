package traefik

import (
	"testing"

	"github.com/heroku-examples/dyno-proxy/internal/config"
)

func TestRouteIDAppendedToNames(t *testing.T) {
	model := config.RouteModel{
		DefaultServiceURL: "http://localhost:5000",
		Routes: []config.Route{
			{ID: "api", Match: config.Matcher{Path: "/api"}, Targets: []config.Upstream{{URL: "https://a.example.com"}}},
			{Match: config.Matcher{Path: "/b"}, Targets: []config.Upstream{{URL: "https://b.example.com"}}},
			{
				ID: "mw", Match: config.Matcher{Path: "/e"}, Targets: []config.Upstream{{URL: "https://e.example.com"}},
				Middlewares: []config.Middleware{{Kind: config.CapSetPath, Config: map[string]any{"path": "/y"}}},
			},
			{ID: "ca.nary@1", Percentage: 10, Match: config.Matcher{Path: "/c"}, Targets: []config.Upstream{{URL: "https://c.example.com"}}},
			{ID: "shadow", Mirror: true, Match: config.Matcher{Path: "/d"}, Targets: []config.Upstream{{URL: "https://d.example.com"}}},
		},
	}
	st := &renderState{
		routers:      map[string]*router{},
		services:     map[string]*service{},
		middlewares:  map[string]*middleware{},
		localPlugins: map[string]string{},
	}
	for i, rt := range model.Routes[:3] {
		if err := st.addPlainRoute(i, rt); err != nil {
			t.Fatal(err)
		}
	}
	st.addPercentageRoutes(model.Routes[3:])

	// Every router must point at the service of the same name, except mirror
	// routes, whose router uses the -mirror service.
	wantService := map[string]string{
		"route-0-api":                         "route-0-api",
		"route-1":                             "route-1",
		"route-2-mw":                          "route-2-mw",
		"percentage-0-0-experiment-ca-nary-1": "percentage-0-0-experiment-ca-nary-1",
		"percentage-1-0-experiment-shadow":    "percentage-1-0-mirror-shadow",
	}
	for router, svc := range wantService {
		r := st.routers[router]
		if r == nil {
			t.Errorf("missing router %q (have %v)", router, keys(st.routers))
			continue
		}
		if r.Service != svc {
			t.Errorf("router %q service = %q, want %q", router, r.Service, svc)
		}
		if st.services[r.Service] == nil {
			t.Errorf("router %q points at missing service %q", router, r.Service)
		}
	}

	// The mirror service mirrors the default app to the experiment service.
	m := st.services["percentage-1-0-mirror-shadow"].Mirroring
	if m == nil || m.Service != "app" || len(m.Mirrors) != 1 || m.Mirrors[0].Name != "percentage-1-0-experiment-shadow" {
		t.Errorf("mirror service = %+v", m)
	}

	// Middlewares are named after the ID'd route and attached to its router.
	mws := st.routers["route-2-mw"].Middlewares
	if len(mws) != 1 || mws[0] != "route-2-mw-set-path-0" || st.middlewares[mws[0]] == nil {
		t.Errorf("route-2-mw middlewares = %v (have %v)", mws, keys(st.middlewares))
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
