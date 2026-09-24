// Package rag — text canonicalisation shared by ingestion and retrieval.
//
// Khmer is written without spaces between words, and the same sentence can be
// encoded with different Unicode sequences depending on the input method
// (combining-mark order, zero-width spaces injected for line breaking, Khmer
// vs Arabic digits). Every match path — the n-gram ILIKE fallback today, the
// segmented tsvector path planned next — compares character sequences, so
// ingestion and querying must agree on one canonical form.
package rag

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizeText canonicalises text for indexing and matching. It is
// idempotent, so applying it on both the write and the read path is safe:
//
//   - NFC composition — Khmer combining marks (vowels, signs, coeng) can be
//     stored in different orders; composed form makes byte-level comparisons
//     agree.
//   - Khmer digits U+17E0–U+17E9 → ASCII 0-9. Merchants and customers mix
//     scripts for prices, quantities and SKUs.
//   - Zero-width characters (U+200B ZWSP, U+200C ZWNJ, U+200D ZWJ, U+FEFF) and
//     NBSP → a plain space. Khmer uses ZWSP as an optional word boundary; as a
//     space it is preserved as a boundary for n-grams and future segmentation
//     instead of gluing two words together.
//   - Runs of spaces/tabs collapse to one space; runs of line breaks collapse
//     to one '\n'; space abutting a break is dropped; ends are trimmed.
//
// Line breaks are deliberately PRESERVED. They are structural, not cosmetic:
// ChunkMarkdown splits documents on headings, and the heading parse works line
// by line. Collapsing breaks into spaces turned every multi-line document into
// a single line whose leading '#' made the whole file look like one heading,
// so ChunkMarkdown emitted the entire document as one chunk. That chunk then
// exceeded the embedding model's input window and was silently truncated to
// its first ~2k characters. Small documents hid this (a sub-1000-rune body
// chunks to one piece either way); it only surfaces on real documents.
// SegmentForSearch splits on strings.Fields, so it is unaffected by breaks.
func NormalizeText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	pendingBreak := false
	wrote := false
	for _, r := range s {
		switch {
		case r >= '០' && r <= '៩': // Khmer digits U+17E0..U+17E9
			r = '0' + (r - '០')
		case r == '\u200B' || r == '\u200C' || r == '\u200D' || r == '\uFEFF' || r == '\u00A0':
			r = ' '
		}
		if r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			if wrote {
				pendingBreak = true
				pendingSpace = false
			}
			continue
		}
		if r == ' ' || r == '\t' {
			if wrote && !pendingBreak {
				pendingSpace = true
			}
			continue
		}
		if pendingBreak {
			b.WriteByte('\n')
			pendingBreak = false
			pendingSpace = false
		} else if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
		wrote = true
	}
	return norm.NFC.String(b.String())
}

func isKhmer(c rune) bool {
	return c >= 0x1780 && c <= 0x17FF
}

func hasKhmer(s string) bool {
	for _, c := range s {
		if isKhmer(c) {
			return true
		}
	}
	return false
}

// hasLexicalScript reports whether s contains a script the 'simple' tsvector
// config cannot segment (CJK or Khmer). Those queries need the n-gram fallback;
// space-delimited scripts don't.
func hasLexicalScript(s string) bool {
	return hasCJK(s) || hasKhmer(s)
}

// lexicalNgrams — character n-grams for the ILIKE fallback path, per script
// run: CJK keeps bigrams (historical behaviour); Khmer uses trigrams, because
// pg_trgm only extracts trigrams from ILIKE patterns of 3+ characters — a
// 2-char pattern cannot use the GIN index. Short runs fall back to the run.
func lexicalNgrams(s string, capN int) []string {
	var out []string
	seen := make(map[string]bool)
	var run []rune
	cjkRun := false
	add := func(gram string) {
		if len(out) >= capN || seen[gram] {
			return
		}
		seen[gram] = true
		out = append(out, gram)
	}
	flush := func() {
		if len(run) == 0 {
			return
		}
		n := 3
		if cjkRun {
			n = 2
		}
		if len(run) >= n {
			for i := 0; i+n <= len(run) && len(out) < capN; i++ {
				add(string(run[i : i+n]))
			}
		} else if len(run) >= 2 {
			add(string(run))
		}
		run = run[:0]
	}
	for _, c := range s {
		switch {
		case isCJK(c):
			if len(run) > 0 && !cjkRun {
				flush()
			}
			cjkRun = true
			run = append(run, c)
		case isKhmer(c):
			if len(run) > 0 && cjkRun {
				flush()
			}
			cjkRun = false
			run = append(run, c)
		default:
			flush()
		}
	}
	flush()
	return out
}
