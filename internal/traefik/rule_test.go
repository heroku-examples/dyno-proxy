package traefik

import (
	"reflect"
	"strings"
	"testing"

	"github.com/heroku-examples/dyno-proxy/internal/config"
)

func TestBuildRulePartsMethods(t *testing.T) {
	tests := []struct {
		name  string
		match config.Matcher
		want  []string
	}{
		{
			name:  "zero methods",
			match: config.Matcher{Path: "/api"},
			want:  []string{"Path(`/api`)"},
		},
		{
			name:  "one method",
			match: config.Matcher{Methods: []string{"GET"}},
			want:  []string{"Method(`GET`)"},
		},
		{
			name:  "two methods",
			match: config.Matcher{Methods: []string{"GET", "POST"}},
			want:  []string{"(Method(`GET`) || Method(`POST`))"},
		},
		{
			name:  "four methods with path",
			match: config.Matcher{Path: "/api", Methods: []string{"GET", "POST", "PUT", "DELETE"}},
			want: []string{
				"Path(`/api`)",
				"(Method(`GET`) || Method(`POST`) || Method(`PUT`) || Method(`DELETE`))",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRuleParts(tt.match)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildRuleParts() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildRulePartsBody(t *testing.T) {
	tests := []struct {
		name  string
		match config.Matcher
		want  []string
	}{
		{
			name:  "exact header value",
			match: config.Matcher{Body: map[string]string{"X-Env": "prod"}},
			want:  []string{"Header(`X-Body-X-Env`, `prod`)"},
		},
		{
			name:  "wildcard value uses HeaderRegexp",
			match: config.Matcher{Body: map[string]string{"X-Env": "prod*"}},
			want:  []string{"HeaderRegexp(`X-Body-X-Env`, `prod.*`)"},
		},
		{
			name:  "regex value passed through verbatim",
			match: config.Matcher{Body: map[string]string{"X-Env": "^v2.*$"}},
			want:  []string{"HeaderRegexp(`X-Body-X-Env`, `^v2.*$`)"},
		},
		{
			name:  "body field wildcard uses HeaderRegexp on X-Body header",
			match: config.Matcher{Body: map[string]string{"version": "v2*"}},
			want:  []string{"HeaderRegexp(`X-Body-version`, `v2.*`)"},
		},
		{
			name:  "body field exact value",
			match: config.Matcher{Body: map[string]string{"name": "canary"}},
			want:  []string{"Header(`X-Body-name`, `canary`)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRuleParts(tt.match)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildRuleParts() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestBuildRulePartsMethodsJoinedRule verifies the full rule string joined by
// " && " keeps the multi-method OR group as a single AND-operand.
func TestBuildRulePartsMethodsJoinedRule(t *testing.T) {
	parts := buildRuleParts(config.Matcher{Path: "/api", Methods: []string{"GET", "POST", "PUT", "DELETE"}})
	got := strings.Join(parts, " && ")
	want := "Path(`/api`) && (Method(`GET`) || Method(`POST`) || Method(`PUT`) || Method(`DELETE`))"
	if got != want {
		t.Errorf("joined rule = %q, want %q", got, want)
	}
}

// TestAddPlainRouteMethodRule is a regression test for the router.Rule string
// that Traefik actually parses. The original bug emitted a single multi-param
// Method(`GET`,`POST`,...) call, which Traefik rejects ("unexpected number of
// parameters"). This exercises the real render path (addPlainRoute), not just
// buildRuleParts, and asserts the multi-method matcher is a parenthesized OR of
// single-param Method() calls.
func TestAddPlainRouteMethodRule(t *testing.T) {
	tests := []struct {
		name    string
		methods []string
		want    string
	}{
		{
			name:    "single method needs no OR group",
			methods: []string{"GET"},
			want:    "Path(`/api/users`) && Method(`GET`)",
		},
		{
			name:    "two methods OR-joined and parenthesized",
			methods: []string{"POST", "PUT"},
			want:    "Path(`/api/users`) && (Method(`POST`) || Method(`PUT`))",
		},
		{
			name:    "four methods OR-joined and parenthesized",
			methods: []string{"GET", "POST", "PUT", "DELETE"},
			want:    "Path(`/api/data`) && (Method(`GET`) || Method(`POST`) || Method(`PUT`) || Method(`DELETE`))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/api/users"
			if len(tt.methods) == 4 {
				path = "/api/data"
			}
			st := &renderState{
				routers:      map[string]*router{},
				services:     map[string]*service{},
				middlewares:  map[string]*middleware{},
				localPlugins: map[string]string{},
				getenv:       func(string) string { return "" },
			}
			rt := config.Route{Match: config.Matcher{Path: path, Methods: tt.methods}}
			if err := st.addPlainRoute(1, rt); err != nil {
				t.Fatalf("addPlainRoute() error = %v", err)
			}
			r, ok := st.routers["route-1"]
			if !ok {
				t.Fatalf("router route-1 not created; routers = %#v", st.routers)
			}
			if r.Rule != tt.want {
				t.Errorf("router.Rule = %q, want %q", r.Rule, tt.want)
			}
		})
	}
}
