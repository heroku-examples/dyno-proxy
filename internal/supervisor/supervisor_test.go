package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeBackend stands in for the LB in the supervisor tests. It launches an
// arbitrary command as the "LB" and carries a configurable reload signal (nil
// means restart).
type fakeBackend struct {
	name    string
	cmdName string
	cmdArgs []string
	reload  os.Signal
}

func (b *fakeBackend) Command() (string, []string) {
	return b.cmdName, b.cmdArgs
}

// collector is a concurrency-safe io.Writer for capturing child output.
type collector struct {
	mu  sync.Mutex
	buf []byte
}

func (c *collector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *collector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// freePort returns a currently-free TCP port as a string.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

// envWithout returns a copy of env with any assignments to the named keys
// removed, so tests can control those variables deterministically.
func envWithout(env []string, keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if drop[name] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func baseConfig(b *fakeBackend, appCmd []string) Config {
	return Config{
		LBCommand:      b.Command,
		ReloadSignal:   b.reload,
		AppCommand:     appCmd,
		Stdout:         &collector{},
		Stderr:         &collector{},
		ReadyTimeout:   2 * time.Second,
		ReadyInterval:  20 * time.Millisecond,
		ShutdownBudget: 3 * time.Second,
		Logf:           func(string, string) {},
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for nil backend")
	}
	if _, err := New(Config{LBCommand: (&fakeBackend{}).Command}); err == nil {
		t.Fatal("expected error for empty app command")
	}
	if _, err := New(Config{LBCommand: (&fakeBackend{}).Command, Background: true}); err == nil {
		t.Fatal("expected error for background mode without an app port")
	}
	if _, err := New(Config{LBCommand: (&fakeBackend{}).Command, Background: true, AppPort: "5001"}); err != nil {
		t.Fatalf("background mode needs no app command: %v", err)
	}
	s, err := New(Config{LBCommand: (&fakeBackend{}).Command, AppCommand: []string{"true"}})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if s.cfg.ShutdownBudget >= 30*time.Second {
		t.Fatalf("shutdown budget must stay < 30s, got %v", s.cfg.ShutdownBudget)
	}
}

// TestAppExitStopsLB: when the app exits, the loop returns an error (the dyno
// must restart even after a clean app exit) and the LB is torn down.
func TestAppExitStopsLB(t *testing.T) {
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	// App exits quickly.
	cfg := baseConfig(b, []string{"sh", "-c", "sleep 0.3; exit 0"})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the app exits")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not return after app exit")
	}

	// LB must have been reaped.
	pid, exited, ok := s.snapshot(s.getLB())
	if !ok || !exited {
		t.Fatal("LB was not stopped after app exit")
	}
	assertPidGone(t, pid)
}

// TestBackgroundAppGoneStopsLB: in Background mode there is no app child; once
// the app port has come up and then gone away, the loop returns cleanly and the
// LB is torn down.
func TestBackgroundAppGoneStopsLB(t *testing.T) {
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	appPort := freePort(t)
	cfg := baseConfig(b, nil)
	cfg.Background = true
	cfg.AppPort = appPort
	cfg.AppWatchInterval = 20 * time.Millisecond
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	// Not up yet: the watcher must wait indefinitely rather than give up.
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("supervisor returned before the app came up: %v", err)
	default:
	}
	if s.getApp() != nil {
		t.Fatal("background mode must not start an app child")
	}

	// App comes up, then goes away.
	l, err := net.Listen("tcp", "localhost:"+appPort)
	if err != nil {
		t.Fatalf("listen app port: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	l.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean return after app went away, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not return after app went away")
	}
	pid, exited, ok := s.snapshot(s.getLB())
	if !ok || !exited {
		t.Fatal("LB was not stopped after app went away")
	}
	assertPidGone(t, pid)
}

// TestLBExitStopsApp: when the LB exits, the loop returns an error and the app
// is torn down.
func TestLBExitStopsApp(t *testing.T) {
	b := &fakeBackend{name: "fake", cmdName: "sh", cmdArgs: []string{"-c", "sleep 0.3; exit 0"}}
	cfg := baseConfig(b, []string{"sleep", "30"})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error when LB exits")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not return after LB exit")
	}
	pid, exited, ok := s.snapshot(s.getApp())
	if !ok || !exited {
		t.Fatal("app was not stopped after LB exit")
	}
	assertPidGone(t, pid)
}

