// This file holds Go tests that drive the classic buildpack script
// (buildpack/bin/compile) hermetically, with no network. compile takes
// positional args BUILD_DIR CACHE_DIR ENV_DIR and writes a .profile.d script
// that starts `dyno-proxy background`; it never reads or modifies the Procfile.
// These tests assert the Procfile is left alone and the .profile.d script's
// behavior when sourced.
//
// The tests shell out to bash/awk, so they t.Skip when those tools or the
// committed platform shim are unavailable, mirroring build_test.go.
package buildpack

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// compileEnv is a booted, hermetic invocation context for bin/compile.
type compileEnv struct {
	buildDir string
	cacheDir string
	envDir   string
}

// newCompileEnv creates temp build/cache/env dirs and (when seedCache is true)
// seeds the CACHE_DIR backend binary + backend-version so bin/compile hits its
// cache branch and never downloads.
func newCompileEnv(t *testing.T, seedCache bool) compileEnv {
	t.Helper()
	ce := compileEnv{buildDir: t.TempDir(), cacheDir: t.TempDir(), envDir: t.TempDir()}
	if seedCache {
		// compile's needs_download() returns false only when BOTH the cached
		// binary file and a matching backend-version file exist. compile then
		// cp's the cached binary into the slug, so it must be a real file.
		cached := filepath.Join(ce.cacheDir, "traefik-"+testTraefikVersion)
		if err := os.WriteFile(cached, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("seed cached backend binary: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ce.cacheDir, "backend-version"), []byte("traefik "+testTraefikVersion), 0o644); err != nil {
			t.Fatalf("seed backend-version: %v", err)
		}
	}
	return ce
}

// run executes buildpack/bin/compile with the classic positional args.
func (ce compileEnv) run(t *testing.T, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "bin/compile", ce.buildDir, ce.cacheDir, ce.envDir)
	cmd.Env = append(os.Environ(),
		"TRAEFIK_VERSION="+testTraefikVersion,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (ce compileEnv) writeProcfile(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ce.buildDir, "Procfile"), []byte(content), 0o644); err != nil {
		t.Fatalf("write Procfile: %v", err)
	}
}

func (ce compileEnv) readProcfile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ce.buildDir, "Procfile"))
	if err != nil {
		t.Fatalf("read Procfile: %v", err)
	}
	return string(b)
}

func (ce compileEnv) procfileExists() bool {
	_, err := os.Stat(filepath.Join(ce.buildDir, "Procfile"))
	return err == nil
}

// compilePlatformArch mirrors bin/compile's uname-based mapping using the Go
// runtime, so the test knows which committed shim compile will try to copy.
func compilePlatformArch() (string, string) {
	arch := runtime.GOARCH // amd64 / arm64 already match compile's naming
	return runtime.GOOS, arch
}

// requireCompileTools skips the test when the tools bin/compile needs, or the
// committed platform shim it copies, are unavailable.
func requireCompileTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH; skipping compile script test", tool)
		}
	}
	platform, arch := compilePlatformArch()
	shim := filepath.Join("bin", "dyno-proxy-"+platform+"-"+arch)
	if _, err := os.Stat(shim); err != nil {
		t.Skipf("%s shim missing; skipping compile script test", shim)
	}
}

// TestCompileLeavesProcfileAlone asserts bin/compile never reads or modifies
// the Procfile, never writes a launch.toml, and writes the same .profile.d
// script whether or not there is a Procfile (it is the only startup mechanism,
// including for a user-written `dyno-proxy run -- ...` web line).
func TestCompileLeavesProcfileAlone(t *testing.T) {
	requireCompileTools(t)

	var want string
	for _, procfile := range []string{
		"",
		"web: bundle exec rackup config.ru -p $PORT\nrelease: bundle exec rake db:migrate\n",
		"web: dyno-proxy run -- rails s\n",
		"web: grep -P \\d+ 'a' \"b\"\n",
		"worker: bundle exec sidekiq\n",
	} {
		ce := newCompileEnv(t, true)
		if procfile != "" {
			ce.writeProcfile(t, procfile)
		}
		if out, err := ce.run(t); err != nil {
			t.Fatalf("bin/compile failed: %v\n%s", err, out)
		}
		if procfile == "" {
			if ce.procfileExists() {
				t.Errorf("bin/compile must not create a Procfile when none exists")
			}
		} else if got := ce.readProcfile(t); got != procfile {
			t.Errorf("Procfile was modified:\n got: %q\nwant: %q", got, procfile)
		}
		if _, err := os.Stat(filepath.Join(ce.buildDir, "launch.toml")); err == nil {
			t.Errorf("Procfile %q: bin/compile must not write launch.toml", procfile)
		}
		got := ce.readProfileScript(t)
		if want == "" {
			want = got
		} else if got != want {
			t.Errorf("Procfile %q: profile script differs from the no-Procfile one:\n got: %q\nwant: %q", procfile, got, want)
		}
	}
}

