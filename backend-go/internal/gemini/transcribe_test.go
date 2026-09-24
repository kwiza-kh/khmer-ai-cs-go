package gemini

import "testing"

// The transcript script guard decides whether to retry a mis-decoded voice
// note as Khmer. It must accept the three languages the platform serves and
// flag the scripts Gemini has wrongly substituted.
func TestHasUnexpectedScript(t *testing.T) {
	ok := []string{
		"ផលិតផលនេះតម្លៃប៉ុន្មាន", // Khmer
		"多少钱？",              // Chinese
		"How much is it?",   // English
		"Price 100 $ (USD)", // ASCII punctuation/digits
		"ខ្ញុំចង់ទិញ 5 បន្ទះ", // Khmer + digits
		"", // empty
	}
	for _, s := range ok {
		if hasUnexpectedScript(s) {
			t.Errorf("expected acceptable scripts for %q", s)
		}
	}
	bad := []string{
		"ዋጋው ስንት ነው",   // Amharic / Ethiopic — the observed mis-decode
		"ราคาเท่าไหร่", // Thai
		"كم السعر",     // Arabic
		"कितना है",     // Devanagari
	}
	for _, s := range bad {
		if !hasUnexpectedScript(s) {
			t.Errorf("expected unexpected script for %q", s)
		}
	}
}

func TestTranscribePromptAutoDetect(t *testing.T) {
	p := transcribePrompt("")
	if p == "" {
		t.Fatal("prompt must not be empty")
	}
	// The auto prompt must name the candidate languages and forbid substitution.
	for _, want := range []string{"Khmer", "English", "Chinese", "script"} {
		if !contains(p, want) {
			t.Errorf("auto prompt missing %q: %s", want, p)
		}
	}
	// A pinned hint must name exactly that language.
	if got := transcribePrompt("km"); !contains(got, "Khmer") {
		t.Errorf("km prompt must pin Khmer: %s", got)
	}
	if got := transcribePrompt("zh"); !contains(got, "Chinese") {
		t.Errorf("zh prompt must pin Chinese: %s", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
