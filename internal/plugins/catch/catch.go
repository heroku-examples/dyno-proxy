package catch

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// CatchHeaderItem defines a single header to catch in the array format.
type CatchHeaderItem struct {
	Header  string `json:"header" yaml:"header"`
	Decrypt string `json:"decrypt,omitempty" yaml:"decrypt,omitempty"`
}

// Config the plugin configuration.
// Uses array format: [{"header": "...", "decrypt": "..."}]
type Config struct {
	Items []CatchHeaderItem `json:"-" yaml:"-"`

	// Catch field for Traefik's nested structure
	Catch interface{} `json:"catch,omitempty" yaml:"catch,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for Config.
// Accepts array format: {"catch": [{"header": "...", "decrypt": "..."}]}
// Note: Traefik passes the config under the plugin name, so it might be double-nested like {"catch": {"catch": [...]}}
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a map to access the "catch" field
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Check if "catch" field exists
	if catchVal, ok := raw["catch"]; ok {
		// Handle double nesting: {"catch": {"catch": [...]}}
		if catchMap, ok := catchVal.(map[string]interface{}); ok {
			if nestedCatchVal, nestedOk := catchMap["catch"]; nestedOk {
				// This is double-nested, use the nested value
				catchVal = nestedCatchVal
			}
		}

		// Unmarshal catch as an array
		catchBytes, _ := json.Marshal(catchVal)
		var items []CatchHeaderItem
		if err := json.Unmarshal(catchBytes, &items); err != nil {
			return fmt.Errorf("catch config must be an array of header items: %v", err)
		}
		c.Items = items
		return nil
	}

	// Try to unmarshal directly as an array (in case Traefik passes the array directly)
	var items []CatchHeaderItem
	if err := json.Unmarshal(data, &items); err == nil {
		c.Items = items
		return nil
	}

	return fmt.Errorf("catch config must be an array of header items")
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{}
}

// HeaderKeyPair stores a header name and its associated decryption key.
type HeaderKeyPair struct {
	HeaderName string
	AESKey     []byte
}

// Demo a catch plugin that intercepts and decrypts headers.
type Demo struct {
	next       http.Handler
	headerKeys []HeaderKeyPair // List of headers with their decryption keys
	name       string
	logLevel   string
}

// getLogLevel returns the log level from environment variable or default
func getLogLevel() string {
	logLevel := os.Getenv("DYNO_PROXY_LOG_LEVEL")
	if logLevel == "" {
		return "INFO"
	}
	return strings.ToUpper(logLevel)
}

// shouldLogDebug returns true if DEBUG level logging should be enabled
func shouldLogDebug(logLevel string) bool {
	level := strings.ToUpper(logLevel)
	return level == "DEBUG" || level == "TRACE"
}

// logDebug logs a debug message if debug logging is enabled
func logDebug(pluginName, message string, fields map[string]interface{}) {
	logLevel := getLogLevel()
	if !shouldLogDebug(logLevel) {
		return
	}

	// Build a simple message with optional fields
	if len(fields) > 0 {
		var parts []string
		for k, v := range fields {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
		message = fmt.Sprintf("%s (%s)", message, strings.Join(parts, ", "))
	}

	fmt.Println(message)
}

// loadAESKey loads an AES-256 key from a key string or environment variable.
func loadAESKey(keySpec string) ([]byte, error) {
	// Get the actual key (could be env var or direct key)
	keyString := keySpec
	if strings.HasPrefix(keySpec, "$") {
		// Environment variable reference
		varName := strings.TrimPrefix(keySpec, "$")
		keyString = os.Getenv(varName)
		if keyString == "" {
			return nil, fmt.Errorf("environment variable %s is not set", varName)
		}
	}

	if keyString == "" {
		return nil, fmt.Errorf("encryption key is empty")
	}

	// Derive 32-byte (256-bit) key from the key string using SHA-256
	keyHash := sha256.Sum256([]byte(keyString))
	return keyHash[:], nil
}

// New created a new Demo plugin.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	var headerKeys []HeaderKeyPair
	logLevel := getLogLevel()

	// Debug: log what we received (this will help diagnose issues)
	if shouldLogDebug(logLevel) {
		configJSON, _ := json.Marshal(config)
		logDebug(name, fmt.Sprintf("Plugin config received - Items: %d, Raw: %s",
			len(config.Items), string(configJSON)), nil)
	}

	// If Items is empty but we have a Catch field (from Traefik's direct map passing), try to process it
	if len(config.Items) == 0 && config.Catch != nil {
		// Try to extract items from the Catch interface{}
		if catchMap, ok := config.Catch.(map[string]interface{}); ok {
			// Check if it's an array under "catch" key
			if catchVal, ok := catchMap["catch"]; ok {
				if catchArray, ok := catchVal.([]interface{}); ok {
					// Convert to Items
					for _, item := range catchArray {
						if itemMap, ok := item.(map[string]interface{}); ok {
							headerItem := CatchHeaderItem{}
							if header, ok := itemMap["header"].(string); ok {
								headerItem.Header = header
							}
							if decrypt, ok := itemMap["decrypt"].(string); ok {
								headerItem.Decrypt = decrypt
							}
							if headerItem.Header != "" {
								config.Items = append(config.Items, headerItem)
							}
						}
					}
				}
			}
		} else if catchArray, ok := config.Catch.([]interface{}); ok {
			// Catch is directly an array
			for _, item := range catchArray {
				if itemMap, ok := item.(map[string]interface{}); ok {
					headerItem := CatchHeaderItem{}
					if header, ok := itemMap["header"].(string); ok {
						headerItem.Header = header
					}
					if decrypt, ok := itemMap["decrypt"].(string); ok {
						headerItem.Decrypt = decrypt
					}
					if headerItem.Header != "" {
						config.Items = append(config.Items, headerItem)
					}
				}
			}
		}
	}

	if len(config.Items) == 0 {
		return nil, fmt.Errorf("header configuration is required: catch config must be an array of header items")
	}

	logDebug(name, fmt.Sprintf("Initializing catch plugin for %d header(s)", len(config.Items)), nil)

	for _, item := range config.Items {
		// Determine decryption key for this header
		decryptKeySpec := item.Decrypt
		if decryptKeySpec == "" {
			// Fall back to environment variable
			decryptKeySpec = "$DYNO_PROXY_ENCRYPTION_KEY"
		}

		// Load AES key for this header
		aesKey, err := loadAESKey(decryptKeySpec)
		if err != nil {
			return nil, fmt.Errorf("failed to load decryption key for header %s: %v", item.Header, err)
		}

		headerKeys = append(headerKeys, HeaderKeyPair{
			HeaderName: item.Header,
			AESKey:     aesKey,
		})
	}

	logDebug(name, fmt.Sprintf("AES-256 keys loaded successfully for %d header(s)", len(headerKeys)), nil)

	return &Demo{
		headerKeys: headerKeys,
		next:       next,
		name:       name,
		logLevel:   logLevel,
	}, nil
}

func (a *Demo) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Process each configured header with its associated key
	for _, headerKey := range a.headerKeys {
		headerName := headerKey.HeaderName
		aesKey := headerKey.AESKey

		encryptedValue := req.Header.Get(headerName)
		if encryptedValue == "" {
			continue
		}

		logDebug(a.name, fmt.Sprintf("Found header %s", headerName), map[string]interface{}{
			"header": headerName,
			"length": len(encryptedValue),
			"path":   req.URL.Path,
			"method": req.Method,
		})

		// Try to decrypt if it looks encrypted (base64, long enough)
		if len(encryptedValue) > 4 {
			logDebug(a.name, fmt.Sprintf("Attempting to decrypt header %s (length: %d)", headerName, len(encryptedValue)), map[string]interface{}{
				"header": headerName,
				"length": len(encryptedValue),
			})

			// Decode base64
			ciphertext, err := base64.StdEncoding.DecodeString(encryptedValue)
			if err != nil {
				logDebug(a.name, fmt.Sprintf("Failed to decode base64 for header %s", headerName), map[string]interface{}{
					"header": headerName,
					"error":  err.Error(),
				})
			} else {
				// Create AES cipher
				block, err := aes.NewCipher(aesKey)
				if err != nil {
					logDebug(a.name, fmt.Sprintf("Failed to create AES cipher for header %s", headerName), map[string]interface{}{
						"header": headerName,
						"error":  err.Error(),
					})
				} else {
					// Create GCM mode
					gcm, err := cipher.NewGCM(block)
					if err != nil {
						logDebug(a.name, fmt.Sprintf("Failed to create GCM for header %s", headerName), map[string]interface{}{
							"header": headerName,
							"error":  err.Error(),
						})
					} else {
						// Extract nonce (first 12 bytes)
						nonceSize := gcm.NonceSize()
						if len(ciphertext) < nonceSize {
							logDebug(a.name, fmt.Sprintf("Ciphertext too short for header %s", headerName), map[string]interface{}{
								"header":    headerName,
								"length":    len(ciphertext),
								"nonceSize": nonceSize,
							})
						} else {
							nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

							// Decrypt and verify authentication
							plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
							if err != nil {
								logDebug(a.name, fmt.Sprintf("Failed to decrypt header %s", headerName), map[string]interface{}{
									"header": headerName,
									"error":  err.Error(),
								})
							} else {
								// Successfully decrypted, rewrite the header
								decryptedValue := string(plaintext)
								req.Header.Set(headerName, decryptedValue)
								logDebug(a.name, fmt.Sprintf("Successfully decrypted header %s", headerName), map[string]interface{}{
									"header":          headerName,
									"decryptedLength": len(decryptedValue),
								})
							}
						}
					}
				}
			}
		} else {
			logDebug(a.name, fmt.Sprintf("Header %s value too short to be encrypted, skipping decryption", headerName), map[string]interface{}{
				"header": headerName,
				"length": len(encryptedValue),
			})
		}
	}

	a.next.ServeHTTP(rw, req)
}
