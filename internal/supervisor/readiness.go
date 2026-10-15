package supervisor

import (
	"context"
	"net"
	"time"
)

// waitReady polls addr with a TCP dial until it accepts a connection, returning
// true on success. It gives up when timeout elapses or ctx is cancelled,
// returning false. This generalizes the old PortWatcher TCP-poll (used for both
// "LB up?" and "app up?") and replaces the fixed 2s warmup sleep.
func waitReady(ctx context.Context, addr string, interval, timeout time.Duration) bool {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)

	// Fast first attempt.
	if dialOnce(addr, interval) {
		return true
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if dialOnce(addr, interval) {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
		}
	}
}

// dialOnce reports whether addr accepts a TCP connection within timeout.
func dialOnce(addr string, timeout time.Duration) bool {
	if timeout <= 0 || timeout > time.Second {
		timeout = time.Second
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// appGoneMisses is how many consecutive failed probes of the app port count as
// "the app went away" in Background mode. More than one so a brief listener gap
// (e.g. a hot restart) does not permanently take the LB down.
const appGoneMisses = 3

// watchPort is the Background-mode app watcher (the successor to the legacy
// PortWatcher/monitor). It polls addr every interval, with no deadline for the
// first connection since app boot time varies, and closes the returned channel
// once addr has accepted a connection and then refused misses consecutive
// probes. It never closes if the app never comes up; it stops when ctx is done.
func watchPort(ctx context.Context, addr string, interval time.Duration, misses int) <-chan struct{} {
	gone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		up, missed := false, 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if dialOnce(addr, interval) {
				up, missed = true, 0
				continue
			}
			if !up {
				continue
			}
			if missed++; missed >= misses {
				close(gone)
				return
			}
		}
	}()
	return gone
}

// watchPID is the Background-mode watcher for a known app pid (the dyno's main
// process, which is not our child). It polls every interval and closes the
// returned channel once pid no longer exists (or is a zombie); it stops when
// ctx is done.
func watchPID(ctx context.Context, pid int, interval time.Duration) <-chan struct{} {
	gone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !pidAlive(pid) {
				close(gone)
				return
			}
		}
	}()
	return gone
}
