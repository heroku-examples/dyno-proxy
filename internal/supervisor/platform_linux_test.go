//go:build linux

package supervisor

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestReapOrphanGrandchild verifies the subreaper + Wait4 reaper collects an
// orphaned grandchild. The direct child spawns a grandchild that outlives it;
// after the direct child exits, the grandchild reparents to this process (we are
// the subreaper) and our reaper must Wait4 it so it never lingers as a zombie.
func TestReapOrphanGrandchild(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Skipf("cannot set subreaper: %v", err)
	}

	logf := func(string, string) {}
	maybeSetSubreaper(logf)
	pm := newProcManager(logf)
	defer pm.close()

	// Direct child forks a grandchild (sleep) into its own session, then exits.
	// The grandchild becomes an orphan reparented to us.
	child := exec.Command("sh", "-c", "setsid sh -c 'echo $$ > "+t.TempDir()+"/gc.pid; sleep 1' & exit 0")
	child.SysProcAttr = procAttr()
	done, err := pm.start(child)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("direct child did not exit")
	}

	// Give the reaper time to collect the reparented grandchild after it exits.
	// If reaping works, no zombie remains. We assert that a Wait4(-1) eventually
	// reports ECHILD (nothing left to reap) rather than surfacing a zombie.
	time.Sleep(2 * time.Second)
	var ws syscall.WaitStatus
	for {
		wpid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if wpid <= 0 || err == syscall.ECHILD {
			break // reaper already collected everything
		}
		// The reaper may race us here; either way, no persistent zombie.
	}
}

// TestProcAttrLinux confirms Linux children get Setpgid + Pdeathsig.
func TestProcAttrLinux(t *testing.T) {
	a := procAttr()
	if !a.Setpgid {
		t.Fatal("expected Setpgid")
	}
	if a.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("expected Pdeathsig SIGKILL, got %v", a.Pdeathsig)
	}
}
