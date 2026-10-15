package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// plugins-local/src is the directory tree Traefik expects local Yaegi plugins
// in, relative to its working directory: it resolves an experimental
// localPlugins moduleName M (the plugin's full import path, e.g.
// github.com/heroku/dyno-proxy/plugins/body — see internal/traefik) as
// <cwd>/plugins-local/src/M.
const (
	pluginsLocalRoot = "plugins-local"     // symlink parent
	pluginsLocalSrc  = "plugins-local/src" // symlink to the bundled plugins tree
	pluginsSubdir    = "plugins"           // bundled plugins dir beside the binary
)

// linkLocalPlugins makes the bundled plugin sources discoverable by the load
// balancer. The buildpack ships the plugins next to the binary under their full
// module path (<install>/plugins/<module>, e.g.
// <install>/plugins/github.com/heroku/dyno-proxy/plugins/body — see
// buildpack/bin/build) and exports the install dir as DYNO_PROXY_DIR. We locate
// that dir and symlink its plugins/ tree to <workingDir>/plugins-local/src, so
// Traefik resolves a full-import-path moduleName M at plugins-local/src/M.
//
// It is best-effort: a missing install dir or plugins/ dir (e.g. local dev with
// no plugins bundled, or a run that uses no local plugins) is not an error — the
// Traefik simply won't find any local plugin, which only matters if a route
// actually references one.
func linkLocalPlugins(workingDir string, getenv func(string) string, logf func(level, msg string)) {
	src := discoverPluginsDir(getenv)
	if src == "" {
		return
	}

	destParent := filepath.Join(workingDir, pluginsLocalRoot)
	dest := filepath.Join(workingDir, pluginsLocalSrc)

	// If the correct symlink already exists, nothing to do.
	if existing, err := os.Readlink(dest); err == nil && existing == src {
		return
	}

	if err := os.MkdirAll(destParent, 0o755); err != nil {
		logf("warn", fmt.Sprintf("plugins: could not create %s: %v", destParent, err))
		return
	}
	// Replace any stale entry (old symlink or copied tree) before linking.
	_ = os.RemoveAll(dest)
	if err := os.Symlink(src, dest); err != nil {
		logf("warn", fmt.Sprintf("plugins: could not link %s -> %s: %v", dest, src, err))
		return
	}
	logf("info", fmt.Sprintf("plugins: linked %s -> %s", dest, src))
}

// discoverPluginsDir returns the bundled plugins directory, or "" if none is
// found. It prefers DYNO_PROXY_DIR (exported by the buildpack), then falls back
// to the directory of the running executable, then to a `dyno-proxy` on PATH —
// so a running dyno finds the plugins the buildpack placed beside it even when
// the env var is absent.
func discoverPluginsDir(getenv func(string) string) string {
	for _, base := range pluginBaseDirs(getenv) {
		if base == "" {
			continue
		}
		candidate := filepath.Join(base, pluginsSubdir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// pluginBaseDirs lists candidate install directories, most-authoritative first.
func pluginBaseDirs(getenv func(string) string) []string {
	dirs := []string{getenv("DYNO_PROXY_DIR")}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if p, err := exec.LookPath("dyno-proxy"); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	return dirs
}
