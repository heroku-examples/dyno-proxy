// Package project loads routing rules from project.toml, the only configuration
// source.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/BurntSushi/toml"
)

// FileName is the configuration file, looked up in the working directory.
const FileName = "project.toml"

// Load reads the `routes` array of the project.toml at path into
// routes. A missing file, or one without routes (a pure buildpack descriptor),
// yields none. getenv resolves $VAR references in targets and the default app
// port.
func Load(path string, getenv func(string) string) ([]Route, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var doc struct {
		Routes []route `toml:"routes"`
	}
	if err := toml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %v", FileName, err)
	}

	var routes []Route
	for _, r := range doc.Routes {
		routes = append(routes, buildRoutes(r, getenv)...)
	}
	return routes, nil
}
