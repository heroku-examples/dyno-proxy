package traefik

import "testing"

func TestTraefikLogFormat(t *testing.T) {
	cases := map[string]string{
		"":          "common",
		"json":      "json",
		"JSON":      "json",
		" json ":    "json",
		"key=value": "common",
		"logfmt":    "common",
	}
	for in, want := range cases {
		if got := traefikLogFormat(in); got != want {
			t.Errorf("traefikLogFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccessLogConfigFormatFollowsRequest(t *testing.T) {
	// json request -> json access log format.
	if got := accessLogConfig("INFO", "json")["format"]; got != "json" {
		t.Errorf("accessLogConfig INFO/json format = %v, want json", got)
	}
	// default (empty) request -> common text format.
	if got := accessLogConfig("DEBUG", "")["format"]; got != "common" {
		t.Errorf("accessLogConfig DEBUG/\"\" format = %v, want common", got)
	}
	// WARN and above disables access logging regardless of format.
	if cfg := accessLogConfig("ERROR", "json"); cfg != nil {
		t.Errorf("accessLogConfig ERROR should be nil, got %v", cfg)
	}
}
