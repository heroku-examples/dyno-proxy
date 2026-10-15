package body_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heroku/dyno-proxy/plugins/body"
)

func TestBodyParserJSON(t *testing.T) {
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

	jsonBody := `{"name": "John", "user_id": "123", "email": "john@example.com"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	assertRequestHeader(t, receivedReq, "X-Body-name", "John")
	assertRequestHeader(t, receivedReq, "X-Body-user_id", "123")
	assertRequestHeader(t, receivedReq, "X-Body-email", "john@example.com")
}

func TestBodyParserFormEncoded(t *testing.T) {
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

	formBody := "name=John&user_id=123&email=john@example.com"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(formBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	assertRequestHeader(t, receivedReq, "X-Body-name", "John")
	assertRequestHeader(t, receivedReq, "X-Body-user_id", "123")
	assertRequestHeader(t, receivedReq, "X-Body-email", "john@example.com")
}

func TestBodyParserWithFieldFilter(t *testing.T) {
	cfg := body.CreateConfig()
	cfg.Fields = []string{"name", "user_id"}

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

	jsonBody := `{"name": "John", "user_id": "123", "email": "john@example.com"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Should have filtered fields
	assertRequestHeader(t, receivedReq, "X-Body-name", "John")
	assertRequestHeader(t, receivedReq, "X-Body-user_id", "123")

	// Should NOT have email (not in filter)
	if receivedReq.Header.Get("X-Body-email") != "" {
		t.Errorf("email should not be extracted when not in fields filter, got: %s", receivedReq.Header.Get("X-Body-email"))
	}
}

func TestBodyParserSkipsLargeFields(t *testing.T) {
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

	// Create a large field (simulating file upload)
	largeField := strings.Repeat("x", 2048) // 2KB, larger than maxFieldSize (1KB)
	jsonBody := `{"name": "John", "file_data": "` + largeField + `"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Should have small field
	assertRequestHeader(t, receivedReq, "X-Body-name", "John")

	// Should NOT have large field
	if receivedReq.Header.Get("X-Body-file_data") != "" {
		t.Errorf("large field should be skipped, got header with length: %d", len(receivedReq.Header.Get("X-Body-file_data")))
	}
}

func TestBodyParserSkipsUnsupportedContentType(t *testing.T) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader("some data"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Should not have any body headers
	if len(receivedReq.Header) > 0 {
		// Check if any X-Body- headers exist
		hasBodyHeaders := false
		for key := range receivedReq.Header {
			if strings.HasPrefix(key, "X-Body-") {
				hasBodyHeaders = true
				break
			}
		}
		if hasBodyHeaders {
			t.Error("should not extract headers from unsupported content type")
		}
	}
}

func TestBodyParserPreservesBody(t *testing.T) {
	cfg := body.CreateConfig()

	ctx := context.Background()
	var bodyBytes []byte
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
	})

	handler, err := body.New(ctx, next, cfg, "body-parser")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	jsonBody := `{"name": "John"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	// Body should be preserved for downstream handlers
	if string(bodyBytes) != jsonBody {
		t.Errorf("body was not preserved: expected %s, got %s", jsonBody, string(bodyBytes))
	}
}

func TestBodyParserJSONNumbers(t *testing.T) {
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

	jsonBody := `{"user_id": 123, "age": 30}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Numbers should be converted to strings
	assertRequestHeader(t, receivedReq, "X-Body-user_id", "123")
	assertRequestHeader(t, receivedReq, "X-Body-age", "30")
}

func TestBodyParserJSONBooleans(t *testing.T) {
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

	jsonBody := `{"active": true, "verified": false}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)

	if receivedReq == nil {
		t.Fatal("next handler was not called")
	}

	// Booleans should be converted to strings
	assertRequestHeader(t, receivedReq, "X-Body-active", "true")
	assertRequestHeader(t, receivedReq, "X-Body-verified", "false")
}

func TestBodyParserNestedFields(t *testing.T) {
	cfg := body.CreateConfig()
	// Configure to extract nested fields
	cfg.Fields = []string{"organization.id", "organization.name", "user.name"}

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

	jsonBody := `{
		"user": {
			"name": "John Doe",
			"email": "john@example.com"
		},
		"organization": {
			"id": "org-123",
			"name": "Acme Corp"
		}
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

	// Should extract nested fields with dot notation in header names
	assertRequestHeader(t, receivedReq, "X-Body-organization.id", "org-123")
	assertRequestHeader(t, receivedReq, "X-Body-organization.name", "Acme Corp")
	assertRequestHeader(t, receivedReq, "X-Body-user.name", "John Doe")

	// Should NOT extract fields not in the filter
	if receivedReq.Header.Get("X-Body-user.email") != "" {
		t.Errorf("user.email should not be extracted when not in fields filter, got: %s", receivedReq.Header.Get("X-Body-user.email"))
	}
}

func TestBodyParserMixedTopLevelAndNestedFields(t *testing.T) {
	cfg := body.CreateConfig()
	// Mix of top-level and nested fields
	cfg.Fields = []string{"name", "organization.id"}

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

	jsonBody := `{
		"name": "John",
		"email": "john@example.com",
		"organization": {
			"id": "org-456",
			"name": "Tech Co"
		}
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

	// Should extract both top-level and nested fields
	assertRequestHeader(t, receivedReq, "X-Body-name", "John")
	assertRequestHeader(t, receivedReq, "X-Body-organization.id", "org-456")

	// Should NOT extract fields not in the filter
	if receivedReq.Header.Get("X-Body-email") != "" {
		t.Errorf("email should not be extracted when not in fields filter")
	}
	if receivedReq.Header.Get("X-Body-organization.name") != "" {
		t.Errorf("organization.name should not be extracted when not in fields filter")
	}
}

func TestBodyParserNestedFieldsWithNumbers(t *testing.T) {
	cfg := body.CreateConfig()
	cfg.Fields = []string{"total.profit", "total.count"}

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

	jsonBody := `{
		"total": {
			"profit": 4.99,
			"count": 42
		}
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

	// Should convert nested numeric values correctly
	assertRequestHeader(t, receivedReq, "X-Body-total.profit", "4.99")
	assertRequestHeader(t, receivedReq, "X-Body-total.count", "42")
}

func TestBodyParserNestedFieldMissing(t *testing.T) {
	cfg := body.CreateConfig()
	cfg.Fields = []string{"organization.id", "organization.missing"}

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

	jsonBody := `{
		"organization": {
			"id": "org-789"
		}
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

	// Should extract existing field
	assertRequestHeader(t, receivedReq, "X-Body-organization.id", "org-789")

	// Should NOT extract missing field (should be empty)
	if receivedReq.Header.Get("X-Body-organization.missing") != "" {
		t.Errorf("missing nested field should not create a header")
	}
}

func TestBodyParserPrefixStripping(t *testing.T) {
	cfg := body.CreateConfig()
	// Configure fields with $. and . prefixes
	cfg.Fields = []string{"$.organization.id", ".user.name", "email"}

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

	jsonBody := `{
		"organization": {
			"id": "org-999"
		},
		"user": {
			"name": "Jane Doe"
		},
		"email": "jane@example.com"
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

	// Headers should have prefixes stripped
	assertRequestHeader(t, receivedReq, "X-Body-organization.id", "org-999")
	assertRequestHeader(t, receivedReq, "X-Body-user.name", "Jane Doe")
	assertRequestHeader(t, receivedReq, "X-Body-email", "jane@example.com")

	// Should NOT create headers with the original prefixed names
	if receivedReq.Header.Get("X-Body-$.organization.id") != "" {
		t.Errorf("should not create header with $. prefix")
	}
	if receivedReq.Header.Get("X-Body-.user.name") != "" {
		t.Errorf("should not create header with . prefix")
	}
}

func TestBodyParserDollarDotPrefix(t *testing.T) {
	cfg := body.CreateConfig()
	cfg.Fields = []string{"$.top.level", "$.simple"}

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

	jsonBody := `{
		"top": {
			"level": "value1"
		},
		"simple": "value2"
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

	// Should strip $. prefix from header names
	assertRequestHeader(t, receivedReq, "X-Body-top.level", "value1")
	assertRequestHeader(t, receivedReq, "X-Body-simple", "value2")
}

func TestBodyParserDotPrefix(t *testing.T) {
	cfg := body.CreateConfig()
	cfg.Fields = []string{".nested.field", ".root"}

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

	jsonBody := `{
		"nested": {
			"field": "test123"
		},
		"root": "abc"
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

	// Should strip . prefix from header names
	assertRequestHeader(t, receivedReq, "X-Body-nested.field", "test123")
	assertRequestHeader(t, receivedReq, "X-Body-root", "abc")
}

func TestBodyParserNoPrefixUnchanged(t *testing.T) {
	cfg := body.CreateConfig()
	cfg.Fields = []string{"organization.id", "name"}

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

	jsonBody := `{
		"organization": {
			"id": "org-123"
		},
		"name": "Test"
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

	// Fields without prefixes should remain unchanged
	assertRequestHeader(t, receivedReq, "X-Body-organization.id", "org-123")
	assertRequestHeader(t, receivedReq, "X-Body-name", "Test")
}

func assertRequestHeader(t *testing.T, req *http.Request, key, expected string) {
	t.Helper()

	actual := req.Header.Get(key)
	if actual != expected {
		t.Errorf("invalid request header value for %s: expected %s, got %s", key, expected, actual)
	}
}
