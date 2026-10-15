package catch_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/heroku/dyno-proxy/plugins/catch"
)

func generateTestKey(t *testing.T) string {
	t.Helper()
	// Generate a random 32-byte key and encode as base64
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(keyBytes)
}

func encryptWithAES(t *testing.T, plaintext string, keyString string) string {
	t.Helper()
	// Derive 32-byte (256-bit) key from the key string using SHA-256
	keyHash := sha256.Sum256([]byte(keyString))
	aesKey := keyHash[:]

	// Create AES cipher
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("failed to create AES cipher: %v", err)
	}

	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("failed to create GCM: %v", err)
	}

	// Generate random nonce (12 bytes for GCM)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("failed to generate nonce: %v", err)
	}

	// Encrypt and authenticate
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode as base64 for safe transport
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func TestDemo_NoHeader(t *testing.T) {
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	cfg := catch.CreateConfig()
	cfg.Items = []catch.CatchHeaderItem{
		{
			Header:  "X-Test-Header",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY",
		},
	}

	ctx := context.Background()
	var receivedHeader string
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedHeader = req.Header.Get("X-Test-Header")
	})

	handler, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}

	handler.ServeHTTP(recorder, req)

	if receivedHeader != "" {
		t.Errorf("expected no header, got %s", receivedHeader)
	}
}

func TestDemo_DecryptHeader(t *testing.T) {
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	cfg := catch.CreateConfig()
	cfg.Items = []catch.CatchHeaderItem{
		{
			Header:  "X-Test-Header",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY",
		},
	}

	ctx := context.Background()
	var receivedHeader string
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedHeader = req.Header.Get("X-Test-Header")
	})

	handler, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err != nil {
		t.Fatal(err)
	}

	// Encrypt a test value
	plaintext := "decrypted-value"
	encrypted := encryptWithAES(t, plaintext, keyString)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Header", encrypted)

	handler.ServeHTTP(recorder, req)

	if receivedHeader != plaintext {
		t.Errorf("expected decrypted header value %s, got %s", plaintext, receivedHeader)
	}
}

func TestDemo_NonEncryptedHeader(t *testing.T) {
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	cfg := catch.CreateConfig()
	cfg.Items = []catch.CatchHeaderItem{
		{
			Header:  "X-Test-Header",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY",
		},
	}

	ctx := context.Background()
	var receivedHeader string
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedHeader = req.Header.Get("X-Test-Header")
	})

	handler, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err != nil {
		t.Fatal(err)
	}

	// Use a short, non-encrypted value
	originalValue := "short-value"

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-Header", originalValue)

	handler.ServeHTTP(recorder, req)

	// Should remain unchanged since it's not encrypted
	if receivedHeader != originalValue {
		t.Errorf("expected unchanged header value %s, got %s", originalValue, receivedHeader)
	}
}

func TestDemo_MissingHeaderConfig(t *testing.T) {
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	cfg := catch.CreateConfig()
	// Header not set

	ctx := context.Background()
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {})

	_, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err == nil {
		t.Fatal("expected error when header configuration is missing")
	}
}

func TestDemo_MissingEncryptionKey(t *testing.T) {
	os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	cfg := catch.CreateConfig()
	cfg.Items = []catch.CatchHeaderItem{
		{
			Header:  "X-Test-Header",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY",
		},
	}

	ctx := context.Background()
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {})

	_, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err == nil {
		t.Fatal("expected error when DYNO_PROXY_ENCRYPTION_KEY is not set")
	}
}

