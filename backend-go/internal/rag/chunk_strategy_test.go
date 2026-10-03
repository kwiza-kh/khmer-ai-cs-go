package rag

import (
	"strings"
	"testing"
)

// Chunking is chosen per document, so each strategy's promise is pinned here:
// the ceiling is respected, nothing is lost, and the structure each one claims to
// preserve really is preserved.

func TestFixedSizeRespectsTheCeilingAndKeepsEverything(t *testing.T) {
	text := strings.Repeat("សូមស្វាគមន៍ ", 200) // Khmer, no ASCII word breaks
	chunks := FixedSizeStrategy{}.Split(text, ChunkOptions{MaxRunes: 120})
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if runeLen(c.Text) > 120 {
			t.Fatalf("chunk %d is %d runes, over the ceiling", i, runeLen(c.Text))
		}
		if c.Index != i {
			t.Fatalf("chunk %d has index %d", i, c.Index)
		}
	}
	// Nothing but whitespace may be dropped.
	joined := strings.Join(chunkTexts(chunks), "")
	for _, r := range []rune(text) {
		if !strings.ContainsRune(joined, r) && r != ' ' {
			t.Fatalf("character %q was lost", string(r))
		}
	}
}

func TestFixedSizeShortTextIsOneChunk(t *testing.T) {
	chunks := FixedSizeStrategy{}.Split("hello", ChunkOptions{MaxRunes: 100})
	if len(chunks) != 1 || chunks[0].Text != "hello" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if got := (FixedSizeStrategy{}).Split("   ", ChunkOptions{MaxRunes: 10}); len(got) != 0 {
		t.Fatalf("blank text must produce nothing, got %+v", got)
	}
}

func TestRecursivePrefersParagraphs(t *testing.T) {
	para := strings.Repeat("a", 50)
	text := para + "\n\n" + para + "\n\n" + para
	chunks := RecursiveStrategy{}.Split(text, ChunkOptions{MaxRunes: 60})
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d (%v), want one per paragraph", len(chunks), chunkTexts(chunks))
	}
	for _, c := range chunks {
		if c.Text != para {
			t.Fatalf("paragraph was altered: %q", c.Text)
		}
	}
}

func TestRecursiveSplitsAParagraphTooBigForOneChunk(t *testing.T) {
	text := strings.Repeat("x", 500)
	chunks := RecursiveStrategy{}.Split(text, ChunkOptions{MaxRunes: 100})
	if len(chunks) < 5 {
		t.Fatalf("chunks = %d, want the oversized paragraph to be cut", len(chunks))
	}
	for i, c := range chunks {
		if runeLen(c.Text) > 100 {
			t.Fatalf("chunk %d over the ceiling: %d", i, runeLen(c.Text))
		}
	}
}

func TestMarkdownKeepsHeadingsWithTheirBody(t *testing.T) {
	text := "# Title\nfirst body\n## Second\nsecond body"
	chunks := MarkdownStrategy{}.Split(text, ChunkOptions{MaxRunes: 500})
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d (%v)", len(chunks), chunkTexts(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "# Title") || !strings.Contains(chunks[0].Text, "first body") {
		t.Fatalf("first chunk = %q", chunks[0].Text)
	}
	if !strings.HasPrefix(chunks[1].Text, "## Second") || !strings.Contains(chunks[1].Text, "second body") {
		t.Fatalf("second chunk = %q", chunks[1].Text)
	}
}

func TestMarkdownFallsBackForALongSection(t *testing.T) {
	text := "# Title\n" + strings.Repeat("y", 400)
	chunks := MarkdownStrategy{}.Split(text, ChunkOptions{MaxRunes: 100})
	if len(chunks) < 4 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].Text, "# Title") {
		t.Fatalf("the heading must stay with the first piece, got %q", chunks[0].Text)
	}
}

// An unknown name must never fail an ingest; it degrades to the strategy that
// copes with the most shapes.
func TestChunkStrategyByNameDefaultsToRecursive(t *testing.T) {
	for name, want := range map[string]string{
		"recursive":  "recursive",
		"":           "recursive",
		"markdowm":   "recursive",
		"fixed":      "fixed_size",
		"fixed_size": "fixed_size",
		"FIXED":      "fixed_size",
		"markdown":   "markdown",
		"md":         "markdown",
	} {
		if got := ChunkStrategyByName(name).Name(); got != want {
			t.Errorf("ChunkStrategyByName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestChunkOptionsAreNormalised(t *testing.T) {
	o := normalise(ChunkOptions{})
	if o.MaxRunes != 1200 {
		t.Fatalf("default ceiling = %d", o.MaxRunes)
	}
	// An overlap at or beyond the ceiling must not stall the walk.
	o = normalise(ChunkOptions{MaxRunes: 100, OverlapRunes: 500})
	if o.OverlapRunes >= o.MaxRunes {
		t.Fatalf("overlap = %d, ceiling = %d", o.OverlapRunes, o.MaxRunes)
	}
	chunks := FixedSizeStrategy{}.Split(strings.Repeat("z", 400), ChunkOptions{MaxRunes: 100, OverlapRunes: 500})
	if len(chunks) == 0 {
		t.Fatal("overlap must not prevent progress")
	}
}

func chunkTexts(chunks []Chunk) []string {
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, c.Text)
	}
	return out
}
