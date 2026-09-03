package security

import "testing"

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func TestRoundtripAndPassthrough(t *testing.T) {
	sealer, err := NewSealer(testKey)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	plain := "EAAG secret page token"
	sealed, err := sealer.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !IsEncrypted(sealed) || sealed == plain {
		t.Fatalf("sealing failed: %s", sealed)
	}
	got, err := sealer.Decrypt(sealed)
	if err != nil || got != plain {
		t.Fatalf("roundtrip failed: %q err=%v", got, err)
	}
	// Idempotent encrypt.
	if again, _ := sealer.Encrypt(sealed); again != sealed {
		t.Fatal("encrypt must be idempotent")
	}
	// Empty + legacy plaintext passthrough.
	if got, _ := sealer.Encrypt(""); got != "" {
		t.Fatal("empty must pass through")
	}
	if got, _ := sealer.Decrypt(""); got != "" {
		t.Fatal("empty must pass through")
	}
	if got, _ := sealer.Decrypt("legacy-plaintext"); got != "legacy-plaintext" {
		t.Fatal("legacy plaintext must pass through")
	}
}

func TestSha256Hex(t *testing.T) {
	// sha256("hello") known value.
	if got := Sha256Hex("hello"); got != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("sha256 mismatch: %s", got)
	}
	if Sha256Hex("") != "" {
		t.Fatal("empty input must yield empty hash")
	}
}

func TestRejectsBadKeys(t *testing.T) {
	if _, err := NewSealer("too-short"); err == nil {
		t.Fatal("short key must be rejected")
	}
	if _, err := NewSealer(""); err == nil {
		t.Fatal("empty key must be rejected")
	}
}
