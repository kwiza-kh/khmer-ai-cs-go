package platform

import "testing"

// Every supported language must define every merchant-facing string. A missing
// entry does not fail loudly at runtime — it renders as an empty Telegram
// message, which is the kind of thing nobody notices until a merchant
// complains that the bot went quiet.
func TestMerchantTextsAreComplete(t *testing.T) {
	for lang, dict := range merchantTexts {
		for _, key := range merchantTextKeys {
			if dict[key] == "" {
				t.Errorf("language %q is missing key %q", lang, key)
			}
		}
	}
	// And the reverse: a key present in a dictionary but absent from
	// merchantTextKeys would never be covered by the loop above.
	known := make(map[string]bool, len(merchantTextKeys))
	for _, k := range merchantTextKeys {
		known[k] = true
	}
	for lang, dict := range merchantTexts {
		for key := range dict {
			if !known[key] {
				t.Errorf("language %q defines %q, which is not in merchantTextKeys", lang, key)
			}
		}
	}
}

func TestBotLangNormalizesTelegramTags(t *testing.T) {
	cases := map[string]string{
		"km":       "km",
		"km-KH":    "km",
		"KM":       "km",
		"zh-hans":  "zh",
		"zh-Hant":  "zh",
		"en-US":    "en",
		"en":       "en",
		"":         "en",
		"fr":       "en",
		"  km  ":   "km",
		"km_KH":    "km",
		"zh-Hans-": "zh",
	}
	for in, want := range cases {
		if got := botLang(in); got != want {
			t.Errorf("botLang(%q) = %q, want %q", in, got, want)
		}
	}
}

// An unknown key must not produce an empty message. It falls back to English,
// and then to the key itself so the gap is visible in the chat.
func TestMerchantTextFallsBackRatherThanEmptying(t *testing.T) {
	if got := merchantText("km", "no_such_key"); got != "no_such_key" {
		t.Errorf("unknown key: got %q, want the key echoed back", got)
	}
	if got := merchantText("km", "btn_takeover"); got == "" {
		t.Error("known key returned empty for km")
	}
	// A key defined only in English would still resolve for other languages via
	// the English fallback; assert the mechanism using a real key.
	if got := merchantText("de", "btn_resolve"); got != merchantTexts["en"]["btn_resolve"] {
		t.Errorf("unsupported language: got %q, want the English string", got)
	}
}
