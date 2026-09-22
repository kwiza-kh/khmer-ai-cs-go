// Package security — platform credential sealing. Byte-compatible with the
// Rust/Go envelope so existing ciphertext decrypts transparently.
//
// Envelope: "enc:v1:" + base64url-no-pad(nonce(12) || AES-256-GCM(plaintext)).
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const credentialPrefix = "enc:v1:"

// Sealer encrypts/decrypts platform credentials with a fixed 32-byte key.
type Sealer struct {
	key []byte
}

// NewSealer builds from the env value (base64 of exactly 32 bytes).
func NewSealer(encodedKey string) (*Sealer, error) {
	trimmed := strings.TrimSpace(encodedKey)
	decoded, err := base64.RawStdEncoding.DecodeString(trimmed)
	if err != nil {
		if decoded, err = base64.StdEncoding.DecodeString(trimmed); err != nil {
			return nil, fmt.Errorf("PLATFORM_CREDENTIAL_KEY must be base64 for exactly 32 random bytes")
		}
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("PLATFORM_CREDENTIAL_KEY must be base64 for exactly 32 random bytes")
	}
	return &Sealer{key: decoded}, nil
}

// IsEncrypted reports the envelope prefix.
func IsEncrypted(value string) bool { return strings.HasPrefix(value, credentialPrefix) }

// Encrypt — passthrough for empty / already-encrypted values.
func (s *Sealer) Encrypt(value string) (string, error) {
	if value == "" || IsEncrypted(value) {
		return value, nil
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("credential encryption failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("credential encryption failed")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("credential encryption failed")
	}
	sealed := gcm.Seal(nil, nonce, []byte(value), nil)
	payload := append(nonce, sealed...)
	return credentialPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// Decrypt — passthrough for empty / plaintext (legacy) values.
func (s *Sealer) Decrypt(value string) (string, error) {
	if value == "" || !IsEncrypted(value) {
		return value, nil
	}
	raw := strings.TrimPrefix(value, credentialPrefix)
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("invalid encrypted credential format")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", fmt.Errorf("credential decryption failed")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("credential decryption failed")
	}
	ns := gcm.NonceSize()
	if len(payload) < ns {
		return "", fmt.Errorf("invalid encrypted credential length")
	}
	plain, err := gcm.Open(nil, payload[:ns], payload[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("credential decryption failed")
	}
	return string(plain), nil
}

// DecryptOrKeep returns the decrypted value, or the input unchanged when it is
// not sealed or cannot be opened. Call sites that cannot fail closed (a boot
// path that must still start, a pre-flight value) use this instead of Decrypt;
// a value that fails to open is passed through so the caller's own validation
// decides. Never use it where an unauthenticated credential must be rejected.
func (s *Sealer) DecryptOrKeep(value string) string {
	plain, err := s.Decrypt(value)
	if err != nil {
		return value
	}
	return plain
}

// Sha256Hex — lowercase hex SHA-256 (webhook secret / bot token hashes).
func Sha256Hex(input string) string {
	if input == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}
