package conformance_test

// Routing conformance suite.
//
// This is the authoritative behavioral contract for dyno-proxy routing. Every
// scenario is a (RouteModel, HTTP request -> expected routing outcome) tuple,
// migrated from the legacy Go + Ruby functional tests (tests/routing,
// tests/percentage, tests/set, tests/plugins and the root *_test.go files):
//
//   - The render layer (build model -> traefik.Render -> assert no error, files
//     written) always runs, so an unavailable Traefik binary still exercises
//     config generation.
//   - The live-traffic layer boots the real Traefik via the conformance harness and
//     asserts the request is routed to the expected in-process upstream. It is
//     t.Skip'd (by Boot) when the traefik binary is not installed on this
//     machine, so the suite still compiles and runs everywhere.
//
// CI installs Traefik, so the entire table must pass there.

import (
	"net/http"
	"testing"
	"time"

	"github.com/heroku-examples/dyno-proxy/internal/config"
	"github.com/heroku-examples/dyno-proxy/internal/conformance"
	"github.com/heroku-examples/dyno-proxy/internal/traefik"
)

// env holds the in-process upstreams a scenario routes between. app is the
// default upstream; proxy/proxyB are additional targets. Their httptest URLs are
// only known at runtime, so scenarios build their RouteModel from an *env.
type env struct {
	app    *conformance.Stub
	proxy  *conformance.Stub
	proxyB *conformance.Stub
}

func newEnv(t *testing.T) *env {
	return &env{
		app:    conformance.NewStub(t, "app"),
		proxy:  conformance.NewStub(t, "proxy"),
		proxyB: conformance.NewStub(t, "proxyB"),
	}
}

// stub returns the stub for a given upstream label.
func (e *env) stub(label string) *conformance.Stub {
	switch label {
	case "app":
		return e.app
	case "proxy":
		return e.proxy
	case "proxyB":
		return e.proxyB
	default:
		return nil
	}
}

// check is one request against a booted proxy and its expected outcome.
type check struct {
	name    string
	method  string // default GET
	path    string
	headers map[string]string

	// wantUpstream is the label of the stub expected to serve the request. Empty
	// means "no upstream" (e.g. an auth rejection); pair with wantStatus.
	wantUpstream string
	// wantStatus, when non-zero, asserts the HTTP status code.
	wantStatus int
	// wantReqPath, when non-empty, asserts the path the upstream actually saw
	// (for strip-prefix / replace-path).
	wantReqPath string
	// wantReqHeader, when non-empty, asserts a header the upstream saw
	// (for set-header).
	wantReqHeader [2]string
}

// scenario is one row of the conformance table.
type scenario struct {
	name string
	// build produces the RouteModel from the runtime upstream URLs.
	build func(e *env) config.RouteModel
	// checks are the live-traffic assertions (skipped when the LB binary is
	// absent). May be empty for render-only scenarios.
	checks []check
	// mirrorTargets, when set, are stub labels expected to receive mirrored
	// (shadow) copies of traffic after the checks run.
	mirrorTargets []string
}

