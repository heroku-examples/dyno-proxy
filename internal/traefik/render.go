package traefik

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/heroku-examples/dyno-proxy/internal/config"
)

// renderState accumulates the Traefik config as it is assembled from the model.
type renderState struct {
	routers      map[string]*router
	services     map[string]*service
	middlewares  map[string]*middleware
	localPlugins map[string]string // plugin name -> module name (for static experimental config)
	getenv       func(string) string
}

// Render turns the RouteModel into Traefik's static + dynamic TOML config
// files.
func Render(model config.RouteModel, opts Options) (Rendered, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	st := &renderState{
		routers:      map[string]*router{},
		services:     map[string]*service{},
		middlewares:  map[string]*middleware{},
		localPlugins: map[string]string{},
		getenv:       getenv,
	}

	st.addDefaultApp(model)

	// Split routes into percentage/mirror rules (weighted / shadow traffic) and
	// plain routes, preserving order. Mirror rules with no percentage default to
	// 100% mirroring, matching the legacy behavior.
	var percentageRoutes []config.Route
	for i, rt := range model.Routes {
		if rt.Percentage > 0 || rt.Mirror {
			percentageRoutes = append(percentageRoutes, rt)
			continue
		}
		if err := st.addPlainRoute(i, rt); err != nil {
			return Rendered{}, err
		}
	}
	st.addPercentageRoutes(percentageRoutes)

	st.addDashboardAndMetrics(model)

	// Assemble dynamic config (routers/services/middlewares). Empty sections are
	// omitted: Traefik's file provider rejects a standalone empty middlewares (or
	// routers/services) table.
	httpCfg := map[string]interface{}{}
	if len(st.routers) > 0 {
		httpCfg["routers"] = st.routers
	}
	if len(st.services) > 0 {
		httpCfg["services"] = st.services
	}
	if len(st.middlewares) > 0 {
		httpCfg["middlewares"] = expandMiddlewaresForTOML(st.middlewares)
	}
	dynamic := map[string]interface{}{"http": httpCfg}

	base := opts.BaseName
	if base == "" {
		base = "app"
	}
	dir := opts.OutputDir
	if dir == "" {
		dir = "."
	}
	staticPath := filepath.Join(dir, base+".dyno-proxy-static.toml")
	dynamicPath := filepath.Join(dir, base+".dyno-proxy-dynamic.toml")

	static := st.staticConfig(opts, dynamicPath)

	if err := writeTOML(staticPath, static); err != nil {
		return Rendered{}, err
	}
	if err := writeTOML(dynamicPath, dynamic); err != nil {
		return Rendered{}, err
	}

	return Rendered{
		StaticConfigPath:  staticPath,
		DynamicConfigPath: dynamicPath,
		Files:             []string{staticPath, dynamicPath},
	}, nil
}

// addDefaultApp adds the default "app" service + catch-all router that receives
// any traffic not matched by a more specific route.
func (st *renderState) addDefaultApp(model config.RouteModel) {
	lb := &loadBalancer{
		PassHostHeader: true,
		Strategy:       "p2c",
		Servers:        []server{{URL: model.DefaultServiceURL}},
	}
	st.services["app"] = &service{LoadBalancer: lb}
	st.routers["app"] = &router{
		Rule:        "PathPrefix(`/`)",
		Service:     "app",
		EntryPoints: []string{"web"},
		Priority:    intPtr(1),
	}
}

// addPlainRoute renders a non-percentage, non-mirror route as a router+service.
func (st *renderState) addPlainRoute(i int, rt config.Route) error {
	parts := buildRuleParts(rt.Match)
	if len(parts) == 0 {
		// No matchers: nothing to route (would shadow the default app router).
		return nil
	}
	key := fmt.Sprintf("route-%d", i)
	mwNames, err := st.addMiddlewares(key, rt.Middlewares)
	if err != nil {
		return err
	}

	rtr := &router{
		Rule:        strings.Join(parts, " && "),
		Service:     key,
		EntryPoints: []string{"web"},
		Priority:    intPtr(10),
	}
	if len(mwNames) > 0 {
		rtr.Middlewares = mwNames
	}
	st.routers[key] = rtr
	st.services[key] = &service{
		LoadBalancer: &loadBalancer{
			PassHostHeader: false,
			Strategy:       "p2c",
			Servers:        []server{{URL: targetURL(rt)}},
		},
	}
	return nil
}

