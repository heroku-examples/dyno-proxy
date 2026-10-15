package traefik

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heroku-examples/dyno-proxy/internal/config"
)

// buildRuleParts builds the Traefik router rule predicates from a neutral
// matcher. Ported from shared.go:buildRuleParts, operating on config.Matcher.
// Map dimensions are emitted in sorted key order for deterministic output.
func buildRuleParts(m config.Matcher) []string {
	var parts []string

	if m.Path != "" {
		switch {
		case strings.Contains(m.Path, ".*"):
			parts = append(parts, fmt.Sprintf("PathRegexp(`%s`)", m.Path))
		case strings.HasSuffix(m.Path, "*"):
			clean := strings.TrimSuffix(m.Path, "*")
			parts = append(parts, fmt.Sprintf("PathPrefix(`%s`)", clean))
		default:
			parts = append(parts, fmt.Sprintf("Path(`%s`)", m.Path))
		}
	}

	for _, name := range sortedKeys(m.Headers) {
		parts = append(parts, headerRule(name, m.Headers[name]))
	}

	for _, name := range sortedKeys(m.Query) {
		v := m.Query[name]
		switch {
		case v == "":
			parts = append(parts, fmt.Sprintf("Query(`%s`)", name))
		case strings.Contains(v, "*"):
			regex := strings.ReplaceAll(v, "*", ".*")
			parts = append(parts, fmt.Sprintf("QueryRegexp(`%s`, `%s`)", name, regex))
		default:
			parts = append(parts, fmt.Sprintf("Query(`%s`, `%s`)", name, v))
		}
	}

	for _, field := range sortedKeys(m.Body) {
		clean := field
		if strings.HasPrefix(clean, "$.") {
			clean = strings.TrimPrefix(clean, "$.")
		} else if strings.HasPrefix(clean, ".") {
			clean = strings.TrimPrefix(clean, ".")
		}
		parts = append(parts, headerRule("X-Body-"+clean, m.Body[field]))
	}

	if len(m.Methods) > 0 {
		// Traefik's Method matcher accepts exactly one parameter. To match
		// multiple methods, OR together single-param Method() calls. The OR
		// group must be wrapped in parentheses so it stays a single operand
		// when the overall rule joins predicates with " && ", since && binds
		// tighter than || in Traefik's rule grammar.
		calls := make([]string, len(m.Methods))
		for i, meth := range m.Methods {
			calls[i] = fmt.Sprintf("Method(`%s`)", meth)
		}
		if len(calls) == 1 {
			parts = append(parts, calls[0])
		} else {
			parts = append(parts, "("+strings.Join(calls, " || ")+")")
		}
	}

	return parts
}

// headerRule builds a Header or HeaderRegexp predicate for a header match.
// A value containing "*" is treated as a wildcard (each "*" becomes ".*") and
// matched with HeaderRegexp; a value already containing ".*" is used as a regex
// verbatim. Plain values use an exact Header match. This mirrors the Path /
// PathPrefix / PathRegexp handling in buildRuleParts.
func headerRule(name, expected string) string {
	switch {
	case strings.Contains(expected, ".*"):
		return fmt.Sprintf("HeaderRegexp(`%s`, `%s`)", name, expected)
	case strings.Contains(expected, "*"):
		regex := strings.ReplaceAll(expected, "*", ".*")
		return fmt.Sprintf("HeaderRegexp(`%s`, `%s`)", name, regex)
	default:
		return fmt.Sprintf("Header(`%s`, `%s`)", name, expected)
	}
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
