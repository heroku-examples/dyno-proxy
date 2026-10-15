package engine

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn and returns everything it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestLoggerDefaultKeyValueFormat(t *testing.T) {
	out := captureStdout(t, func() {
		newLogger("info", "")("info", "hello world")
	})
	out = strings.TrimSpace(out)

	if strings.HasPrefix(out, "{") {
		t.Fatalf("default format should be key=value, got JSON: %q", out)
	}
	for _, want := range []string{
		"level=info",
		"caller=dyno-proxy",
		`message="hello world"`, // space forces quoting
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestLoggerJSONFormat(t *testing.T) {
	for _, format := range []string{"json", "JSON", "Json"} {
		out := captureStdout(t, func() {
			newLogger("info", format)("warn", "boom")
		})
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &entry); err != nil {
			t.Fatalf("format %q: expected JSON, got %q (%v)", format, out, err)
		}
		if entry["level"] != "warn" || entry["message"] != "boom" || entry["caller"] != "dyno-proxy" {
			t.Errorf("format %q: unexpected entry %v", format, entry)
		}
	}
}

func TestLoggerHonorsMinLevel(t *testing.T) {
	out := captureStdout(t, func() {
		log := newLogger("error", "")
		log("info", "suppressed")
		log("error", "shown")
	})
	if strings.Contains(out, "suppressed") {
		t.Errorf("info message should be filtered below error level: %q", out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("error message should pass: %q", out)
	}
}

func TestLogfmtValueQuoting(t *testing.T) {
	cases := map[string]string{
		"plain":     "plain",
		"":          `""`,
		"has space": `"has space"`,
		`a=b`:       `"a=b"`,
	}
	for in, want := range cases {
		if got := logfmtValue(in); got != want {
			t.Errorf("logfmtValue(%q) = %q, want %q", in, got, want)
		}
	}
}
