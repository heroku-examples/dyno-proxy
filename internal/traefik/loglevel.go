package traefik

import "strings"

// traefikLogLevel maps a dyno-proxy log level to Traefik's log level. Ported
// from shared.go:getTraefikLogLevel.
func traefikLogLevel(level string) string {
	switch strings.ToUpper(level) {
	case "TRACE", "DEBUG":
		return "DEBUG"
	case "INFO", "":
		return "INFO"
	case "WARN", "WARNING":
		return "WARN"
	case "ERROR", "FATAL", "PANIC":
		return "ERROR"
	default:
		return "INFO"
	}
}

// traefikLogFormat maps the dyno-proxy log format to a Traefik log format.
// Traefik only supports "common" (text) and "json"; anything other than an
// explicit "json" falls back to the "common" text default.
func traefikLogFormat(format string) string {
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return "json"
	}
	return "common"
}

// accessLogConfig returns the Traefik access-log config for the given level, or
// nil to disable access logging (WARN and above). Ported from
// shared.go:getAccessLogConfig. The format follows traefikLogFormat.
func accessLogConfig(level, format string) map[string]interface{} {
	logFormat := traefikLogFormat(format)
	switch strings.ToUpper(level) {
	case "TRACE", "DEBUG":
		return map[string]interface{}{
			"format": logFormat,
			"fields": map[string]interface{}{
				"defaultMode": "keep",
				"names": map[string]interface{}{
					"ClientUsername": "drop",
					"RequestHeader":  "keep",
				},
				"headers": map[string]interface{}{
					"defaultMode": "keep",
					"names": map[string]interface{}{
						"Authorization": "drop",
						"X-API-Key":     "drop",
						"Cookie":        "drop",
					},
				},
			},
		}
	case "INFO", "":
		return map[string]interface{}{
			"format": logFormat,
			"fields": map[string]interface{}{
				"defaultMode": "drop",
				"names": map[string]interface{}{
					"ClientUsername":  "drop",
					"RequestHeader":   "drop",
					"RequestPath":     "keep",
					"RequestHost":     "keep",
					"RequestMethod":   "keep",
					"RequestProtocol": "drop",
					"RequestScheme":   "drop",
					"RequestURI":      "keep",
					"ResponseCode":    "keep",
					"ResponseSize":    "keep",
					"ServiceURL":      "keep",
					"Duration":        "keep",
				},
				"headers": map[string]interface{}{
					"defaultMode": "drop",
					"names": map[string]interface{}{
						"X-Request-ID": "keep",
						"Host":         "keep",
					},
				},
			},
		}
	default:
		return nil
	}
}