// TestSignalShutdown: Shutdown gracefully terminates both children.
func TestSignalShutdown(t *testing.T) {
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	cfg := baseConfig(b, []string{"sleep", "30"})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	// Give both children time to start.
	time.Sleep(300 * time.Millisecond)
	lbPid, _, _ := s.snapshot(s.getLB())
	appPid, _, _ := s.snapshot(s.getApp())

	s.Shutdown()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil on clean shutdown, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not shut down")
	}

	assertPidGone(t, lbPid)
	assertPidGone(t, appPid)
}

// TestContextCancelShutdown: cancelling ctx tears down and returns ctx.Err().
func TestContextCancelShutdown(t *testing.T) {
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	cfg := baseConfig(b, []string{"sleep", "30"})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not honor context cancel")
	}
}

// TestReadinessGate: the app does not start until the LB readiness probe sees
// the port accept a connection. The test itself opens the listener (the LB is
// just a long sleep) so the probe target is deterministic.
func TestReadinessGate(t *testing.T) {
	// Reserve a free port but keep it closed for now.
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close() // closed until we reopen below

	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	cfg := baseConfig(b, []string{"sleep", "30"})
	cfg.LBPort = port
	cfg.ReadyTimeout = 5 * time.Second
	cfg.ReadyInterval = 50 * time.Millisecond

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	// While the port is closed, readiness blocks and the app must not start.
	time.Sleep(500 * time.Millisecond)
	if _, _, ok := s.snapshot(s.getApp()); ok {
		t.Fatal("app started before LB port was ready")
	}

	// Open the port; readiness should complete and the app should start.
	ln, err := net.Listen("tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("reopen listener: %v", err)
	}
	defer ln.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, exited, ok := s.snapshot(s.getApp()); ok && !exited {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, exited, ok := s.snapshot(s.getApp()); !ok || exited {
		t.Fatal("app did not start after LB port became ready")
	}
	s.Shutdown()
	<-done
}

// TestReloadHotSignal: a reload with a non-restart signal delivers that signal
// to the LB without restarting it.
func TestReloadHotSignal(t *testing.T) {
	// LB traps SIGHUP (does not die) and records receipt to a file, so we can
	// assert the signal was delivered and the same process kept running.
	dir := t.TempDir()
	marker := dir + "/hup"
	script := "trap 'echo got > " + marker + "' HUP; while true; do sleep 0.1; done"
	b := &fakeBackend{
		name:    "fake",
		cmdName: "sh",
		cmdArgs: []string{"-c", script},
		reload:  syscall.SIGHUP,
	}
	cfg := baseConfig(b, []string{"sleep", "30"})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	time.Sleep(300 * time.Millisecond)
	origPid, _, _ := s.snapshot(s.getLB())

	s.Reload()

	// Wait for the trap to fire.
	gotSignal := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			gotSignal = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gotSignal {
		t.Fatal("LB did not receive hot-reload SIGHUP")
	}

	// Same process must still be supervised (hot reload, not restart).
	pid, exited, ok := s.snapshot(s.getLB())
	if !ok || exited || pid != origPid {
		t.Fatalf("hot reload should keep same LB process; orig=%d now=%d exited=%v", origPid, pid, exited)
	}
	s.Shutdown()
	<-done
}

// TestReloadRestart: a reload with the restart sentinel replaces the LB with a
// new process (new pid).
func TestReloadRestart(t *testing.T) {
	b := &fakeBackend{
		name:    "fake",
		cmdName: "sleep",
		cmdArgs: []string{"30"},
	}
	cfg := baseConfig(b, []string{"sleep", "30"})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	time.Sleep(300 * time.Millisecond)
	origPid, _, _ := s.snapshot(s.getLB())

	s.Reload()

	// Wait for the pid to change.
	changed := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pid, _, ok := s.snapshot(s.getLB()); ok && pid != origPid && pid != 0 {
			changed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !changed {
		t.Fatalf("LB pid did not change after restart reload (still %d)", origPid)
	}
	assertPidGone(t, origPid)

	s.Shutdown()
	<-done
}

// TestAppGetsPortEnv: APP_PORT/PORT are exported to the app.
func TestAppGetsPortEnv(t *testing.T) {
	out := &collector{}
	port := freePort(t)
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	// Read PORT via `printenv` rather than a literal `$PORT` token: the latter
	// is rewritten to `$APP_PORT` before exec, which would make the assertion
	// pass off APP_PORT expansion and stop guarding the PORT env export.
	cfg := baseConfig(b, []string{"sh", "-c", "echo APP_PORT=$APP_PORT PORT=$(printenv PORT); sleep 30"})
	cfg.Stdout = out
	// Start from a clean env with no inherited PORT so the assertion fails if
	// the supervisor stops exporting PORT.
	cfg.Env = envWithout(os.Environ(), "PORT", "APP_PORT")
	cfg.AppPort = port

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	time.Sleep(500 * time.Millisecond)
	got := out.String()
	want := "APP_PORT=" + port + " PORT=" + port
	if !strings.Contains(got, want) {
		t.Fatalf("app env not exported; want %q in output, got %q", want, got)
	}
	s.Shutdown()
	<-done
}

// TestRewritePortRefs verifies whole-identifier rewriting of $PORT/${PORT} to
// $APP_PORT/${APP_PORT} while leaving lookalikes and bare PORT untouched.
func TestRewritePortRefs(t *testing.T) {
	strCases := []struct {
		name string
		in   string
		want string
	}{
		{"bare", "$PORT", "$APP_PORT"},
		{"braced", "${PORT}", "${APP_PORT}"},
		{"braced default value", "${PORT:-3000}", "${APP_PORT:-3000}"},
		{"braced assign default", "${PORT:=8080}", "${APP_PORT:=8080}"},
		{"flag space", "--port $PORT", "--port $APP_PORT"},
		{"flag equals", "--port=$PORT", "--port=$APP_PORT"},
		{"embedded url", ":$PORT/foo", ":$APP_PORT/foo"},
		{"rackup", "bundle exec rackup config.ru --host 0.0.0.0 --port $PORT", "bundle exec rackup config.ru --host 0.0.0.0 --port $APP_PORT"},
		{"portal unchanged", "$PORTAL", "$PORTAL"},
		{"port_name unchanged", "$PORT_NAME", "$PORT_NAME"},
		{"app_port unchanged", "$APP_PORT", "$APP_PORT"},
		{"app_port braced unchanged", "${APP_PORT}", "${APP_PORT}"},
		{"app_port braced default unchanged", "${APP_PORT:-3000}", "${APP_PORT:-3000}"},
		{"bare word unchanged", "PORT", "PORT"},
		{"assignment unchanged", "PORT=8080", "PORT=8080"},
		{"multiple occurrences", "$PORT ${PORT} :$PORT", "$APP_PORT ${APP_PORT} :$APP_PORT"},
	}
	for _, tc := range strCases {
		t.Run(tc.name, func(t *testing.T) {
			got := rewritePortRefs([]string{tc.in})
			if !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("rewritePortRefs(%q) = %q, want %q", tc.in, got[0], tc.want)
			}
		})
	}

	// Operates per-argv-element across a []string.
	in := []string{"bundle", "exec", "rackup", "--port", "$PORT", "--url", ":${PORT}", "$PORTAL"}
	want := []string{"bundle", "exec", "rackup", "--port", "$APP_PORT", "--url", ":${APP_PORT}", "$PORTAL"}
	if got := rewritePortRefs(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("rewritePortRefs(%v) = %v, want %v", in, got, want)
	}
}

// TestAppPortRefRewritten: a web command that references $PORT resolves to the
// APP_PORT value because the ref is rewritten to $APP_PORT before exec.
func TestAppPortRefRewritten(t *testing.T) {
	out := &collector{}
	appPort := freePort(t)
	routerPort := freePort(t)
	for routerPort == appPort {
		routerPort = freePort(t)
	}
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	cfg := baseConfig(b, []string{"sh", "-c", "echo GOT=$PORT; sleep 30"})
	cfg.Stdout = out
	// Give the child a router PORT that differs from APP_PORT. We intentionally
	// leave cfg.AppPort empty so the supervisor does not also export PORT=APP_PORT,
	// isolating the argv rewrite as the only thing that can steer the app off the
	// router PORT: with the rewrite, $PORT becomes $APP_PORT and the app sees
	// appPort; without it, the shell would print routerPort.
	cfg.Env = append(envWithout(os.Environ(), "PORT", "APP_PORT"),
		"PORT="+routerPort, "APP_PORT="+appPort)

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	time.Sleep(500 * time.Millisecond)
	got := out.String()
	want := "GOT=" + appPort
	notWant := "GOT=" + routerPort
	if !strings.Contains(got, want) || strings.Contains(got, notWant) {
		t.Fatalf("$PORT ref not rewritten to APP_PORT; want %q (not %q) in output, got %q", want, notWant, got)
	}
	s.Shutdown()
	<-done
}

// --- helpers ---

func assertPidGone(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // process gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d still alive", pid)
}
