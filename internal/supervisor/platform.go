package supervisor

import "os/exec"

// procManager starts children and delivers their exit on a per-child channel.
// Two implementations exist:
//
//   - Linux (platform_linux.go): a single subreaper-aware reaper goroutine calls
//     Wait4(-1, WNOHANG) so orphaned grandchildren are reaped too, dispatching
//     each direct child's exit to its channel.
//   - other (platform_other.go): a per-child cmd.Wait() goroutine (portable, used
//     on the darwin dev machine; no PID-1 reaping needed there).
type procManager interface {
	// start launches cmd and returns a channel that receives its exit error
	// exactly once when the process terminates.
	start(cmd *exec.Cmd) (<-chan error, error)
	// close stops any background reaping goroutine.
	close()
}
