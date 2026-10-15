// Package supervisor owns the dyno's process tree.
//
// The decided architecture (docs/rewrite-plan.md, Workstream 4) inverts control:
// dyno-proxy is the supervising parent and launches BOTH the load-balancer (LB)
// process — via Config.LBCommand — and
// the web/app process as sibling *exec.Cmd children. A single lifecycle loop
// (Run) selects over the LB exit, the app exit, OS signals, a reload trigger and
// context cancellation; it replaces the two hand-rolled loops (startProxyAndApp,
// startBackgroundProxy/runMonitor) and the re-exec'd "monitor" subprocess.
//
// PID-1 concerns (subreaper + orphan reaping, explicit signal handling) and
// process-group signalling live in the platform_*.go files, guarded so the
// package still builds and tests on darwin.
package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config is the immutable input to a Supervisor. The engine constructs it.
type Config struct {
	// LBCommand returns the executable name and args that launch the LB. It is
	// called for every (re)launch.
	LBCommand func() (name string, args []string)
	// ReloadSignal is delivered to the LB to reload its config. Nil means the LB
	// cannot hot-reload and is restarted instead.
	ReloadSignal os.Signal

	// AppCommand is the web command (argv) dyno-proxy launches as a child.
	// Ignored (and may be empty) when Background is set.
	AppCommand []string
	// Background runs only the LB: there is no app child. The app is started
	// independently (the platform's default web command, see `dyno-proxy
	// background`) and is expected to bind AppPort; the loop watches that port
	// and shuts the LB down once the app has come up and then gone away.
	Background bool
	// AppPID is, in Background mode, the dyno's main process: the one that runs
	// the web command and whose exit makes the platform restart the dyno (0 =
	// unknown). When set it replaces the AppPort watch: the loop stops the LB
	// once AppPID is gone, and if the LB dies it SIGTERMs AppPID (SIGKILL after
	// AppKillGrace) so the dyno restarts instead of running on with nothing
	// bound to $PORT.
	AppPID int
	// SentinelCommand, when set with Background and AppPID, builds the argv of a
	// sentinel (see RunSentinel) started for the daemon's lifetime: the daemon is
	// not the dyno's main process, so if it is killed outright nothing else would
	// notice and the dyno would keep running with no LB. The sentinel is a
	// separate process (own session, not tied to the daemon via Pdeathsig) that
	// terminates AppPID once the daemon (daemonPID) is gone; the daemon restarts
	// it if it dies.
	SentinelCommand func(daemonPID int) (name string, args []string)
	// AppPort is exported to the app as APP_PORT and PORT. In addition, any
	// $PORT / ${PORT} references in the web command (AppCommand) are rewritten
	// to $APP_PORT / ${APP_PORT} so the app binds APP_PORT rather than the real
	// router PORT that traefik owns.
	AppPort string
	// LBPort is the entrypoint port used for the LB readiness probe. Empty
	// skips the probe.
	LBPort string

	// Env is the base environment for both children (defaults to os.Environ()).
	Env []string

	// Stdout/Stderr for children (default os.Stdout/os.Stderr).
	Stdout io.Writer
	Stderr io.Writer

	// Logf receives structured log lines; nil is a no-op.
	Logf func(level, msg string)

	// ReadyTimeout bounds each readiness probe (default 30s).
	ReadyTimeout time.Duration
	// ReadyInterval is the poll cadence for readiness (default 250ms).
	ReadyInterval time.Duration
	// ShutdownBudget is the total graceful-shutdown budget; kept < 30s so the
	// platform SIGKILL never beats us (default 25s).
	ShutdownBudget time.Duration
	// AppWatchInterval is the Background-mode poll cadence for AppPort /
	// AppPID (default 1s).
	AppWatchInterval time.Duration
	// AppKillGrace is how long AppPID gets to exit after SIGTERM before it is
	// SIGKILLed (default 10s).
	AppKillGrace time.Duration
}

// managedProc is one supervised child.
type managedProc struct {
	name   string
	cmd    *exec.Cmd
	done   <-chan error // nil once the exit has been consumed
	exited bool
}

