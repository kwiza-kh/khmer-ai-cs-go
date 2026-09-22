package rag

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// maxPDFPages bounds the page loop. NumPage() returns the file-declared
// /Root /Pages /Count, so without a ceiling a few bytes of /Count drive up to
// 2^31-1 iterations, each re-walking the page tree (the pinned library keeps no
// resolution cache). Real knowledge-base documents are far below this.
const maxPDFPages = 2000

// maxPDFXrefEntries bounds the cross-reference table the pinned library
// allocates during parsing. readXrefStream takes the /Size integer out of the
// attacker-controlled stream dictionary and immediately runs
// make([]xref, size) — 32 bytes per entry — before reading any stream data, and
// /Index can grow the same table to a file-declared offset. A few hundred bytes
// of PDF can therefore request gigabytes. The library recovers the panic form
// of an impossible size but cannot intercept a runtime allocation failure, so
// the bound has to be enforced before the parse.
const maxPDFXrefEntries = 500000

// checkPDFStructuralBounds rejects a document whose declared cross-reference
// sizing is implausible for the capped input size, before the parser allocates
// anything from it. This is a cheap literal scan of the raw bytes: a real
// knowledge-base PDF has thousands of objects at most, so any /Size or /Index
// value above maxPDFXrefEntries is treated as hostile and the document is
// refused instead of parsed.
func checkPDFStructuralBounds(data []byte) error {
	for _, key := range []string{"/Size", "/Index"} {
		needle := []byte(key)
		off := 0
		for {
			rel := bytes.Index(data[off:], needle)
			if rel < 0 {
				break
			}
			p := off + rel + len(needle)
			if p >= len(data) {
				break
			}
			// Every integer immediately following the key (e.g. "/Index [0 12 ...]")
			// is a candidate allocation size or offset; all of them must fit.
			sawDigit := false
			for p < len(data) {
				c := data[p]
				if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
					p++
					continue
				}
				if c < '0' || c > '9' {
					break
				}
				sawDigit = true
				var v int64
				for p < len(data) && data[p] >= '0' && data[p] <= '9' {
					v = v*10 + int64(data[p]-'0')
					if v > maxPDFXrefEntries {
						return fmt.Errorf("pdf: declared %s %d exceeds limit %d", key, v, maxPDFXrefEntries)
					}
					p++
				}
			}
			if !sawDigit {
				off = p
				if off >= len(data) {
					break
				}
				continue
			}
			off = p
			if off >= len(data) {
				break
			}
		}
	}
	return nil
}

// pdfTextFromBytes extracts concatenated plain text from all pages.
func pdfTextFromBytes(data []byte) (string, error) {
	if err := checkPDFStructuralBounds(data); err != nil {
		return "", err
	}
	reader := bytes.NewReader(data)
	r, err := pdf.NewReader(reader, int64(len(data)))
	if err != nil {
		return "", err
	}
	pages := r.NumPage()
	if pages > maxPDFPages {
		return "", fmt.Errorf("pdf: declared page count %d exceeds limit %d", pages, maxPDFPages)
	}
	var b strings.Builder
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
