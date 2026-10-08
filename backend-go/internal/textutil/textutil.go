// Package textutil holds the handful of text coercions this tree needs in more
// than one package.
//
// Every function here previously existed as two to five near-identical copies
// across api, platform, gemini, rag, typesafe, sqlcheck and two cmd binaries. The
// copies had drifted, and the drift was not cosmetic: the same job existed under
// four names with three different meanings —
//
//	platform.truncateStr    []rune(s)[:n] + "…"   (n+1 runes out)
//	gemini.truncateRunes    []rune(s)[:n-1] + "…" (n runes out, panicked on n<=0)
//	rag.truncateRunes       []rune(s)[:n-1] + "…" (n runes out, guarded)
//	platform.truncate       []rune(s)[:n]         (no marker at all)
//	platform.truncateRunes  []rune(s)[:n]         (no marker at all)
//
// so "truncate this" did not tell you whether the result would be marked or
// whether it could panic. The two meanings are now separate, named functions:
// TruncateRunes never marks, Ellipsize always marks what it cut.
package textutil

import "strings"

// TruncateRunes cuts s to at most n runes with no marker: the result is exactly
// the first n runes. Use it when the string is persisted or shown as data (a
// session title, an error column) where an appended "…" would become part of the
// value.
//
// n<=0 is answered with "" rather than left to the slice expression, because
// runes[:n] and runes[:n-1] are negative indexes that panic.
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// Ellipsize cuts s to at most n runes INCLUDING the trailing "…" it appends, so
// the result never exceeds the caller's budget and a reader can tell the string
// was cut. Use it for anything shown to a human or handed to a model as a quote:
// a silent cut reads as "this is the whole thing".
func Ellipsize(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// FirstNonEmpty returns the first value that is not blank. The value is returned
// verbatim (padding and all) — only the emptiness test trims, so a configured
// " 127.0.0.1 " still reaches its caller unchanged.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// OneLine makes arbitrary text safe to print on one table row: control
// characters become spaces, whitespace runs collapse, and the result is capped
// at max runes with an ellipsis. max<=0 means "no cap".
//
// Runes, not bytes: the caller that motivated this counts Khmer queries, and a
// byte-indexed cut printed mojibake.
func OneLine(s string, max int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, s)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if max <= 0 {
		return cleaned
	}
	return Ellipsize(cleaned, max)
}

// DerefString reads a nullable string column without a nil check at every call
// site: a nil pointer and an empty string are the same thing to this schema.
func DerefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// NullIfEmpty is DerefString's inverse for writes: an empty string becomes SQL
// NULL so that a column's "unset" state is not spelled two ways.
func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ContainsAny reports whether hay contains any of the needles. One implementation,
// so a case-folding or empty-needle fix lands once.
func ContainsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(hay, n) {
			return true
		}
	}
	return false
}
