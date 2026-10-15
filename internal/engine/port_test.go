package engine

import (
	"net"
	"strconv"
	"testing"
)

// busyRun holds wildcard listeners on n consecutive ports (n may be 0) whose
// next port is free, and returns the first port of the run. It retries from
// fresh kernel-assigned ports until such a run is found.
func busyRun(t *testing.T, n int) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		first := l.Addr().(*net.TCPAddr).Port
		l.Close()
		var ls []net.Listener
		ok := true
		for p := first; p < first+n; p++ {
			lp, err := net.Listen("tcp", ":"+strconv.Itoa(p))
			if err != nil {
				ok = false
				break
			}
			ls = append(ls, lp)
		}
		if ok && portFree(first+n) {
			t.Cleanup(func() {
				for _, l := range ls {
					l.Close()
				}
			})
			return first
		}
		for _, l := range ls {
			l.Close()
		}
	}
	t.Fatalf("could not find a run of %d busy ports followed by a free one", n)
	return 0
}

func TestDefaultAppPortIsPortPlusOne(t *testing.T) {
	first := busyRun(t, 0) // first is free
	port := first - 1
	if got := defaultAppPort(port); got != port+1 {
		t.Errorf("defaultAppPort(%d) = %d, want %d", port, got, port+1)
	}
}

// Fir: a bind shim already listens on PORT+1 and PORT+2. APP_PORT must skip
// them instead of handing the app a port it cannot bind.
func TestDefaultAppPortSkipsListeners(t *testing.T) {
	first := busyRun(t, 2) // first, first+1 busy; first+2 free
	port := first - 1
	if got := defaultAppPort(port); got != port+3 {
		t.Errorf("defaultAppPort(%d) with %d,%d busy = %d, want %d", port, first, first+1, got, port+3)
	}
}
