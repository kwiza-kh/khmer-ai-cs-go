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
//
// It REFUSES a well-known placeholder key. Length alone is not a security
// property: the demo key this repo documents base64-decodes to
// "0123456789abcdef…", and a deployment that kept it sealed every credential
// (page tokens, bot tokens, webhook secrets, TOTP secrets) under a value anyone
// with a database read could reproduce — found in production on 2026-10-17,
// where the startup log had said nothing about it.
func NewSealer(encodedKey string) (*Sealer, error) {
	s, err := NewSealerRaw(encodedKey)
	if err != nil {
		return nil, err
	}
	if reason := placeholderKeyReason(s.key); reason != "" {
		return nil, fmt.Errorf("PLATFORM_CREDENTIAL_KEY is a placeholder (%s): generate a real key with `openssl rand -base64 32`, rotate the stored ciphertext (cmd/rotatekey) and restart", reason)
	}
	return s, nil
}

// NewSealerRaw skips the placeholder check. Only the rotation tool may use it:
// it has to read values sealed under the very key NewSealer refuses.
func NewSealerRaw(encodedKey string) (*Sealer, error) {
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

// placeholderKeyReason names why a key is not a secret, or "" when it is usable.
// Deliberately small and explicit: a deny-list of the shapes this repo has
// actually shipped as examples, not a guess at an attacker's dictionary.
func placeholderKeyReason(key []byte) string {
	if len(key) == 0 {
		return "empty"
	}
	same := true
	for _, b := range key[1:] {
		if b != key[0] {
			same = false
			break
		}
	}
	if same {
		return "every byte is identical"
	}
	lower := strings.ToLower(string(key))
	for _, pat := range []string{"0123456789abcdef", "changeme", "password", "placeholder", "secretkey", "testtest"} {
		if strings.HasPrefix(lower, pat) || strings.Contains(lower, pat) {
			return "matches the documented example " + pat
		}
	}
	return ""
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
