package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// sentinelRestartDelay is the pause before restarting a sentinel that died.
const sentinelRestartDelay = time.Second

// RunSentinel blocks until appPID exits (returning nil) or daemonPID exits while
// appPID is still running, in which case it terminates appPID (SIGTERM, SIGKILL
// after grace) so the platform restarts the dyno. It is the body of the
// `dyno-proxy sentinel` process that guards a Background daemon against being
// killed outright. It reports an error when appPID cannot be terminated.
func RunSentinel(daemonPID, appPID int, interval, grace time.Duration, logf func(level, msg string)) error {
	if interval <= 0 {
		interval = time.Second
	}
	if grace <= 0 {
		grace = 10 * time.Second
	}
	for {
		if !pidAlive(appPID) {
			return nil
		}
		if !pidAlive(daemonPID) {
			logf("error", fmt.Sprintf("dyno-proxy daemon %d is gone", daemonPID))
			if !terminatePID(appPID, grace, syscall.Kill, logf) {
				return fmt.Errorf("dyno main process %d cannot be terminated", appPID)
			}
			return nil
		}
		time.Sleep(interval)
	}
}

// keepSentinel runs the sentinel process for the daemon's lifetime, restarting
// it if it dies while the app is still running. The returned function stops it
// and must run before the daemon exits cleanly: a sentinel that outlived a
// deliberate shutdown would see the daemon gone and kill the app mid-drain.
func (s *Supervisor) keepSentinel() (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for pidAlive(s.cfg.AppPID) {
			name, args := s.cfg.SentinelCommand(os.Getpid())
			cmd := exec.Command(name, args...)
			cmd.Stdout = s.cfg.Stdout
			cmd.Stderr = s.cfg.Stderr
			cmd.Env = s.cfg.Env
			// Own session, and deliberately no Pdeathsig: it must outlive us.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			done, err := s.pm.start(cmd)
			if err != nil {
				s.logf("error", "sentinel failed to start: "+err.Error())
			} else {
				s.logf("debug", fmt.Sprintf("sentinel started (pid %d)", cmd.Process.Pid))
				select {
				case <-done:
					s.logf("warn", "sentinel exited; restarting")
				case <-quit:
					_ = cmd.Process.Kill()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
					}
					return
				}
			}
			select {
			case <-time.After(sentinelRestartDelay):
			case <-quit:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-finished
	}
}
