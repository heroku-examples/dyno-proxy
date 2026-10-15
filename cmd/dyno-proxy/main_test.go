package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProxyDisabledByDisableVar locks the runtime on/off switch: the proxy is ON
// by default and ONLY DISABLE_DYNO_PROXY set to exactly true, TRUE or 1
// disables it; anything else is a no-op.
func TestProxyDisabledByDisableVar(t *testing.T) {
	cases := []struct {
		value    string
		disabled bool
	}{
		{"true", true},
		{"TRUE", true},
		{"1", true},
		{"True", false},
		{"yes", false},
		{"0", false},
		{"false", false},
		{"", false},
		{" true", false},
		{"1\n", false},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			getenv := func(k string) string {
				if k == "DISABLE_DYNO_PROXY" {
					return c.value
				}
				return ""
			}
			if got := proxyDisabled(getenv); got != c.disabled {
				t.Errorf("proxyDisabled(DISABLE_DYNO_PROXY=%q) = %v, want %v", c.value, got, c.disabled)
			}
		})
	}
}

func TestParseRunArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantProject string
		wantCommand []string
		wantErr     bool
	}{
		{
			name:        "separator then command",
			args:        []string{"--", "ruby", "app.rb"},
			wantCommand: []string{"ruby", "app.rb"},
		},
		{
			name:        "filename flag then separator",
			args:        []string{"--filename", "m.toml", "--", "ruby", "app.rb"},
			wantProject: "m.toml",
			wantCommand: []string{"ruby", "app.rb"},
		},
		{
			name:        "short filename flag",
			args:        []string{"-f", "m.toml", "--", "ruby"},
			wantProject: "m.toml",
			wantCommand: []string{"ruby"},
		},
		{
			name:        "filename equals form",
			args:        []string{"--filename=m.toml", "--", "ruby"},
			wantProject: "m.toml",
			wantCommand: []string{"ruby"},
		},
		{
			name:    "filename without value is an error",
			args:    []string{"--filename"},
			wantErr: true,
		},
		{
			name:        "bare token ergonomic path",
			args:        []string{"ruby", "app.rb"},
			wantCommand: []string{"ruby", "app.rb"},
		},
		{
			name:    "separator with empty command is an error",
			args:    []string{"--"},
			wantErr: true,
		},
		{
			name:    "empty args is an error",
			args:    []string{},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseRunArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseRunArgs(%v) = %+v, want error", c.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRunArgs(%v) unexpected error: %v", c.args, err)
			}
			if got.ProjectPath != c.wantProject {
				t.Errorf("project = %q, want %q", got.ProjectPath, c.wantProject)
			}
			if !reflect.DeepEqual(got.AppCommand, c.wantCommand) {
				t.Errorf("command = %v, want %v", got.AppCommand, c.wantCommand)
			}
		})
	}
}

// TestRunDynoProxyFalseExecsChild builds the binary and drives the disabled
// passthrough as a subprocess (syscall.Exec replaces the process image, so it
// cannot be exercised in-process). It asserts that DISABLE_DYNO_PROXY=true execs the
// child directly, passes the environment through, and surfaces a clear error
// when the child binary is absent — and that a non-"false" value does NOT
// passthrough.
func TestRunDynoProxyFalseExecsChild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping subprocess build")
	}
	bin := filepath.Join(t.TempDir(), "dyno-proxy")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dyno-proxy: %v\n%s", err, out)
	}

	t.Run("execs child with env passthrough", func(t *testing.T) {
		cmd := exec.Command(bin, "run", "--", "bash", "-c", "echo GOT=$SENTINEL")
		cmd.Env = append(os.Environ(), "DISABLE_DYNO_PROXY=true", "SENTINEL=hello")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "GOT=hello") {
			t.Errorf("expected child to run with env passthrough, got: %q", out)
		}
	})

	t.Run("missing child binary reports cannot exec", func(t *testing.T) {
		cmd := exec.Command(bin, "run", "--", "/no/such/binary-xyz")
		cmd.Env = append(os.Environ(), "DISABLE_DYNO_PROXY=true")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected non-zero exit, got success: %q", out)
		}
		if !strings.Contains(string(out), "cannot exec") {
			t.Errorf("expected 'cannot exec' error, got: %q", out)
		}
	})
}