func TestDemo_NewArrayFormat(t *testing.T) {
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	// Test new array format
	cfg := catch.CreateConfig()
	cfg.Items = []catch.CatchHeaderItem{
		{
			Header:  "X-FORWARDED-DATABASE-URL",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY",
		},
	}

	ctx := context.Background()
	var receivedHeader string
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedHeader = req.Header.Get("X-FORWARDED-DATABASE-URL")
	})

	handler, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err != nil {
		t.Fatal(err)
	}

	// Encrypt a test value
	plaintext := "postgres://user:pass@host:5432/db"
	encrypted := encryptWithAES(t, plaintext, keyString)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-FORWARDED-DATABASE-URL", encrypted)

	handler.ServeHTTP(recorder, req)

	if receivedHeader != plaintext {
		t.Errorf("expected decrypted header value %s, got %s", plaintext, receivedHeader)
	}
}

func TestDemo_NewArrayFormatMultipleHeaders(t *testing.T) {
	keyString1 := generateTestKey(t)
	keyString2 := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString1)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY_2", keyString2)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY_2")

	// Test new array format with multiple headers and different keys
	cfg := catch.CreateConfig()
	cfg.Items = []catch.CatchHeaderItem{
		{
			Header:  "X-FORWARDED-DATABASE-URL",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY",
		},
		{
			Header:  "X-FORWARDED-API-KEY",
			Decrypt: "$DYNO_PROXY_ENCRYPTION_KEY_2",
		},
	}

	ctx := context.Background()
	var receivedDBURL string
	var receivedAPIKey string
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedDBURL = req.Header.Get("X-FORWARDED-DATABASE-URL")
		receivedAPIKey = req.Header.Get("X-FORWARDED-API-KEY")
	})

	handler, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err != nil {
		t.Fatal(err)
	}

	// Encrypt test values
	plaintextDB := "postgres://user:pass@host:5432/db"
	plaintextAPI := "secret-api-key-123"
	encryptedDB := encryptWithAES(t, plaintextDB, keyString1)
	encryptedAPI := encryptWithAES(t, plaintextAPI, keyString2)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-FORWARDED-DATABASE-URL", encryptedDB)
	req.Header.Set("X-FORWARDED-API-KEY", encryptedAPI)

	handler.ServeHTTP(recorder, req)

	if receivedDBURL != plaintextDB {
		t.Errorf("expected decrypted DB URL %s, got %s", plaintextDB, receivedDBURL)
	}
	if receivedAPIKey != plaintextAPI {
		t.Errorf("expected decrypted API key %s, got %s", plaintextAPI, receivedAPIKey)
	}
}

func TestConfig_UnmarshalJSON_ArrayFormat(t *testing.T) {
	// Test array format JSON unmarshaling
	jsonData := `{"catch": [{"header": "X-Test-Header", "decrypt": "$DYNO_PROXY_ENCRYPTION_KEY"}]}`
	var cfg catch.Config
	err := json.Unmarshal([]byte(jsonData), &cfg)
	if err != nil {
		t.Fatalf("Failed to unmarshal array format: %v", err)
	}

	if len(cfg.Items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(cfg.Items))
	}

	if cfg.Items[0].Header != "X-Test-Header" {
		t.Errorf("Expected header 'X-Test-Header', got '%s'", cfg.Items[0].Header)
	}
	if cfg.Items[0].Decrypt != "$DYNO_PROXY_ENCRYPTION_KEY" {
		t.Errorf("Expected decrypt '$DYNO_PROXY_ENCRYPTION_KEY', got '%s'", cfg.Items[0].Decrypt)
	}
}

