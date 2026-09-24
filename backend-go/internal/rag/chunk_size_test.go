package rag

import (
	"strings"
	"testing"
)

// maxSafeChunkRunes bounds a single chunk well below the embedding model's
// input window. 1800 runes is TableChunkSize (the atomic table budget); the
// slack above it absorbs the heading prefix ChunkMarkdown glues on.
const maxSafeChunkRunes = 2500

// TestChunkMarkdownSplitsLargeDocument is the regression guard for the bug
// where every multi-line document chunked to exactly ONE piece: NormalizeText
// flattened line breaks, so the whole file looked like a single '#' heading and
// was emitted verbatim as one chunk. That chunk then blew past the embedding
// window and was silently truncated to its opening characters, leaving the rest
// of the document unreachable by vector search.
func TestChunkMarkdownSplitsLargeDocument(t *testing.T) {
	var b strings.Builder
	b.WriteString("# ឯកសារសាកល្បង — Document Title\n")
	b.WriteString("> កូដឯកសារ: SVN-999 · ធ្វើបច្ចុប្បន្នភាព: 2026-09-15\n")
	b.WriteString("> ប្រធានបទ: test\n\n")
	section := strings.Repeat("នេះជាប្រយោគសាកល្បងសម្រាប់ការធ្វើតេស្តការបែងចែកជាកំណាត់។ ", 20)
	for i := 0; i < 8; i++ {
		b.WriteString("## ផ្នែកទី ")
		b.WriteByte(byte('1' + i))
		b.WriteByte('\n')
		b.WriteString(section)
		b.WriteString("\n\n")
	}
	raw := b.String()

	normalized := NormalizeText(raw)
	if !strings.Contains(normalized, "\n## ") {
		t.Fatalf("normalisation destroyed the markdown structure:\n%q", truncateRunes(normalized, 200))
	}

	chunks := ChunkMarkdown(normalized)
	if len(chunks) < 8 {
		t.Fatalf("a %d-rune document with 8 sections produced only %d chunk(s) — "+
			"heading split is broken", len([]rune(normalized)), len(chunks))
	}
	assertChunkBounds(t, chunks)

	// No content may be dropped: the concatenation of the chunks must contain
	// every section heading and the whole body.
	for i := 0; i < 8; i++ {
		want := "## ផ្នែកទី " + string(rune('1'+i))
		if !containsChunk(chunks, want) {
			t.Fatalf("heading %q vanished from the chunk set", want)
		}
	}
	total := 0
	for _, c := range chunks {
		total += len([]rune(c))
	}
	if total < len([]rune(normalized)) {
		t.Fatalf("chunks lost content: %d chunk runes < %d document runes", total, len([]rune(normalized)))
	}
}

// TestChunkMarkdownTableStaysAtomicButBounded — a wide markdown table is kept
// in one chunk so rows are not split, but a table larger than TableChunkSize
// must still be sliced rather than emitted whole.
func TestChunkMarkdownTableStaysAtomicButBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Price table\n\n## តារាងតម្លៃ\n")
	b.WriteString("| ដង់ស៊ីតេ | តម្លៃ |\n|---|---|\n")
	for i := 0; i < 300; i++ {
		b.WriteString("| 10 kg/m³ row description padding text | $32.00 |\n")
	}
	chunks := ChunkMarkdown(NormalizeText(b.String()))
	if len(chunks) < 2 {
		t.Fatalf("an oversized table must be sliced, got %d chunk(s)", len(chunks))
	}
	assertChunkBounds(t, chunks)
}

// TestChunkTextNeverExceedsEmbeddingBudget applies the bound to plain prose
// without any headings at all (the ChunkText fallback path).
func TestChunkTextNeverExceedsEmbeddingBudget(t *testing.T) {
	text := strings.Repeat("ពាក្យសាកល្បង ", 3000)
	chunks := ChunkText(NormalizeText(text))
	if len(chunks) < 2 {
		t.Fatalf("long headingless prose produced %d chunk(s)", len(chunks))
	}
	assertChunkBounds(t, chunks)
}

func assertChunkBounds(t *testing.T, chunks []string) {
	t.Helper()
	for i, c := range chunks {
		if n := len([]rune(c)); n > maxSafeChunkRunes {
			t.Fatalf("chunk %d is %d runes (limit %d) — it would be truncated by the "+
				"embedding model:\n%.120s", i, n, maxSafeChunkRunes, c)
		}
		if strings.TrimSpace(c) == "" {
			t.Fatalf("chunk %d is empty", i)
		}
	}
}

func containsChunk(chunks []string, needle string) bool {
	for _, c := range chunks {
		if strings.Contains(c, needle) {
			return true
		}
	}
	return false
}
