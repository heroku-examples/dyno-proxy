// Package conformance is the shared functional harness for dyno-proxy. Given a
// config.RouteModel, it boots the full stack:
//
//   - stub upstreams via net/http/httptest (in-process),
//   - renders the config via the traefik package,
//   - launches the real load-balancer binary via internal/supervisor,
//   - waits on readiness by polling the proxy port (never a fixed sleep),
//   - hands back an *http.Client pointed at the proxy.
//
// Teardown closes everything. If the traefik binary is not installed on
// this machine, Boot calls t.Skip with a clear message so the suite still
// compiles and the render/golden layers keep running.
package conformance

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/heroku-examples/dyno-proxy/internal/config"
	"github.com/heroku-examples/dyno-proxy/internal/supervisor"
	"github.com/heroku-examples/dyno-proxy/internal/traefik"
)

// Stub is an in-process upstream. Every response carries an X-Upstream header
// naming the stub and echoes the request so tests can assert which upstream
// served a request and what it received.
type Stub struct {
	Label  string
	Server *httptest.Server

	mu       sync.Mutex
	requests []RecordedRequest
}

// RecordedRequest captures a request a stub received.
type RecordedRequest struct {
	Method  string
	Path    string
	RawPath string
	Query   string
	Headers http.Header
}

// NewStub starts an httptest upstream labeled label. It records every request it
// receives and is closed automatically at test end.
func NewStub(t *testing.T, label string) *Stub {
	t.Helper()
	s := &Stub{Label: label}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, RecordedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			RawPath: r.URL.EscapedPath(),
			Query:   r.URL.RawQuery,
			Headers: r.Header.Clone(),
		})
		s.mu.Unlock()
		w.Header().Set("X-Upstream", label)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "upstream=%s path=%s", label, r.URL.Path)
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// URL is the base URL clients/routes should target for this stub.
func (s *Stub) URL() string { return s.Server.URL }

// Requests returns a snapshot of the requests the stub has received.
func (s *Stub) Requests() []RecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// Count returns how many requests the stub has received.
func (s *Stub) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Harness is a booted proxy stack.
type Harness struct {
	// ProxyURL is the base URL of the load balancer entrypoint.
	ProxyURL string
	// Client is an *http.Client pointed at the proxy (does not follow redirects
	// automatically and has a short timeout).
	Client *http.Client
	// Files are the rendered config files (for debugging on failure).
	Files []string

	sup    *supervisor.Supervisor
	cancel context.CancelFunc
	done   chan error
}

// BootOptions tunes a Boot.
type BootOptions struct {
	// LogLevel passed to Render (default "ERROR" to keep test output quiet).
	LogLevel string
	// Getenv is passed to Render for value resolution; may be nil.
	Getenv func(string) string
	// ReadyTimeout bounds readiness polling (default 20s).
	ReadyTimeout time.Duration
}

// Boot renders model with Traefik and launches it via the supervisor. It skips
// the test (t.Skip) if the traefik binary is not installed.
func Boot(t *testing.T, model config.RouteModel, opts BootOptions) *Harness {
	t.Helper()

	// Resolve the LB binary; skip cleanly if absent so the suite still runs.
	binName, _ := traefik.Command(traefik.Rendered{})
	if _, err := exec.LookPath(binName); err != nil {
		t.Skipf("%q not found in PATH; skipping live-traffic test (render/golden tests still run)", binName)
	}

	port := freePort(t)
	logLevel := opts.LogLevel
	if logLevel == "" {
		logLevel = "ERROR"
	}

	dir := t.TempDir()
	rendered, err := traefik.Render(model, traefik.Options{
		OutputDir: dir,
		Port:      port,
		LogLevel:  logLevel,
		BaseName:  "e2e",
		Getenv:    opts.Getenv,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	sup, err := supervisor.New(supervisor.Config{
		LBCommand:    func() (string, []string) { return traefik.Command(rendered) },
		ReloadSignal: traefik.ReloadSignal,
		// The web app is replaced by in-process stubs; the supervisor still
		// requires an app command, so give it a harmless long-running sleeper.
		AppCommand:     []string{"sleep", "3600"},
		LBPort:         port,
		Stdout:         io.Discard,
		Stderr:         io.Discard,
		ReadyTimeout:   readyTimeout(opts),
		ReadyInterval:  50 * time.Millisecond,
		ShutdownBudget: 5 * time.Second,
		Logf:           func(string, string) {},
	})
	if err != nil {
		t.Fatalf("supervisor.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &Harness{
		ProxyURL: "http://localhost:" + port,
		Client:   &http.Client{Timeout: 5 * time.Second},
		Files:    rendered.Files,
		sup:      sup,
		cancel:   cancel,
		done:     make(chan error, 1),
	}
	go func() { h.done <- sup.Run(ctx) }()

	t.Cleanup(h.Close)

	if !h.waitReady(readyTimeout(opts)) {
		// Dump config to aid debugging, then fail.
		t.Fatalf("proxy %s did not become ready within %s (config files: %v)", h.ProxyURL, readyTimeout(opts), rendered.Files)
	}
	return h
}

func readyTimeout(opts BootOptions) time.Duration {
	if opts.ReadyTimeout > 0 {
		return opts.ReadyTimeout
	}
	return 20 * time.Second
}

// waitReady polls the proxy until its *dynamic* routing config is live, or
// timeout elapses. A bare TCP/HTTP probe is insufficient: the entrypoint binds
// (and returns 404s) before the file provider has parsed the routers/services,
// which races real requests. We therefore poll GET / until it reaches an
// upstream stub (the default app), evidenced by the stub's X-Upstream response
// header. Every model in the harness routes / to the default app service, so
// this reliably signals "dynamic config loaded".
func (h *Harness) waitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	probe := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := probe.Get(h.ProxyURL + "/")
		if err == nil {
			upstream := resp.Header.Get("X-Upstream")
			resp.Body.Close()
			if upstream != "" {
				return true
			}
		}
		// Fail fast if the supervisor exited (LB crashed).
		select {
		case <-h.done:
			return false
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Get issues a GET to the given path (e.g. "/api?x=1") through the proxy.
func (h *Harness) Get(t *testing.T, path string) *http.Response {
	t.Helper()
	return h.Do(t, http.MethodGet, path, nil, nil)
}

// Do issues an arbitrary request through the proxy with optional headers/body.
func (h *Harness) Do(t *testing.T, method, path string, headers map[string]string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, h.ProxyURL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	return resp
}

// Close tears down the proxy stack. Safe to call multiple times.
func (h *Harness) Close() {
	if h.sup != nil {
		h.sup.Shutdown()
	}
	if h.cancel != nil {
		h.cancel()
	}
	select {
	case <-h.done:
	case <-time.After(8 * time.Second):
	}
}

// freePort reserves and releases a free TCP port, returning it as a string.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

// ReadUpstream returns the X-Upstream label from a proxied response and drains
// the body.
func ReadUpstream(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("X-Upstream")
}
