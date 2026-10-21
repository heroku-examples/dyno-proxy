package traefik

import (
	"testing"

	"github.com/heroku-examples/dyno-proxy/internal/config"
)

func TestHeaderQueryBodyRoutesOutrankPercentage(t *testing.T) {
	up := []config.Upstream{{URL: "https://x.example.com"}}
	plain := []struct {
		name  string
		match config.Matcher
		want  int
	}{
		{"path", config.Matcher{Path: "/p"}, 10},
		{"header", config.Matcher{Path: "/h", Headers: map[string]string{"x-a": "1"}}, 60},
		{"query", config.Matcher{Path: "/q", Query: map[string]string{"a": "1"}}, 60},
		{"body", config.Matcher{Path: "/b", Body: map[string]string{"$.a": "1"}}, 60},
	}

	st := &renderState{
		routers:      map[string]*router{},
		services:     map[string]*service{},
		middlewares:  map[string]*middleware{},
		localPlugins: map[string]string{},
	}
	for i, tc := range plain {
		if err := st.addPlainRoute(i, config.Route{Match: tc.match, Targets: up}); err != nil {
			t.Fatal(err)
		}
	}
	st.addPercentageRoutes([]config.Route{{Percentage: 10, Match: config.Matcher{Path: "/c"}, Targets: up}})

	pct := st.routers["percentage-0-0-experiment"]
	if pct == nil || pct.Priority == nil {
		t.Fatalf("missing percentage router (have %v)", keys(st.routers))
	}
	for i, tc := range plain {
		r := st.routers[routeKey(i)]
		if r == nil || r.Priority == nil {
			t.Fatalf("%s: missing router", tc.name)
		}
		if *r.Priority != tc.want {
			t.Errorf("%s: priority = %d, want %d", tc.name, *r.Priority, tc.want)
		}
		if tc.want > 10 && *r.Priority <= *pct.Priority {
			t.Errorf("%s: priority %d does not outrank percentage %d", tc.name, *r.Priority, *pct.Priority)
		}
	}
}

func routeKey(i int) string { return "route-" + string(rune('0'+i)) }
