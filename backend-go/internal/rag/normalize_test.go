package rag

import "testing"

func TestNormalizeTextKhmerDigits(t *testing.T) {
	got := NormalizeText("តម្លៃ ១២៣ ដុល្លារ")
	want := "តម្លៃ 123 ដុល្លារ"
	if got != want {
		t.Fatalf("Khmer digits not normalised:\n got %q\nwant %q", got, want)
	}
}

func TestNormalizeTextZeroWidth(t *testing.T) {
	got := NormalizeText("ខ្មែរ\u200bជាតិ\u200cភាសា")
	want := "ខ្មែរ ជាតិ ភាសា"
	if got != want {
		t.Fatalf("zero-width characters not mapped to spaces:\n got %q\nwant %q", got, want)
	}
}

func TestNormalizeTextWhitespaceCollapse(t *testing.T) {
	got := NormalizeText("  ជំនួយ\tការ\n\n អតិថិជន  ")
	want := "ជំនួយ ការ អតិថិជន"
	if got != want {
		t.Fatalf("whitespace not collapsed:\n got %q\nwant %q", got, want)
	}
}

func TestNormalizeTextIdempotent(t *testing.T) {
	once := NormalizeText("តម្លៃ ១០\u200bដុល្លារ   ")
	twice := NormalizeText(once)
	if once != twice {
		t.Fatalf("NormalizeText must be idempotent:\n once  %q\n twice %q", once, twice)
	}
}

func TestNormalizeTextEmpty(t *testing.T) {
	if got := NormalizeText(""); got != "" {
		t.Fatalf("empty input must stay empty, got %q", got)
	}
}

func TestLexicalNgramsKhmerTrigrams(t *testing.T) {
	got := lexicalNgrams("ជំនួយការអតិថិជន", 8)
	if len(got) == 0 {
		t.Fatalf("expected Khmer trigrams")
	}
	for _, g := range got {
		if len([]rune(g)) == 3 {
			continue
		}
		t.Fatalf("unexpected n-gram width: %q", g)
	}
	if len(got) > 8 {
		t.Fatalf("cap not honoured: %d", len(got))
	}
}

func TestLexicalNgramsShortRun(t *testing.T) {
	got := lexicalNgrams("កក", 8)
	if len(got) == 1 && got[0] == "កក" {
		return
	}
	t.Fatalf("short run must fall back to the run itself: %v", got)
}

func TestLexicalNgramsMixedScripts(t *testing.T) {
	got := lexicalNgrams("价格表 តម្លៃ", 8)
	foundCJK := false
	foundKhmer := false
	for _, g := range got {
		if g == "价格" || g == "格表" {
			foundCJK = true
		}
		if len([]rune(g)) == 3 && isKhmer([]rune(g)[0]) {
			foundKhmer = true
		}
	}
	if foundCJK == false || foundKhmer == false {
		t.Fatalf("expected both CJK bigrams and Khmer trigrams: %v", got)
	}
}

func TestHasLexicalScript(t *testing.T) {
	if hasLexicalScript("hello world") {
		t.Fatalf("latin script must not trigger the fallback")
	}
	if hasLexicalScript("ជំនួយ") == false {
		t.Fatalf("khmer must trigger the fallback")
	}
	if hasLexicalScript("价格") == false {
		t.Fatalf("CJK must trigger the fallback")
	}
}
