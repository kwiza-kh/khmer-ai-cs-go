package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// signRequest builds a Meta-shaped signed_request the way Meta does: the
// signature covers the base64url payload segment as transmitted.
func signRequest(t *testing.T, secret string, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(enc))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) + "." + enc
}

func validPayload() map[string]any {
	return map[string]any{
		"algorithm": "HMAC-SHA256",
		"user_id":   "1234567890123456",
		"issued_at": 1790000000,
	}
}

func TestParseSignedRequestAcceptsAWellFormedRequest(t *testing.T) {
	secret := "s3cr3t-app-secret"
	signed := signRequest(t, secret, validPayload())

	p, err := parseSignedRequest(secret, signed)
	if err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if p.UserID != "1234567890123456" {
		t.Fatalf("user_id = %q, want 1234567890123456", p.UserID)
	}
	if p.Algorithm != "HMAC-SHA256" {
		t.Fatalf("algorithm = %q", p.Algorithm)
	}
}

// The whole security boundary: anything but a correct HMAC over the exact
// transmitted payload segment must be refused.
func TestParseSignedRequestRejectsForgery(t *testing.T) {
	secret := "s3cr3t-app-secret"
	good := signRequest(t, secret, validPayload())
	parts := strings.SplitN(good, ".", 2)

	cases := []struct {
		name   string
		secret string
		signed string
	}{
		{"wrong secret", "not-the-secret", good},
		{"empty secret", "", good},
		{"tampered payload, original signature", secret, parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"algorithm":"HMAC-SHA256","user_id":"9999999999999999","issued_at":1790000000}`))},
		{"tampered signature", secret, strings.Repeat("A", len(parts[0])) + "." + parts[1]},
		{"no separator", secret, good},
		{"empty signature part", secret, "." + parts[1]},
		{"empty payload part", secret, parts[0] + "."},
		{"signature is not base64", secret, "!!!!." + parts[1]},
		{"payload is not base64", secret, parts[0] + ".!!!!"},
		{"payload is not json", secret, func() string {
			enc := base64.RawURLEncoding.EncodeToString([]byte("not json at all"))
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(enc))
			return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) + "." + enc
		}()},
		{"truncated", secret, good[:len(good)/2]},
		{"empty", secret, ""},
	}
	// "no separator" must genuinely lack a dot to test that branch.
	for i := range cases {
		if cases[i].name == "no separator" {
			cases[i].signed = strings.ReplaceAll(good, ".", "")
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseSignedRequest(c.secret, c.signed); err == nil {
				t.Fatal("forged/invalid request was accepted")
			}
		})
	}
}

// Only the algorithm actually verified may pass; anything else is an attempt to
// get the payload trusted under a weaker scheme.
func TestParseSignedRequestRejectsAlgorithmConfusion(t *testing.T) {
	secret := "s3cr3t-app-secret"
	for _, algo := range []string{"", "none", "MD5", "HS256", "PLAINTEXT"} {
		p := validPayload()
		p["algorithm"] = algo
		if _, err := parseSignedRequest(secret, signRequest(t, secret, p)); err == nil {
			t.Fatalf("algorithm %q was accepted", algo)
		}
	}
	// Case-insensitive acceptance of the genuine scheme.
	p := validPayload()
	p["algorithm"] = "hmac-sha256"
	if _, err := parseSignedRequest(secret, signRequest(t, secret, p)); err != nil {
		t.Fatalf("lowercase HMAC-SHA256 rejected: %v", err)
	}
}

func TestParseSignedRequestRequiresUserID(t *testing.T) {
	secret := "s3cr3t-app-secret"

	// Present but empty.
	p := validPayload()
	p["user_id"] = ""
	if _, err := parseSignedRequest(secret, signRequest(t, secret, p)); err == nil {
		t.Fatal("empty user_id was accepted")
	}

	// Absent entirely.
	delete(p, "user_id")
	if _, err := parseSignedRequest(secret, signRequest(t, secret, p)); err == nil {
		t.Fatal("payload without user_id was accepted")
	}
}

// Some senders pad base64url segments. The signature always covers the payload
// segment exactly as transmitted, so a padded payload is valid as long as it
// was signed in that padded form — and a padded *signature* segment must
// decode the same as an unpadded one.
func TestParseSignedRequestToleratesPaddedSegments(t *testing.T) {
	secret := "s3cr3t-app-secret"
	raw, err := json.Marshal(validPayload())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pad := func(s string) string {
		for len(s)%4 != 0 {
			s += "="
		}
		return s
	}
	// Sign the padded payload form, exactly as a padding sender would.
	paddedPayload := pad(base64.RawURLEncoding.EncodeToString(raw))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(paddedPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if _, err := parseSignedRequest(secret, pad(sig)+"."+paddedPayload); err != nil {
		t.Fatalf("padded signature segment rejected: %v", err)
	}
	if _, err := parseSignedRequest(secret, sig+"."+paddedPayload); err != nil {
		t.Fatalf("padded payload segment rejected: %v", err)
	}
	// A padded payload whose signature covers the *unpadded* form must fail:
	// that is a real mismatch, not a tolerated encoding difference.
	macUnpadded := hmac.New(sha256.New, []byte(secret))
	macUnpadded.Write([]byte(strings.TrimRight(paddedPayload, "=")))
	sigUnpadded := base64.RawURLEncoding.EncodeToString(macUnpadded.Sum(nil))
	if _, err := parseSignedRequest(secret, sigUnpadded+"."+paddedPayload); err == nil {
		t.Fatal("signature over the unpadded form must not validate a padded payload")
	}
}

// The delete path refuses ids that could collide with a real value.
func TestSubjectIDIsSafe(t *testing.T) {
	ok := []string{"12345", "6017151165000810", strings.Repeat("a", 64)}
	for _, id := range ok {
		if !subjectIDIsSafe(id) {
			t.Errorf("id %q should be accepted", id)
		}
	}
	bad := []string{"", "1", "1234", strings.Repeat("a", 65), "0"}
	for _, id := range bad {
		if subjectIDIsSafe(id) {
			t.Errorf("id %q (len=%d) should be refused", id, len(id))
		}
	}
}

func TestNewConfirmationCodeIsAlphanumericAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		code, err := newConfirmationCode()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if code == "" {
			t.Fatal("empty code")
		}
		for _, r := range code {
			if !(r >= 'A' && r <= 'Z') && !(r >= '2' && r <= '7') {
				t.Fatalf("code %q has a non-alphanumeric or ambiguous rune %q", code, r)
			}
		}
		if seen[code] {
			t.Fatalf("duplicate code after %d draws: %q", i, code)
		}
		seen[code] = true
	}
}

// metaFamily must stay scoped to Meta's identifier space: widening it would let
// an id collision delete another platform's sessions.
func TestMetaFamilyIsScopedToMetaPlatforms(t *testing.T) {
	want := map[string]bool{"meta": true, "instagram": true, "whatsapp": true}
	for _, p := range metaFamily {
		if !want[p] {
			t.Errorf("metaFamily contains %q, which is outside Meta's id space", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("metaFamily is missing %q", p)
	}
	// Telegram and LINE ids must never be swept by a Meta deletion.
	for _, foreign := range []string{"telegram", "line", "zalo"} {
		for _, p := range metaFamily {
			if p == foreign {
				t.Fatalf("metaFamily must not contain %q", foreign)
			}
		}
	}
}
