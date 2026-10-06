package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// TestTokenIssuer checks both halves of the rename window: tokens minted now
// carry the RelayChat issuer, tokens minted before it still parse (so the
// rename does not sign every logged-in user out), and a token from an
// unrelated issuer is still rejected.
func TestTokenIssuer(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	a := NewJWT(secret, 24)

	fresh, err := a.GenerateToken(7, "alice", "user", 0)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if claims, err := a.ParseToken(fresh); err != nil {
		t.Fatalf("fresh token rejected: %v", err)
	} else if claims.Issuer != JWTIssuer {
		t.Fatalf("new tokens must carry issuer %q, got %q", JWTIssuer, claims.Issuer)
	}

	sign := func(issuer string) string {
		t.Helper()
		claims := Claims{
			UserID: 5, Username: "legacy", Role: "user",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				Issuer:    issuer,
			},
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
		if err != nil {
			t.Fatalf("sign %q: %v", issuer, err)
		}
		return signed
	}

	if claims, err := a.ParseToken(sign(legacyJWTIssuer)); err != nil {
		t.Fatalf("pre-rename token rejected — the deploy would log everyone out: %v", err)
	} else if claims.UserID != 5 {
		t.Fatalf("legacy token parsed to the wrong user: %d", claims.UserID)
	}

	if _, err := a.ParseToken(sign("some-other-service")); err == nil {
		t.Fatal("token from an unrelated issuer was accepted")
	}
}

func TestTokenRoundtripAndClaims(t *testing.T) {
	a := NewJWT("0123456789abcdef0123456789abcdef", 24)
	token, err := a.GenerateToken(7, "alice", "user", 0)
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
	if claims.Issuer != JWTIssuer {
		t.Fatalf("issuer mismatch: %s", claims.Issuer)
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expiry not in the future")
	}
}

func TestRejectsWrongSecretAndGarbage(t *testing.T) {
	a := NewJWT("0123456789abcdef0123456789abcdef", 24)
	other := NewJWT("fedcba9876543210fedcba9876543210", 24)
	token, err := a.GenerateToken(1, "bob", "user", 0)
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
	token, _ := a.GenerateToken(1, "eve", "admin", 0)
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
	return fmt.Sprintf("%06d", code)
}
