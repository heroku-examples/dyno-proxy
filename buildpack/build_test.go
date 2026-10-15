// Package buildpack holds Go tests that drive the real buildpack scripts
// (buildpack/bin/build) hermetically, with no network, docker, or pack. Each
// test runs bin/build against temp CNB_APP_DIR / CNB_LAYERS_DIR directories and
// asserts on the artifacts it produces (the exec.d hook, the layer env, and the
// absence of any Procfile or launch.toml change).
//
// The tests shell out to bash/tar/awk, so they t.Skip when those tools or the
// committed linux-amd64 shim are unavailable, mirroring internal/conformance/harness.go.
package buildpack

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// testTraefikVersion is an arbitrary version pinned by the tests. Seeding
// backend-version with it makes download_traefik hit its cache branch, so the
// build never reaches curl. It is deliberately not the real default so a drift
// in the default cannot make these tests silently start hitting the network.
const testTraefikVersion = "v9.9.9-test"

// buildEnv is a booted, hermetic invocation context for bin/build.
type buildEnv struct {
	appDir    string
	layersDir string
}

// newBuildEnv creates temp app/layers dirs and (unless skipDownloadCache is
// false) seeds the backend-version cache so bin/build never downloads.
func newBuildEnv(t *testing.T, seedCache bool) buildEnv {
	t.Helper()
	be := buildEnv{appDir: t.TempDir(), layersDir: t.TempDir()}
	if seedCache {
		layer := filepath.Join(be.layersDir, "dyno-proxy")
		if err := os.MkdirAll(layer, 0o755); err != nil {
			t.Fatalf("mkdir layer: %v", err)
		}
		if err := os.WriteFile(filepath.Join(layer, "backend-version"), []byte("traefik "+testTraefikVersion), 0o644); err != nil {
			t.Fatalf("seed backend-version: %v", err)
		}
	}
	return be
}

// run executes buildpack/bin/build with the CNB env vars set. extraEnv entries
// (e.g. "DISABLE_DYNO_PROXY=true") are appended after the defaults. It returns combined
// output and any error.
func (be buildEnv) run(t *testing.T, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "bin/build")
	cmd.Env = append(os.Environ(),
		"CNB_LAYERS_DIR="+be.layersDir,
		"CNB_APP_DIR="+be.appDir,
		"CNB_TARGET_ARCH=amd64",
		"TRAEFIK_VERSION="+testTraefikVersion,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (be buildEnv) writeProcfile(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(be.appDir, "Procfile"), []byte(content), 0o644); err != nil {
		t.Fatalf("write Procfile: %v", err)
	}
}

func (be buildEnv) readProcfile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(be.appDir, "Procfile"))
	if err != nil {
		t.Fatalf("read Procfile: %v", err)
	}
	return string(b)
}

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "tar", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH; skipping buildpack script test", tool)
		}
	}
	if _, err := os.Stat(filepath.Join("bin", "dyno-proxy-linux-amd64")); err != nil {
		t.Skip("bin/dyno-proxy-linux-amd64 shim missing; skipping buildpack script test")
	}
}

// TestBuildLeavesProcfileAlone asserts bin/build never reads or modifies the
// Procfile and never declares a process type (no launch.toml): the exec.d hook
// is the only startup mechanism, so the web process stays whatever the app or
// another buildpack defined, including a user-written `dyno-proxy run -- ...`.
func TestBuildLeavesProcfileAlone(t *testing.T) {
	requireTools(t)

	for _, procfile := range []string{
		"web: bundle exec rackup config.ru -p $PORT\nrelease: bundle exec rake db:migrate\n",
		"web: dyno-proxy run -- rails s\n",
		"web: grep -P \\d+ 'a' \"b\"\n",
		"worker: bundle exec sidekiq\n",
		"",
	} {
		t.Run(procfile, func(t *testing.T) {
			be := newBuildEnv(t, true)
			if procfile != "" {
				be.writeProcfile(t, procfile)
			}
			if out, err := be.run(t); err != nil {
				t.Fatalf("bin/build failed: %v\n%s", err, out)
			}
			if procfile == "" {
				if _, err := os.Stat(filepath.Join(be.appDir, "Procfile")); err == nil {
					t.Errorf("bin/build must not create a Procfile")
				}
			} else if got := be.readProcfile(t); got != procfile {
				t.Errorf("Procfile was modified:\n got: %q\nwant: %q", got, procfile)
			}
			if _, err := os.Stat(filepath.Join(be.layersDir, "launch.toml")); err == nil {
				t.Errorf("bin/build must not write launch.toml")
			}
			if _, err := os.Stat(filepath.Join(be.layersDir, "dyno-proxy", "exec.d", "dyno-proxy")); err != nil {
				t.Errorf("exec.d hook not generated: %v", err)
			}
		})
	}
}

