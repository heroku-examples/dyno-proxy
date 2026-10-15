package engine

import (
	"net"
	"strconv"
)

// appPortScan is how many ports above PORT defaultAppPort tries.
const appPortScan = 20

// defaultAppPort picks APP_PORT when it is not set: the first port in
// port+1..port+appPortScan that nothing is listening on, falling back to
// port+1. Normally that is port+1; on Fir a bind shim in the dyno already owns
// port+1 and port+2 ([::]:8001 forwarding to the app on 8000, health on 8002),
// so a blind port+1 made the app's bind fail with EADDRINUSE. The CNB exec.d
// hook (buildpack/bin/build) and the classic .profile.d script
// (buildpack/bin/compile) apply the same rule in bash.
func defaultAppPort(port int) int {
	for p := port + 1; p <= port+appPortScan; p++ {
		if portFree(p) {
			return p
		}
	}
	return port + 1
}

// portFree reports whether a wildcard listener (what an app binding [::] or
// 0.0.0.0 asks for) can be opened on p.
func portFree(p int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}
