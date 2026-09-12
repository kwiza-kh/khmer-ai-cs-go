package platform

import (
	"errors"
	"testing"

	"khmer-ai-cs-go/internal/config"
)

// TestIsQuotaExhausted — the alert it gates is deduped per 15 minutes, so a
// false positive silences the real warning for that window, and a false
// negative means the operator hears about a dead AI from a customer instead.
// Both directions matter.
func TestIsQuotaExhausted(t *testing.T) {
	exhausted := []string{
		"gemini returned HTTP 429: RESOURCE_EXHAUSTED",
		"gemini: failed (429): quota exceeded",
		"token endpoint 429: billing account has no positive balance",
		"prepay balance depleted",
	}
	for _, s := range exhausted {
		if !isQuotaExhausted(errors.New(s)) {
			t.Errorf("should be treated as quota exhaustion: %q", s)
		}
	}

	transient := []string{
		"gemini request failed: dial tcp: connection refused",
		"gemini returned HTTP 503: model overloaded",
		"gemini returned unparsable JSON: unexpected end of input",
		"context deadline exceeded",
	}
	for _, s := range transient {
		if isQuotaExhausted(errors.New(s)) {
			t.Errorf("should NOT be treated as quota exhaustion: %q", s)
		}
	}

	if isQuotaExhausted(nil) {
		t.Error("nil error must not report quota exhaustion")
	}
}

// TestPlatformBotLink pins the deep-link shape, including the @-prefix strip —
// BotFather shows handles with a leading @ and pasting that into the config
// would otherwise produce a link Telegram cannot resolve.
func TestPlatformBotLink(t *testing.T) {
	p := &Pipeline{Cfg: &config.Config{}}
	p.Cfg.PlatformBot.PublicBotUsername = "@relaychat_bot"

	if got, want := p.PlatformBotLink("tok123"), "https://t.me/relaychat_bot?start=tok123"; got != want {
		t.Errorf("link = %q, want %q", got, want)
	}

	p.Cfg.PlatformBot.PublicBotUsername = ""
	if got := p.PlatformBotLink("tok123"); got != "" {
		t.Errorf("no username configured should yield no link, got %q", got)
	}

	p.Cfg.PlatformBot.PublicBotUsername = "relaychat_bot"
	if got := p.PlatformBotLink(""); got != "" {
		t.Errorf("no token should yield no link, got %q", got)
	}
}

// TestIsPlatformAdminChat — the operator console reads across every tenant, so
// the allow-list must match exactly and never on a prefix or a substring.
func TestIsPlatformAdminChat(t *testing.T) {
	p := &Pipeline{Cfg: &config.Config{}}
	p.Cfg.PlatformBot.AdminIDs = []int64{123456789, 987654321}

	if !p.IsPlatformAdminChat(123456789) {
		t.Error("allow-listed id rejected")
	}
	if p.IsPlatformAdminChat(1234567890) {
		t.Error("id accepted on a prefix match — the allow-list must be exact")
	}
	if p.IsPlatformAdminChat(0) {
		t.Error("unknown id accepted")
	}
}

// TestPlatformAlertChatFallback documents where alerts land when no explicit
// chat is configured: a DM to the first allow-listed admin.
func TestPlatformAlertChatFallback(t *testing.T) {
	p := &Pipeline{Cfg: &config.Config{}}
	p.Cfg.PlatformBot.AdminIDs = []int64{555, 666}
	if got := p.platformAlertChat(); got != "555" {
		t.Errorf("fallback chat = %q, want the first admin id", got)
	}

	p.Cfg.PlatformBot.AlertChatID = "-100123"
	if got := p.platformAlertChat(); got != "-100123" {
		t.Errorf("explicit alert chat ignored: got %q", got)
	}

	empty := &Pipeline{Cfg: &config.Config{}}
	if got := empty.platformAlertChat(); got != "" {
		t.Errorf("no config should yield no chat, got %q", got)
	}
}