// addPercentageRoutes renders weighted / mirror routes. Routes are grouped by
// their matcher string; within a group, percentage ranges are
// tracked cumulatively so multiple experiments at the same path do not overlap.
// Ported from transformToTraefikConfig's percentageRules handling.
func (st *renderState) addPercentageRoutes(routes []config.Route) {
	type group struct {
		routes     []config.Route
		ruleString string
	}
	groups := map[string]*group{}
	var order []string
	for _, rt := range routes {
		parts := buildRuleParts(rt.Match)
		if len(parts) == 0 {
			parts = []string{"PathPrefix(`/`)"}
		}
		ruleString := strings.Join(parts, " && ")
		g, ok := groups[ruleString]
		if !ok {
			g = &group{ruleString: ruleString}
			groups[ruleString] = g
			order = append(order, ruleString)
		}
		g.routes = append(g.routes, rt)
	}
	sort.Strings(order)

	groupIndex := 0
	for _, key := range order {
		g := groups[key]
		cumulative := 0
		for idx, rt := range g.routes {
			pct := rt.Percentage
			if rt.Mirror && pct == 0 {
				pct = 100
			}

			base := fmt.Sprintf("percentage-%d-%d", groupIndex, idx)
			experimentService := base + "-experiment"
			experimentRouter := base + "-experiment"

			startPct := cumulative
			endPct := cumulative + pct
			if endPct > 100 {
				endPct = 100
			}

			var rule string
			if pct >= 100 {
				rule = g.ruleString
			} else {
				var pattern string
				if len(g.routes) == 1 {
					pattern = getUUIDPercentagePattern(pct)
				} else {
					pattern = getUUIDPercentageRangePattern(startPct, endPct)
				}
				if pattern == "" {
					continue
				}
				rule = g.ruleString + " && " + fmt.Sprintf("HeaderRegexp(`x-request-id`, `%s`)", pattern)
			}
			cumulative = endPct

			serviceName := experimentService
			if rt.Mirror {
				st.services[experimentService] = &service{
					LoadBalancer: &loadBalancer{
						PassHostHeader: false,
						Strategy:       "p2c",
						Servers:        []server{{URL: targetURL(rt)}},
					},
				}
				mirrorName := base + "-mirror"
				st.services[mirrorName] = &service{
					Mirroring: &mirroring{
						Service: "app",
						Mirrors: []mirrorService{{Name: experimentService, Percent: 100}},
					},
				}
				serviceName = mirrorName
			} else {
				st.services[experimentService] = &service{
					LoadBalancer: &loadBalancer{
						PassHostHeader: false,
						Strategy:       "p2c",
						Servers:        []server{{URL: targetURL(rt)}},
					},
				}
			}

			mwNames, err := st.addMiddlewares(experimentRouter, rt.Middlewares)
			if err != nil {
				// Middleware errors on percentage routes are non-fatal to keep
				// the rest of the config renderable.
				mwNames = nil
			}
			rtr := &router{
				Rule:        rule,
				Service:     serviceName,
				EntryPoints: []string{"web"},
				Priority:    intPtr(50),
			}
			if len(mwNames) > 0 {
				rtr.Middlewares = mwNames
			}
			st.routers[experimentRouter] = rtr
		}
		groupIndex++
	}
}

