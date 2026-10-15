//go:build !linux

package supervisor

import (
	"os/exec"
	"syscall"
)

// maybeSetSubreaper is a no-op off Linux. PR_SET_CHILD_SUBREAPER and orphan
// reaping are Linux/PID-1 concerns; the dev machine (darwin) does not need them.
func maybeSetSubreaper(logf func(level, msg string)) {}

// procAttr starts children in their own process group so the supervisor can
// signal the whole group. Pdeathsig is Linux-only and omitted here.
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the child's process group (negative pid), falling
// back to the single process if the group is gone.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// otherProcManager waits on each child in its own goroutine — portable and
// sufficient where we are not PID 1.
type otherProcManager struct {
	logf func(level, msg string)
}

func newProcManager(logf func(level, msg string)) procManager {
	return &otherProcManager{logf: logf}
}

func (m *otherProcManager) start(cmd *exec.Cmd) (<-chan error, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch, nil
}

func (m *otherProcManager) close() {}

// pidAlive reports whether pid exists (EPERM still means it exists).
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
