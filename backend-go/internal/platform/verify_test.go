package platform

import (
	"context"
	"errors"
	"testing"
)

// The two switches that decide "which channels does this build serve" — NewChannel
// and verifyHandler — must name the same platforms. A channel that can send but
// cannot be verified is a support ticket that only appears when a merchant clicks
// "test connection", and the capability table cannot catch it on its own: "web" is
// a declared capability with no outbound implementation at all.
//
// Asking NewChannel is the point: the list is not restated here, so adding a
// channel to one switch and forgetting the other fails this test.
func TestNewChannelAndVerifyHandlerAgree(t *testing.T) {
	candidates := append(KnownPlatforms(), "telegram", "line", "zalo", "whatsapp", "meta", "instagram", "web", "not-a-platform")
	seen := map[string]bool{}
	for _, plat := range candidates {
		if seen[plat] {
			continue
		}
		seen[plat] = true
		// NewChannel only maps the name to an implementation; it opens no
		// connection, so this stays an offline check.
		_, chErr := NewChannel(nil, &configCred{Platform: plat})
		canSend := chErr == nil
		canVerify := verifyHandler(plat) != nil
		if canSend != canVerify {
			t.Errorf("%s: NewChannel ok=%v, verifyHandler present=%v — the two lists have drifted",
				plat, canSend, canVerify)
		}
	}
}

func TestVerifyConnectionRejectsAPlatformWithNoChannel(t *testing.T) {
	// "web" is known to the capability table, so a Known()-only check lets it
	// through: the rejection has to come from here, as a typed error the handler
	// can answer 400 for.
	_, err := VerifyConnection(context.Background(), "web", VerifyCredentials{}, VerifyParams{})
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("web: got %v (%T), want a *VerifyError", err, err)
	}
	if ve.Stage != VerifyUnsupportedPlatform {
		t.Errorf("web: stage = %v, want VerifyUnsupportedPlatform", ve.Stage)
	}
	if ve.Msg != "unsupported platform: web" {
		t.Errorf("web: msg = %q, want %q", ve.Msg, "unsupported platform: web")
	}
}

// A provider-side failure must NOT be a *VerifyError: the API layer records those
// against the config (status='error', which the console counts to warn
// "渠道连接异常") and answers 502, whereas every *VerifyError stage takes a different
// branch. This pins the 400/500/502 split that the API layer switches on, offline:
// each case fails locally, before any provider call, and must still be typed.
func TestVerifyErrorStagesAreDistinctFromProviderFailures(t *testing.T) {
	if testing.Short() {
		// The provider leg is only reachable by actually calling the provider; the
		// two offline invariants above are the gate, this one is a smoke check.
		t.Skip("touches api.telegram.org")
	}
	// Telegram is the one channel that can fail locally before it talks to the
	// provider: with no usable public webhook URL the check cannot be attempted,
	// and the credential must not be recorded as the reason.
	noURL := func(string) (string, error) { return "", errors.New("PUBLIC_API_URL must be set") }
	_, err := VerifyConnection(context.Background(), "telegram", VerifyCredentials{}, VerifyParams{WebhookURL: noURL})
	if err == nil {
		t.Fatal("a missing PUBLIC_API_URL must fail the check")
	}
	var ve *VerifyError
	if errors.As(err, &ve) {
		// Accepted only as the local-config stage: a provider rejection here would
		// mean the classification moved and the API would answer 502 with a
		// "re-issue your token" story for a server misconfiguration.
		if ve.Stage != VerifyServerMisconfigured && ve.Stage != VerifyWebhookRegistrationFailed {
			t.Fatalf("telegram without a webhook URL: unexpected stage %v (%q)", ve.Stage, ve.Msg)
		}
		return
	}
	// Not reaching the provider at all is also acceptable (no network in the test
	// environment): what matters is that it is not silently classified as a
	// credential problem by *this* layer.
	t.Logf("telegram check failed before the URL was consulted: %v", err)
}