// Supervisor runs the single lifecycle loop over the LB and app children.
type Supervisor struct {
	cfg  Config
	logf func(level, msg string)
	pm   procManager

	reload   chan struct{}
	shutdown chan struct{}

	// mu guards lb/app, which are (re)assigned by the Run loop and observed by
	// external callers/tests.
	mu  sync.Mutex
	lb  *managedProc
	app *managedProc

	// signalPID delivers signals to AppPID (syscall.Kill; replaced in tests).
	signalPID func(pid int, sig syscall.Signal) error
}

func (s *Supervisor) getLB() *managedProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lb
}

func (s *Supervisor) setLB(mp *managedProc) {
	s.mu.Lock()
	s.lb = mp
	s.mu.Unlock()
}

func (s *Supervisor) getApp() *managedProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.app
}

func (s *Supervisor) setApp(mp *managedProc) {
	s.mu.Lock()
	s.app = mp
	s.mu.Unlock()
}

// markExited records mp's exit under the lock (done consumed, exited=true).
func (s *Supervisor) markExited(mp *managedProc) {
	s.mu.Lock()
	if mp != nil {
		mp.done, mp.exited = nil, true
	}
	s.mu.Unlock()
}

// snapshot returns mp's pid and exited flag under the lock; ok is false if mp is
// nil. Used by external observers/tests without racing the Run loop.
func (s *Supervisor) snapshot(mp *managedProc) (pid int, exited, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mp == nil {
		return 0, false, false
	}
	if mp.cmd != nil && mp.cmd.Process != nil {
		pid = mp.cmd.Process.Pid
	}
	return pid, mp.exited, true
}

// New validates cfg and returns a Supervisor. It does not start anything; call
// Run to take over the process tree.
func New(cfg Config) (*Supervisor, error) {
	if cfg.LBCommand == nil {
		return nil, fmt.Errorf("supervisor: nil LB command")
	}
	if cfg.Background {
		if cfg.AppPort == "" {
			return nil, fmt.Errorf("supervisor: background mode requires an app port")
		}
	} else if len(cfg.AppCommand) == 0 {
		return nil, fmt.Errorf("supervisor: empty app command")
	}
	if cfg.Env == nil {
		cfg.Env = os.Environ()
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 30 * time.Second
	}
	if cfg.ReadyInterval <= 0 {
		cfg.ReadyInterval = 250 * time.Millisecond
	}
	if cfg.ShutdownBudget <= 0 || cfg.ShutdownBudget >= 30*time.Second {
		cfg.ShutdownBudget = 25 * time.Second
	}
	if cfg.AppWatchInterval <= 0 {
		cfg.AppWatchInterval = time.Second
	}
	if cfg.AppKillGrace <= 0 {
		cfg.AppKillGrace = 10 * time.Second
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, string) {}
	}
	return &Supervisor{
		cfg:       cfg,
		logf:      logf,
		reload:    make(chan struct{}, 1),
		shutdown:  make(chan struct{}, 1),
		signalPID: syscall.Kill,
	}, nil
}

// Reload asks the running loop to reload the LB (hot signal or restart, per the
// backend). Non-blocking and coalescing; safe from any goroutine.
func (s *Supervisor) Reload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

// Shutdown asks the running loop to gracefully stop both children and return.
// Non-blocking and idempotent; safe from any goroutine.
func (s *Supervisor) Shutdown() {
	select {
	case s.shutdown <- struct{}{}:
	default:
	}
}