func (ce compileEnv) readProfileScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ce.buildDir, ".profile.d", "dyno-proxy-defaults.sh"))
	if err != nil {
		t.Fatalf("read .profile.d script: %v", err)
	}
	return string(b)
}

// TestCompileProfileBackground covers the .profile.d script: it must parse
// under POSIX sh and, when sourced, start `dyno-proxy background` and point
// PORT at APP_PORT (exporting DYNO_PROXY_FRONTED=1) only on a web dyno with the
// proxy on, not already fronted, and the launcher succeeding. A Procfile is
// present to show it does not change this. A stub dyno-proxy records its calls.
func TestCompileProfileBackground(t *testing.T) {
	requireCompileTools(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found in PATH")
	}

	ce := newCompileEnv(t, true)
	ce.writeProcfile(t, "web: dyno-proxy run -- rails s\n")
	out, err := ce.run(t)
	if err != nil {
		t.Fatalf("bin/compile failed: %v\n%s", err, out)
	}
	script := filepath.Join(ce.buildDir, ".profile.d", "dyno-proxy-defaults.sh")
	if b, err := exec.Command("sh", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("sh -n %s: %v\n%s", script, err, b)
	}
	got := ce.readProfileScript(t)
	for _, want := range []string{"dyno-proxy background", "export PORT=$APP_PORT", "export APP_PORT\n", "export DYNO_PROXY_FRONTED=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("profile script missing %q:\n%s", want, got)
		}
	}

	cases := []struct {
		name        string
		env         []string
		stubExit    int
		wantPort    string
		wantCalled  bool
		wantFronted string
	}{
		{"web dyno", []string{"DYNO=web.1", "PORT=5000"}, 0, "5001:5001", true, "1"},
		{"explicit APP_PORT", []string{"DYNO=web.1", "PORT=5000", "APP_PORT=6000"}, 0, "6000:6000", true, "1"},
		{"already fronted", []string{"DYNO=web.1", "PORT=5001", "APP_PORT=5001", "DYNO_PROXY_FRONTED=1"}, 0, "5001:5001", false, "1"},
		{"disabled", []string{"DYNO=web.1", "PORT=5000", "DISABLE_DYNO_PROXY=true"}, 0, "5000:", false, ""},
		{"disabled via DISABLE_DYNO_PROXY=true", []string{"DYNO=web.1", "PORT=5000", "DISABLE_DYNO_PROXY=true"}, 0, "5000:", false, ""},
		{"disabled via DISABLE_DYNO_PROXY=TRUE", []string{"DYNO=web.1", "PORT=5000", "DISABLE_DYNO_PROXY=TRUE"}, 0, "5000:", false, ""},
		{"disabled via DISABLE_DYNO_PROXY=1", []string{"DYNO=web.1", "PORT=5000", "DISABLE_DYNO_PROXY=1"}, 0, "5000:", false, ""},
		{"DISABLE_DYNO_PROXY=false keeps it on", []string{"DYNO=web.1", "PORT=5000", "DISABLE_DYNO_PROXY=false"}, 0, "5001:5001", true, "1"},
		{"worker dyno", []string{"DYNO=worker.1", "PORT=5000"}, 0, "5000:", false, ""},
		{"webhooks dyno is not web", []string{"DYNO=webhooks.1", "PORT=5000"}, 0, "5000:", false, ""},
		{"webpacker dyno is not web", []string{"DYNO=webpacker.1", "PORT=5000"}, 0, "5000:", false, ""},
		{"web-api dyno is not web", []string{"DYNO=web-api.1", "PORT=5000"}, 0, "5000:", false, ""},
		{"one-off run dyno", []string{"DYNO=run.1", "PORT=5000"}, 0, "5000:", false, ""},
		{"Fir web dyno", []string{"DYNO=web-7646c496dd-s5mvs", "PORT=5000"}, 0, "5001:5001", true, "1"},
		{"Fir webhooks dyno is not web", []string{"DYNO=webhooks-7646c496dd-s5mvs", "PORT=5000"}, 0, "5000:", false, ""},
		{"Fir web-api dyno is not web", []string{"DYNO=web-api-7646c496dd-s5mvs", "PORT=5000"}, 0, "5000:", false, ""},
		// A failed launch leaves PORT alone so the app still binds the router port.
		{"launcher fails", []string{"DYNO=web.1", "PORT=5000"}, 1, "5000:5001", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubDir := t.TempDir()
			calls := filepath.Join(stubDir, "calls")
			stub := "#!/bin/sh\necho \"$* PORT=$PORT APP_PORT=$APP_PORT\" >> " + calls + "\nexit " + strconv.Itoa(c.stubExit) + "\n"
			if err := os.WriteFile(filepath.Join(stubDir, "dyno-proxy"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", `. "$1"; echo "$PORT:$APP_PORT"; echo "$$"; echo "$_dyno_proxy_web"; echo "$DYNO_PROXY_FRONTED"`, "sh", script)
			cmd.Env = append([]string{"PATH=" + stubDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}, c.env...)
			b, err := cmd.Output()
			if err != nil {
				t.Fatalf("sourcing profile script: %v", err)
			}
			lines := strings.Split(string(b), "\n")
			if len(lines) < 4 {
				t.Fatalf("unexpected output %q", b)
			}
			if got := lines[0]; got != c.wantPort {
				t.Errorf("PORT:APP_PORT after sourcing = %q, want %q", got, c.wantPort)
			}
			if got := lines[3]; got != c.wantFronted {
				t.Errorf("DYNO_PROXY_FRONTED after sourcing = %q, want %q", got, c.wantFronted)
			}
			shellPID := lines[1]
			if lines[2] != "" {
				t.Errorf("profile script leaked its _dyno_proxy_web helper var into the env: %q", lines[2])
			}
			rec, _ := os.ReadFile(calls)
			if called := len(rec) > 0; called != c.wantCalled {
				t.Fatalf("dyno-proxy called = %v, want %v (%q)", called, c.wantCalled, rec)
			}
			// The launcher must see the router PORT (the LB binds it), not APP_PORT.
			// --app-pid is the sourcing shell ($$), i.e. the dyno main process
			// that goes on to run the web command.
			if want := "background --app-pid " + shellPID + " PORT=5000 "; c.wantCalled && !strings.HasPrefix(string(rec), want) {
				t.Errorf("dyno-proxy invoked as %q, want prefix %q", rec, want)
			}
		})
	}
}

