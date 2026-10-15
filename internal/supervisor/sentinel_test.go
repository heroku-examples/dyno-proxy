package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func noLog(string, string) {}

// The daemon vanishing (SIGKILL) while the app runs must take the app down so
// the dyno restarts.
func TestRunSentinelTerminatesAppWhenDaemonGone(t *testing.T) {
	daemonPID, daemon := startVictim(t, "sleep", "30")
	appPID, app := startVictim(t, "sleep", "30")

	done := make(chan error, 1)
	go func() { done <- RunSentinel(daemonPID, appPID, 20*time.Millisecond, 500*time.Millisecond, noLog) }()

	_ = syscall.Kill(daemonPID, syscall.SIGKILL)
	<-daemon
	waitVictimSignal(t, app, syscall.SIGTERM, 5*time.Second)
	if err := <-done; err != nil {
		t.Fatalf("RunSentinel = %v, want nil", err)
	}
}

// The app exiting first ends the sentinel and leaves the daemon alone.
func TestRunSentinelExitsWhenAppGone(t *testing.T) {
	daemonPID, _ := startVictim(t, "sleep", "30")
	appPID, app := startVictim(t, "sleep", "0.2")

	done := make(chan error, 1)
	go func() { done <- RunSentinel(daemonPID, appPID, 20*time.Millisecond, 500*time.Millisecond, noLog) }()
	<-app
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunSentinel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sentinel did not exit after the app exited")
	}
	if !pidAlive(daemonPID) {
		t.Fatal("daemon was touched")
	}
}

// The supervisor starts the sentinel, restarts it if it dies, and stops it on
// shutdown (a sentinel left behind would kill the app mid-drain).
func TestBackgroundKeepsSentinelRunning(t *testing.T) {
	appPID, _ := startVictim(t, "sleep", "30")
	b := &fakeBackend{name: "fake", cmdName: "sleep", cmdArgs: []string{"30"}}
	cfg := backgroundConfig(t, b, appPID)
	pidFile := filepath.Join(t.TempDir(), "sentinel.pid")
	var starts atomic.Int32
	cfg.SentinelCommand = func(int) (string, []string) {
		starts.Add(1)
		return "sh", []string{"-c", "echo $$ > " + pidFile + "; exec sleep 30"}
	}
	s, done := runAsync(t, cfg)

	waitStarts := func(n int32) int {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if starts.Load() >= n {
				if b, err := os.ReadFile(pidFile); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
						return pid
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("sentinel started %d times, want %d", starts.Load(), n)
		return 0
	}
	first := waitStarts(1)
	_ = os.Remove(pidFile)
	_ = syscall.Kill(first, syscall.SIGKILL)
	second := waitStarts(2)
	if second == first {
		t.Fatal("sentinel was not restarted")
	}

	s.Shutdown()
	waitDone(t, done)
	assertPidGone(t, second)
}
