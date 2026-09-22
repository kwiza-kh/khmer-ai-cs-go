package api

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// jwksCache fetches and caches a provider JWKS document so id_token
// signatures can be verified with the provider's public keys. Keys are
// cached for one hour and refreshed on an unknown kid (one extra fetch per
// key rotation, not per request).
type jwksCache struct {
	url    string
	client *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewJWKSCache builds a JWKS cache for the given provider endpoint.
func NewJWKSCache(jwksURL string) *jwksCache {
	return &jwksCache{
		url:    jwksURL,
		client: &http.Client{Timeout: 10 * time.Second},
		keys:   map[string]*rsa.PublicKey{},
	}
}

type jwksDocument struct {
	Keys []struct {
		Kid string   `json:"kid"`
		Kty string   `json:"kty"`
		N   string   `json:"n"`
		E   string   `json:"e"`
		X5C []string `json:"x5c"`
	} `json:"keys"`
}

func (c *jwksCache) publicKey(kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key, ok := c.keys[kid]; ok && time.Since(c.fetched) < time.Hour {
		return key, nil
	}
	resp, err := c.client.Get(c.url)
	if err != nil {
		// Fall back to the cached set when the refresh fails and we have one.
		if key, ok := c.keys[kid]; ok {
			return key, nil
		}
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("jwks endpoint %d", resp.StatusCode)
	}
	var doc jwksDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		key, err := rsaKeyFromJWK(k.N, k.E, k.X5C)
		if err != nil || key == nil {
			continue
		}
		keys[k.Kid] = key
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("jwks contained no RSA keys")
	}
	c.keys = keys
	c.fetched = time.Now()
	key, ok := c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("id_token kid %q not in jwks", kid)
	}
	return key, nil
}

func rsaKeyFromJWK(nB64, eB64 string, x5c []string) (*rsa.PublicKey, error) {
	if nB64 != "" && eB64 != "" {
		n, err := base64.RawURLEncoding.DecodeString(nB64)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(eB64)
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	if len(x5c) > 0 {
		der, err := base64.StdEncoding.DecodeString(x5c[0])
		if err != nil {
			return nil, err
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return pub, nil
		}
	}
	return nil, fmt.Errorf("unsupported jwk")
}

// jwksKidPublicKey extracts the kid from the token header and resolves it
// against the cached JWKS.
func jwksKidPublicKey(c *jwksCache, idToken string) (*rsa.PublicKey, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed id_token")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode id_token header: %w", err)
	}
	var header struct {
		Kid string `json:"kid"`
	}
	if json.Unmarshal(headerRaw, &header) != nil {
		return nil, fmt.Errorf("id_token header")
	}
	return c.publicKey(header.Kid)
}

// verifyRS256 checks the RS256 signature of a compact JWS against key.
func verifyRS256(idToken string, key *rsa.PublicKey) error {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return fmt.Errorf("malformed id_token")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("decode id_token header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(headerRaw, &header) != nil {
		return fmt.Errorf("id_token header")
	}
	if !strings.EqualFold(header.Alg, "RS256") {
		return fmt.Errorf("unexpected id_token alg %q", header.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decode id_token signature: %w", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("id_token signature invalid")
	}
	return nil
}
