package config

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// route is a single routing rule as it appears in project.toml. It carries just
// enough to build a Route: matchers, a target, and the ordered middleware
// directives (plugins, middleware, catch, body, set).
type route struct {
	Path       string            `toml:"path"`
	To         string            `toml:"to"`
	Header     map[string]string `toml:"header"`
	Query      map[string]string `toml:"query"`
	Body       map[string]string `toml:"body"`
	Methods    []string          `toml:"methods"`
	Percentage int               `toml:"percentage"`
	Mirror     bool              `toml:"mirror"`

	Plugins    []plugin                 `toml:"plugins"`
	Middleware []map[string]interface{} `toml:"middleware"`
	Set        []setItem                `toml:"set"`
	Catch      []catchItem              `toml:"catch"`

	// Nested routes. A rule may gate a set of child rules; the parent's
	// matchers and middlewares apply, and each child adds its own.
	Routes []route `toml:"routes"`
}

// plugin is an arbitrary (Yaegi/Traefik) plugin reference.
type plugin struct {
	Source string                 `toml:"source"`
	Config map[string]interface{} `toml:"config"`
}

// setItem is one `set` entry: either a header (with an optional encrypt key) or
// a path rewrite.
type setItem struct {
	Header  string `toml:"header"`
	Value   string `toml:"value"`
	Encrypt string `toml:"encrypt"`
	Path    string `toml:"path"`
}

// catchItem is one `catch` entry: a header to decrypt.
type catchItem struct {
	Header  string `toml:"header"`
	Decrypt string `toml:"decrypt"`
}

// buildRoutes converts a route into one or more Routes. A rule without
// nested children yields exactly one route. A rule with nested children yields
// one route per child, each combining the parent's matchers/middlewares with the
// child's (children fall back to the parent's target when they define none).
func buildRoutes(r route, getenv func(string) string) []Route {
	if len(r.Routes) == 0 {
		return []Route{{
			Match:       buildMatcher(r),
			Targets:     []Upstream{resolveTarget(r, getenv)},
			Percentage:  r.Percentage,
			Mirror:      r.Mirror,
			Middlewares: buildMiddlewares(r),
		}}
	}

	parentMatch := buildMatcher(r)
	parentMW := buildMiddlewares(r)

	var routes []Route
	for _, child := range r.Routes {
		// Each child resolves its own target (it never inherits the
		// parent's `to`; a child without one falls back to the default app
		// service).
		mw := append(append([]Middleware{}, parentMW...), buildMiddlewares(child)...)
		routes = append(routes, Route{
			Match:       mergeMatchers(parentMatch, buildMatcher(child)),
			Targets:     []Upstream{resolveTarget(child, getenv)},
			Percentage:  child.Percentage,
			Mirror:      child.Mirror,
			Middlewares: mw,
		})
	}
	return routes
}

// buildMatcher assembles a Matcher from a rule's matching predicates.
func buildMatcher(r route) Matcher {
	m := Matcher{Path: r.Path}
	if len(r.Header) > 0 {
		m.Headers = r.Header
	}
	if len(r.Query) > 0 {
		m.Query = r.Query
	}
	if len(r.Body) > 0 {
		m.Body = r.Body
	}
	if len(r.Methods) > 0 {
		methods := make([]string, len(r.Methods))
		for i, mth := range r.Methods {
			methods[i] = strings.ToUpper(mth)
		}
		m.Methods = methods
	}
	return m
}

// mergeMatchers overlays child predicates onto the parent's. Child scalar values
// (Path) win when set; map dimensions are unioned with the child taking
// precedence on key collisions; child methods replace the parent's when present.
func mergeMatchers(parent, child Matcher) Matcher {
	out := Matcher{Path: parent.Path}
	if child.Path != "" {
		out.Path = child.Path
	}
	out.Headers = mergeStringMaps(parent.Headers, child.Headers)
	out.Query = mergeStringMaps(parent.Query, child.Query)
	out.Body = mergeStringMaps(parent.Body, child.Body)
	out.Methods = parent.Methods
	if len(child.Methods) > 0 {
		out.Methods = child.Methods
	}
	return out
}

func mergeStringMaps(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// buildMiddlewares produces the ordered middleware chain for a rule, preserving
// the historical order: user middleware, then plugins, then the catch and body
// internal-plugin attributes, then set (path/header) directives.
func buildMiddlewares(r route) []Middleware {
	var out []Middleware

	// 1. User-defined raw middlewares (rateLimit, retry, ...).
	for _, mw := range r.Middleware {
		if len(mw) == 0 {
			continue
		}
		out = append(out, Middleware{
			Kind:   CapRawTraefikPlugin,
			Config: map[string]any{"raw": mw},
		})
	}

	// 2. Arbitrary Yaegi/Traefik plugins declared in the plugins array.
	for _, p := range r.Plugins {
		cfg := map[string]any{"source": p.Source}
		if p.Config != nil {
			cfg["config"] = p.Config
		}
		out = append(out, Middleware{Kind: CapRawTraefikPlugin, Config: cfg})
	}

	// 3. catch attribute -> Decrypt capability.
	if len(r.Catch) > 0 {
		headers := make(map[string]string, len(r.Catch))
		var decrypt string
		for _, item := range r.Catch {
			headers[item.Header] = item.Decrypt
			if decrypt == "" && item.Decrypt != "" {
				decrypt = item.Decrypt
			}
		}
		cfg := map[string]any{"headers": headers}
		if decrypt != "" {
			cfg["decrypt"] = decrypt
		}
		out = append(out, Middleware{Kind: CapDecrypt, Config: cfg})
	}

	// 4. body attribute -> BodyToHeader capability (fields are the JSON keys).
	if len(r.Body) > 0 {
		fields := make([]string, 0, len(r.Body))
		for k := range r.Body {
			fields = append(fields, k)
		}
		sort.Strings(fields)
		out = append(out, Middleware{
			Kind:   CapBodyToHeader,
			Config: map[string]any{"fields": fields},
		})
	}

	// 5. set directives -> SetPath / SetHeader capabilities, in declared order.
	for _, item := range r.Set {
		if item.Path != "" {
			out = append(out, Middleware{
				Kind:   CapSetPath,
				Config: map[string]any{"path": item.Path},
			})
			continue
		}
		if item.Header != "" {
			cfg := map[string]any{"header": item.Header, "value": item.Value}
			if item.Encrypt != "" {
				cfg["encrypt"] = item.Encrypt
			}
			out = append(out, Middleware{Kind: CapSetHeader, Config: cfg})
		}
	}

	return out
}

// resolveTarget derives the concrete upstream for a rule: explicit `to` (with
// env expansion), otherwise the default app service on APP_PORT.
func resolveTarget(r route, getenv func(string) string) Upstream {
	if r.To != "" {
		return Upstream{URL: os.Expand(r.To, getenv)}
	}
	return Upstream{URL: appServiceURL(getenv)}
}

// appServiceURL returns the default app service URL: APP_PORT if set, otherwise PORT+1,
// defaulting to 8889 (8888+1) when PORT is unset or invalid.
func appServiceURL(getenv func(string) string) string {
	appPort := getenv("APP_PORT")
	if appPort == "" {
		port := getenv("PORT")
		if port == "" {
			port = "8888"
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			portNum = 8888
		}
		appPort = strconv.Itoa(portNum + 1)
	}
	return fmt.Sprintf("http://localhost:%s", appPort)
}