// scenarios is the full conformance table.
var scenarios = []scenario{
	{
		name: "default-and-path-routing",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{Match: config.Matcher{Path: "/api"}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
					{Match: config.Matcher{Path: "/path-prefix*"}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
					{Match: config.Matcher{Path: ".*/wildcard.*"}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			{name: "default route -> app", path: "/", wantUpstream: "app"},
			{name: "unmatched path -> app", path: "/home", wantUpstream: "app"},
			{name: "exact path -> proxy", path: "/api", wantUpstream: "proxy"},
			{name: "exact path is not a prefix", path: "/api/users", wantUpstream: "app"},
			{name: "prefix root -> proxy", path: "/path-prefix", wantUpstream: "proxy"},
			{name: "prefix child -> proxy", path: "/path-prefix/deep/nested", wantUpstream: "proxy"},
			{name: "non-matching prefix -> app", path: "/different-path", wantUpstream: "app"},
			{name: "regex root -> proxy", path: "/wildcard", wantUpstream: "proxy"},
			{name: "regex mid-path -> proxy", path: "/anything/wildcard/more", wantUpstream: "proxy"},
			{name: "regex no leading slash -> app", path: "/something-wildcard-else", wantUpstream: "app"},
		},
	},
	{
		name: "header-routing",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{Match: config.Matcher{Headers: map[string]string{"x-custom-header": "proxy-server"}}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			{name: "matching header -> proxy", path: "/any", headers: map[string]string{"x-custom-header": "proxy-server"}, wantUpstream: "proxy"},
			{name: "matching header on root -> proxy", path: "/", headers: map[string]string{"x-custom-header": "proxy-server"}, wantUpstream: "proxy"},
			{name: "wrong header value -> app", path: "/any", headers: map[string]string{"x-custom-header": "different"}, wantUpstream: "app"},
			{name: "missing header -> app", path: "/any", wantUpstream: "app"},
			{name: "different header -> app", path: "/any", headers: map[string]string{"x-other": "value"}, wantUpstream: "app"},
		},
	},
	{
		name: "query-routing",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{Match: config.Matcher{Query: map[string]string{"x-exact": "proxy-server"}}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
					{Match: config.Matcher{Query: map[string]string{"x-wild": "yo-*"}}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
					{Match: config.Matcher{Query: map[string]string{"x-empty": ""}}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			{name: "exact query -> proxy", path: "/any?x-exact=proxy-server", wantUpstream: "proxy"},
			{name: "wrong query value -> app", path: "/any?x-exact=nope", wantUpstream: "app"},
			{name: "wildcard query -> proxy", path: "/any?x-wild=yo-anything", wantUpstream: "proxy"},
			{name: "wildcard wrong prefix -> app", path: "/any?x-wild=hello-x", wantUpstream: "app"},
			{name: "empty-value query present -> proxy", path: "/any?x-empty=", wantUpstream: "proxy"},
			{name: "empty-value query bare -> proxy", path: "/any?x-empty", wantUpstream: "proxy"},
			{name: "no query -> app", path: "/any", wantUpstream: "app"},
			{name: "different query -> app", path: "/any?other=1", wantUpstream: "app"},
		},
	},
	{
		name: "method-routing",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{Match: config.Matcher{Path: "/resource", Methods: []string{"POST"}}, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			{name: "matching method -> proxy", method: "POST", path: "/resource", wantUpstream: "proxy"},
			{name: "other method -> app", method: "GET", path: "/resource", wantUpstream: "app"},
		},
	},
	{
		// Percentage: 100 (or unset, per the IR contract where 0 == "unset ->
		// 100") sends all matching traffic to the experiment target. A genuine 0%
		// route is not expressible in this IR (Percentage 0 means unset), so it is
		// dropped upstream in the source/merge layer and is intentionally not
		// exercised here.
		name: "percentage-100",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{Match: config.Matcher{Path: "/full"}, Percentage: 100, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			{name: "100% -> proxy", path: "/full", wantUpstream: "proxy"},
			{name: "unmatched still -> app", path: "/other", wantUpstream: "app"},
		},
	},
	{
		name: "percentage-50-deterministic",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					// 50% split: matched on the last two hex chars of x-request-id.
					{Match: config.Matcher{Path: "/split"}, Percentage: 50, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			// 50% pattern is [0-7][0-9a-f]$: a request-id ending in "00" is inside
			// the experiment bucket, one ending in "ff" is outside it. This makes
			// the statistical split deterministic for a single request.
			{name: "in-bucket id -> proxy", path: "/split", headers: map[string]string{"x-request-id": "00000000-0000-0000-0000-000000000000"}, wantUpstream: "proxy"},
			{name: "out-of-bucket id -> app", path: "/split", headers: map[string]string{"x-request-id": "ffffffff-ffff-ffff-ffff-ffffffffffff"}, wantUpstream: "app"},
		},
	},
	{
		name:          "mirror",
		mirrorTargets: []string{"proxy"},
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					// Mirror all traffic at /shadow to proxy while app still serves.
					{Match: config.Matcher{Path: "/shadow"}, Mirror: true, Targets: []config.Upstream{{URL: e.proxy.URL()}}},
				},
			}
		},
		checks: []check{
			{name: "mirror still serves from app", path: "/shadow", wantUpstream: "app"},
		},
	},
	{
		name: "set-header",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{
						Match:   config.Matcher{Path: "/inject*"},
						Targets: []config.Upstream{{URL: e.proxy.URL()}},
						Middlewares: []config.Middleware{
							{Kind: config.CapSetHeader, Config: map[string]any{"header": "X-Injected", "value": "hello"}},
						},
					},
				},
			}
		},
		checks: []check{
			{name: "header injected upstream", path: "/inject", wantUpstream: "proxy", wantReqHeader: [2]string{"X-Injected", "hello"}},
		},
	},
	{
		name: "replace-path",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{
						Match:   config.Matcher{Path: "/rewrite*"},
						Targets: []config.Upstream{{URL: e.proxy.URL()}},
						Middlewares: []config.Middleware{
							{Kind: config.CapSetPath, Config: map[string]any{"path": "/replaced"}},
						},
					},
				},
			}
		},
		checks: []check{
			{name: "path rewritten upstream", path: "/rewrite/original", wantUpstream: "proxy", wantReqPath: "/replaced"},
		},
	},
	{
		name: "strip-prefix",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{
						Match:   config.Matcher{Path: "/strip*"},
						Targets: []config.Upstream{{URL: e.proxy.URL()}},
						Middlewares: []config.Middleware{
							{Kind: config.CapRawTraefikPlugin, Config: map[string]any{"raw": map[string]interface{}{"stripPrefix": map[string]interface{}{"prefixes": []string{"/strip"}}}}},
						},
					},
				},
			}
		},
		checks: []check{
			{name: "prefix stripped upstream", path: "/strip/tail", wantUpstream: "proxy", wantReqPath: "/tail"},
		},
	},
	{
		name: "basic-auth",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{
						Match:   config.Matcher{Path: "/guarded*"},
						Targets: []config.Upstream{{URL: e.proxy.URL()}},
						Middlewares: []config.Middleware{
							// htpasswd for user "u" / password "p" (bcrypt-free MD5 apr1).
							{Kind: config.CapRawTraefikPlugin, Config: map[string]any{"raw": map[string]interface{}{"basicAuth": map[string]interface{}{"users": []string{"u:$apr1$xyz$Oj0mQ0AqQ2xY9zLwq0eMv1"}}}}},
						},
					},
				},
			}
		},
		checks: []check{
			{name: "no credentials -> 401", path: "/guarded", wantStatus: http.StatusUnauthorized},
		},
	},
	{
		name: "raw-traefik-plugin-render-only",
		build: func(e *env) config.RouteModel {
			return config.RouteModel{
				DefaultServiceURL: e.app.URL(),
				Routes: []config.Route{
					{
						Match:   config.Matcher{Path: "/raw*"},
						Targets: []config.Upstream{{URL: e.proxy.URL()}},
						Middlewares: []config.Middleware{
							{Kind: config.CapRawTraefikPlugin, Config: map[string]any{
								"raw": map[string]interface{}{
									"headers": map[string]interface{}{
										"customRequestHeaders": map[string]interface{}{"X-Raw": "1"},
									},
								},
							}},
						},
					},
				},
			}
		},
		// No live checks: exercises render only (a raw plugin block may reference
		// external modules that are not present in a bare test environment).
	},
}

