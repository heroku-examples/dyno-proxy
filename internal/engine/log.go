package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// logLevel is an ordered severity used to gate messages, ported from the legacy
// shared.go log-level hierarchy.
type logLevel int

const (
	levelTrace logLevel = iota
	levelDebug
	levelInfo
	levelWarn
	levelError
	levelFatal
	levelPanic
)

func parseLevel(level string) logLevel {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "TRACE":
		return levelTrace
	case "DEBUG":
		return levelDebug
	case "INFO":
		return levelInfo
	case "WARN", "WARNING":
		return levelWarn
	case "ERROR":
		return levelError
	case "FATAL":
		return levelFatal
	case "PANIC":
		return levelPanic
	default:
		return levelInfo
	}
}

// newLogger returns a Logf compatible with supervisor.Config.Logf. It honors the
// configured minimum level and emits each entry as a key=value line by default,
// or as a JSON object when format is "json".
func newLogger(configuredLevel, format string) func(level, msg string) {
	min := parseLevel(configuredLevel)
	jsonFormat := strings.EqualFold(strings.TrimSpace(format), "json")
	return func(level, msg string) {
		if parseLevel(level) < min {
			return
		}
		ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		if jsonFormat {
			data, err := json.Marshal(map[string]interface{}{
				"level":   level,
				"time":    ts,
				"caller":  "dyno-proxy",
				"message": msg,
			})
			if err != nil {
				fmt.Printf("[%s] %s\n", level, msg)
				return
			}
			fmt.Println(string(data))
			return
		}
		// Default key=value line; quote fields that need it (spaces, quotes).
		fmt.Printf("level=%s time=%s caller=dyno-proxy message=%s\n",
			level, ts, logfmtValue(msg))
	}
}

// logfmtValue quotes a logfmt value when it contains characters that would break
// key=value parsing (whitespace, quotes, or =); otherwise it returns it as-is.
func logfmtValue(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\"=") {
		return strconv.Quote(s)
	}
	return s
}

// EnvLogger returns a logger configured from DYNO_PROXY_LOG_LEVEL (default
// ERROR) and DYNO_PROXY_LOG_FORMAT, for subcommands that don't run the engine.
func EnvLogger() func(level, msg string) {
	level := os.Getenv("DYNO_PROXY_LOG_LEVEL")
	if level == "" {
		level = "ERROR"
	}
	return newLogger(level, os.Getenv("DYNO_PROXY_LOG_FORMAT"))
}
