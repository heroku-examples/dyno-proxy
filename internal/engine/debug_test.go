package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heroku-examples/dyno-proxy/internal/traefik"
)

// TestLogRenderedConfig prints rendered TOML config files with per-file
// headers, and reports (without aborting) files that cannot be read.
func TestLogRenderedConfig(t *testing.T) {
	dir := t.TempDir()
	staticPath := filepath.Join(dir, "app.dyno-proxy-static.toml")
	if err := os.WriteFile(staticPath, []byte("[log]\nlevel = \"DEBUG\"\n"), 0o644); err != nil {
		t.Fatalf("write static: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.toml")

	var buf bytes.Buffer
	logRenderedConfig(&buf, traefik.Rendered{
		StaticConfigPath:  staticPath,
		DynamicConfigPath: missing,
	})

	out := buf.String()
	for _, want := range []string{
		"# static config: " + staticPath,
		"| [log]",
		"| level = \"DEBUG\"",
		"# dynamic config: " + missing,
		"unavailable:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q\n---\n%s", want, out)
		}
	}
}
