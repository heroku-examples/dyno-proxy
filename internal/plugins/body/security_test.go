package body_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heroku/dyno-proxy/plugins/body"
)

// TestBodyParserDoSProtection verifies that the body parser limits body size to prevent DoS attacks.
func TestBodyParserDoSProtection(t *testing.T) {
	cfg := body.CreateConfig()

	ctx := context.Background()
	var receivedReq *http.Request
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedReq = req
	})

	handler, err := body.New(ctx, next, cfg, "body-parser")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	// Create a body larger than 1MB (maxBodySize)
	// Using 1.5MB to ensure it exceeds the limit
	largeBody := strings.Repeat("x", 1536*1024) // 1.5MB
	jsonBody := `{"name": "John", "data": "` + largeBody + `"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// The body should be truncated at maxBodySize (1MB), so JSON parsing will fail
	// and no headers should be extracted
	if receivedReq.Header.Get("X-Body-name") != "" {
		t.Error("headers should not be extracted when body exceeds size limit")
	}
}

// TestBodyParserHeaderInjectionProtection verifies that malicious field names are rejected.
func TestBodyParserHeaderInjectionProtection(t *testing.T) {
	tests := []struct {
		name        string
		fields      []string
		jsonBody    string
		shouldBlock bool
	}{
		{
			name:        "newline in field name",
			fields:      []string{"name\nX-Evil"},
			jsonBody:    `{"name\nX-Evil": "injected"}`,
			shouldBlock: true,
		},
		{
			name:        "carriage return in field name",
			fields:      []string{"name\rX-Evil"},
			jsonBody:    `{"name\rX-Evil": "injected"}`,
			shouldBlock: true,
		},
		{
			name:        "colon in field name",
			fields:      []string{"name:evil"},
			jsonBody:    `{"name:evil": "injected"}`,
			shouldBlock: true,
		},
		{
			name:        "space in field name",
			fields:      []string{"name evil"},
			jsonBody:    `{"name evil": "injected"}`,
			shouldBlock: true,
		},
		{
			name:        "null byte in field name",
			fields:      []string{"name\x00evil"},
			jsonBody:    `{"name\x00evil": "injected"}`,
			shouldBlock: true,
		},
		{
			name:        "valid field with dots",
			fields:      []string{"organization.id"},
			jsonBody:    `{"organization": {"id": "org-123"}}`,
			shouldBlock: false,
		},
		{
			name:        "valid field with dashes",
			fields:      []string{"user-id"},
			jsonBody:    `{"user-id": "123"}`,
			shouldBlock: false,
		},
		{
			name:        "valid field with underscores",
			fields:      []string{"user_id"},
			jsonBody:    `{"user_id": "123"}`,
			shouldBlock: false,
		},
		{
			name:        "valid alphanumeric field",
			fields:      []string{"user123"},
			jsonBody:    `{"user123": "value"}`,
			shouldBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := body.CreateConfig()
			cfg.Fields = tt.fields

			ctx := context.Background()
			var receivedReq *http.Request
			next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				receivedReq = req
			})

			handler, err := body.New(ctx, next, cfg, "body-parser")
			if err != nil {
				t.Fatal(err)
			}

			recorder := httptest.NewRecorder()

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(tt.jsonBody))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")

			handler.ServeHTTP(recorder, req)

			if receivedReq == nil {
				t.Fatal("next handler was not called")
			}

			// Check if any X-Body- headers were created
			hasBodyHeaders := false
			for key := range receivedReq.Header {
				if strings.HasPrefix(key, "X-Body-") {
					hasBodyHeaders = true
					break
				}
			}

			if tt.shouldBlock && hasBodyHeaders {
				t.Errorf("expected malicious field %q to be blocked, but headers were created", tt.fields[0])
			}

			if !tt.shouldBlock && !hasBodyHeaders {
				t.Errorf("expected valid field %q to be accepted, but no headers were created", tt.fields[0])
			}
		})
	}
}

// TestBodyParserAllFieldsHeaderInjectionProtection verifies that malicious field names
// are rejected even when no field filter is configured (all fields mode).
func TestBodyParserAllFieldsHeaderInjectionProtection(t *testing.T) {
	cfg := body.CreateConfig()
	// Empty fields = extract all fields

	ctx := context.Background()
	var receivedReq *http.Request
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedReq = req
	})

	handler, err := body.New(ctx, next, cfg, "body-parser")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	// JSON with both valid and invalid field names
	jsonBody := `{
		"validName": "good",
		"valid_field": "good",
		"valid-field": "good",
		"evil\ninjection": "bad",
		"space injection": "bad",
		"colon:injection": "bad"
	}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Check that valid fields are extracted
	if receivedReq.Header.Get("X-Body-validName") != "good" {
		t.Error("valid field 'validName' should be extracted")
	}
	if receivedReq.Header.Get("X-Body-valid_field") != "good" {
		t.Error("valid field 'valid_field' should be extracted")
	}
	if receivedReq.Header.Get("X-Body-valid-field") != "good" {
		t.Error("valid field 'valid-field' should be extracted")
	}

	// Check that invalid fields are NOT extracted
	// Note: In Go, JSON unmarshaling will preserve the field names as-is
	for key := range receivedReq.Header {
		if strings.Contains(key, "\n") || strings.Contains(key, " ") || strings.Contains(key, ":") {
			t.Errorf("header with invalid characters should not be created: %q", key)
		}
	}
}

// TestBodyParserFormEncodedHeaderInjection verifies header injection protection for form-encoded bodies.
func TestBodyParserFormEncodedHeaderInjection(t *testing.T) {
	cfg := body.CreateConfig()
	// Empty fields = extract all fields

	ctx := context.Background()
	var receivedReq *http.Request
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedReq = req
	})

	handler, err := body.New(ctx, next, cfg, "body-parser")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	// Form with both valid and invalid field names
	formBody := "valid_name=good&evil%0Ainjection=bad&space+injection=bad"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(formBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Check that valid field is extracted
	if receivedReq.Header.Get("X-Body-valid_name") != "good" {
		t.Error("valid field 'valid_name' should be extracted")
	}

	// Check that invalid fields are NOT extracted
	for key := range receivedReq.Header {
		if strings.Contains(key, "\n") || strings.Contains(key, " ") {
			t.Errorf("header with invalid characters should not be created: %q", key)
		}
	}
}