// TestCompileProfileSkipsBusyAppPort: with APP_PORT unset, the .profile.d
// script must skip ports above PORT that something already
// listens on (the app would hit EADDRINUSE), like the exec.d hook and `run`.
func TestCompileProfileSkipsBusyAppPort(t *testing.T) {
	requireCompileTools(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found in PATH")
	}
	ce := newCompileEnv(t, true)
	if out, err := ce.run(t); err != nil {
		t.Fatalf("bin/compile failed: %v\n%s", err, out)
	}
	script := filepath.Join(ce.buildDir, ".profile.d", "dyno-proxy-defaults.sh")

	var port int
	for attempt := 0; attempt < 50 && port == 0; attempt++ {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		if next, err := net.Listen("tcp", ":"+strconv.Itoa(p+1)); err == nil {
			next.Close()
			port = p - 1
			t.Cleanup(func() { l.Close() })
		} else {
			l.Close()
		}
	}
	if port == 0 {
		t.Fatal("could not reserve a busy PORT+1 with a free PORT+2")
	}

	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "dyno-proxy"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$1"; echo "$PORT:$APP_PORT"; echo "$_dyno_proxy_p"`, "bash", script)
	cmd.Env = []string{"PATH=" + stubDir + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "DYNO=web.1", "PORT=" + strconv.Itoa(port)}
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("sourcing profile script: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	want := strconv.Itoa(port+2) + ":" + strconv.Itoa(port+2)
	if lines[0] != want {
		t.Errorf("PORT:APP_PORT after sourcing = %q, want %q (PORT+1 is busy)", lines[0], want)
	}
	if len(lines) > 1 && lines[1] != "" {
		t.Errorf("profile script leaked its _dyno_proxy_p helper var into the env: %q", lines[1])
	}
}
