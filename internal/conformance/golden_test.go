package conformance_test

// Render golden tests.
//
// These are fast (no process boot): each canonical RouteModel is rendered and
// the emitted config files are compared byte-for-byte against a checked-in
// golden file under testdata/golden/. Absolute
// temp paths are normalized so the goldens are stable across machines.
//
// Regenerate after an intentional rendering change:
//
//	go test ./internal/conformance -run TestRenderGolden -update
//
// Golden diffs make config regressions reviewable in the diff, complementing the
// behavioral conformance table.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heroku-examples/dyno-proxy/internal/config"
	"github.com/heroku-examples/dyno-proxy/internal/traefik"
)

var update = flag.Bool("update", false, "update golden files")

// goldenModels are the canonical models exercised by the golden tests. They use
// fixed upstream URLs (not runtime ports) so rendering is deterministic.
var goldenModels = []struct {
	name  string
	model config.RouteModel
}{
	{
		name: "basic-routing",
		model: config.RouteModel{
			DefaultServiceURL: "http://localhost:5000",
			Routes: []config.Route{
				{Match: config.Matcher{Path: "/api"}, Targets: []config.Upstream{{URL: "http://localhost:8080"}}},
				{Match: config.Matcher{Path: "/path-prefix*"}, Targets: []config.Upstream{{URL: "http://localhost:8081"}}},
				{Match: config.Matcher{Path: ".*/wildcard.*"}, Targets: []config.Upstream{{URL: "http://localhost:8082"}}},
				{Match: config.Matcher{Headers: map[string]string{"x-custom-header": "proxy-server"}}, Targets: []config.Upstream{{URL: "http://localhost:8083"}}},
				{Match: config.Matcher{Query: map[string]string{"x-q": "yo-*"}}, Targets: []config.Upstream{{URL: "http://localhost:8084"}}},
				{Match: config.Matcher{Methods: []string{"POST"}, Path: "/m"}, Targets: []config.Upstream{{URL: "http://localhost:8085"}}},
			},
		},
	},
	{
		name: "percentage-and-mirror",
		model: config.RouteModel{
			DefaultServiceURL: "http://localhost:5000",
			Routes: []config.Route{
				{Match: config.Matcher{Path: "/exp"}, Percentage: 50, Targets: []config.Upstream{{URL: "http://localhost:9001"}}},
				{Match: config.Matcher{Path: "/shadow"}, Mirror: true, Targets: []config.Upstream{{URL: "http://localhost:9002"}}},
			},
		},
	},
	{
		name: "middlewares",
		model: config.RouteModel{
			DefaultServiceURL: "http://localhost:5000",
			Routes: []config.Route{
				{
					Match:   config.Matcher{Path: "/x*"},
					Targets: []config.Upstream{{URL: "http://localhost:8080"}},
					Middlewares: []config.Middleware{
						{Kind: config.CapSetHeader, Config: map[string]any{"header": "X-A", "value": "1"}},
						{Kind: config.CapSetPath, Config: map[string]any{"path": "/y"}},
						{Kind: config.CapRawTraefikPlugin, Config: map[string]any{"raw": map[string]interface{}{"stripPrefix": map[string]interface{}{"prefixes": []string{"/x"}}}}},
					},
				},
			},
		},
	},
	{
		name: "route-ids",
		model: config.RouteModel{
			DefaultServiceURL: "http://localhost:5000",
			Routes: []config.Route{
				{ID: "api", Match: config.Matcher{Path: "/api"}, Targets: []config.Upstream{{URL: "http://localhost:8080"}}},
				{Match: config.Matcher{Path: "/no-id"}, Targets: []config.Upstream{{URL: "http://localhost:8081"}}},
				{
					ID:      "with-mw",
					Match:   config.Matcher{Path: "/x*"},
					Targets: []config.Upstream{{URL: "http://localhost:8082"}},
					Middlewares: []config.Middleware{
						{Kind: config.CapSetHeader, Config: map[string]any{"header": "X-A", "value": "1"}},
						{Kind: config.CapSetPath, Config: map[string]any{"path": "/y"}},
						{Kind: config.CapRawTraefikPlugin, Config: map[string]any{"raw": map[string]interface{}{"stripPrefix": map[string]interface{}{"prefixes": []string{"/x"}}}}},
					},
				},
				{ID: "canary.v2", Match: config.Matcher{Path: "/exp"}, Percentage: 50, Targets: []config.Upstream{{URL: "http://localhost:9001"}}},
				{ID: "shadow", Match: config.Matcher{Path: "/shadow"}, Mirror: true, Targets: []config.Upstream{{URL: "http://localhost:9002"}}},
			},
		},
	},
	{
		name: "dashboard-and-metrics",
		model: config.RouteModel{
			DefaultServiceURL: "http://localhost:5000",
			Dashboard: config.DashboardRoute{
				PathPrefix:     "/.dyno-proxy/traefik",
				User:           "dyno_proxy_admin",
				HashedPassword: "$apr1$abc$def",
			},
			Metrics: config.MetricsRoute{
				PathPrefix: "/.dyno-proxy/metrics",
			},
		},
	},
}

func TestRenderGolden(t *testing.T) {
	for _, gm := range goldenModels {
		gm := gm
		t.Run(gm.name, func(t *testing.T) {
			dir := t.TempDir()
			rendered, err := traefik.Render(gm.model, traefik.Options{
				OutputDir: dir,
				Port:      "8888",
				LogLevel:  "INFO",
				BaseName:  "app",
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			// Concatenate every rendered file (sorted by base name) into one
			// normalized golden document so a single file captures the render.
			got := normalizeRender(t, dir, rendered.Files)

			goldenPath := filepath.Join("testdata", "golden", gm.name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("render mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", gm.name, got, want)
			}
		})
	}
}

// normalizeRender reads each rendered file and produces a single deterministic
// document, replacing the absolute temp dir with a stable placeholder so goldens
// are machine-independent.
func normalizeRender(t *testing.T, dir string, files []string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read rendered file %s: %v", f, err)
		}
		content := strings.ReplaceAll(string(data), dir, "<DIR>")
		b.WriteString("=== FILE: ")
		b.WriteString(filepath.Base(f))
		b.WriteString(" ===\n")
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}
