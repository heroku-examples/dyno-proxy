//go:build linux

package supervisor

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// maybeSetSubreaper makes dyno-proxy the subreaper for its subtree so that
// orphaned grandchildren (e.g. a forked Puma worker) reparent to us instead of
// PID 1 elsewhere, letting our reaper collect them.
func maybeSetSubreaper(logf func(level, msg string)) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		logf("warn", "PR_SET_CHILD_SUBREAPER failed: "+err.Error())
		return
	}
	logf("debug", "installed as child subreaper")
}

// procAttr starts children in their own process group (Setpgid) so the
// supervisor mediates group signals rather than each child catching the dyno's
// group SIGTERM independently. Pdeathsig ensures children die if the supervisor
// crashes.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

// signalGroup sends sig to the child's entire process group (negative pid).
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Negative pid targets the process group created by Setpgid.
	if err := syscall.Kill(-pid, sig); err != nil {
		// Fall back to the single process if the group is already gone.
		_ = cmd.Process.Signal(sig)
	}
}

// linuxProcManager reaps every child via Wait4(-1, WNOHANG) from a single
// goroutine woken by SIGCHLD, so orphaned grandchildren are reaped too. Direct
// children have their exit dispatched to a per-child channel.
type linuxProcManager struct {
	logf func(level, msg string)

	mu      sync.Mutex
	waiters map[int]chan error // direct-child pid -> exit channel

	sigCh  chan os.Signal
	stopCh chan struct{}
	doneCh chan struct{}
}

func newProcManager(logf func(level, msg string)) procManager {
	m := &linuxProcManager{
		logf:    logf,
		waiters: make(map[int]chan error),
		sigCh:   make(chan os.Signal, 1),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	signal.Notify(m.sigCh, syscall.SIGCHLD)
	go m.reapLoop()
	return m
}

func (m *linuxProcManager) start(cmd *exec.Cmd) (<-chan error, error) {
	// Hold the lock across Start + registration so the reaper cannot observe the
	// child's exit before we have a channel for it.
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	ch := make(chan error, 1)
	m.waiters[pid] = ch
	return ch, nil
}

func (m *linuxProcManager) close() {
	signal.Stop(m.sigCh)
	close(m.stopCh)
	<-m.doneCh
}

func (m *linuxProcManager) reapLoop() {
	defer close(m.doneCh)
	for {
		select {
		case <-m.stopCh:
			m.drain()
			return
		case <-m.sigCh:
			m.drain()
		}
	}
}

// drain reaps every currently-exited child (WNOHANG) and dispatches direct
// children to their channels; orphan grandchildren are simply collected.
func (m *linuxProcManager) drain() {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err == syscall.ECHILD {
			return
		}
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		m.dispatch(pid, ws)
	}
}

func (m *linuxProcManager) dispatch(pid int, ws unix.WaitStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, ok := m.waiters[pid]
	if !ok {
		// Orphan grandchild reparented to us by the subreaper: reaped, nothing
		// else to do. (Direct children are always registered before their exit
		// can be observed, since start holds the lock across Start+register.)
		return
	}
	delete(m.waiters, pid)
	ch <- exitError(ws)
}

// exitError mirrors os/exec: non-zero exit or signal death yields an error.
func exitError(ws unix.WaitStatus) error {
	switch {
	case ws.Exited() && ws.ExitStatus() == 0:
		return nil
	case ws.Exited():
		return fmt.Errorf("exit status %d", ws.ExitStatus())
	case ws.Signaled():
		return fmt.Errorf("signal: %v", ws.Signal())
	default:
		return nil
	}
}

// pidAlive reports whether pid exists and is not a zombie. The pid is usually
// not our child, so a dead process may linger as a zombie until its own parent
// reaps it; /proc/<pid>/stat's state field tells the two apart.
func pidAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// Format: "pid (comm) state ..."; comm may contain spaces/parens, so take
	// the field after the LAST ')'.
	if i := bytes.LastIndexByte(b, ')'); i >= 0 && i+2 < len(b) {
		switch b[i+2] {
		case 'Z', 'X', 'x':
			return false
		}
	}
	return true
}