// Run takes over the process tree and blocks until a child exits, a signal or
// Shutdown is received, or ctx is cancelled. It always attempts a graceful
// teardown of both children before returning.
func (s *Supervisor) Run(ctx context.Context) error {
	// PID-1 correctness: become subreaper so orphaned grandchildren reparent to
	// us, and reap them (Linux only; no-op elsewhere).
	maybeSetSubreaper(s.logf)
	s.pm = newProcManager(s.logf)
	defer s.pm.close()

	// PID 1 gets no default signal disposition; wire it explicitly.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Start the LB first, then wait for readiness (no fixed warmup sleep).
	lbCmd := s.buildLBCmd()
	lbDone, err := s.pm.start(lbCmd)
	if err != nil {
		s.terminateAppPID()
		return fmt.Errorf("supervisor: failed to start load balancer: %w", err)
	}
	s.setLB(&managedProc{name: "load-balancer", cmd: lbCmd, done: lbDone})
	s.logf("info", fmt.Sprintf("load balancer started (pid %d)", lbCmd.Process.Pid))
	s.waitReadyLB(ctx)

	if s.cfg.Background && s.cfg.AppPID > 0 && s.cfg.SentinelCommand != nil {
		defer s.keepSentinel()()
	}

	// Start the app, or in Background mode watch the independently started app
	// instead: its process when AppPID is known, else its port (there is no app
	// child; app stays nil and stopAll skips it).
	var appGone <-chan struct{}
	if s.cfg.Background {
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		if s.cfg.AppPID > 0 {
			appGone = watchPID(watchCtx, s.cfg.AppPID, s.cfg.AppWatchInterval)
			s.logf("info", fmt.Sprintf("background mode: watching app process %d", s.cfg.AppPID))
		} else {
			appGone = watchPort(watchCtx, "localhost:"+s.cfg.AppPort, s.cfg.AppWatchInterval, appGoneMisses)
			s.logf("info", "background mode: watching app on port "+s.cfg.AppPort)
		}
	} else {
		appCmd := s.buildAppCmd()
		appDone, err := s.pm.start(appCmd)
		if err != nil {
			lb := s.getLB()
			s.terminate(lb)
			s.waitExit(lb, 5*time.Second)
			return fmt.Errorf("supervisor: failed to start app: %w", err)
		}
		s.setApp(&managedProc{name: "app", cmd: appCmd, done: appDone})
		s.logf("info", fmt.Sprintf("app started (pid %d): %v", appCmd.Process.Pid, s.cfg.AppCommand))
	}

	// The one lifecycle loop, serving both foreground and background modes.
	for {
		lb := s.getLB()
		app := s.getApp()
		var lbCh, appCh <-chan error
		if lb != nil {
			lbCh = lb.done
		}
		if app != nil {
			appCh = app.done
		}

		select {
		// Either child dying is fatal to the dyno: return an error (non-zero
		// exit) so the platform restarts it. In Background mode we are not the
		// dyno's main process, so also take that down (terminateAppPID).
		case err := <-lbCh:
			s.markExited(lb)
			s.logf("error", "load balancer exited: "+errStr(err))
			s.stopAll()
			if s.terminateAppPID() {
				return fmt.Errorf("load balancer exited: %w", orNoErr(err))
			}
			// The main process outlived SIGTERM and SIGKILL: it is a namespace
			// init (PID 1, e.g. on Fir) with no SIGTERM handler, which the kernel
			// shields from both. The dyno cannot be restarted from here, so keep
			// it serving by relaunching the LB in place.
			s.logf("error", fmt.Sprintf("dyno main process %d cannot be terminated; relaunching the load balancer", s.cfg.AppPID))
			time.Sleep(time.Second)
			if rerr := s.startLB(ctx); rerr != nil {
				return fmt.Errorf("load balancer exited and could not be relaunched: %w", rerr)
			}

		case err := <-appCh:
			s.markExited(app)
			s.logf("error", "app exited: "+errStr(err))
			s.stopAll()
			return fmt.Errorf("app exited: %w", orNoErr(err))

		case <-appGone:
			if s.cfg.AppPID > 0 {
				s.logf("info", fmt.Sprintf("app process %d exited, shutting down", s.cfg.AppPID))
			} else {
				s.logf("info", "app stopped listening on port "+s.cfg.AppPort+", shutting down")
			}
			s.stopAll()
			return nil

		case sig := <-sigChan:
			s.logf("info", "received signal "+sig.String()+", shutting down")
			s.stopAll()
			return nil

		case <-s.reload:
			if err := s.doReload(ctx); err != nil {
				s.logf("error", err.Error())
				s.stopAll()
				s.terminateAppPID()
				return err
			}

		case <-s.shutdown:
			s.logf("info", "shutdown requested")
			s.stopAll()
			return nil

		case <-ctx.Done():
			s.logf("info", "context cancelled, shutting down")
			s.stopAll()
			return ctx.Err()
		}
	}
}