func TestConfig_UnmarshalJSON_TraefikFormat(t *testing.T) {
	// Test the format that Traefik actually passes to the plugin
	// This simulates what's in the dynamic JSON: {"catch": [{"header": "...", "decrypt": "..."}]}
	jsonData := `{"catch": [{"header": "X-FORWARDED-DATABASE-URL", "decrypt": "$DYNO_PROXY_ENCRYPTION_KEY"}]}`
	var cfg catch.Config
	err := json.Unmarshal([]byte(jsonData), &cfg)
	if err != nil {
		t.Fatalf("Failed to unmarshal Traefik format: %v", err)
	}

	if len(cfg.Items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(cfg.Items))
	}

	if cfg.Items[0].Header != "X-FORWARDED-DATABASE-URL" {
		t.Errorf("Expected header 'X-FORWARDED-DATABASE-URL', got '%s'", cfg.Items[0].Header)
	}
	if cfg.Items[0].Decrypt != "$DYNO_PROXY_ENCRYPTION_KEY" {
		t.Errorf("Expected decrypt '$DYNO_PROXY_ENCRYPTION_KEY', got '%s'", cfg.Items[0].Decrypt)
	}

	// Test that the plugin can be created with this config
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	ctx := context.Background()
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {})

	handler, err := catch.New(ctx, next, &cfg, "catch-plugin")
	if err != nil {
		t.Fatalf("Failed to create plugin with Traefik format config: %v", err)
	}
	if handler == nil {
		t.Fatal("Handler should not be nil")
	}
}

func TestConfig_UnmarshalJSON_DoubleNested(t *testing.T) {
	// Test the double-nested format that Traefik actually uses
	// This simulates: {"catch": {"catch": [{"header": "...", "decrypt": "..."}]}}
	jsonData := `{"catch": {"catch": [{"header": "X-FORWARDED-DATABASE-URL", "decrypt": "$DYNO_PROXY_ENCRYPTION_KEY"}]}}`
	var cfg catch.Config
	err := json.Unmarshal([]byte(jsonData), &cfg)
	if err != nil {
		t.Fatalf("Failed to unmarshal double-nested format: %v", err)
	}

	if len(cfg.Items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(cfg.Items))
	}

	if cfg.Items[0].Header != "X-FORWARDED-DATABASE-URL" {
		t.Errorf("Expected header 'X-FORWARDED-DATABASE-URL', got '%s'", cfg.Items[0].Header)
	}
	if cfg.Items[0].Decrypt != "$DYNO_PROXY_ENCRYPTION_KEY" {
		t.Errorf("Expected decrypt '$DYNO_PROXY_ENCRYPTION_KEY', got '%s'", cfg.Items[0].Decrypt)
	}

	// Test that the plugin can be created with this config
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	ctx := context.Background()
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {})

	handler, err := catch.New(ctx, next, &cfg, "catch-plugin")
	if err != nil {
		t.Fatalf("Failed to create plugin with double-nested config: %v", err)
	}
	if handler == nil {
		t.Fatal("Handler should not be nil")
	}
}

func TestNew_WithCatchFieldAsMap(t *testing.T) {
	// Test the case where Traefik passes config with Catch field as a map (bypassing UnmarshalJSON)
	// This simulates what actually happens when Traefik loads the plugin
	keyString := generateTestKey(t)
	os.Setenv("DYNO_PROXY_ENCRYPTION_KEY", keyString)
	defer os.Unsetenv("DYNO_PROXY_ENCRYPTION_KEY")

	cfg := catch.CreateConfig()
	// Simulate Traefik setting the Catch field directly as a map
	cfg.Catch = map[string]interface{}{
		"catch": []interface{}{
			map[string]interface{}{
				"header":  "X-FORWARDED-DATABASE-URL",
				"decrypt": "$DYNO_PROXY_ENCRYPTION_KEY",
			},
		},
	}

	ctx := context.Background()
	var receivedHeader string
	next := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedHeader = req.Header.Get("X-FORWARDED-DATABASE-URL")
	})

	handler, err := catch.New(ctx, next, cfg, "catch-plugin")
	if err != nil {
		t.Fatalf("Failed to create plugin with Catch field as map: %v", err)
	}
	if handler == nil {
		t.Fatal("Handler should not be nil")
	}

	// Encrypt a test value
	plaintext := "postgres://user:pass@host:5432/db"
	encrypted := encryptWithAES(t, plaintext, keyString)

	recorder := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-FORWARDED-DATABASE-URL", encrypted)

	handler.ServeHTTP(recorder, req)

	if receivedHeader != plaintext {
		t.Errorf("expected decrypted header value %s, got %s", plaintext, receivedHeader)
	}
}
