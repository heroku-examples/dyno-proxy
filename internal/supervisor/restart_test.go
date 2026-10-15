package supervisor

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests pin the "the dyno restarts" contract: whichever of the LB or the
// app dies, the dyno's main process must exit so the platform restarts the
// dyno. In run mode dyno-proxy IS the main process and must exit non-zero; in
// background mode the main process is the app (AppPID), which the daemon must
// terminate when the LB dies.

// startVictim starts cmd as a stand-in for the dyno's main process and reaps it
// in the background (as the platform's init would), so a dead victim does not
// linger as a zombie. The returned channel yields its exit state.
func startVictim(t *testing.T, name string, args ...string) (int, <-chan *exec.ExitError) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	ch := make(chan *exec.ExitError, 1)
	go func() {
		err := cmd.Wait()
		var ee *exec.ExitError
		errors.As(err, &ee)
		ch <- ee
	}()
	return pid, ch
}

func waitVictimSignal(t *testing.T, ch <-chan *exec.ExitError, want syscall.Signal, within time.Duration) {
	t.Helper()
	select {
	case ee := <-ch:
		if ee == nil {
			t.Fatalf("victim exited cleanly, want death by %v", want)
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != want {
			t.Fatalf("victim exit = %v, want death by %v", ee, want)
		}
	case <-time.After(within):
		t.Fatalf("victim (dyno main process) not killed within %v", within)
	}
}

func listenOn(t *testing.T, port string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("listen %s: %v", port, err)
	}
	return l
}

func backgroundConfig(t *testing.T, b *fakeBackend, appPID int) Config {
	cfg := baseConfig(b, nil)
	cfg.Background = true
	cfg.AppPort = freePort(t)
	cfg.AppPID = appPID
	cfg.AppWatchInterval = 20 * time.Millisecond
	cfg.AppKillGrace = 500 * time.Millisecond
	return cfg
}

func runAsync(t *testing.T, cfg Config) (*Supervisor, <-chan error) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	return s, done
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not return")
		return nil
	}
}

// Background mode, LB dies: the daemon SIGTERMs the dyno main process.
func TestBackgroundLBExitTerminatesAppPID(t *testing.T) {
	appPID, victim := startVictim(t, "sleep", "30")
	b := &fakeBackend{name: "fake", cmdName: "sh", cmdArgs: []string{"-c", "sleep 0.3; exit 1"}}
	_, done := runAsync(t, backgroundConfig(t, b, appPID))

	if err := waitDone(t, done); err == nil {
		t.Fatal("expected an error when the LB exits")
	}
	waitVictimSignal(t, victim, syscall.SIGTERM, 2*time.Second)
}

// Background mode, LB dies and the main process ignores SIGTERM: SIGKILL after
// the grace period.
func TestBackgroundLBExitKillsStubbornAppPID(t *testing.T) {
	appPID, victim := startVictim(t, "sh", "-c", `trap "" TERM; while :; do sleep 0.05; done`)
	time.Sleep(100 * time.Millisecond) // let the trap install
	b := &fakeBackend{name: "fake", cmdName: "sh", cmdArgs: []string{"-c", "sleep 0.3; exit 0"}}
	cfg := backgroundConfig(t, b, appPID)
	start := time.Now()
	_, done := runAsync(t, cfg)

	if err := waitDone(t, done); err == nil {
		t.Fatal("expected an error when the LB exits")
	}
	waitVictimSignal(t, victim, syscall.SIGKILL, 2*time.Second)
	if el := time.Since(start); el < cfg.AppKillGrace {
		t.Errorf("SIGKILL after %v, before the %v grace elapsed", el, cfg.AppKillGrace)
	}
}

// countingBackend counts LB launches; every launch exits after a short while.
type countingBackend struct {
	fakeBackend
	mu    sync.Mutex
	calls int
}

func (b *countingBackend) Command() (string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return "sh", []string{"-c", "sleep 0.3; exit 1"}
}

func (b *countingBackend) launches() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// Background mode, LB dies and the main process survives SIGTERM and SIGKILL
// (a PID 1 without a SIGTERM handler, as on Fir): the daemon relaunches the LB
// instead of exiting and leaving nothing on $PORT.
func TestBackgroundLBExitRelaunchesWhenAppPIDUnkillable(t *testing.T) {
	appPID, _ := startVictim(t, "sleep", "30")
	b := &countingBackend{fakeBackend: fakeBackend{name: "fake"}}
	cfg := backgroundConfig(t, &b.fakeBackend, appPID)
	cfg.LBCommand = b.Command
	cfg.AppKillGrace = 100 * time.Millisecond
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.signalPID = func(int, syscall.Signal) error { return nil } // signals are dropped
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	deadline := time.Now().Add(8 * time.Second)
	for b.launches() < 2 && time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("supervisor returned instead of relaunching the LB: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if n := b.launches(); n < 2 {
		t.Fatalf("LB launched %d times, want a relaunch", n)
	}
	s.Shutdown()
	waitDone(t, done)
}

