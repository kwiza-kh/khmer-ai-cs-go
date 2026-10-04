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
	"syscall"
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
		// Representation-amplification guard: a <=10MB deflate stream can
		// expand ~1000:1, so the decompressed entry is capped before reading
		// (the declared size is untrusted, hence the LimitReader backstop).
		const maxDecompressed = 32 << 20 // 32 MB of XML is far above any real document
		if f.UncompressedSize64 > maxDecompressed {
			return "", fmt.Errorf("docx: document.xml too large (%d bytes declared)", f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("read docx entry: %w", err)
		}
		docXML, err = io.ReadAll(io.LimitReader(rc, maxDecompressed+1))
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("read document.xml: %w", err)
		}
		if len(docXML) > maxDecompressed {
			return "", fmt.Errorf("docx: document.xml exceeds %d bytes", maxDecompressed)
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
	// wtClose caches where the text run ends, so an unterminated <w:t> does
	// not make every later tag repeat the same suffix scan (that made the walk
	// quadratic in the 32 MB cap: ~30 KB of bare <w:t> cost ~1e14 comparisons).
	// -1 means "no </w:t> anywhere at or after this point".
	wtClose := -1
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
			// i MUST advance here. Without it the loop re-parses the same
			// opening <w:p> forever, which hangs the request goroutine for any
			// document that contains a paragraph — i.e. every real .docx.
			inParagraph = true
			i = close
		case local == "w:t" && !strings.HasPrefix(tag, "/"):
			contentStart := close
			if wtClose < contentStart {
				rel := strings.Index(xml[contentStart:], "</w:t>")
				if rel < 0 {
					// No terminator remains: the rest of the document has no
					// text run, so stop instead of re-scanning per tag.
					i = len(xml)
					break
				}
				wtClose = contentStart + rel
			}
			out.WriteString(xml[contentStart:wtClose])
			i = wtClose + len("</w:t>")
			continue
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

// publicFetchTransport is built ONCE and shared.
//
// It used to be constructed per call, which leaked: a fresh http.Transport has
// a zero IdleConnTimeout, and zero means "no limit" rather than "immediate
// close", so its idle connection was never reaped, the transport's readLoop and
// writeLoop goroutines stayed alive, and nothing calls CloseIdleConnections.
// Every downloaded media item and fetched URL therefore left a goroutine pair
// and a socket behind. Measured against a keep-alive server: a shared transport
// held a flat 5 goroutines over 30 fetches, while a per-call transport went
// 95 -> 152 -> 302 -> 452 and stayed there after idle + GC.
//
// Pooling does not weaken the guard: it is the dialer's Control hook and the
// redirect policy that refuse non-public targets, not the client's identity.
var publicFetchTransport = func() *http.Transport {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("dial address: %w", err)
			}
			ip := net.ParseIP(host)
			if ip == nil || !isPublicIP(ip) {
				return fmt.Errorf("dial target is not a public address")
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext:         dialer.DialContext,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
	}
}()

// publicFetchClient is safe to share: http.Client is goroutine-safe, and every
// guard above is per-request.
var publicFetchClient = &http.Client{
	Timeout:   20 * time.Second,
	Transport: publicFetchTransport,
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

// PublicFetchClient returns an HTTP client whose dialer refuses any address
// that is not globally routable, plus a redirect policy that re-validates each
// hop and caps the chain. Callers that fetch an attacker-supplied URL must use
// it: a plain http.Client follows up to 10 redirects with no address check.
//
// This is the same guard FetchURLContent applies, exported so the platform
// media path (which fetches a webhook-supplied source_url) cannot drift from it.
func PublicFetchClient() *http.Client { return publicFetchClient }

// ValidateFetchURL enforces the scheme allowlist and the pre-dial public-host
// check on a caller-supplied URL, returning the parsed URL for use.
func ValidateFetchURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, fmt.Errorf("only public http(s) URLs are allowed")
	}
	if err := ensurePublicHost(parsed.Hostname()); err != nil {
		return nil, fmt.Errorf("URL host is not public: %w", err)
	}
	return parsed, nil
}

// FetchURLContent fetches a public URL (http/https only), enforcing SSRF
// guards: the target must resolve to a public IP and redirects are
// re-validated. Returns (title, text). Body capped at 5 MB.
func FetchURLContent(ctx context.Context, rawURL string) (string, string, error) {
	parsed, err := ValidateFetchURL(rawURL)
	if err != nil {
		return "", "", err
	}

	// The dialer Control hook closes the check-to-dial gap: net/http re-
	// resolves DNS at connection time, so validating the hostname up front is
	// not enough. Control runs with the address actually being dialed (after
	// resolution) and rejects any private/loopback target — defeating DNS
	// rebinding between the guard's LookupIP and the dial, and the mixed
	// public/private A-record variant (Happy Eyeballs can no longer land on
	// the private member unnoticed).
	client := PublicFetchClient()
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
	// IPv6 is an ALLOWLIST, not a denylist: the previous fallback accepted every
	// IPv6 address outside fc00::/7, so 6to4 (2002::/16), Teredo (2001::/32),
	// NAT64 (64:ff9b::/96), deprecated site-local (fec0::/10), documentation
	// (2001:db8::/32) and any internal host in global unicast space all passed
	// both the pre-dial check and the dial-time Control hook. Only addresses the
	// IANA special-purpose registry marks globally reachable are accepted here.
	return isGloballyRoutableV6(addr)
}

// isGloballyRoutableV6 accepts only 2000::/3 (the global unicast range that the
// special-purpose registry delegates for public routing) while excluding the
// embedded/transitional ranges inside it that can carry a non-public
// destination: 2001:db8::/32 documentation, 2001::/32 Teredo, 2001:2::/48
// benchmarking, 2002::/16 6to4, and 64:ff9b::/96 NAT64 (which maps IPv4).
func isGloballyRoutableV6(addr netip.Addr) bool {
	if !addr.Is6() {
		return false
	}
	segs := addr.As16()
	g0 := uint16(segs[0])<<8 | uint16(segs[1]) // first 16-bit group
	g1 := uint16(segs[2])<<8 | uint16(segs[3]) // second 16-bit group
	// 2000::/3 → first three bits 001, i.e. g0 in 0x2000..0x3fff.
	if g0 < 0x2000 || g0 > 0x3fff {
		return false
	}
	switch g0 {
	case 0x2001:
		switch g1 {
		case 0x0db8: // 2001:db8::/32 documentation
			return false
		case 0x0000: // 2001::/32 Teredo
			return false
		case 0x0002: // 2001:2::/48 benchmarking
			return false
		}
	case 0x2002: // 2002::/16 6to4
		return false
	}
	// 64:ff9b::/96 NAT64 — outside 2000::/3, but checked explicitly so the
	// reason is recorded if the range ever moves.
	if g0 == 0x0064 && g1 == 0xff9b {
		return false
	}
	return true
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