// buildLBCmd builds a fresh *exec.Cmd for the LB from Config.LBCommand.
func (s *Supervisor) buildLBCmd() *exec.Cmd {
	name, args := s.cfg.LBCommand()
	cmd := exec.Command(name, args...)
	cmd.Stdout = s.cfg.Stdout
	cmd.Stderr = s.cfg.Stderr
	cmd.Env = s.cfg.Env
	cmd.SysProcAttr = procAttr()
	return cmd
}

// rewritePortRefs rewrites shell references to the PORT variable in the web
// command to APP_PORT. The supervised app must listen on APP_PORT (traefik owns
// the real PORT), but Procfile web commands are written against $PORT. We steer
// them to APP_PORT here rather than clobbering the PORT env var, which is
// unreliable (a duplicate PORT entry in the child env is shadowed by the
// platform's real PORT).
//
// Both the bare form ($PORT) and the braced form (${PORT}, including
// default-value expansions like ${PORT:-3000}) are handled — only the variable
// name is replaced, so any brace modifier is preserved. Matching is bounded to
// whole shell identifiers: $PORTAL, $PORT_NAME and a bare PORT (no leading $)
// are left untouched, as is $APP_PORT.
var portRefRe = regexp.MustCompile(`\$\{PORT\b|\$PORT\b`)

func rewritePortRefs(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = portRefRe.ReplaceAllStringFunc(a, func(m string) string {
			if strings.HasPrefix(m, "${") {
				return "${APP_PORT"
			}
			return "$APP_PORT"
		})
	}
	return out
}

// buildAppCmd builds a fresh *exec.Cmd for the web command.
func (s *Supervisor) buildAppCmd() *exec.Cmd {
	args := rewritePortRefs(s.cfg.AppCommand)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = s.cfg.Stdout
	cmd.Stderr = s.cfg.Stderr
	cmd.Stdin = os.Stdin
	env := append([]string(nil), s.cfg.Env...)
	if s.cfg.AppPort != "" {
		env = append(env, "APP_PORT="+s.cfg.AppPort, "PORT="+s.cfg.AppPort)
	}
	cmd.Env = env
	cmd.SysProcAttr = procAttr()
	return cmd
}

// waitReadyLB probes the LB entrypoint port until it accepts a connection,
// bounded by ReadyTimeout. A miss is logged but not fatal (the port may differ
// from our expectation); the loop proceeds.
func (s *Supervisor) waitReadyLB(ctx context.Context) {
	if s.cfg.LBPort == "" {
		return
	}
	if waitReady(ctx, "localhost:"+s.cfg.LBPort, s.cfg.ReadyInterval, s.cfg.ReadyTimeout) {
		s.logf("info", "load balancer ready on port "+s.cfg.LBPort)
	} else {
		s.logf("warn", "load balancer not ready on port "+s.cfg.LBPort+" within timeout; continuing")
	}
}

// doReload reloads the LB according to Config.ReloadSignal. A
// non-nil error means the LB is gone (a restart could not relaunch it), which
// the loop treats like an LB death.
func (s *Supervisor) doReload(ctx context.Context) error {
	lb := s.getLB()
	if lb == nil || lb.exited {
		return nil
	}
	sig := s.cfg.ReloadSignal
	if sig == nil {
		s.logf("info", "reload: restarting load balancer")
		s.terminate(lb)
		if !s.waitExit(lb, s.cfg.ShutdownBudget/2) {
			s.kill(lb)
			s.waitExit(lb, 2*time.Second)
		}
		if err := s.startLB(ctx); err != nil {
			return fmt.Errorf("reload: failed to restart load balancer: %w", err)
		}
		return nil
	}
	// Hot reload: deliver the signal to the LB process directly.
	if lb.cmd.Process != nil {
		if err := lb.cmd.Process.Signal(sig); err != nil {
			s.logf("error", "reload: failed to signal load balancer: "+err.Error())
		} else {
			s.logf("info", "reload: signalled load balancer with "+sig.String())
		}
	}
	return nil
}