// Background mode, app (main process) dies: the daemon stops the LB and exits,
// even though the app never bound its port.
func TestBackgroundAppPIDExitStopsLB(t *testing.T) {
	appPID, victim := startVictim(t, "sleep", "0.3")
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	s, done := runAsync(t, backgroundConfig(t, b, appPID))

	if err := waitDone(t, done); err != nil {
		t.Fatalf("expected clean return after the app exited, got %v", err)
	}
	<-victim
	pid, exited, ok := s.snapshot(s.getLB())
	if !ok || !exited {
		t.Fatal("LB was not stopped after the app exited")
	}
	assertPidGone(t, pid)
}

// Background mode with an app PID: the PID, not the port, is authoritative. A
// port that comes up and goes away while the main process lives on must NOT
// take the LB down (that would leave the dyno serving nothing on $PORT).
func TestBackgroundAppPIDIgnoresPortGap(t *testing.T) {
	appPID, _ := startVictim(t, "sleep", "30")
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	cfg := backgroundConfig(t, b, appPID)
	s, done := runAsync(t, cfg)

	l := listenOn(t, cfg.AppPort)
	time.Sleep(150 * time.Millisecond)
	l.Close()
	time.Sleep(300 * time.Millisecond) // > appGoneMisses * interval

	select {
	case err := <-done:
		t.Fatalf("supervisor returned on a port gap while the app process lives: %v", err)
	default:
	}
	s.Shutdown()
	waitDone(t, done)
}

// Run mode, LB dies: non-zero (error) return and the app is killed.
func TestRunLBCrashReturnsErrorAndKillsApp(t *testing.T) {
	b := &fakeBackend{name: "fake", cmdName: "sh", cmdArgs: []string{"-c", "sleep 0.3; kill -9 $$"}}
	s, done := runAsync(t, baseConfig(b, []string{"sleep", "30"}))
	err := waitDone(t, done)
	if err == nil || !strings.Contains(err.Error(), "load balancer exited") {
		t.Fatalf("want load balancer exit error, got %v", err)
	}
	pid, exited, ok := s.snapshot(s.getApp())
	if !ok || !exited {
		t.Fatal("app was not stopped after LB crash")
	}
	assertPidGone(t, pid)
}

// Run mode, app dies (crash or even a clean exit): dyno-proxy must return an
// error so the main process exits non-zero, and the LB is stopped.
func TestRunAppExitReturnsError(t *testing.T) {
	for _, c := range []struct{ name, script string }{
		{"crash", "sleep 0.3; exit 3"},
		{"killed", "sleep 0.3; kill -9 $$"},
		{"clean exit", "sleep 0.3; exit 0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
			s, done := runAsync(t, baseConfig(b, []string{"sh", "-c", c.script}))
			err := waitDone(t, done)
			if err == nil || !strings.Contains(err.Error(), "app exited") {
				t.Fatalf("want app exit error, got %v", err)
			}
			pid, exited, ok := s.snapshot(s.getLB())
			if !ok || !exited {
				t.Fatal("LB was not stopped after app exit")
			}
			assertPidGone(t, pid)
		})
	}
}

// failingRestartBackend starts fine once, then its restart command cannot be
// launched: a reload that loses the LB must be treated like an LB death.
type failingRestartBackend struct {
	fakeBackend
	mu    sync.Mutex
	calls int
}

func (b *failingRestartBackend) Command() (string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls > 1 {
		return "/nonexistent/lb-binary", nil
	}
	return "sleep", []string{"30"}
}

func TestReloadRestartFailureReturnsError(t *testing.T) {
	b := &failingRestartBackend{fakeBackend: fakeBackend{name: "fake"}}
	cfg := baseConfig(&b.fakeBackend, []string{"sleep", "30"})
	cfg.LBCommand = b.Command
	s, done := runAsync(t, cfg)
	time.Sleep(300 * time.Millisecond)
	s.Reload()
	err := waitDone(t, done)
	if err == nil || !strings.Contains(err.Error(), "load balancer") {
		t.Fatalf("want load balancer error after failed restart, got %v", err)
	}
	pid, exited, ok := s.snapshot(s.getApp())
	if !ok || !exited {
		t.Fatal("app was not stopped after the LB was lost")
	}
	assertPidGone(t, pid)
}
