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
	want := "ជំនួយ ការ\nអតិថិជន"
	if got != want {
		t.Fatalf("whitespace not collapsed:\n got %q\nwant %q", got, want)
	}
}

// Line breaks are structural: ChunkMarkdown parses headings line by line, so
// flattening breaks into spaces collapses a whole document into one "heading"
// and then into one over-long chunk (which the embedding model truncates).
func TestNormalizeTextPreservesLineBreaks(t *testing.T) {
	got := NormalizeText("# Heading\n\nBody line one.\nBody line two.\n")
	want := "# Heading\nBody line one.\nBody line two."
	if got != want {
		t.Fatalf("line breaks must survive normalisation:\n got %q\nwant %q", got, want)
	}
	if n := countLines(got); n != 3 {
		t.Fatalf("expected 3 lines, got %d in %q", n, got)
	}
}

// A run of breaks must collapse to exactly one, and a space abutting a break
// must be dropped, otherwise repeated normalisation would keep changing the
// string and chunk boundaries would drift between ingest and re-ingest.
func TestNormalizeTextBreakRunsCollapse(t *testing.T) {
	got := NormalizeText("a  \n \n\n  b\r\n\r\nc")
	want := "a\nb\nc"
	if got != want {
		t.Fatalf("break runs not collapsed:\n got %q\nwant %q", got, want)
	}
}

func TestNormalizeTextIdempotentWithBreaks(t *testing.T) {
	for _, in := range []string{
		"# H\n\n\n body \n\n\t more  \n",
		"តម្លៃ ១០\u200bដុល្លារ   \n\n ថ្មី",
		"a\nb\nc",
	} {
		once := NormalizeText(in)
		twice := NormalizeText(once)
		if once != twice {
			t.Fatalf("NormalizeText must be idempotent:\n in    %q\n once  %q\n twice %q", in, once, twice)
		}
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
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
