package config

import (
	"os"
	"path/filepath"
	"testing"
)

func envFn(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func writeProject(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write project.toml: %v", err)
	}
	return path
}

func load(t *testing.T, content string) []Route {
	t.Helper()
	routes, err := Load(writeProject(t, content), envFn(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return routes
}

func TestLoad_InvalidProject(t *testing.T) {
	if _, err := Load(writeProject(t, `[[routes]`), envFn(nil)); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestLoad_Missing(t *testing.T) {
	routes, err := Load(filepath.Join(t.TempDir(), FileName), envFn(nil))
	if err != nil || routes != nil {
		t.Fatalf("routes = %v, err = %v", routes, err)
	}
}

func TestLoad_OnlyBuildpack(t *testing.T) {
	routes := load(t, `
[_]
schema-version = "0.2"

[[io.buildpacks.group]]
routeID = "heroku/ruby"
`)
	if len(routes) != 0 {
		t.Fatalf("expected no routes, got %d", len(routes))
	}
}

func TestLoad_DefaultTarget(t *testing.T) {
	path := writeProject(t, "[[routes]]\npath = \"/a\"\n")
	routes, err := Load(path, envFn(map[string]string{"APP_PORT": "9000"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := routes[0].Targets[0].URL; got != "http://localhost:9000" {
		t.Errorf("default target = %q", got)
	}
}

func TestLoad_EnvExpansionInTo(t *testing.T) {
	path := writeProject(t, "[[routes]]\npath = \"/a\"\nto = \"https://$HOST/x\"\n")
	routes, err := Load(path, envFn(map[string]string{"HOST": "example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := routes[0].Targets[0].URL; got != "https://example.com/x" {
		t.Errorf("target = %q", got)
	}
}

func TestLoad_BasicRoutes(t *testing.T) {
	routes := load(t, `
[_]
schema-version = "0.2"

[[io.buildpacks.group]]
routeID = "heroku/ruby"

[[routes]]
path = "/api"
to = "https://api.example.com/"

[[routes]]
path = "/admin"
to = "https://admin.example.com/"
`)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].Match.Path != "/api" {
		t.Errorf("path = %q", routes[0].Match.Path)
	}
	if routes[0].Targets[0].URL != "https://api.example.com/" {
		t.Errorf("target = %q", routes[0].Targets[0].URL)
	}
	if routes[1].Match.Path != "/admin" {
		t.Errorf("path = %q", routes[1].Match.Path)
	}
}

func TestLoad_HeaderMatcher(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/secure"
to = "https://secure.example.com/"

[routes.header]
"x-api-key" = "secret123"
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Match.Headers["x-api-key"] != "secret123" {
		t.Errorf("header matcher = %v", routes[0].Match.Headers)
	}
}

func TestLoad_Percentage(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/canary"
to = "https://canary.example.com/"
percentage = 15
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Percentage != 15 {
		t.Errorf("percentage = %d", routes[0].Percentage)
	}
}

func TestLoad_NestedRoutes(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/parse-body"

[[routes.routes]]
to = "https://canary.example.com/"

[routes.routes.header]
"x-body-name" = "canary"
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route from nested rule, got %d", len(routes))
	}
	if routes[0].Match.Path != "/parse-body" {
		t.Errorf("path = %q", routes[0].Match.Path)
	}
	if routes[0].Match.Headers["x-body-name"] != "canary" {
		t.Errorf("merged header = %v", routes[0].Match.Headers)
	}
	if routes[0].Targets[0].URL != "https://canary.example.com/" {
		t.Errorf("target = %q", routes[0].Targets[0].URL)
	}
}

func TestLoad_Middleware(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/rate-limited"
to = "https://api.example.com/"

[[routes.middleware]]
[routes.middleware.rateLimit]
average = 10
period = "1m"
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	mw := routes[0].Middlewares
	if len(mw) != 1 || mw[0].Kind != CapRawTraefikPlugin {
		t.Fatalf("expected RawTraefikPlugin middleware, got %+v", mw)
	}
	raw, ok := mw[0].Config["raw"].(map[string]interface{})
	if !ok {
		t.Fatalf("raw type = %T", mw[0].Config["raw"])
	}
	if _, ok := raw["rateLimit"]; !ok {
		t.Errorf("expected rateLimit in raw, got %v", raw)
	}
}

func TestLoad_SetPath(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/blog"
to = "https://heroku.com"

[[routes.set]]
path = "/blog/category/engineering"
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	mw := routes[0].Middlewares
	if len(mw) != 1 || mw[0].Kind != CapSetPath {
		t.Fatalf("expected SetPath middleware, got %+v", mw)
	}
	if mw[0].Config["path"] != "/blog/category/engineering" {
		t.Errorf("set path config = %v", mw[0].Config)
	}
}

func TestLoad_SetHeader(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/x"
to = "https://heroku.com"

[[routes.set]]
header = "X-Token"
value = "$TOKEN"
encrypt = "$KEY"
`)
	mw := routes[0].Middlewares
	if len(mw) != 1 || mw[0].Kind != CapSetHeader {
		t.Fatalf("expected SetHeader middleware, got %+v", mw)
	}
	if mw[0].Config["header"] != "X-Token" || mw[0].Config["value"] != "$TOKEN" || mw[0].Config["encrypt"] != "$KEY" {
		t.Errorf("set header config = %v", mw[0].Config)
	}
}

func TestLoad_Catch(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/decrypted"

[[routes.catch]]
header = "x-forwarded-database-url"
decrypt = "$DYNO_PROXY_ENCRYPTION_KEY"
`)
	mw := routes[0].Middlewares
	if len(mw) != 1 || mw[0].Kind != CapDecrypt {
		t.Fatalf("expected Decrypt middleware, got %+v", mw)
	}
	headers, ok := mw[0].Config["headers"].(map[string]string)
	if !ok || headers["x-forwarded-database-url"] != "$DYNO_PROXY_ENCRYPTION_KEY" {
		t.Errorf("catch headers = %v", mw[0].Config["headers"])
	}
}

func TestLoad_BuildpackCoexistence(t *testing.T) {
	// A project.toml with both buildpack tables and routes should parse both.
	routes := load(t, `
[_]
schema-version = "0.2"

[[io.buildpacks.group]]
routeID = "heroku/ruby"

[[io.buildpacks.group]]
routeID = "heroku/procfile"

[[routes]]
path = "/api"
to = "https://api.example.com/"
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Match.Path != "/api" {
		t.Errorf("path = %q", routes[0].Match.Path)
	}
}

// TOML permits two equivalent spellings for arrays of tables: the [[routes.x]]
// array-of-tables syntax and the inline-table array x = [{...}]. BurntSushi/toml
// decodes them to different Go types, so exercise the inline form to guard
// against the child fields being silently dropped.
func TestLoad_InlineTableArrays(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/inline"
to = "https://api.example.com/"
plugins = [{ source = "./plugins/tacos" }]
set = [{ path = "/rewritten" }]
catch = [{ header = "x-secret", decrypt = "$KEY" }]
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	// plugins, set, and catch all lower to middlewares; if the inline arrays
	// were dropped these capabilities would be missing.
	var havePlugin, haveSetPath, haveDecrypt bool
	for _, mw := range routes[0].Middlewares {
		switch {
		case mw.Kind == CapRawTraefikPlugin && mw.Config["source"] == "./plugins/tacos":
			havePlugin = true
		case mw.Kind == CapSetPath && mw.Config["path"] == "/rewritten":
			haveSetPath = true
		case mw.Kind == CapDecrypt:
			haveDecrypt = true
		}
	}
	if !havePlugin {
		t.Errorf("inline plugins dropped: %+v", routes[0].Middlewares)
	}
	if !haveSetPath {
		t.Errorf("inline set dropped: %+v", routes[0].Middlewares)
	}
	if !haveDecrypt {
		t.Errorf("inline catch dropped: %+v", routes[0].Middlewares)
	}
}

func TestLoad_InlineNestedRoutes(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/parse-body"
routes = [{ to = "https://canary.example.com/", header = { x-body-name = "canary" } }]
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route from inline nested rule, got %d", len(routes))
	}
	if routes[0].Match.Path != "/parse-body" {
		t.Errorf("path = %q", routes[0].Match.Path)
	}
	if routes[0].Match.Headers["x-body-name"] != "canary" {
		t.Errorf("merged header = %v", routes[0].Match.Headers)
	}
	if routes[0].Targets[0].URL != "https://canary.example.com/" {
		t.Errorf("target = %q", routes[0].Targets[0].URL)
	}
}

func TestLoad_InlineMiddleware(t *testing.T) {
	routes := load(t, `
[[routes]]
path = "/inline-mw"
to = "https://api.example.com/"
middleware = [{ rateLimit = { average = 10, period = "1m" } }]
`)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	mw := routes[0].Middlewares
	if len(mw) != 1 || mw[0].Kind != CapRawTraefikPlugin {
		t.Fatalf("inline middleware dropped, got %+v", mw)
	}
	raw, ok := mw[0].Config["raw"].(map[string]interface{})
	if !ok {
		t.Fatalf("raw type = %T", mw[0].Config["raw"])
	}
	if _, ok := raw["rateLimit"]; !ok {
		t.Errorf("expected rateLimit in raw, got %v", raw)
	}
}

func TestRouteID(t *testing.T) {
	routes := load(t, `
[[routes]]
routeID = "plain"
path = "/a"
to = "https://a.example.com"

[[routes]]
routeID = "parent"
path = "/b"
  [[routes.routes]]
  routeID = "child"
  to = "https://c.example.com"
  [[routes.routes]]
  to = "https://d.example.com"
`)
	var got []string
	for _, r := range routes {
		got = append(got, r.ID)
	}
	want := []string{"plain", "child", "parent"}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}