// TestRunStartupHook builds the binary and drives the optional `dyno-proxy-start-hook`
// hook as a subprocess. The hook is a script placed next to the dyno-proxy binary
// that runs synchronously before the web command starts. The contract:
//   - present + succeeds  -> runs before the web command, then startup continues
//   - present + fails      -> startup aborts; the web command never runs
//   - absent               -> no-op; startup proceeds normally
//
// Driven with DISABLE_DYNO_PROXY=true so the run path execs the child directly and no
// load balancer is required — the hook runs before that switch either way.
func TestRunStartupHook(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping subprocess build")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dyno-proxy")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dyno-proxy: %v\n%s", err, out)
	}
	// The hook is discovered alongside the running binary.
	hookPath := filepath.Join(dir, "dyno-proxy-start-hook")

	writeHook := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}
	}
	removeHook := func(t *testing.T) {
		t.Helper()
		if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove hook: %v", err)
		}
	}
	runProxy := func(t *testing.T) (string, error) {
		t.Helper()
		cmd := exec.Command(bin, "run", "--", "bash", "-c", "echo CHILD_RAN")
		cmd.Env = append(os.Environ(), "DISABLE_DYNO_PROXY=true")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("hook runs before the web command", func(t *testing.T) {
		writeHook(t, "#!/bin/sh\necho HOOK_RAN\n")
		defer removeHook(t)
		out, err := runProxy(t)
		if err != nil {
			t.Fatalf("run failed: %v\n%s", err, out)
		}
		hookAt := strings.Index(out, "HOOK_RAN")
		childAt := strings.Index(out, "CHILD_RAN")
		if hookAt < 0 || childAt < 0 {
			t.Fatalf("expected both hook and web command to run, got: %q", out)
		}
		if hookAt > childAt {
			t.Errorf("startup hook must run before the web command, got: %q", out)
		}
	})

	t.Run("hook failure aborts startup", func(t *testing.T) {
		writeHook(t, "#!/bin/sh\necho HOOK_FAILING\nexit 3\n")
		defer removeHook(t)
		out, err := runProxy(t)
		if err == nil {
			t.Fatalf("expected non-zero exit when hook fails, got success: %q", out)
		}
		if !strings.Contains(out, "startup hook") {
			t.Errorf("expected 'startup hook' failure error, got: %q", out)
		}
		if strings.Contains(out, "CHILD_RAN") {
			t.Errorf("web command must not run when the startup hook fails, got: %q", out)
		}
	})

	t.Run("hook may be a non-shell executable", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "hook.go")
		prog := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"BINARY_HOOK_RAN\") }\n"
		if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
			t.Fatalf("write hook source: %v", err)
		}
		if out, err := exec.Command("go", "build", "-o", hookPath, src).CombinedOutput(); err != nil {
			t.Fatalf("build binary hook: %v\n%s", err, out)
		}
		defer removeHook(t)
		out, err := runProxy(t)
		if err != nil {
			t.Fatalf("run failed: %v\n%s", err, out)
		}
		hookAt := strings.Index(out, "BINARY_HOOK_RAN")
		childAt := strings.Index(out, "CHILD_RAN")
		if hookAt < 0 || childAt < 0 || hookAt > childAt {
			t.Errorf("expected binary hook to run before the web command, got: %q", out)
		}
	})

	t.Run("absent hook is a no-op", func(t *testing.T) {
		removeHook(t)
		out, err := runProxy(t)
		if err != nil {
			t.Fatalf("run failed with no hook present: %v\n%s", err, out)
		}
		if !strings.Contains(out, "CHILD_RAN") {
			t.Errorf("web command should run when no startup hook is present, got: %q", out)
		}
	})
}

func TestParseBackgroundArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    backgroundArgs
		wantErr bool
	}{
		{name: "no args", args: nil, want: backgroundArgs{}},
		{name: "foreground", args: []string{"--foreground"}, want: backgroundArgs{Foreground: true}},
		{name: "unexpected argument", args: []string{"ruby"}, wantErr: true},
		{name: "app pid", args: []string{"--app-pid", "42"}, want: backgroundArgs{AppPID: 42}},
		{name: "app pid equals", args: []string{"--app-pid=42", "--foreground"}, want: backgroundArgs{AppPID: 42, Foreground: true}},
		{name: "app pid missing value", args: []string{"--app-pid"}, wantErr: true},
		{name: "app pid not a number", args: []string{"--app-pid", "abc"}, wantErr: true},
		{name: "app pid not positive", args: []string{"--app-pid=0"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseBackgroundArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestDaemonArgs: the launcher re-execs itself as `background --foreground`,
// forwarding the dyno's main pid.
func TestDaemonArgs(t *testing.T) {
	if got, want := daemonArgs(backgroundArgs{}), []string{"background", "--foreground"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// The dyno main-process pid must reach the daemon, which kills it if the
	// LB dies so the platform restarts the dyno.
	got := daemonArgs(backgroundArgs{AppPID: 42})
	if want := []string{"background", "--foreground", "--app-pid", "42"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestBackgroundDisabledIsNoop: DISABLE_DYNO_PROXY=true makes `background` return
// without spawning or running anything, in either mode.
func TestBackgroundDisabledIsNoop(t *testing.T) {
	t.Setenv("DISABLE_DYNO_PROXY", "true")
	for _, args := range [][]string{nil, {"--foreground"}} {
		if err := backgroundCmd(args); err != nil {
			t.Errorf("backgroundCmd(%v) with DISABLE_DYNO_PROXY=true: %v", args, err)
		}
	}
	if err := backgroundCmd([]string{"bogus"}); err == nil {
		t.Error("expected argument error even when disabled")
	}
}

// TestRunFrontedExecsChild: when the buildpack (exec.d or .profile.d) has
// already fronted the app (DYNO_PROXY_FRONTED set, PORT already pointed at
// APP_PORT), `run` execs the web command in place with PORT untouched, starts
// no engine, and skips the startup hook (it already ran inside `background`).
func TestRunFrontedExecsChild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping subprocess build")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "dyno-proxy")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dyno-proxy: %v\n%s", err, out)
	}
	marker := filepath.Join(dir, "hook-ran")
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dyno-proxy-start-hook"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// A deadline so an accidental engine start fails instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "--", "bash", "-c", "echo GOT=$PORT")
	cmd.Env = append(os.Environ(), "DYNO_PROXY_FRONTED=1", "PORT=5001", "APP_PORT=5001", "DISABLE_DYNO_PROXY=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "GOT=5001") {
		t.Errorf("expected child to see PORT=5001 unchanged, got: %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("startup hook must not run again when already fronted")
	}
}

func startDaemon(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd
}

// `background` must not report success (which moves the app off $PORT) when
// the daemon dies before its LB serves $PORT.
func TestWaitDaemonReadyFailsWhenDaemonExits(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	err = waitDaemonReady(startDaemon(t, "exit 3"), port, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited before serving") {
		t.Fatalf("want early-exit error, got %v", err)
	}
}

func TestWaitDaemonReadySucceedsOnceListening(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	if err := waitDaemonReady(startDaemon(t, "sleep 30"), port, 5*time.Second); err != nil {
		t.Fatalf("want ready, got %v", err)
	}
}

func TestWaitDaemonReadyTimeoutKillsDaemon(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	cmd := startDaemon(t, "sleep 30")
	err = waitDaemonReady(cmd, port, 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not serving") {
		t.Fatalf("want timeout error, got %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(cmd.Process.Pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(cmd.Process.Pid, 0) == nil {
		t.Fatal("daemon still running after readiness timeout")
	}
}
