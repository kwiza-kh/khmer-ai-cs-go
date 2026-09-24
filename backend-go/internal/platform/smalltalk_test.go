package platform

import (
	"errors"
	"strings"
	"testing"
)

func TestSmallTalkReplyCoversEveryLanguage(t *testing.T) {
	seen := map[string]bool{}
	for _, lang := range []string{"km", "en", "zh"} {
		reply := SmallTalkReply(lang)
		if strings.TrimSpace(reply) == "" {
			t.Fatalf("no small-talk reply for %q", lang)
		}
		if seen[reply] {
			t.Errorf("language %q falls back to a reply already used elsewhere: %q", lang, reply)
		}
		seen[reply] = true
	}
	// An unknown tag must land on the Khmer default rather than an empty string.
	if SmallTalkReply("fr") != SmallTalkReply("km") {
		t.Error("unknown language must default to Khmer")
	}
}

func TestSmallTalkCannedToggle(t *testing.T) {
	if !SmallTalkCanned() {
		t.Error("canned small talk must be on by default")
	}
	t.Setenv("JEV_CHITCHAT_CANNED", "0")
	if SmallTalkCanned() {
		t.Error("JEV_CHITCHAT_CANNED=0 must disable it")
	}
	t.Setenv("JEV_CHITCHAT_CANNED", "garbage")
	if !SmallTalkCanned() {
		t.Error("an unparseable value must keep the default, not disable the path")
	}
}

func TestIsQuotaExhaustedOnRealSpendBody(t *testing.T) {
	// The exact body Google returns when the rolling spend window is full. It
	// says "billing", not "quota" — the reason the predicate matches several
	// needles instead of one.
	spend := errors.New("gemini returned HTTP 429: " +
		`{"error":{"code":429,"message":"You exceeded your spend-based rate limit. ` +
		`Your spending rate has exceeded the allowed limit for your account's billing history and tier.",` +
		`"status":"RESOURCE_EXHAUSTED"}}`)
	if !IsQuotaExhausted(spend) {
		t.Error("a spend-based 429 must be recognised")
	}
	if IsQuotaExhausted(errors.New("gemini request failed: context deadline exceeded")) {
		t.Error("a timeout is transient and must not be treated as exhausted")
	}
	if IsQuotaExhausted(nil) {
		t.Error("nil is not an exhaustion")
	}
}
