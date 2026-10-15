package engine

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/heroku-examples/dyno-proxy/internal/traefik"
)

// gutter prefixes every emitted config line. Heroku's log pipeline (and many
// log drains) strip leading whitespace from each line, which would collapse the
// indentation and make the structure unreadable. A non-whitespace gutter at
// column 0 protects the spaces that follow it, so the indentation survives.
const gutter = "| "

// logRenderedConfig writes the rendered static and dynamic load-balancer config
// to w, each preceded by a "# <label> config: <path>" header. It is meant for
// DEBUG-level startup output so operators can see the effective config without
// hunting for the files.
//
// A read failure for one file is reported inline and does not abort the others:
// debug output must never break startup.
func logRenderedConfig(w io.Writer, rendered traefik.Rendered) {
	for _, f := range []struct {
		label string
		path  string
	}{
		{"static", rendered.StaticConfigPath},
		{"dynamic", rendered.DynamicConfigPath},
	} {
		if f.path == "" {
			continue
		}
		data, err := os.ReadFile(f.path)
		if err != nil {
			fmt.Fprintf(w, "# %s config: %s (unavailable: %v)\n", f.label, f.path, err)
			continue
		}
		fmt.Fprintf(w, "# %s config: %s\n", f.label, f.path)
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			fmt.Fprintf(w, "%s%s\n", gutter, line)
		}
	}
}
