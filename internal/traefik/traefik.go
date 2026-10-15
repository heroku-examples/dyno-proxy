// Package traefik renders a config.RouteModel into Traefik's static + dynamic
// TOML config and tells the supervisor how to launch and reload Traefik, the
// load balancer dyno-proxy always runs.
package traefik

import (
	"os"
	"syscall"
)

// defaultTraefikVersion is the canonical Traefik version pin. The buildpack
// scripts buildpack/bin/compile and buildpack/bin/build carry a bare (no-"v")
// mirror of it as their TRAEFIK_VERSION default, and .github/workflows/test.yml
// derives its version from this const at CI time. The scheduled
// .github/workflows/bump-traefik.yml job keeps this const and the two buildpack
// mirrors in sync automatically. If you edit this line by hand, update the two
// buildpack defaults to match, and keep the const's shape (`= "vX.Y.Z"`) stable
// so the greps in test.yml and bump-traefik.yml keep matching.
const defaultTraefikVersion = "v3.7.13"

// Options carries render-time inputs beyond the model itself.
type Options struct {
	// OutputDir is the directory rendered config files are written to.
	OutputDir string

	// Port is the entrypoint port Traefik listens on (PORT).
	Port string

	// LogLevel is the desired Traefik log level (e.g. "info", "warn").
	LogLevel string

	// LogFormat is the desired log output format. Empty means Traefik's text
	// default ("common"); "json" selects JSON.
	LogFormat string

	// BaseName is the prefix used for generated file names.
	BaseName string

	// Getenv reads an environment variable. May be nil (os.Getenv).
	Getenv func(key string) string
}

// Rendered is the result of Render: the files that were written.
type Rendered struct {
	// StaticConfigPath is the static config file Traefik is launched against.
	StaticConfigPath string

	// DynamicConfigPath is the hot-reloaded dynamic config file.
	DynamicConfigPath string

	// Files lists every file written (absolute paths).
	Files []string
}

// Command returns the executable name and args that launch Traefik against the
// rendered static config.
func Command(r Rendered) (string, []string) {
	return "traefik", []string{"--configFile=" + r.StaticConfigPath}
}

// ReloadSignal is the signal that triggers a config reload. Traefik's file
// provider is configured with watch=true, so re-rendering the dynamic config
// file is picked up automatically; SIGHUP is a benign signal that keeps the same
// process across reloads.
var ReloadSignal os.Signal = syscall.SIGHUP
