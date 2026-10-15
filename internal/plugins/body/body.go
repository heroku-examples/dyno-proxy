package body

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

const (
	// maxBodySize is the maximum size in bytes for the entire request body.
	// This prevents DoS attacks from extremely large request bodies.
	maxBodySize = 1 * 1024 * 1024 // 1MB

	// maxFieldSize is the maximum size in bytes for a field to be extracted as a header.
	// Fields larger than this are considered file uploads or large binary data and are skipped.
	maxFieldSize = 1024
)

// Config the plugin configuration.
type Config struct {
	// Fields is the list of body field names to extract. Each field is looked up
	// in the JSON (or form-encoded) body and added as an "X-Body-<field>" header.
	// Field names may be top-level ("name") or nested with dot notation
	// ("organization.id"). A leading "$." or "." is stripped, so JSONPath-style
	// names like "$.user.id" also work.
	//
	// Matching (exact value or regex) is performed downstream by Traefik's
	// Header / HeaderRegexp router rules against these headers; this plugin only
	// extracts the values.
	//
	// If empty, all top-level fields are extracted.
	Fields []string `json:"fields,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{}
}

// BodyParser a body parser plugin that extracts fields from request bodies and adds them as headers.
type BodyParser struct {
	next   http.Handler
	fields map[string]bool // Set of field names to extract (empty means all top-level fields)
	name   string
}

// New created a new BodyParser plugin.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	bp := &BodyParser{
		next:   next,
		name:   name,
		fields: make(map[string]bool),
	}
	for _, field := range config.Fields {
		bp.fields[field] = true
	}
	return bp, nil
}

func (b *BodyParser) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Only process requests that have a body
	if req.Body == nil {
		b.next.ServeHTTP(rw, req)
		return
	}

	// Read the request body with size limit to prevent DoS
	bodyBytes, err := io.ReadAll(io.LimitReader(req.Body, maxBodySize))
	if err != nil {
		// If we can't read the body, just pass through
		b.next.ServeHTTP(rw, req)
		return
	}

	// Restore the body for downstream handlers
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Check content type
	contentType := req.Header.Get("Content-Type")
	isJSON := strings.HasPrefix(contentType, "application/json")
	isFormEncoded := strings.HasPrefix(contentType, "application/x-www-form-urlencoded")

	switch {
	case isJSON:
		b.parseJSON(bodyBytes, req)
	case isFormEncoded:
		b.parseFormEncoded(bodyBytes, req)
	}
	// Any other content type is passed through unchanged.

	b.next.ServeHTTP(rw, req)
}

// parseJSON extracts fields from a JSON body and adds them as headers.
func (b *BodyParser) parseJSON(bodyBytes []byte, req *http.Request) {
	var jsonData map[string]any
	if err := json.Unmarshal(bodyBytes, &jsonData); err != nil {
		// If JSON parsing fails, skip
		return
	}

	b.extractFields(jsonData, req)
}

// parseFormEncoded extracts fields from form-encoded body and adds them as headers.
func (b *BodyParser) parseFormEncoded(bodyBytes []byte, req *http.Request) {
	values, err := url.ParseQuery(string(bodyBytes))
	if err != nil {
		// If parsing fails, skip
		return
	}

	// Convert url.Values to a map[string]any for consistent handling
	formData := make(map[string]any)
	for key, val := range values {
		if len(val) > 0 {
			// Take the first value if multiple values exist
			formData[key] = val[0]
		}
	}

	b.extractFields(formData, req)
}

// extractFields extracts fields from a data map and adds them as request headers.
// It intelligently skips large fields (file uploads) and respects the fields filter.
// Supports both top-level fields ("name") and nested fields ("organization.id").
func (b *BodyParser) extractFields(data map[string]any, req *http.Request) {
	// If no fields filter is specified, extract all top-level fields
	if len(b.fields) == 0 {
		for key := range data {
			b.extractAndSetField(data, key, req)
		}
		return
	}

	// Otherwise, extract only the specified fields (may include nested paths)
	for field := range b.fields {
		b.extractAndSetField(data, field, req)
	}
}

// extractAndSetField extracts a single field (top-level or nested) and sets it as a header.
func (b *BodyParser) extractAndSetField(data map[string]any, field string, req *http.Request) {
	// Validate field name to prevent header injection attacks
	if !isValidFieldName(field) {
		// Skip fields with invalid names
		return
	}

	// Strip '$.' or '.' prefix from field name for both lookup and header creation
	lookupField := field
	if strings.HasPrefix(lookupField, "$.") {
		lookupField = strings.TrimPrefix(lookupField, "$.")
	} else if strings.HasPrefix(lookupField, ".") {
		lookupField = strings.TrimPrefix(lookupField, ".")
	}

	// Use GetNestedValueAsString to support both top-level and nested fields
	// Use the lookupField (without prefix) to traverse the JSON data
	valueStr, exists, err := GetNestedValueAsString(data, lookupField)
	if err != nil {
		// Error during traversal (e.g., trying to traverse through non-map)
		// Skip this field silently
		return
	}
	if !exists {
		// Field not found, skip
		return
	}

	// Skip large fields (likely file uploads or binary data)
	if len(valueStr) > maxFieldSize {
		return
	}

	// Add as header with X-Body- prefix to avoid conflicts
	// Use the processed field name (with prefix stripped) as the header suffix
	headerName := "X-Body-" + lookupField
	req.Header.Set(headerName, valueStr)
}

// isValidFieldName checks if a field name contains only safe characters.
// Valid characters are: alphanumeric (a-z, A-Z, 0-9), dot (.), dash (-), underscore (_), and dollar sign ($).
// This prevents malicious field names from being used in HTTP headers.
func isValidFieldName(name string) bool {
	if name == "" {
		return false
	}

	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' && r != '_' && r != '$' {
			return false
		}
	}

	return true
}
