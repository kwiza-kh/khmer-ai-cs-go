package rag

import (
	"strings"
	"testing"
)

func TestDiversifyByDoc(t *testing.T) {
	in := []SearchChunk{
		{ChunkID: 1, DocID: 1},
		{ChunkID: 2, DocID: 1},
		{ChunkID: 3, DocID: 1},
		{ChunkID: 4, DocID: 2},
		{ChunkID: 5, DocID: 2},
		{ChunkID: 6, DocID: 3},
	}
	out := diversifyByDoc(in, 2)
	if len(out) != 5 {
		t.Fatalf("len = %d, want 5", len(out))
	}
	want := []int32{1, 2, 4, 5, 6}
	for i := range want {
		if out[i].ChunkID != want[i] {
			t.Fatalf("position %d = chunk %d, want %d", i, out[i].ChunkID, want[i])
		}
	}
	if len(diversifyByDoc(in, 0)) != len(in) {
		t.Fatal("maxPerDoc <= 0 must be a no-op")
	}
}

func TestDefaultTopKEnvOverride(t *testing.T) {
	t.Setenv("RAG_TOP_K", "7")
	if got := envI("RAG_TOP_K", int(DefaultTopK)); got != 7 {
		t.Fatalf("envI RAG_TOP_K = %d, want 7", got)
	}
	if got := envI("RAG_TOP_K_UNSET", int(DefaultTopK)); got != int(DefaultTopK) {
		t.Fatalf("fallback = %d, want %d", got, DefaultTopK)
	}
}

func TestGroundSourceLimitEnv(t *testing.T) {
	t.Setenv("RAG_SOURCE_LIMIT_RUNES", "450")
	t.Setenv("RAG_TABLE_LIMIT_RUNES", "1200")
	if got := groundSourceLimit("plain prose content"); got != 450 {
		t.Fatalf("prose limit = %d, want 450", got)
	}
	if got := groundSourceLimit("| a | b |\n| 1 | 2 |"); got != 1200 {
		t.Fatalf("table limit = %d, want 1200", got)
	}
}

// TestGroundingExcerptKeepsAListWhole — the production failure this exists for.
// In the FAQ document the product-line answer begins at rune 695 of a 951-rune
// chunk and runs to ~915, with its fifth item (EPS-R) at 876 — past the 800-rune
// per-source allowance. A hard cut handed the model a claim of five lines, four
// lines and no sign that anything was missing, so it answered "5 types" and
// listed four (measured 2026-09-29). The document's Q&A entries are separated by
// a SINGLE newline, which is why the cut must be allowed to finish the line.
func TestGroundingExcerptKeepsAListWhole(t *testing.T) {
	pad := strings.Repeat("ក", 695)
	question := "\n**ស9. តើអ្នកមានផលិតផលប៉ុន្មាន្រភេទ?**\n"
	answer := "មាន 5 បន្ទាត់៖ " + strings.Repeat("EPS-X · ", 20) + "EPS-R (គ្រាប់ឆ្វ)។\n"
	next := "**ស10. តើអ្នកមានថ្នាក់ធន់ភ្លើងទេ?**\nមាន។\n"

	got := groundingExcerpt(pad+question+answer+next, 800, groundExcerptSlack)
	if !strings.Contains(got, "EPS-R") {
		t.Fatalf("the excerpt lost the list's fifth item (len=%d)", len([]rune(got)))
	}
	if !strings.Contains(got, "បន្ទាត់") {
		t.Fatalf("the excerpt lost the sentence it has to be complete against")
	}
	if strings.Contains(got, "ស10") {
		t.Errorf("the cut ran past the line it was supposed to stop at")
	}
	if strings.Contains(got, truncationMark) {
		t.Errorf("a cut at a line boundary is not a truncation to warn about")
	}
	if n := len([]rune(got)); n > 800+groundExcerptSlack {
		t.Errorf("overshoot went past the slack: %d runes", n)
	}

	// Nothing to cut at within reach: retreat to a whole line, and SAY the
	// excerpt is short — ending mid-list silently is the original bug.
	lines := ""
	for i := 0; i < 6; i++ {
		lines += strings.Repeat("ក", 50) + "\n"
	}
	retreated := groundingExcerpt(lines, 120, 0)
	if !strings.HasSuffix(retreated, truncationMark) {
		t.Errorf("an early cut must be marked, at the end: %q", retreated)
	}
	if !strings.HasSuffix(strings.TrimSuffix(retreated, truncationMark), "\n") {
		t.Errorf("an early cut must still end on a line boundary: %q", retreated)
	}

	// No boundary at all in range: hard cut, still marked, marker at the end.
	blob := strings.Repeat("ក", 400)
	hard := groundingExcerpt(blob, 100, 0)
	if !strings.HasSuffix(hard, truncationMark) {
		t.Errorf("a hard cut must be marked: %q", truncateRunes(hard, 60))
	}
	if n := len([]rune(hard)); n > 100+len([]rune(truncationMark)) {
		t.Errorf("marker blew the budget: %d runes", n)
	}

	// Under the allowance nothing is touched, and no marker appears.
	if got := groundingExcerpt(answer, 800, 0); got != answer {
		t.Errorf("a short source was rewritten: %q", got)
	}

	// room <= 0 means "nothing fits", never "no limit".
	if got := groundingExcerpt("anything at all", 0, 0); got != "" {
		t.Errorf("room=0 must yield an empty excerpt, got %q", got)
	}
	if got := truncateRunes("abcdef", 0); got != "" {
		t.Errorf("truncateRunes(n<=0) must be empty (and must not panic), got %q", got)
	}
}
