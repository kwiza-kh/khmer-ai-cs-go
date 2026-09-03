package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestTokenRoundtripAndClaims(t *testing.T) {
	a := NewJWT("0123456789abcdef0123456789abcdef", 24)
	token, err := a.GenerateToken(7, "alice", "user")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	claims, err := a.ParseToken(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != 7 || claims.Username != "alice" || claims.Role != "user" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if claims.Issuer != "khmer-ai-cs" {
		t.Fatalf("issuer mismatch: %s", claims.Issuer)
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expiry not in the future")
	}
}

func TestRejectsWrongSecretAndGarbage(t *testing.T) {
	a := NewJWT("0123456789abcdef0123456789abcdef", 24)
	other := NewJWT("fedcba9876543210fedcba9876543210", 24)
	token, err := a.GenerateToken(1, "bob", "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ParseToken(token); err == nil {
		t.Fatal("wrong secret must not verify")
	}
	if _, err := a.ParseToken("not.a.jwt"); err == nil {
		t.Fatal("garbage token must not verify")
	}
}

func TestRejectsAlgorithmConfusion(t *testing.T) {
	a := NewJWT("0123456789abcdef0123456789abcdef", 24)
	token, _ := a.GenerateToken(1, "eve", "admin")
	// Craft a token signed with alg=none: must be rejected.
	parts := strings.Split(token, ".")
	forged := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"user_id": 1})
	noneToken, _ := forged.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := a.ParseToken(noneToken); err == nil {
		t.Fatal("alg=none must be rejected")
	}
	_ = parts
}

func TestBcryptCompatAndRehashDetection(t *testing.T) {
	old, err := bcrypt.GenerateFromPassword([]byte("secret1"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("secret1", string(old)) {
		t.Fatal("cost-10 hash must verify")
	}
	if !PasswordNeedsRehash(string(old)) {
		t.Fatal("cost-10 hash must need rehash")
	}
	current, err := HashPassword("secret2")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("secret2", current) {
		t.Fatal("current hash must verify")
	}
	if PasswordNeedsRehash(current) {
		t.Fatal("cost-12 hash must not need rehash")
	}
	if VerifyPassword("wrong", current) {
		t.Fatal("wrong password must not verify")
	}
}

func TestTOTPRoundtrip(t *testing.T) {
	secret := GenerateTOTPSecret()
	if secret == "" {
		t.Fatal("empty secret")
	}
	// Generate the code the same way a client would.
	key, _ := base32Decode(secret)
	step := uint64(time.Now().Unix()) / 30
	code := hotp(key, step)
	candidate := padCode(code)
	if !VerifyTOTP(secret, candidate) {
		t.Fatalf("current code %s must verify", candidate)
	}
	if VerifyTOTP(secret, padCode(code^0x1)) && padCode(code^0x1) != candidate {
		t.Fatal("wrong code must not verify")
	}
}

func padCode(code int) string {
	s := strings.Repeat("0", 6-len(itoa(code))) + itoa(code)
	return s
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf []byte
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	return string(buf)
}
