// Package engine is the thin orchestrator that ties the pipeline together:
// project.toml -> route model -> render -> supervise.
//
// The flow is:
//
//  1. Load the routes from project.toml and add the standard dashboard, metrics
//     and default-app concerns into a config.RouteModel, then render it with
//     traefik.Render into Traefik config files on disk.
//  2. Hand the rendered config to the supervisor to start (or, on project.toml
//     change, to reload).
//
// The file watcher re-runs step 1 and asks the supervisor to Reload.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heroku-examples/dyno-proxy/internal/config"
	"github.com/heroku-examples/dyno-proxy/internal/supervisor"
	"github.com/heroku-examples/dyno-proxy/internal/traefik"
)

// Config is the resolved input to a single engine run. main() populates it from
// the CLI and the environment.
type Config struct {
	// AppCommand is the web command (argv) dyno-proxy supervises as a child.
	// Unused when Background is set.
	AppCommand []string
	// Background runs only the load balancer, with no app child: the web
	// command is started separately and binds APP_PORT (see supervisor.Config).
	Background bool
	// AppPID is the dyno's main process in Background mode (0 = unknown); see
	// supervisor.Config.AppPID.
	AppPID int
	// ProjectPath overrides the config file (for testing); empty means
	// project.toml in WorkingDir. It uses the same format as project.toml.
	ProjectPath string
	// WorkingDir is where project.toml lives; defaults to the process working
	// directory.
	WorkingDir string
	// Getenv reads the environment; defaults to os.Getenv.
	Getenv func(key string) string
}

const (
	dashboardPathPrefix = "/.dyno-proxy/traefik"
	metricsPathPrefix   = "/.dyno-proxy/metrics"
)

// resolved holds the values derived once from Config + environment and reused by
// both the first render and every watch-triggered re-render.
type resolved struct {
	getenv      func(string) string // sees the resolved APP_PORT
	workingDir  string
	projectPath string            // <workingDir>/project.toml
	model       config.RouteModel // everything but the routes, which render loads
	renderOpts  traefik.Options   // carries the LB port, log level and format
	appPort     string            // app port (APP_PORT), exported to the web command
}

// Run executes the full pipeline and blocks until the supervised process tree
// exits (see supervisor.Run). It renders the initial config, wires the project.toml
// watcher to Reload, and supervises both the load balancer and the web command.
func Run(ctx context.Context, cfg Config) error {
	if len(cfg.AppCommand) == 0 && !cfg.Background {
		return fmt.Errorf("engine: no app command provided")
	}

	r, err := cfg.resolve()
	if err != nil {
		return err
	}

	logf := newLogger(r.renderOpts.LogLevel, r.renderOpts.LogFormat)

	// Make the bundled local plugins discoverable by Traefik before it
	// starts (Traefik resolves a full-import-path moduleName under
	// <cwd>/plugins-local/src).
	linkLocalPlugins(r.workingDir, r.getenv, logf)

	// Step 1 render.
	rendered, err := r.render()
	if err != nil {
		return fmt.Errorf("engine: render failed: %w", err)
	}
	logf("info", "rendered config: "+strings.Join(rendered.Files, ", "))

	// At DEBUG (or TRACE) print the effective static and dynamic config so
	// operators can inspect what was rendered without hunting for the files.
	if parseLevel(r.renderOpts.LogLevel) <= levelDebug {
		logRenderedConfig(os.Stdout, rendered)
	}

	sup, err := supervisor.New(supervisor.Config{
		LBCommand:    func() (string, []string) { return traefik.Command(rendered) },
		ReloadSignal: traefik.ReloadSignal,
		AppCommand:   cfg.AppCommand,
		Background:   cfg.Background,
		AppPID:       cfg.AppPID,
		SentinelCommand: func(daemonPID int) (string, []string) {
			exe, err := os.Executable()
			if err != nil {
				exe = os.Args[0]
			}
			return exe, []string{"sentinel", "--daemon-pid", strconv.Itoa(daemonPID), "--app-pid", strconv.Itoa(cfg.AppPID)}
		},
		AppPort: r.appPort,
		LBPort:  r.renderOpts.Port,
		Logf:    logf,
	})
	if err != nil {
		return err
	}

	// The watcher re-runs load+render and asks the supervisor to reload.
	if fileExists(r.projectPath) {
		go watch(ctx, r.projectPath, logf, func() {
			if _, err := r.render(); err != nil {
				logf("error", "regenerate failed: "+err.Error())
				return
			}
			logf("info", "project.toml changed, reloading")
			sup.Reload()
		})
	}

	return sup.Run(ctx)
}

