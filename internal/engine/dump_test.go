package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDumpConfig_WithProject renders the config for a project.toml and asserts the
// output stream contains both files with their path headers.
func TestDumpConfig_WithProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "project.toml"), []byte(`
[[routes]]
path = "/api"
to = "https://api.example.com/"
`), 0o644); err != nil {
		t.Fatalf("write project.toml: %v", err)
	}

	var buf bytes.Buffer
	cfg := Config{
		WorkingDir: dir,
		Getenv:     func(string) string { return "" },
	}
	if err := DumpConfig(cfg, &buf); err != nil {
		t.Fatalf("DumpConfig: %v", err)
	}

	out := buf.String()
	staticPath := filepath.Join(dir, "project.dyno-proxy-static.toml")
	dynamicPath := filepath.Join(dir, "project.dyno-proxy-dynamic.toml")
	for _, want := range []string{
		"# " + staticPath,
		"# " + dynamicPath,
		`rule = "Path(` + "`/api`" + `)"`,
		"https://api.example.com/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q\n---\n%s", want, out)
		}
	}
}

// TestDumpConfig_NoProject renders from defaults alone when no project.toml exists,
// producing a valid config that still routes to the default app service.
func TestDumpConfig_NoProject(t *testing.T) {
	dir := t.TempDir() // empty: no project.toml

	var buf bytes.Buffer
	cfg := Config{
		WorkingDir: dir,
		Getenv:     func(string) string { return "" },
	}
	if err := DumpConfig(cfg, &buf); err != nil {
		t.Fatalf("DumpConfig: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "[http.services.app]") {
		t.Errorf("dump output missing default app service\n---\n%s", out)
	}
	if !strings.Contains(out, "# "+filepath.Join(dir, "project.dyno-proxy-static.toml")) {
		t.Errorf("dump output missing static config header\n---\n%s", out)
	}
}

// TestDumpConfig_AdminRoutes asserts the dashboard and metrics routes are only
// rendered when both admin credentials are set.
func TestDumpConfig_AdminRoutes(t *testing.T) {
	dump := func(env map[string]string) string {
		var buf bytes.Buffer
		cfg := Config{WorkingDir: t.TempDir(), Getenv: func(k string) string { return env[k] }}
		if err := DumpConfig(cfg, &buf); err != nil {
			t.Fatalf("DumpConfig: %v", err)
		}
		return buf.String()
	}
	for name, env := range map[string]map[string]string{
		"none":          {},
		"user only":     {"DYNO_PROXY_ADMIN_USER": "u"},
		"password only": {"DYNO_PROXY_ADMIN_PASSWORD": "p"},
	} {
		out := dump(env)
		for _, bad := range []string{".dyno-proxy/traefik", ".dyno-proxy/metrics", "dyno_proxy_admin", "prometheus"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: output contains %q", name, bad)
			}
		}
	}
	out := dump(map[string]string{"DYNO_PROXY_ADMIN_USER": "u", "DYNO_PROXY_ADMIN_PASSWORD": "p"})
	for _, want := range []string{".dyno-proxy/traefik", ".dyno-proxy/metrics", "dyno_proxy_admin", "prometheus"} {
		if !strings.Contains(out, want) {
			t.Errorf("with credentials: output missing %q", want)
		}
	}
}