// addDashboardAndMetrics adds the reserved dashboard + metrics routers and their
// guarding basic-auth middleware, ported from generateConfig.
func (st *renderState) addDashboardAndMetrics(model config.RouteModel) {
	dash := model.Dashboard
	if dash.PathPrefix != "" {
		st.routers["dashboard"] = &router{
			Rule:        fmt.Sprintf("PathPrefix(`%s`)", dash.PathPrefix),
			Service:     "api@internal",
			EntryPoints: []string{"web"},
			Middlewares: []string{"dyno_proxy_admin"},
			Priority:    intPtr(100),
		}
		st.middlewares["dyno_proxy_admin"] = &middleware{
			BasicAuth: &basicAuthMiddleware{
				Users: []string{fmt.Sprintf("%s:%s", dash.User, dash.HashedPassword)},
			},
		}
	}

	m := model.Metrics
	if m.PathPrefix != "" {
		st.routers["metrics"] = &router{
			Rule:        fmt.Sprintf("PathPrefix(`%s`)", m.PathPrefix),
			Service:     "prometheus@internal",
			EntryPoints: []string{"web"},
			Middlewares: []string{"dyno_proxy_admin"},
			Priority:    intPtr(100),
		}
		st.middlewares["metrics_strip_prefix"] = &middleware{
			Raw: map[string]interface{}{
				"stripPrefix": map[string]interface{}{
					"prefixes": []string{m.PathPrefix},
				},
			},
		}
	}
}

// staticConfig builds the Traefik static configuration map. Ported from
// generateConfig's staticConfig assembly.
func (st *renderState) staticConfig(opts Options, dynamicPath string) map[string]interface{} {
	port := opts.Port
	if port == "" {
		port = "8888"
	}
	static := map[string]interface{}{
		// Disable Traefik's version check and anonymous usage reporting; the proxy
		// should never phone home.
		"global": map[string]interface{}{
			"checkNewVersion":    false,
			"sendAnonymousUsage": false,
		},
		"entryPoints": map[string]interface{}{
			"web": map[string]interface{}{
				"address": ":" + port,
				"forwardedHeaders": map[string]interface{}{
					"trustedIPs": []string{"0.0.0.0/0"},
				},
			},
		},
		"providers": map[string]interface{}{
			"file": map[string]interface{}{
				"filename": dynamicPath,
				"watch":    true,
			},
		},
		"log": map[string]interface{}{
			"level":  traefikLogLevel(opts.LogLevel),
			"format": traefikLogFormat(opts.LogFormat),
		},
	}
	if st.routers["dashboard"] != nil {
		static["api"] = map[string]interface{}{
			"basepath":  "/.dyno-proxy/traefik",
			"dashboard": true,
		}
	}
	// Only enable the Prometheus metrics registry when a metrics router was
	// emitted (metrics are disabled by default). This keeps the static metrics
	// config in lock-step with the dynamic metrics route.
	if st.routers["metrics"] != nil {
		static["metrics"] = map[string]interface{}{
			"prometheus": map[string]interface{}{
				"manualRouting":        true,
				"addServicesLabels":    true,
				"addRoutersLabels":     true,
				"addEntryPointsLabels": true,
			},
		}
	}
	if len(st.localPlugins) > 0 {
		lp := map[string]interface{}{}
		for name, module := range st.localPlugins {
			lp[name] = map[string]interface{}{"moduleName": module}
		}
		static["experimental"] = map[string]interface{}{"localPlugins": lp}
	}
	if accessLog := accessLogConfig(opts.LogLevel, opts.LogFormat); accessLog != nil {
		static["accessLog"] = accessLog
	}
	return static
}

// targetURL returns the upstream URL for a route (its first target).
func targetURL(rt config.Route) string {
	if len(rt.Targets) > 0 {
		return rt.Targets[0].URL
	}
	return ""
}

// writeTOML marshals v to TOML and writes it to path.
func writeTOML(path string, v interface{}) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(v); err != nil {
		return fmt.Errorf("traefik: encoding TOML for %s: %w", path, err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("traefik: writing %s: %w", path, err)
	}
	return nil
}
