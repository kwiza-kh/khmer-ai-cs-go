// Package rag — document text extraction + SSRF-guarded URL ingestion
// (port of the Rust extract.rs). TXT/MD/CSV read as UTF-8; DOCX unzipped and
// its OOXML walked; PDF via ledongthuc/pdf; HTML→text with script skipping.
package rag

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxUploadBytes — single-file cap (Go rag.MaxUploadBytes = 10 MB).
const MaxUploadBytes = 10 * 1024 * 1024

// AcceptedExtensions maps extension → human-readable format label (UI).
func AcceptedExtensions() map[string]string {
	return map[string]string{
		".txt":  "Plain text",
		".md":   "Markdown",
		".csv":  "CSV",
		".pdf":  "PDF",
		".docx": "Microsoft Word",
		".jpg":  "JPEG image (OCR)",
		".jpeg": "JPEG image (OCR)",
		".png":  "PNG image (OCR)",
		".webp": "WebP image (OCR)",
	}
}

// IsAccepted reports whether the filename's extension is supported.
func IsAccepted(filename string) bool {
	_, ok := AcceptedExtensions()[strings.ToLower(filepath.Ext(filename))]
	return ok
}

// ExtractText pulls plain text from file bytes based on the extension.
func ExtractText(filename string, data []byte) (string, error) {
	if len(data) > MaxUploadBytes {
		return "", fmt.Errorf("file exceeds %d bytes", MaxUploadBytes)
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".txt", ".md", ".csv":
		return string(data), nil
	case ".pdf":
		return extractPDF(data)
	case ".docx":
		return extractDocx(data)
	default:
		return "", fmt.Errorf("unsupported file type: %s (allowed: txt, md, csv, pdf, docx)", filepath.Ext(filename))
	}
}

func extractPDF(data []byte) (string, error) {
	text, err := pdfTextFromBytes(data)
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("pdf contains no extractable text (possibly scanned images)")
	}
	return text, nil
}

// extractDocx walks the OOXML tree: <w:p> paragraphs, <w:t> text,
// <w:br>/<w:tab> breaks.
func extractDocx(data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open docx (unzip): %w", err)
	}
	var docXML []byte
	for _, f := range archive.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("read docx entry: %w", err)
		}
		docXML, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("read document.xml: %w", err)
		}
		break
	}
	if len(docXML) == 0 {
		return "", fmt.Errorf("docx: word/document.xml not found")
	}

	xml := string(docXML)
	var out strings.Builder
	inParagraph := false
	i := 0
	for i < len(xml) {
		if xml[i] != '<' {
			i++
			continue
		}
		closeIdx := strings.IndexByte(xml[i:], '>')
		var close int
		if closeIdx < 0 {
			close = len(xml)
		} else {
			close = i + closeIdx + 1
		}
		tag := xml[i+1 : max(i+1, close-1)]
		nameEnd := len(tag)
		for k, c := range tag {
			if c == ' ' || c == '\t' || c == '\n' || c == '/' || c == '>' {
				nameEnd = k
				break
			}
		}
		local := strings.TrimSpace(tag[:nameEnd])
		switch {
		case local == "w:p" && !strings.HasPrefix(tag, "/"):
			inParagraph = true
		case local == "w:t" && !strings.HasPrefix(tag, "/"):
			contentStart := close
			rel := strings.Index(xml[contentStart:], "</w:t>")
			if rel >= 0 {
				contentEnd := contentStart + rel
				out.WriteString(xml[contentStart:contentEnd])
				i = contentEnd + len("</w:t>")
				continue
			}
			i = close
		case (local == "w:br" || local == "w:tab") && inParagraph:
			out.WriteByte('\n')
			i = close
		default:
			i = close
		}
		if strings.HasPrefix(tag, "/") && local == "w:p" {
			out.WriteByte('\n')
			inParagraph = false
		}
	}

	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("docx contains no extractable text")
	}
	return text, nil
}

// ============================================
// URL ingestion (SSRF-guarded)
// ============================================

// FetchURLContent fetches a public URL (http/https only), enforcing SSRF
// guards: the target must resolve to a public IP and redirects are
// re-validated. Returns (title, text). Body capped at 5 MB.
func FetchURLContent(ctx context.Context, rawURL string) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid url: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", "", fmt.Errorf("only public http(s) URLs are allowed")
	}
	if err := ensurePublicHost(parsed.Hostname()); err != nil {
		return "", "", fmt.Errorf("URL host is not public: %w", err)
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if err := ensurePublicHost(req.URL.Hostname()); err != nil {
				return fmt.Errorf("redirect host is not public")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "KhmerAI-CS-KnowledgeBot/1.0 (+ingest)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024+1))
	if err != nil {
		return "", "", fmt.Errorf("read body: %w", err)
	}
	if len(body) > 5*1024*1024 {
		return "", "", fmt.Errorf("page body exceeds 5 MB")
	}
	title, text := ExtractHTMLText(body)
	return title, text, nil
}

func ensurePublicHost(host string) error {
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve failed: %w", err)
	}
	for _, ip := range ips {
		if isPublicIP(ip) {
			return nil
		}
	}
	return fmt.Errorf("no public address")
}

func isPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if addr.Is4() {
		o := addr.As4()
		switch {
		case o[0] == 0:
			return false
		case o[0] == 100 && o[1]&0xc0 == 0x40: // CGNAT 100.64.0.0/10
			return false
		case o[0] == 192 && (o[1] == 0 || o[1] == 168):
			return false
		case o[0] == 198 && (o[1] == 18 || o[1] == 19):
			return false
		case o[0] == 192 && o[1] == 0 && o[2] == 2: // TEST-NET-1
			return false
		case o[0] == 198 && o[1] == 51 && o[2] == 100: // TEST-NET-2
			return false
		case o[0] == 203 && o[1] == 0 && o[2] == 113: // TEST-NET-3
			return false
		case o[0] >= 224:
			return false
		}
		return true
	}
	return !addr.Is6() || !isUniqueLocalV6(addr)
}

func isUniqueLocalV6(addr netip.Addr) bool {
	if !addr.Is6() {
		return false
	}
	segs := addr.As16()
	return segs[0] == 0xfc || segs[0] == 0xfd
}

// ExtractHTMLText extracts visible text from an HTML page: skips
// script/style/svg blocks, inserts newlines at block boundaries, captures the
// first <title>. Returns (title, text).
func ExtractHTMLText(htmlBytes []byte) (string, string) {
	html := string(htmlBytes)
	var title, out strings.Builder
	inTitle := false
	skipDepth := 0
	runes := []rune(html)
	i := 0
	for i < len(runes) {
		if runes[i] != '<' {
			var text strings.Builder
			for i < len(runes) && runes[i] != '<' {
				text.WriteRune(runes[i])
				i++
			}
			trimmed := strings.TrimSpace(text.String())
			if trimmed != "" && skipDepth == 0 {
				if inTitle {
					if title.Len() == 0 {
						title.WriteString(trimmed)
					}
				} else if out.Len() == 0 || strings.HasSuffix(out.String(), "\n") {
					out.WriteString(trimmed)
				} else {
					out.WriteByte(' ')
					out.WriteString(trimmed)
				}
			}
			continue
		}
		// Tag.
		pos := i
		for pos < len(runes) && runes[pos] != '>' {
			pos++
		}
		var close int
		if pos >= len(runes) {
			close = len(runes)
		} else {
			close = pos + 1
		}
		end := close - 1
		if end < i+1 {
			end = i + 1
		}
		tagRaw := string(runes[i+1 : end])
		isEnd := strings.HasPrefix(tagRaw, "/")
		tag := strings.TrimPrefix(tagRaw, "/")
		if idx := strings.IndexAny(tag, " \t\n>/"); idx >= 0 {
			tag = tag[:idx]
		}
		tag = strings.ToLower(tag)
		isBlock := func(t string) bool {
			switch t {
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6",
				"section", "article", "header", "footer", "nav", "blockquote":
				return true
			}
			return false
		}
		switch {
		case !isEnd && (tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" || tag == "template"):
			skipDepth++
		case isEnd && (tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" || tag == "template"):
			if skipDepth > 0 {
				skipDepth--
			}
		case !isEnd && tag == "title":
			inTitle = true
		case isEnd && tag == "title":
			inTitle = false
		case isBlock(tag) && out.Len() > 0 && !strings.HasSuffix(out.String(), "\n"):
			out.WriteByte('\n')
		}
		i = close
	}
	return strings.TrimSpace(title.String()), strings.TrimSpace(out.String())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// imageExtensions — formats routed through the vision OCR path instead of the
// native text extractors. Gemini accepts these MIME types directly; HEIC is
// absent because the API does not accept it.
var imageExtensions = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

// IsImage reports whether filename should be handled by OCR rather than
// ExtractText.
func IsImage(filename string) bool {
	_, ok := imageExtensions[strings.ToLower(filepath.Ext(filename))]
	return ok
}

// ImageMimeType returns the MIME type to send the vision model for filename.
func ImageMimeType(filename string) string {
	return imageExtensions[strings.ToLower(filepath.Ext(filename))]
}

// LooksLikeBadExtraction reports whether native extraction produced something
// unusable — empty, very short, or littered with U+FFFD replacement characters
// (the classic symptom of a PDF whose embedded Khmer font has no ToUnicode
// map). It is the trigger for the vision-OCR fallback.
func LooksLikeBadExtraction(text string) bool {
	t := strings.TrimSpace(text)
	runes := utf8.RuneCountInString(t)
	if runes < 40 {
		return true
	}
	bad := strings.Count(t, "\uFFFD")
	return bad*20 >= runes
}
