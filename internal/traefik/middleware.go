package traefik

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"

	"github.com/heroku-examples/dyno-proxy/internal/config"
)

// addMiddlewares renders a route's ordered middleware chain into Traefik
// middleware definitions, returning the ordered middleware names to attach to
// the router. keyBase namespaces the generated middleware names so routes do not
// collide. Ported from transformToTraefikConfig's per-route middleware handling
// and processSetConfig.
func (st *renderState) addMiddlewares(keyBase string, mws []config.Middleware) ([]string, error) {
	var names []string

	// set-header directives are accumulated into a single Headers middleware, as
	// in the legacy processSetConfig which produced one customRequestHeaders map.
	setHeaders := map[string]string{}

	for i, mw := range mws {
		switch mw.Kind {
		case config.CapSetPath:
			p, _ := mw.Config["path"].(string)
			name := fmt.Sprintf("%s-set-path-%d", keyBase, i)
			st.middlewares[name] = &middleware{ReplacePath: &replacePath{Path: p}}
			names = append(names, name)

		case config.CapSetHeader:
			header, _ := mw.Config["header"].(string)
			value, _ := mw.Config["value"].(string)
			encrypt, _ := mw.Config["encrypt"].(string)
			resolved, err := st.resolveValue(value, encrypt)
			if err != nil {
				// Match legacy: log-and-skip the failed header, keep rendering.
				continue
			}
			setHeaders[header] = resolved

		case config.CapDecrypt: // catch internal-plugin -> Yaegi plugin
			name := fmt.Sprintf("%s-catch-%d", keyBase, i)
			st.localPlugins["catch"] = "github.com/heroku/dyno-proxy/plugins/catch"
			st.middlewares[name] = &middleware{Plugin: map[string]interface{}{"catch": mw.Config}}
			names = append(names, name)

		case config.CapBodyToHeader: // body internal-plugin -> Yaegi plugin
			name := fmt.Sprintf("%s-body-%d", keyBase, i)
			st.localPlugins["body"] = "github.com/heroku/dyno-proxy/plugins/body"
			st.middlewares[name] = &middleware{Plugin: map[string]interface{}{"body": mw.Config}}
			names = append(names, name)

		case config.CapRawTraefikPlugin:
			name := fmt.Sprintf("%s-plugin-%d", keyBase, i)
			mwDef, err := st.rawPlugin(mw.Config)
			if err != nil {
				return nil, err
			}
			st.middlewares[name] = mwDef
			names = append(names, name)

		default:
			return nil, fmt.Errorf("traefik: unsupported middleware kind %q", mw.Kind)
		}
	}

	if len(setHeaders) > 0 {
		name := keyBase + "-set-headers"
		st.middlewares[name] = &middleware{Headers: &headersMiddleware{CustomRequestHeaders: setHeaders}}
		names = append(names, name)
	}

	return names, nil
}

// rawPlugin renders a CapRawTraefikPlugin middleware. It carries either a Yaegi
// plugin reference ("source" + optional "config") or an arbitrary Traefik
// middleware directive ("raw"), matching sources/convert.go.
func (st *renderState) rawPlugin(cfg map[string]any) (*middleware, error) {
	if raw, ok := cfg["raw"].(map[string]interface{}); ok {
		return &middleware{Raw: raw}, nil
	}
	if source, ok := cfg["source"].(string); ok && source != "" {
		name := pluginName(source)
		st.localPlugins[name] = source
		pluginCfg := map[string]interface{}{}
		if c, ok := cfg["config"].(map[string]interface{}); ok {
			pluginCfg = c
		}
		return &middleware{Plugin: map[string]interface{}{name: pluginCfg}}, nil
	}
	return nil, fmt.Errorf("traefik: raw plugin middleware missing source/raw config")
}

// pluginName derives a stable plugin name from its source (the last path
// element, sans extension), reproducing the legacy plugin naming for local
// plugins without the network fetch.
func pluginName(source string) string {
	base := path.Base(strings.TrimSuffix(source, "/"))
	base = strings.TrimSuffix(base, ".git")
	if base == "" || base == "." || base == "/" {
		return "plugin"
	}
	return base
}

// resolveValue reproduces set.go:getOrExecuteValue: it resolves a value spec
// (literal, $ENV, or $(command)) and optionally AES-256-GCM encrypts it.
func (st *renderState) resolveValue(spec, encryptKey string) (string, error) {
	value, err := st.executeShellCommand(spec)
	if err != nil {
		return "", err
	}
	if encryptKey != "" {
		return encryptValue(value, encryptKey, st)
	}
	return value, nil
}

// executeShellCommand ports set.go:executeShellCommand.
func (st *renderState) executeShellCommand(cmd string) (string, error) {
	if strings.HasPrefix(cmd, "$") && !strings.Contains(cmd, "$(") {
		name := strings.TrimPrefix(cmd, "$")
		v := st.getenv(name)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		return v, nil
	}
	if strings.HasPrefix(cmd, "$(") && strings.HasSuffix(cmd, ")") {
		actual := strings.TrimSuffix(strings.TrimPrefix(cmd, "$("), ")")
		out, err := exec.Command("sh", "-c", actual).Output()
		if err != nil {
			return "", fmt.Errorf("failed to execute command %q: %w", actual, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return cmd, nil
}

// encryptValue ports set.go:encryptValue (AES-256-GCM, base64). The 32-byte key
// is derived by SHA-256 of the key spec (itself resolved via shell/env).
func encryptValue(value, keySpec string, st *renderState) (string, error) {
	keyString, err := st.executeShellCommand(keySpec)
	if err != nil {
		return "", fmt.Errorf("failed to get encryption key from %q: %w", keySpec, err)
	}
	if keyString == "" {
		return "", fmt.Errorf("encryption key from %q is empty", keySpec)
	}
	keyHash := sha256.Sum256([]byte(keyString))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}