// TestBuildBundlesPluginsAndExportsDir asserts bin/build copies the buildpack's
// plugins into <layer>/bin/plugins and publishes the install dir as
// DYNO_PROXY_DIR via a CNB env override, so later buildpacks and the running
// dyno can locate the bundled plugins.
func TestBuildBundlesPluginsAndExportsDir(t *testing.T) {
	requireTools(t)

	// bin/build bundles buildpack/plugins (resolved as SCRIPT_DIR/../plugins).
	// That directory is populated at package time by buildpack-server.rb and is
	// gitignored, so seed a fixture plugin (with its go.mod + .traefik.yml
	// manifest) to exercise the copy deterministically, then remove it. Traefik
	// resolves a local plugin under plugins-local/src/<moduleName>, so bin/build
	// copies each plugin into bin/plugins/<module> — its go.mod module path.
	fixture := filepath.Join("plugins", "fixture-plugin")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatalf("seed fixture plugin: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(fixture) })
	const fixtureModule = "example.com/test/fixture-plugin"
	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), []byte("module "+fixtureModule+"\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("seed go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture, ".traefik.yml"), []byte("type: middleware\n"), 0o644); err != nil {
		t.Fatalf("seed .traefik.yml: %v", err)
	}

	be := newBuildEnv(t, true)
	be.writeProcfile(t, "web: rackup\n")
	if out, err := be.run(t); err != nil {
		t.Fatalf("bin/build failed: %v\n%s", err, out)
	}

	layerBin := filepath.Join(be.layersDir, "dyno-proxy", "bin")

	// DYNO_PROXY_DIR env override points at the install (bin) dir.
	envFile := filepath.Join(be.layersDir, "dyno-proxy", "env", "DYNO_PROXY_DIR.override")
	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read DYNO_PROXY_DIR.override: %v", err)
	}
	if string(got) != layerBin {
		t.Errorf("DYNO_PROXY_DIR = %q, want %q", got, layerBin)
	}

	// Every plugin dir with a go.mod must appear under bin/plugins at its full
	// module path, so a moduleName that equals the import path resolves under
	// plugins-local/src. Dirs without a go.mod are not Go plugins and bin/build
	// skips them.
	srcEntries, err := os.ReadDir("plugins")
	if err != nil {
		t.Fatalf("read buildpack/plugins: %v", err)
	}
	for _, e := range srcEntries {
		if !e.IsDir() {
			continue
		}
		module := goModModule(filepath.Join("plugins", e.Name(), "go.mod"))
		if module == "" {
			continue
		}
		dst := filepath.Join(layerBin, "plugins", filepath.FromSlash(module))
		if info, err := os.Stat(dst); err != nil || !info.IsDir() {
			t.Errorf("plugin %q not bundled into %s (err=%v)", e.Name(), dst, err)
		}
	}
	// The fixture's manifest must survive the copy at its module path (guards
	// against a glob that silently drops dotfiles).
	if _, err := os.Stat(filepath.Join(layerBin, "plugins", filepath.FromSlash(fixtureModule), ".traefik.yml")); err != nil {
		t.Errorf(".traefik.yml not bundled with plugin: %v", err)
	}
}

// goModModule returns the module path declared in the go.mod at path, or "" if
// the file is absent or declares no module — mirroring bin/build's awk check.
func goModModule(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}

// stubDyno is the fake `dyno-proxy` the exec.d hook finds on PATH. It records
// its args and the env the daemon would see, then leaves a long-lived
// grandchild holding every fd it inherited, so any fd 1/2/3 the hook leaks to
// it keeps the launcher-faithful harness below from seeing EOF.
const stubDyno = `#!/bin/bash
{ echo "ARGS=$*"; echo "PORT=$PORT"; echo "APP_PORT=$APP_PORT"; } >> "$STUB_LOG"
sleep 30 &
echo $! >> "$STUB_LOG.pid"
exit "${STUB_EXIT:-0}"
`

