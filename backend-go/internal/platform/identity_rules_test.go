package platform

import (
	"testing"

	"khmer-ai-cs-go/internal/security"
)

// The declared credential list is now the server-side validation's source of truth,
// so a platform that declares a field Required has to be rejected without it.
//
// This test exists because the two copies drifted in the field: the table said
// whatsapp_business_account_id was Required and the handler never checked it, so a
// WhatsApp config could be saved with the id blank and only fail later, at send
// time.
func TestMissingRequiredCredentialFollowsTheDeclaredList(t *testing.T) {
	for _, plat := range KnownPlatforms() {
		caps := CapabilitiesFor(plat)
		if len(caps.Credentials) == 0 {
			continue
		}
		if msg := caps.MissingRequiredCredential(map[string]string{}); msg == "" {
			t.Errorf("%s declares credentials but accepts an empty set", plat)
		}
		full := map[string]string{}
		for _, f := range caps.Credentials {
			full[f.Key] = "submitted"
		}
		if msg := caps.MissingRequiredCredential(full); msg != "" {
			t.Errorf("%s: a complete set was rejected with %q", plat, msg)
		}
		for _, f := range caps.Credentials {
			if f.Required {
				if f.MissingMessage == "" {
					t.Errorf("%s: required field %s has no MissingMessage — the console "+
						"would show an untranslated generated sentence", plat, f.Key)
				}
				partial := map[string]string{}
				for k, v := range full {
					partial[k] = v
				}
				delete(partial, f.Key)
				if msg := caps.MissingRequiredCredential(partial); msg == "" {
					t.Errorf("%s: missing required %s was accepted", plat, f.Key)
				}
			}
		}
	}
}

// The cross-tenant conflict column, pinned to the bug that shipped.
//
// The handler compared page_id for every platform except Instagram, so LINE/Zalo/
// WhatsApp — whose page_id is empty because those channels never use one — compared
// ” = ” and the second merchant to connect one was told
// "该平台账号已连接到其他客户".
func TestIdentityRulesKeyOnEachChannelsOwnIdentity(t *testing.T) {
	for plat, want := range map[string]string{
		"meta":      "page_id",
		"instagram": "instagram_business_id",
		"whatsapp":  "whatsapp_business_account_id",
		"telegram":  "bot_token_hash",
	} {
		r := CapabilitiesFor(plat).Identity
		if r.Column != want {
			t.Errorf("%s identity column = %q, want %q", plat, r.Column, want)
		}
		if !r.Unique {
			t.Errorf("%s must dedupe cross-tenant on %s", plat, want)
		}
		if r.ConflictMessage == "" {
			t.Errorf("%s has no conflict message for the operator", plat)
		}
	}
	// Channels whose identity is only learned at verify time must declare nothing
	// here: migration 051's partial unique index covers that half at the database,
	// where the value actually exists.
	for _, plat := range []string{"line", "zalo"} {
		if r := CapabilitiesFor(plat).Identity; r.Column != "" || r.Unique {
			t.Errorf("%s must declare no save-time identity, got %+v", plat, r)
		}
	}
	// No channel outside the Meta family may dedupe on page_id — that is the bug.
	for _, plat := range KnownPlatforms() {
		if r := CapabilitiesFor(plat).Identity; r.Column == "page_id" && plat != "meta" {
			t.Errorf("%s dedupes on page_id, which is the false-conflict bug", plat)
		}
	}
}

func TestIdentityRuleHashesBotTokensAndSkipsBlanks(t *testing.T) {
	tg := CapabilitiesFor("telegram").Identity
	got, ok := tg.Value(func(string) string { return "123:ABC" })
	if !ok || got != security.Sha256Hex("123:ABC") {
		t.Fatalf("telegram identity = %q ok=%v, want the sha256 of the submitted token", got, ok)
	}
	if _, ok := tg.Value(func(string) string { return "   " }); ok {
		t.Fatal("a blank credential must not count as an identity")
	}
	meta := CapabilitiesFor("meta").Identity
	if got, ok := meta.Value(func(string) string { return " 12345 " }); !ok || got != "12345" {
		t.Fatalf("meta identity = %q ok=%v, want the trimmed page id", got, ok)
	}
	if _, ok := CapabilitiesFor("line").Identity.Value(func(string) string { return "x" }); ok {
		t.Fatal("line declares no identity rule, so Value must report none")
	}
}

// Telegram's avatar is a file id that has to be downloaded and re-hosted, so its
// profile is fetched on every inbound message; every other channel is only asked
// when the display name is missing. The bit that drives that used to be
// `cfg.Platform == "telegram"` written twice in the orchestrator.
func TestAvatarNeedsRehostIsTelegramOnly(t *testing.T) {
	for _, plat := range KnownPlatforms() {
		want := plat == "telegram"
		if got := CapabilitiesFor(plat).AvatarNeedsRehost; got != want {
			t.Errorf("%s AvatarNeedsRehost = %v, want %v", plat, got, want)
		}
	}
}
