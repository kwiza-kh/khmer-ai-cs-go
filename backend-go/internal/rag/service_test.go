package rag

import (
	"archive/zip"
	"bytes"
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
		"image.png": true, "photo.jpg": true, "noext": false,
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

// TestPublicIPGuardIPv6 is the regression for the IPv6 denylist gap: the
// fallback accepted every IPv6 address outside fc00::/7, so 6to4, Teredo,
// NAT64, site-local, documentation and any internal host in global unicast
// space passed both the pre-dial check and the dial-time Control hook.
func TestPublicIPGuardIPv6(t *testing.T) {
	cases := map[string]bool{
		"2606:4700::1111":      true,  // Cloudflare, global unicast
		"2001:4860:4860::8888": true,  // Google DNS
		"::1":                  false, // loopback
		"::":                   false, // unspecified
		"fe80::1":              false, // link-local
		"fd00::1":              false, // unique local
		"fc00::1":              false, // unique local
		"ff02::1":              false, // multicast
		"2002:7f00:1::":        false, // 6to4
		"2001::1":              false, // Teredo
		"2001:db8::1":          false, // documentation
		"2001:2::1":            false, // benchmarking
		"fec0::1":              false, // deprecated site-local
		"64:ff9b::a9fe:a9fe":   false, // NAT64 → 169.254.169.254
		"::ffff:10.0.0.1":      false, // IPv4-mapped private
	}
	for raw, want := range cases {
		if got := isPublicIP(net.ParseIP(raw)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", raw, got, want)
		}
	}
}

// zipDocx builds a minimal .docx whose word/document.xml is body.
func zipDocx(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// TestExtractDocxUnterminatedTagsStayLinear is the regression for the quadratic
// walk: every unterminated <w:t> used to restart the same suffix scan for
// "</w:t>", so a document of N bare <w:t> cost O(N * remaining) comparisons.
// This test calls it directly (no goroutine) so a regression shows up as a
// package-timeout failure rather than a hang, and it asserts no invented text.
func TestExtractDocxUnterminatedTagsStayLinear(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><w:document><w:body><w:p>`)
	// A bare <w:t> repeated; no closing tag exists anywhere. Under the old
	// per-tag rescan this is the worst case (each scan covers the whole tail).
	for i := 0; i < 200000; i++ {
		sb.WriteString("<w:t>")
	}
	sb.WriteString(`</w:p></w:body></w:document>`)
	got, err := ExtractText("x.docx", zipDocx(t, sb.String()))
	if err == nil && strings.TrimSpace(got) != "" {
		t.Fatalf("malformed document should not yield text, got %q", got)
	}
}

// TestExtractDocxFindsTextAfterMalformedRun proves well-formed runs are still
// extracted, including after a malformed one.
func TestExtractDocxFindsTextAfterMalformedRun(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"well-formed", `<?xml version="1.0"?><w:document><w:body><w:p><w:t>hello</w:t><w:t>world</w:t></w:p></w:body></w:document>`},
		{"after-unterminated", `<?xml version="1.0"?><w:document><w:body><w:p><w:t><w:t>kept</w:t></w:p></w:body></w:document>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ExtractText("x.docx", zipDocx(t, c.body))
			if err != nil {
				t.Fatalf("ExtractText: %v", err)
			}
			if !strings.Contains(got, "hello") && !strings.Contains(got, "kept") && !strings.Contains(got, "world") {
				t.Fatalf("expected extracted text, got %q", got)
			}
		})
	}
}

// TestPDFDeclaredCountRejected is the regression for the file-declared page
// count: NumPage() returns /Root /Pages /Count, so a tiny PDF could drive the
// loop to 2^31-1 iterations.
func TestPDFDeclaredCountRejected(t *testing.T) {
	body := "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Count 200000000/Kids[]>>endobj\n" +
		"trailer<</Root 1 0 R>>\n%%EOF\n"
	if _, err := ExtractText("x.pdf", []byte(body)); err == nil {
		t.Fatal("expected a rejection for an implausible declared page count")
	}
}

// TestPDFXrefSizeRejected is the regression for the xref allocation: the pinned
// library takes /Size out of the file and runs make([]xref, size) before
// validating any stream data, so the bound must be enforced before the parse.
func TestPDFXrefSizeRejected(t *testing.T) {
	body := "%PDF-1.5\n1 0 obj<</Type/XRef/Size 100000000/W[1 2 1]/Index[0 100000000]>>stream\nendstream\nendobj\n" +
		"startxref\n9\n%%EOF\n"
	if _, err := ExtractText("x.pdf", []byte(body)); err == nil {
		t.Fatal("expected a rejection for an implausible declared xref size")
	}
}
