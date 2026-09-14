// Package rag — segmentation shared by the lexical index (content_seg /
// content_tsv, migration 058) and the query builder.
//
// PostgreSQL has no Khmer word segmenter, so segmentation happens here: runs
// of scripts written without inter-word spaces become character n-grams, and
// both sides (index and query) call the same functions so they cannot drift.
// A real Khmer segmenter (dictionary/CRF) can replace runNgrams later and the
// tsvector column can be rebuilt without touching embeddings.
package rag

import (
	"strings"
	"unicode"
)

// SegmentForSearch converts text into the space-separated token stream stored
// in knowledge_chunks.content_seg:
//
// plain words keep their form; CJK runs become bigrams (price table ->
// "价格 格表"), Khmer runs become trigrams ("តម្លៃ" -> "តម្ ម្ល ្លៃ"). Trigrams are
// the shortest pattern pg_trgm's GIN can index, and PostgreSQL keeps Khmer
// combining marks inside a token (verified on PG 17), so raw grams are safe.
func SegmentForSearch(s string) string {
	s = NormalizeText(s)
	var b strings.Builder
	for _, piece := range strings.Fields(s) {
		seg := segmentPiece(piece)
		for _, w := range seg.words {
			writeToken(&b, w)
		}
		for _, g := range seg.grams {
			writeToken(&b, g)
		}
	}
	return b.String()
}

type runKind int

const (
	runNone runKind = iota
	runCJK
	runKhmer
	runPlain
)

func classifyRune(r rune) runKind {
	switch {
	case isCJK(r):
		return runCJK
	case isKhmer(r):
		return runKhmer
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return runPlain
	}
	return runNone
}

type pieceSegments struct {
	words []string
	grams []string
}

func segmentPiece(s string) pieceSegments {
	var seg pieceSegments
	var run []rune
	kind := runNone
	flush := func() {
		if len(run) == 0 {
			return
		}
		switch kind {
		case runCJK, runKhmer:
			seg.grams = append(seg.grams, runNgrams(run, kind)...)
		case runPlain:
			seg.words = append(seg.words, string(run))
		}
		run = run[:0]
	}
	for _, r := range s {
		k := classifyRune(r)
		if k == runNone {
			flush()
			kind = runNone
			continue
		}
		if len(run) > 0 && k != kind {
			flush()
		}
		kind = k
		run = append(run, r)
	}
	flush()
	return seg
}

// runNgrams — n-grams for one unspaced-script run, de-duplicated: CJK bigrams,
// Khmer trigrams. Runs shorter than the window fall back to the whole run when
// it has 2+ runes (single runes are too noisy to index).
func runNgrams(run []rune, kind runKind) []string {
	n := 3
	if kind == runCJK {
		n = 2
	}
	if len(run) >= n {
		seen := make(map[string]bool)
		out := make([]string, 0, len(run))
		for i := 0; i+n <= len(run); i++ {
			g := string(run[i : i+n])
			if seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
		return out
	}
	if len(run) >= 2 {
		return []string{string(run)}
	}
	return nil
}

func writeToken(b *strings.Builder, tok string) {
	if tok == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(tok)
}

// maxQueryGrams caps one query run's OR group so a long Khmer message cannot
// build a thousand-term tsquery.
const maxQueryGrams = 24

// SegmentedTSQuery builds a to_tsquery() string for a query containing an
// unspaced script: every piece becomes an OR group of its n-grams / words
// ("refund តម្លៃ" -> "refund | (តម្ល... | ...)"). A flat OR, not AND,
// because natural-language questions carry question words (ប៉ុន្មាន / អ្វី /
// មែនទេ) that never appear in documents: AND measured 0 hits on 3 of 40 real
// Khmer queries, while OR + ts_rank_cd ranks by term coverage and lets
// RRF/rerank handle precision. Returns "" when no unspaced script is
// present; callers keep websearch_to_tsquery for those.
func SegmentedTSQuery(query string) string {
	query = NormalizeText(query)
	if hasLexicalScript(query) == false {
		return ""
	}
	var terms []string
	for _, piece := range strings.Fields(query) {
		seg := segmentPiece(piece)
		for _, w := range seg.words {
			if w = sanitizeTerm(w); w == "" {
				continue
			}
			terms = append(terms, w)
		}
		if len(seg.grams) > maxQueryGrams {
			seg.grams = seg.grams[:maxQueryGrams]
		}
		if len(seg.grams) > 0 {
			terms = append(terms, "("+strings.Join(seg.grams, " | ")+")")
		}
	}
	return strings.Join(terms, " | ")
}

// sanitizeTerm keeps only word characters so punctuation cannot change the
// meaning of the generated tsquery syntax.
func sanitizeTerm(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
