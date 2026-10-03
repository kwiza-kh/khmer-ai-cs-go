package platform

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The reply-window rules moved out of a platform switch and into capabilities.
// Tests 1–4 pass a nil pool on purpose: they prove the capability gate decides
// *before* any database access, which is exactly what the old switch did.

func TestEnsureReplyWindowWindowlessChannelsSkipTheDatabase(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, p := range []string{"telegram", "line", "zalo"} {
		deadline, extended, err := EnsureReplyWindow(context.Background(), nil, p, 1, "sess", false, false, now)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", p, err)
		}
		if extended {
			t.Fatalf("%s: windowless channels never set extended", p)
		}
		if want := now.Add(CustomerCareWindowHours * time.Hour); !deadline.Equal(want) {
			t.Fatalf("%s: deadline = %s, want %s", p, deadline, want)
		}
	}
}

func TestEnsureReplyWindowUnknownPlatformIsRefused(t *testing.T) {
	_, _, err := EnsureReplyWindow(context.Background(), nil, "myspace", 1, "sess", false, false, time.Now())
	if err == nil || err.Error() != "unsupported platform reply policy" {
		t.Fatalf("err = %v, want the legacy refusal", err)
	}
}

// The website widget is Known but has no window and is not windowless: the old
// switch refused it with the same error, and that must not change.
func TestEnsureReplyWindowWebChannelIsStillRefused(t *testing.T) {
	_, _, err := EnsureReplyWindow(context.Background(), nil, "web", 1, "sess", false, false, time.Now())
	if err == nil || err.Error() != "unsupported platform reply policy" {
		t.Fatalf("err = %v, want the legacy refusal", err)
	}
}

func TestEnsureReplyWindowWhatsAppTemplateIsExempt(t *testing.T) {
	now := time.Now()
	deadline, extended, err := EnsureReplyWindow(context.Background(), nil, "whatsapp", 1, "sess", true, false, now)
	if err != nil {
		t.Fatalf("template send must be allowed without a customer context, got %v", err)
	}
	if extended {
		t.Fatal("template exemption is not a human-agent extension")
	}
	if want := now.Add(CustomerCareWindowHours * time.Hour); !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
}

func TestDecideInWindowInsideWindow(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	last := now.Add(-1 * time.Hour)
	caps := CapabilitiesFor("meta")

	deadline, extended, err := decideInWindow(caps, &last, false, now)
	if err != nil || extended {
		t.Fatalf("inside the window: err=%v extended=%v", err, extended)
	}
	if want := last.Add(24 * time.Hour); !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
}

func TestDecideInWindowHumanExtensionIsAgentOnly(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	last := now.Add(-72 * time.Hour) // 3 days: past 24h, inside the 7-day extension
	caps := CapabilitiesFor("meta")

	_, _, err := decideInWindow(caps, &last, false, now)
	if err == nil {
		t.Fatal("an automatic reply 3 days out must be refused")
	}
	if !strings.Contains(err.Error(), "automatic replies are not allowed") {
		t.Fatalf("err = %v, want the automatic-reply refusal", err)
	}
	if !strings.Contains(err.Error(), HumanAgentTag) {
		t.Fatalf("err = %v, must tell the operator about the %s tag", err, HumanAgentTag)
	}

	deadline, extended, err := decideInWindow(caps, &last, true, now)
	if err != nil {
		t.Fatalf("a human reply inside the extension must be allowed, got %v", err)
	}
	if !extended {
		t.Fatal("a human reply inside the extension must set extended")
	}
	if want := last.Add(HumanAgentWindowDays * 24 * time.Hour); !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
}

func TestDecideInWindowExpiredEntirely(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	last := now.Add(-8 * 24 * time.Hour)
	_, _, err := decideInWindow(CapabilitiesFor("instagram"), &last, true, now)
	if err == nil || !strings.Contains(err.Error(), "have expired") {
		t.Fatalf("err = %v, want the fully-expired refusal", err)
	}
}

func TestDecideInWindowWhatsAppHardStop(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	last := now.Add(-25 * time.Hour)
	_, _, err := decideInWindow(CapabilitiesFor("whatsapp"), &last, true, now)
	if err == nil {
		t.Fatal("whatsapp past 24h must be refused even for a human")
	}
	if !strings.Contains(err.Error(), "approved template") {
		t.Fatalf("err = %v, want the template advice", err)
	}
}

// Merchant-facing messages must name the product, not the database enum value:
// the old code interpolated the platform string and produced "The meta 24-hour
// reply window…".
func TestPolicyMessagesUseDisplayNameNotEnumValue(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	last := now.Add(-72 * time.Hour)

	_, _, err := decideInWindow(CapabilitiesFor("meta"), &last, false, now)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "The meta ") {
		t.Fatalf("raw enum value leaked into a merchant message: %v", err)
	}
	if !strings.Contains(err.Error(), "Messenger") {
		t.Fatalf("err = %v, want the Messenger display name", err)
	}

	last = now.Add(-25 * time.Hour)
	_, _, err = decideInWindow(CapabilitiesFor("whatsapp"), &last, false, now)
	if err == nil || !strings.HasPrefix(err.Error(), "The WhatsApp ") {
		t.Fatalf("err = %v, want the WhatsApp display name", err)
	}
}
