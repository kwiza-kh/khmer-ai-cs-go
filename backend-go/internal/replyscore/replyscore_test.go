package replyscore

import (
	"strings"
	"testing"
)

// The rubric is what every reply comparison is scored with, so its rules are pinned
// here: a change that loosens one of them would silently turn a worse model into an
// equal one.

func TestMustIncludeAcceptsAnyAlternativeSpelling(t *testing.T) {
	c := Case{MustInclude: []string{"30 kg|30kg", "WANFANG"}}
	if got := MissingFacts(c, "We stock 30kg and 50kg. WANFANG INSULATION."); len(got) != 0 {
		t.Fatalf("missing = %v, want none: one spelling of each group is present", got)
	}
	if got := MissingFacts(c, "We stock thirty kilograms."); len(got) != 2 {
		t.Fatalf("missing = %v, want both groups reported", got)
	}
}

func TestForbiddenLiteralsAreCaseInsensitive(t *testing.T) {
	c := Case{MustNotContain: []string{"SVN-|Source 1"}}
	got := Leaked(c, "see svn-023-export-customs for the rule")
	if len(got) != 1 || got[0] != "SVN-" {
		t.Fatalf("leaked = %v, want the matched alternative %q", got, "SVN-")
	}
	if leaked := Leaked(c, "prices are on the sheet"); len(leaked) != 0 {
		t.Fatalf("leaked = %v, want none", leaked)
	}
}

func TestFormatProblemsCarryTheHardWonRules(t *testing.T) {
	km := Case{Language: "km"}
	reply := func(s string) []string { return FormatProblems(km, s) }

	khmer := strings.Repeat("ខ", 40)
	if got := reply(khmer); len(got) != 0 {
		t.Errorf("a Khmer reply was failed: %v", got)
	}
	if got := reply(""); len(got) == 0 || !strings.Contains(got[0], "empty") {
		t.Errorf("empty reply = %v, want it reported", got)
	}
	if got := reply(khmer + "\u200b"); len(got) == 0 {
		t.Error("a zero-width space must fail")
	}
	if got := reply(khmer + " **bold**"); len(got) == 0 {
		t.Error("markdown must fail")
	}
	if got := reply(khmer + " 已为您转接"); len(got) == 0 {
		t.Error("a Chinese handoff sentence in a Khmer reply must fail")
	}
	if got := reply(khmer + " តម្លៃ ១០ ដុល្លារ"); len(got) == 0 {
		t.Error("Khmer numerals must fail (prices are recorded in ASCII digits)")
	}
	// The floor: a reply that came back in English. A Latin-heavy but genuinely
	// Khmer reply (a legal name and address) must still pass.
	english := strings.Repeat("The price is 45 dollars per cubic metre. ", 3)
	if got := reply(english); len(got) == 0 {
		t.Error("an English reply to a Khmer question must fail the Khmer floor")
	}
	latinHeavy := "WANFANG INSULATION PACKAGING MATERIAL CO., LTD, #777, Road No. 2, Phnom Penh — " + khmer
	if got := reply(latinHeavy); len(got) != 0 {
		t.Errorf("a Khmer reply carrying a Latin legal name was failed: %v", got)
	}
	// English cases have no Khmer floor: an English question may be answered in English.
	if got := FormatProblems(Case{Language: "en"}, english); len(got) != 0 {
		t.Errorf("an English reply to an English question was failed: %v", got)
	}
}

func TestKhmerLetterRatioIgnoresNonLetters(t *testing.T) {
	if got := KhmerLetterRatio("ខខខខ ABC 12345 !!!"); got != 4.0/7.0 {
		t.Errorf("ratio = %v, want 4/7 (4 Khmer letters, 3 Latin)", got)
	}
	if got := KhmerLetterRatio("12345 ..."); got != 0 {
		t.Errorf("ratio with no letters = %v, want 0", got)
	}
	if got := KhmerLetters("ខខ 1"); got != 2 {
		t.Errorf("KhmerLetters = %d, want 2", got)
	}
}