// TestExecdHook drives the generated exec.d hook the way the CNB launcher does:
// fd 3 is a pipe (ExtraFiles), stdout/stderr are pipes, and the launcher reads
// all three to EOF and waits for exit. The contract:
//   - enabled web dyno with a numeric PORT: start `dyno-proxy background` with
//     the ORIGINAL PORT and APP_PORT (PORT+1 unless set), then write PORT and
//     APP_PORT = <app port> and DYNO_PROXY_FRONTED = "1" to fd 3 with the
//     defaults — but only if background succeeded;
//   - no/non-numeric PORT or already fronted: defaults only, nothing started;
//   - disabled or non-web dyno: nothing written, nothing started;
//   - never exit non-zero, and never leak fd 1/2/3 into the daemon (the stub
//     leaves a 30s grandchild; a leak fails the 5s deadline as a boot hang).
func TestExecdHook(t *testing.T) {
	requireTools(t)

	be := newBuildEnv(t, true)
	be.writeProcfile(t, "web: rackup\n")
	if out, err := be.run(t); err != nil {
		t.Fatalf("bin/build failed: %v\n%s", err, out)
	}
	hook := filepath.Join(be.layersDir, "dyno-proxy", "exec.d", "dyno-proxy")
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("exec.d hook not generated: %v", err)
	}

	// Static guard: the hook must not exec over itself or background with a
	// bare `&`/nohup; it spawns the daemon only through `dyno-proxy background`.
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "#!") {
			continue
		}
		if strings.HasPrefix(trimmed, "exec ") || strings.Contains(trimmed, "nohup ") || strings.HasSuffix(trimmed, "&") {
			t.Errorf("exec.d hook appears to exec/background a process (forbidden): %q", line)
		}
	}

	stubBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubBin, "dyno-proxy"), []byte(stubDyno), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// runHook returns the fd 3 output and the stub log ("" if never called).
	runHook := func(t *testing.T, env ...string) (string, string) {
		t.Helper()
		stubLog := filepath.Join(t.TempDir(), "stub.log")
		t.Cleanup(func() {
			pids, _ := os.ReadFile(stubLog + ".pid")
			for _, f := range strings.Fields(string(pids)) {
				if pid, err := strconv.Atoi(f); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		})

		pipe := func() (*os.File, *os.File) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			return r, w
		}
		r3, w3 := pipe()
		r1, w1 := pipe()
		r2, w2 := pipe()

		cmd := exec.Command(hook)
		cmd.ExtraFiles = []*os.File{w3}
		cmd.Stdout, cmd.Stderr = w1, w2
		cmd.Env = append(os.Environ(),
			"PATH="+stubBin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"STUB_LOG="+stubLog,
			"PORT=", "APP_PORT=", "DYNO_PROXY_FRONTED=", "DISABLE_DYNO_PROXY=",
			"DYNO_PROXY_LOG_LEVEL=", "STUB_EXIT=",
		)
		cmd.Env = append(cmd.Env, env...)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start hook: %v", err)
		}
		// Drop our copies of the write ends so EOF depends only on the hook
		// and anything it spawned.
		w1.Close()
		w2.Close()
		w3.Close()

		var fd3, stderr []byte
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); fd3, _ = io.ReadAll(r3) }()
		go func() { defer wg.Done(); _, _ = io.ReadAll(r1) }()
		go func() { defer wg.Done(); stderr, _ = io.ReadAll(r2) }()
		waitErr := make(chan error, 1)
		go func() { wg.Wait(); waitErr <- cmd.Wait() }()

		select {
		case err := <-waitErr:
			if err != nil {
				t.Fatalf("hook exited non-zero: %v\nstderr: %s", err, stderr)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			r1.Close()
			r2.Close()
			r3.Close()
			t.Fatalf("launcher would hang: fd 1/2/3 leaked to background child (no EOF within 5s)")
		}
		log, _ := os.ReadFile(stubLog)
		return string(fd3), string(log)
	}

	const defaults = "DYNO_PROXY_LOG_LEVEL = \"ERROR\"\n"
	fronted := func(port string) string {
		return defaults + "PORT = \"" + port + "\"\nAPP_PORT = \"" + port + "\"\nDYNO_PROXY_FRONTED = \"1\"\n"
	}

	t.Run("web dyno fronts app on PORT+1", func(t *testing.T) {
		got, log := runHook(t, "DYNO=web.1", "PORT=5000")
		if want := fronted("5001"); got != want {
			t.Errorf("fd3 output = %q, want %q", got, want)
		}
		// --app-pid is the launcher's pid ($PPID of the hook; here the test
		// process), which the launcher keeps when it execs the app.
		appPID := "ARGS=background --app-pid " + strconv.Itoa(os.Getpid()) + "\n"
		for _, want := range []string{appPID, "PORT=5000\n", "APP_PORT=5001\n"} {
			if !strings.Contains(log, want) {
				t.Errorf("stub log missing %q:\n%s", want, log)
			}
		}
	})

	t.Run("preset APP_PORT honoured", func(t *testing.T) {
		got, log := runHook(t, "DYNO=web.1", "PORT=5000", "APP_PORT=7000")
		if want := fronted("7000"); got != want {
			t.Errorf("fd3 output = %q, want %q", got, want)
		}
		if !strings.Contains(log, "PORT=5000\n") || !strings.Contains(log, "APP_PORT=7000\n") {
			t.Errorf("daemon should see PORT=5000 APP_PORT=7000, got:\n%s", log)
		}
	})

	// Fir runs a bind shim in the dyno on PORT+1 and PORT+2; APP_PORT must skip
	// ports something already listens on (the app would hit EADDRINUSE).
	t.Run("busy PORT+1 and PORT+2 skipped", func(t *testing.T) {
		var port int
		for attempt := 0; attempt < 50 && port == 0; attempt++ {
			l1, err := net.Listen("tcp", ":0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			p1 := l1.Addr().(*net.TCPAddr).Port
			l2, err := net.Listen("tcp", ":"+strconv.Itoa(p1+1))
			if err != nil {
				l1.Close()
				continue
			}
			if l3, err := net.Listen("tcp", ":"+strconv.Itoa(p1+2)); err == nil {
				l3.Close()
				port = p1 - 1
				t.Cleanup(func() { l1.Close(); l2.Close() })
			} else {
				l1.Close()
				l2.Close()
			}
		}
		if port == 0 {
			t.Fatal("could not reserve two consecutive ports")
		}
		ps, want := strconv.Itoa(port), strconv.Itoa(port+3)
		got, log := runHook(t, "DYNO=web-6846b64bb5-6tnhk", "PORT="+ps)
		if got != fronted(want) {
			t.Errorf("fd3 output = %q, want %q", got, fronted(want))
		}
		if !strings.Contains(log, "PORT="+ps+"\n") || !strings.Contains(log, "APP_PORT="+want+"\n") {
			t.Errorf("daemon should see PORT=%s APP_PORT=%s, got:\n%s", ps, want, log)
		}
	})

	t.Run("background failure leaves PORT alone", func(t *testing.T) {
		got, log := runHook(t, "DYNO=web.1", "PORT=5000", "STUB_EXIT=1")
		if got != defaults {
			t.Errorf("fd3 output = %q, want %q", got, defaults)
		}
		if !strings.Contains(log, "ARGS=background") {
			t.Errorf("stub should have been called, log: %q", log)
		}
	})

	for _, c := range []struct{ name, env string }{
		{"web dyno without PORT", "PORT="},
		{"non-numeric PORT", "PORT=abc"},
		{"already fronted", "DYNO_PROXY_FRONTED=1"},
	} {
		t.Run(c.name+" exports defaults only", func(t *testing.T) {
			env := []string{"DYNO=web.1", "PORT=5001", c.env}
			got, log := runHook(t, env...)
			if got != defaults {
				t.Errorf("fd3 output = %q, want %q", got, defaults)
			}
			if log != "" {
				t.Errorf("background must not start, stub log: %q", log)
			}
		})
	}

	for _, env := range []string{"DISABLE_DYNO_PROXY=true", "DISABLE_DYNO_PROXY=TRUE", "DISABLE_DYNO_PROXY=1"} {
		t.Run("disabled "+env+" exports nothing", func(t *testing.T) {
			got, log := runHook(t, "DYNO=web.1", "PORT=5000", env)
			if got != "" || log != "" {
				t.Errorf("%s should export and start nothing, got fd3=%q stub=%q", env, got, log)
			}
		})
	}

	// Exact web process-type match: Common Runtime/Cedar names dynos
	// <type>.<n>, Fir names them <type>-<hash>-<id>.
	for _, dyno := range []string{"web.1", "web.12", "web-7646c496dd-s5mvs"} {
		t.Run(dyno+" is fronted", func(t *testing.T) {
			got, log := runHook(t, "DYNO="+dyno, "PORT=5000")
			if want := fronted("5001"); got != want || log == "" {
				t.Errorf("%s should be fronted, got fd3=%q stub=%q", dyno, got, log)
			}
		})
	}

	for _, dyno := range []string{"worker.1", "run.1", "webhooks.1", "webpacker.1", "web", "",
		"run-857tn25qrk-vsb5z", "webhooks-7646c496dd-s5mvs", "web-api-7646c496dd-s5mvs", "web-api.1"} {
		t.Run(dyno+" exports nothing", func(t *testing.T) {
			got, log := runHook(t, "DYNO="+dyno, "PORT=5000")
			if got != "" || log != "" {
				t.Errorf("%s should export and start nothing, got fd3=%q stub=%q", dyno, got, log)
			}
		})
	}
}