// DumpConfig runs the config pipeline (resolve -> load -> render) and writes the
// rendered load-balancer config files to w, then returns without starting the
// supervisor. Unlike Run it does not require a web command.
//
// It renders to the same on-disk location Run would (so cross-references between
// the files — e.g. the static config's pointer at the dynamic config path — are
// correct), then streams each file to w preceded by a "# <path>" comment header
// so multiple files are distinguishable in a single stream.
func DumpConfig(cfg Config, w io.Writer) error {
	r, err := cfg.resolve()
	if err != nil {
		return err
	}

	rendered, err := r.render()
	if err != nil {
		return fmt.Errorf("engine: render failed: %w", err)
	}

	for _, path := range rendered.Files {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("engine: reading rendered config %s: %w", path, err)
		}
		if _, err := fmt.Fprintf(w, "# %s\n", path); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

// resolve derives all reusable values from cfg + environment.
func (cfg Config) resolve() (*resolved, error) {
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	wd := cfg.WorkingDir
	if wd == "" {
		if cwd, err := os.Getwd(); err == nil {
			wd = cwd
		}
	}

	// Ports: PORT is the LB entrypoint; APP_PORT is the first free port above
	// PORT (normally PORT+1, see defaultAppPort) unless set. When it was not
	// set, overlay it on getenv so route loading (the default app service) and
	// the supervised child agree on the port without mutating the process
	// environment.
	port := getenv("PORT")
	if port == "" {
		port = "8888"
	}
	appPort := getenv("APP_PORT")
	if appPort == "" {
		portNum, err := strconv.Atoi(port)
		if err != nil {
			portNum = 8888
		}
		appPort = strconv.Itoa(defaultAppPort(portNum))
		base := getenv
		getenv = func(key string) string {
			if key == "APP_PORT" {
				return appPort
			}
			return base(key)
		}
	}

	logLevel := getenv("DYNO_PROXY_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "ERROR"
	}

	// Log output is key=value by default; DYNO_PROXY_LOG_FORMAT=json switches to
	// JSON lines.
	logFormat := getenv("DYNO_PROXY_LOG_FORMAT")

	projectPath := cfg.ProjectPath
	if projectPath == "" {
		projectPath = filepath.Join(wd, config.FileName)
	}

	// The dashboard and metrics routes are only served when admin credentials are
	// configured.
	var dashboard config.DashboardRoute
	var metrics config.MetricsRoute
	if user, hashedPass, ok := adminCredentials(getenv); ok {
		dashboard = config.DashboardRoute{PathPrefix: dashboardPathPrefix, User: user, HashedPassword: hashedPass}
		metrics = config.MetricsRoute{PathPrefix: metricsPathPrefix}
	}

	// Output filenames are "<name>.dyno-proxy-*.toml" next to the config file.
	return &resolved{
		getenv:      getenv,
		workingDir:  wd,
		projectPath: projectPath,
		model: config.RouteModel{
			DefaultServiceURL: fmt.Sprintf("http://localhost:%s", appPort),
			Dashboard:         dashboard,
			Metrics:           metrics,
		},
		renderOpts: traefik.Options{
			OutputDir: filepath.Dir(projectPath),
			Port:      port,
			LogLevel:  logLevel,
			LogFormat: logFormat,
			BaseName:  strings.TrimSuffix(filepath.Base(projectPath), filepath.Ext(projectPath)),
			Getenv:    getenv,
		},
		appPort: appPort,
	}, nil
}

// render loads the routes from project.toml into the RouteModel,
// then renders it with Traefik. Reused by the first boot and the watcher.
func (r *resolved) render() (traefik.Rendered, error) {
	routes, err := config.Load(r.projectPath, r.getenv)
	if err != nil {
		return traefik.Rendered{}, err
	}
	model := r.model
	model.Routes = routes
	return traefik.Render(model, r.renderOpts)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
