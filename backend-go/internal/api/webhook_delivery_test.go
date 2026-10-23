package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWebhookSignatureKnownVector(t *testing.T) {
	const secret = "s3cret-signing-key"
	const ts = int64(1700000000)
	body := []byte(`{"event":"session.created","data":{}}`)

	got := webhookSignature(secret, ts, body)

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.", ts)
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	// The timestamp is part of the signed material: the same body signed at a
	// different second must not verify with the first header's timestamp.
	if other := webhookSignature(secret, ts+1, body); other == got {
		t.Fatal("timestamp is not part of the signed material — a replay would verify")
	}
	if empty := webhookSignature("", ts, body); !strings.HasPrefix(empty, "sha256=") || empty == got {
		t.Fatal("a different secret must produce a different signature")
	}
}

func TestValidateWebhookURL(t *testing.T) {
	ok := []string{
		"https://example.com/hook",
		"https://hooks.example.com:8443/path?x=1",
		"https://8.8.8.8/hook",
	}
	for _, raw := range ok {
		if err := validateWebhookURL(raw); err != nil {
			t.Errorf("validateWebhookURL(%q) = %v, want nil", raw, err)
		}
	}

	bad := map[string]string{
		"empty":            "",
		"plain http":       "http://example.com/hook",
		"localhost":        "https://localhost/hook",
		"localhost suffix": "https://api.localhost/hook",
		"loopback v4":      "https://127.0.0.1/hook",
		"loopback v6":      "https://[::1]/hook",
		"private v4":       "https://10.0.0.5/hook",
		"private 192":      "https://192.168.1.4/hook",
		"metadata":         "https://169.254.169.254/latest/meta-data/",
		"userinfo":         "https://user:pass@example.com/hook",
		"garbage":          "not-a-url",
		"too long":         "https://example.com/" + strings.Repeat("a", 500),
	}
	for name, raw := range bad {
		if err := validateWebhookURL(raw); err == nil {
			t.Errorf("%s: validateWebhookURL(%q) = nil, want an error", name, raw)
		}
	}
}

func TestWebhookRetryDelay(t *testing.T) {
	cases := []struct {
		attempts int32
		want     time.Duration
	}{
		{0, 1 * time.Minute},
		{1, 1 * time.Minute},
		{2, 5 * time.Minute},
		{3, 30 * time.Minute},
		{4, 2 * time.Hour},
		{5, 6 * time.Hour},
		{6, 6 * time.Hour},
	}
	for _, c := range cases {
		if got := webhookRetryDelay(c.attempts); got != c.want {
			t.Errorf("webhookRetryDelay(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}

func TestClassifyWebhookAttempt(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		err        error
		attempts   int32
		success    bool
		terminal   bool
		retryAfter time.Duration
	}{
		{"200", 200, nil, 1, true, false, 0},
		{"204", 204, nil, 3, true, false, 0},
		{"500 retries", 500, nil, 1, false, false, 1 * time.Minute},
		{"redirect is a failure", 302, nil, 1, false, false, 1 * time.Minute},
		{"transport error retries", 0, errors.New("dial timeout"), 2, false, false, 5 * time.Minute},
		{"attempt cap is terminal", 500, nil, webhookDeliveryMaxAttempts, false, true, 0},
		{"transport cap is terminal", 0, errors.New("dial timeout"), webhookDeliveryMaxAttempts, false, true, 0},
	}
	for _, c := range cases {
		got := classifyWebhookAttempt(c.status, c.err, c.attempts)
		if got.success != c.success || got.terminal != c.terminal || got.retryIn != c.retryAfter {
			t.Errorf("%s: outcome = %+v, want success=%v terminal=%v retryIn=%v",
				c.name, got, c.success, c.terminal, c.retryAfter)
		}
	}
}

func TestWebhookDeliveryTargetRejectsPlainHTTP(t *testing.T) {
	if _, err := webhookDeliveryTarget("http://example.com/hook"); err == nil {
		t.Fatal("plain http must be rejected at delivery time too")
	}
	if _, err := webhookDeliveryTarget("https://example.com/hook"); err != nil {
		t.Fatalf("https URL rejected: %v", err)
	}
}
