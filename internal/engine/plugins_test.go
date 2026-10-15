package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func noopLog(string, string) {}

// TestLinkLocalPlugins_FromDynoProxyDir verifies that DYNO_PROXY_DIR/plugins is
// linked into <workingDir>/plugins-local/src so Traefik can resolve a
// full-import-path moduleName under that tree.
func TestLinkLocalPlugins_FromDynoProxyDir(t *testing.T) {
	install := t.TempDir()
	pluginsSrc := filepath.Join(install, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsSrc, "catch"), 0o755); err != nil {
		t.Fatal(err)
	}

	wd := t.TempDir()
	getenv := func(k string) string {
		if k == "DYNO_PROXY_DIR" {
			return install
		}
		return ""
	}

	linkLocalPlugins(wd, getenv, noopLog)

	dest := filepath.Join(wd, pluginsLocalSrc)
	target, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", dest, err)
	}
	if target != pluginsSrc {
		t.Errorf("symlink target = %q, want %q", target, pluginsSrc)
	}
	// The linked tree exposes the plugin.
	if _, err := os.Stat(filepath.Join(dest, "catch")); err != nil {
		t.Errorf("catch plugin not reachable through link: %v", err)
	}
}

// TestLinkLocalPlugins_NoPlugins is a no-op (and must not error) when no bundled
// plugins directory can be discovered.
func TestLinkLocalPlugins_NoPlugins(t *testing.T) {
	wd := t.TempDir()
	getenv := func(k string) string {
		if k == "DYNO_PROXY_DIR" {
			return t.TempDir() // exists but has no plugins/ subdir
		}
		return ""
	}

	linkLocalPlugins(wd, getenv, noopLog)

	if _, err := os.Lstat(filepath.Join(wd, pluginsLocalSrc)); !os.IsNotExist(err) {
		t.Errorf("expected no link created, got err=%v", err)
	}
}

// TestLinkLocalPlugins_ReplacesStale replaces a stale entry (wrong link/dir)
// with the correct symlink.
func TestLinkLocalPlugins_ReplacesStale(t *testing.T) {
	install := t.TempDir()
	pluginsSrc := filepath.Join(install, "plugins")
	if err := os.MkdirAll(pluginsSrc, 0o755); err != nil {
		t.Fatal(err)
	}

	wd := t.TempDir()
	dest := filepath.Join(wd, pluginsLocalSrc)
	if err := os.MkdirAll(dest, 0o755); err != nil { // stale real dir
		t.Fatal(err)
	}

	getenv := func(k string) string {
		if k == "DYNO_PROXY_DIR" {
			return install
		}
		return ""
	}

	linkLocalPlugins(wd, getenv, noopLog)

	target, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("expected symlink at %s after replacing stale dir: %v", dest, err)
	}
	if target != pluginsSrc {
		t.Errorf("symlink target = %q, want %q", target, pluginsSrc)
	}
}
