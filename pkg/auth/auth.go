package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ai-research-mindmapper-go/pkg/config"
)

type Authenticator struct {
	cfg        *config.Config
	jwksURL    string
	keysMutex  sync.RWMutex
	cachedKeys map[string]*rsa.PublicKey
	lastFetch  time.Time
}

type jwksResponse struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func NewAuthenticator(cfg *config.Config) *Authenticator {
	jwksURL := deriveJWKSURL(cfg.ClerkPublishableKey)
	return &Authenticator{
		cfg:        cfg,
		jwksURL:    jwksURL,
		cachedKeys: make(map[string]*rsa.PublicKey),
	}
}

func deriveJWKSURL(publishableKey string) string {
	parts := strings.Split(publishableKey, "_")
	if len(parts) >= 3 {
		b64 := parts[len(parts)-1]
		if rem := len(b64) % 4; rem > 0 {
			b64 += strings.Repeat("=", 4-rem)
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err == nil {
			domain := strings.TrimRight(string(decoded), "$")
			if domain != "" {
				return fmt.Sprintf("https://%s/.well-known/jwks.json", domain)
			}
		}
	}
	return "https://skilled-orca-78.clerk.accounts.dev/.well-known/jwks.json"
}

func (a *Authenticator) GetUserID(authHeader string) (string, error) {
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return "", errors.New("authentication required")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Stress-test bypass
	if a.cfg.StressTestKey != "" && token == a.cfg.StressTestKey {
		return "user_stress_test", nil
	}

	// Local script bypass
	if a.cfg.ScriptAPIKey != "" && token == a.cfg.ScriptAPIKey {
		return "user_local_script", nil
	}

	// Chrome extension bypass: "ext_<install_uuid>_<EXTENSION_PUBLIC_KEY>"
	if a.cfg.ExtensionPublicKey != "" && strings.HasSuffix(token, "_"+a.cfg.ExtensionPublicKey) {
		parts := strings.Split(token, "_")
		installID := "unknown"
		if len(parts) >= 3 {
			installID = parts[1]
			if len(installID) > 32 {
				installID = installID[:32]
			}
		}
		return fmt.Sprintf("user_ext_%s", installID), nil
	}

	// Parse Clerk JWT with RS256
	parsedToken, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		return a.getKey(kid)
	}, jwt.WithLeeway(60*time.Second))

	if err != nil || !parsedToken.Valid {
		return "", errors.New("invalid or expired token")
	}

	claims, ok := parsedToken.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid token claims")
	}

	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return "", errors.New("missing sub claim")
	}

	return sub, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	sClean := strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(sClean); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(sClean); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func (a *Authenticator) getKey(kid string) (*rsa.PublicKey, error) {
	a.keysMutex.RLock()
	key, found := a.cachedKeys[kid]
	fresh := time.Since(a.lastFetch) < 1*time.Hour
	a.keysMutex.RUnlock()

	if found && fresh {
		return key, nil
	}

	a.keysMutex.Lock()
	defer a.keysMutex.Unlock()

	// Double check under write lock
	if key, found = a.cachedKeys[kid]; found && time.Since(a.lastFetch) < 1*time.Hour {
		return key, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(a.jwksURL)
	if err != nil {
		if key != nil {
			return key, nil // stale key is better than total failure
		}
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if key != nil {
			return key, nil
		}
		return nil, fmt.Errorf("JWKS returned status %d", resp.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %w", err)
	}

	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := decodeBase64URL(k.N)
		if err != nil {
			continue
		}
		eBytes, err := decodeBase64URL(k.E)
		if err != nil {
			continue
		}
		var eInt int
		for _, b := range eBytes {
			eInt = (eInt << 8) | int(b)
		}

		n := new(big.Int).SetBytes(nBytes)
		pub := &rsa.PublicKey{
			N: n,
			E: eInt,
		}
		a.cachedKeys[k.Kid] = pub
	}
	a.lastFetch = time.Now()

	if key, ok := a.cachedKeys[kid]; ok {
		return key, nil
	}
	// Fallback to first available key if specific kid wasn't found
	if len(a.cachedKeys) > 0 {
		for _, k := range a.cachedKeys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("key with kid %s not found in JWKS", kid)
}
