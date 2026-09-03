package rag

import (
	"net"
	"strings"
	"testing"
)

func TestChunkTextBasic(t *testing.T) {
	text := strings.Repeat("a", 2500)
	chunks := ChunkText(text)
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c)) > DefaultChunkSize {
			t.Fatalf("chunk exceeds window size")
		}
	}
	if len([]rune(chunks[0])) != DefaultChunkSize {
		t.Fatalf("first chunk must be exactly the window size")
	}
}

func TestChunkTextSmall(t *testing.T) {
	got := ChunkText("hello")
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("small text must stay whole: %v", got)
	}
}

func TestChunkTextOverlap(t *testing.T) {
	text := strings.Repeat("x", 1500)
	chunks := ChunkText(text)
	if len(chunks) < 2 {
		t.Fatalf("expected overlapping chunks, got %d", len(chunks))
	}
	if len([]rune(chunks[0])) != DefaultChunkSize {
		t.Fatalf("first chunk must be the window size")
	}
}

func TestChunkMarkdownKeepsHeadingsGlued(t *testing.T) {
	md := "# Intro\nhello\n\n## Pricing\nhow much does it cost"
	chunks := ChunkMarkdown(md)
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(chunks))
	}
	hasIntro, hasPricing := false, false
	for _, c := range chunks {
		if strings.HasPrefix(c, "# Intro") {
			hasIntro = true
		}
		if strings.HasPrefix(c, "## Pricing") {
			hasPricing = true
		}
	}
	if !hasIntro || !hasPricing {
		t.Fatalf("heading prefixes lost: %v", chunks)
	}
}

func TestChunkMarkdownFallsBackWithoutHeadings(t *testing.T) {
	got := ChunkMarkdown("plain text")
	if len(got) != 1 || got[0] != "plain text" {
		t.Fatalf("no-heading text must stay whole: %v", got)
	}
}

func TestCandidateLimitCapping(t *testing.T) {
	if got := searchCandidateLimit(5); got != 20 {
		t.Fatalf("limit(5) = %d, want 20", got)
	}
	if got := searchCandidateLimit(100); got != 40 {
		t.Fatalf("limit(100) = %d, want 40", got)
	}
}

func TestRRFFusionPrioritizesSharedChunks(t *testing.T) {
	sim := 0.8
	dense := []SearchChunk{{ChunkID: 1, DocID: 10, Content: "a", Title: "t", Similarity: 0.8, DenseSim: &sim}}
	lexical := []SearchChunk{{ChunkID: 1, DocID: 10, Content: "a", Title: "t", Similarity: 0.7}}
	fused := fuseSearchResults(dense, lexical, nil)
	if len(fused) != 1 {
		t.Fatalf("shared chunk must fuse to one, got %d", len(fused))
	}
	if fused[0].DocID != 10 {
		t.Fatalf("wrong doc fused")
	}
	if fused[0].Similarity != 0.8 {
		t.Fatalf("best similarity must win")
	}
	if fused[0].DenseSim == nil || *fused[0].DenseSim != 0.8 {
		t.Fatalf("dense_sim lost in fusion")
	}
}

func TestAcceptedExtensionSet(t *testing.T) {
	cases := map[string]bool{
		"README.md": true, "report.PDF": true, "data.docx": true,
		"image.png": false, "noext": false,
	}
	for name, want := range cases {
		if got := IsAccepted(name); got != want {
			t.Errorf("IsAccepted(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPlainTextFormats(t *testing.T) {
	if got, _ := ExtractText("a.txt", []byte("hello world")); got != "hello world" {
		t.Errorf("txt extraction broken")
	}
	if got, _ := ExtractText("b.md", []byte("# Title\nbody")); got != "# Title\nbody" {
		t.Errorf("md extraction broken")
	}
	if got, _ := ExtractText("c.csv", []byte("a,b\n1,2")); got != "a,b\n1,2" {
		t.Errorf("csv extraction broken")
	}
	if _, err := ExtractText("x.png", []byte("xx")); err == nil {
		t.Errorf("png must be rejected")
	}
}

func TestHTMLTextExtractionSkipsScripts(t *testing.T) {
	html := []byte("<html><head><title>My Site</title></head><body><h1>Hi</h1><script>alert(1)</script><p>Real content</p></body></html>")
	title, text := ExtractHTMLText(html)
	if title != "My Site" {
		t.Errorf("title = %q, want My Site", title)
	}
	if !strings.Contains(text, "Hi") || !strings.Contains(text, "Real content") {
		t.Errorf("visible text lost: %q", text)
	}
	if strings.Contains(text, "alert") {
		t.Errorf("script content leaked into text")
	}
}

func TestPublicIPGuard(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":         true,
		"127.0.0.1":       false,
		"192.168.1.1":     false,
		"10.0.0.1":        false,
		"169.254.169.254": false,
	}
	for raw, want := range cases {
		if got := isPublicIP(net.ParseIP(raw)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestCJKBigrams(t *testing.T) {
	got := cjkBigrams("价格表查询", 8)
	if len(got) == 0 {
		t.Fatal("bigrams must be produced for CJK text")
	}
	if got[0] != "价格" {
		t.Fatalf("first bigram = %s, want 价格", got[0])
	}
	if len(cjkBigrams("no cjk here", 8)) != 0 {
		t.Fatal("latin text must produce no bigrams")
	}
}