// TestConformance runs the full conformance table.
func TestConformance(t *testing.T) {
	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			runScenario(t, sc)
		})
	}
}

func runScenario(t *testing.T, sc scenario) {
	e := newEnv(t)
	model := sc.build(e)

	// Render layer: always runs, even without the traefik binary.
	dir := t.TempDir()
	rendered, err := traefik.Render(model, traefik.Options{
		OutputDir: dir,
		Port:      "0",
		LogLevel:  "ERROR",
		BaseName:  "conformance",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(rendered.Files) == 0 {
		t.Fatal("render produced no files")
	}

	if len(sc.checks) == 0 && len(sc.mirrorTargets) == 0 {
		return // render-only scenario
	}

	// Live-traffic layer: Boot skips the test if the LB binary is absent.
	h := conformance.Boot(t, model, conformance.BootOptions{})

	for _, chk := range sc.checks {
		chk := chk
		t.Run(chk.name, func(t *testing.T) {
			method := chk.method
			if method == "" {
				method = http.MethodGet
			}
			resp := h.Do(t, method, chk.path, chk.headers, nil)
			label := conformance.ReadUpstream(t, resp)

			if chk.wantStatus != 0 && resp.StatusCode != chk.wantStatus {
				t.Errorf("status: got %d, want %d", resp.StatusCode, chk.wantStatus)
			}
			if chk.wantUpstream != "" && label != chk.wantUpstream {
				t.Errorf("upstream: got %q, want %q", label, chk.wantUpstream)
			}
			if chk.wantReqPath != "" || chk.wantReqHeader[0] != "" {
				assertLastRequest(t, e.stub(chk.wantUpstream), chk)
			}
		})
	}

	for _, label := range sc.mirrorTargets {
		label := label
		t.Run("mirror-to-"+label, func(t *testing.T) {
			stub := e.stub(label)
			if stub == nil {
				t.Fatalf("unknown mirror target %q", label)
			}
			// Mirrored copies are delivered asynchronously; poll briefly.
			if !waitForCount(stub, 1, 3*time.Second) {
				t.Errorf("mirror target %q received no traffic", label)
			}
		})
	}
}

// assertLastRequest checks the most recent request the stub for chk.wantUpstream
// received against chk's request-shape expectations.
func assertLastRequest(t *testing.T, stub *conformance.Stub, chk check) {
	t.Helper()
	if stub == nil {
		t.Fatalf("no stub for upstream %q", chk.wantUpstream)
	}
	reqs := stub.Requests()
	if len(reqs) == 0 {
		t.Fatalf("stub %q received no requests", chk.wantUpstream)
	}
	last := reqs[len(reqs)-1]
	if chk.wantReqPath != "" && last.Path != chk.wantReqPath {
		t.Errorf("upstream path: got %q, want %q", last.Path, chk.wantReqPath)
	}
	if chk.wantReqHeader[0] != "" {
		got := last.Headers.Get(chk.wantReqHeader[0])
		if got != chk.wantReqHeader[1] {
			t.Errorf("upstream header %s: got %q, want %q", chk.wantReqHeader[0], got, chk.wantReqHeader[1])
		}
	}
}

// waitForCount polls stub until it has received at least n requests or timeout.
func waitForCount(stub *conformance.Stub, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if stub.Count() >= n {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return stub.Count() >= n
}
