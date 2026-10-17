package security

import (
	"encoding/base64"
	"testing"
)

// A key of the right length is not a secret. The deployment that prompted this
// check sealed every platform credential (page tokens, bot tokens, webhook
// secrets, TOTP secrets) under the demo key this repo documents, so a database
// backup was enough to read them all (2026-10-17).
func TestNewSealerRefusesPlaceholderKeys(t *testing.T) {
	demo := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cases := map[string]string{
		"documented demo key": demo,
		"all one byte":        base64.StdEncoding.EncodeToString(append(make([]byte, 32), 0)[:32]),
		"random-looking but named": base64.StdEncoding.EncodeToString(
			[]byte("changeme-changeme-changeme-1234")),
	}
	for name, key := range cases {
		if _, err := NewSealer(key); err == nil {
			t.Errorf("%s: NewSealer accepted a placeholder", name)
		}
	}
	// The raw constructor must still accept it: the rotation tool has to read
	// values sealed under the old, refused key.
	if _, err := NewSealerRaw(demo); err != nil {
		t.Fatalf("NewSealerRaw refused the demo key: %v", err)
	}
}

// A genuinely random 32-byte key passes, and the envelope round-trips with it.
func TestNewSealerAcceptsRandomKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("3f9a0c1d5e8b2746af30c9d17b6e0542"))
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	sealed, err := s.Encrypt("EAApage-token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := s.Decrypt(sealed)
	if err != nil || got != "EAApage-token" {
		t.Fatalf("round trip = (%q, %v)", got, err)
	}
	// Ciphertext sealed under another key must not decrypt.
	other, _ := NewSealer(base64.StdEncoding.EncodeToString([]byte("9e2b1a7c4f8d6350ab47e9c2d51f8a06")))
	if _, err := other.Decrypt(sealed); err == nil {
		t.Fatal("a different key decrypted the value")
	}
}