// startLB launches a fresh LB process in place of an exited one and waits for
// it to accept connections.
func (s *Supervisor) startLB(ctx context.Context) error {
	cmd := s.buildLBCmd()
	done, err := s.pm.start(cmd)
	if err != nil {
		return err
	}
	s.setLB(&managedProc{name: "load-balancer", cmd: cmd, done: done})
	s.logf("info", fmt.Sprintf("load balancer restarted (pid %d)", cmd.Process.Pid))
	s.waitReadyLB(ctx)
	return nil
}

// stopAll performs the graceful teardown: SIGTERM the app first (it holds client
// connections), wait within budget, then the LB; SIGKILL anything that overruns.
func (s *Supervisor) stopAll() {
	budget := s.cfg.ShutdownBudget
	appBudget := budget * 4 / 5
	lbBudget := budget - appBudget

	app := s.getApp()
	s.terminate(app)
	if !s.waitExit(app, appBudget) {
		s.logf("warn", "app did not exit within budget; killing")
		s.kill(app)
		s.waitExit(app, 2*time.Second)
	}

	lb := s.getLB()
	s.terminate(lb)
	if !s.waitExit(lb, lbBudget) {
		s.logf("warn", "load balancer did not exit within budget; killing")
		s.kill(lb)
		s.waitExit(lb, 2*time.Second)
	}
}

// terminateAppPID takes down the dyno's main process in Background mode so the
// platform restarts the dyno: SIGTERM, then SIGKILL if it outlives
// AppKillGrace. Only that one pid is signalled (it is not our child and not in
// our process group); the platform tears down the rest of the dyno. Reports
// false only when the pid survives both signals (a PID 1 without a SIGTERM
// handler); true otherwise, including when there is no AppPID to take down.
func (s *Supervisor) terminateAppPID() bool {
	pid := s.cfg.AppPID
	if !s.cfg.Background || pid <= 0 {
		return true
	}
	return terminatePID(pid, s.cfg.AppKillGrace, s.signalPID, s.logf)
}

// terminatePID SIGTERMs pid, then SIGKILLs it if it outlives grace. Reports
// false only when it survives both; true otherwise, including when it is
// already gone.
func terminatePID(pid int, grace time.Duration, signalPID func(int, syscall.Signal) error, logf func(level, msg string)) bool {
	if !pidAlive(pid) {
		return true
	}
	logf("error", fmt.Sprintf("terminating dyno main process %d so the dyno restarts", pid))
	_ = signalPID(pid, syscall.SIGTERM)
	if waitPIDGone(pid, grace) {
		return true
	}
	logf("warn", fmt.Sprintf("dyno main process %d did not exit within %v; killing", pid, grace))
	_ = signalPID(pid, syscall.SIGKILL)
	return waitPIDGone(pid, 2*time.Second)
}

func waitPIDGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !pidAlive(pid)
}

// terminate SIGTERMs the process group of mp (if still running).
func (s *Supervisor) terminate(mp *managedProc) {
	if mp == nil || mp.exited {
		return
	}
	signalGroup(mp.cmd, syscall.SIGTERM)
}

// kill SIGKILLs the process group of mp.
func (s *Supervisor) kill(mp *managedProc) {
	if mp == nil || mp.exited {
		return
	}
	signalGroup(mp.cmd, syscall.SIGKILL)
}

// waitExit waits up to timeout for mp to exit, consuming its done channel.
// Returns true if mp has exited (or was already gone).
func (s *Supervisor) waitExit(mp *managedProc, timeout time.Duration) bool {
	if mp == nil || mp.exited || mp.done == nil {
		return true
	}
	select {
	case err := <-mp.done:
		s.markExited(mp)
		s.logf("debug", mp.name+" reaped: "+errStr(err))
		return true
	case <-time.After(timeout):
		return false
	}
}

func errStr(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func orNoErr(err error) error {
	if err == nil {
		return fmt.Errorf("clean exit")
	}
	return err
}
