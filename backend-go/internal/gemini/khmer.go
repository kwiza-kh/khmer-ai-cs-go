package gemini

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Outbound text hygiene.
//
// Everything here is applied to what the MODEL produced, on its way to a
// customer — the mirror image of rag.NormalizeText, which canonicalises what a
// customer (or a document) produced on its way INTO the system. The two cannot
// be one function:
//
//   - NormalizeText turns a zero-width space into a SPACE, because for matching,
//     a boundary is exactly what it wants. Displayed to a customer, that same
//     conversion SPLITS a Khmer word in half — the script has no word spaces, so
//     a ZWSP the model slipped inside a word is not a boundary, it is corruption.
//     Here they are deleted.
//   - The behaviours that ARE shared (NFC, Khmer digits → ASCII) are duplicated
//     rather than imported: package rag imports gemini, so gemini importing rag
//     for one helper would be a cycle.
//
// The order matters: NFC first, so composition sees the bytes the client will
// actually render; digits after, because Khmer digits are separate code points
// either way; whitespace last, so a deleted invisible cannot leave a double
// space behind.

// SanitizeReply canonicalises one complete model reply.
//
// It is idempotent and total: any byte sequence a model can emit comes out as
// text a chat bubble renders faithfully and a search box can match.
//
// It also drops the chat-hostile markup the prompt forbids twice ("no **bold**, no
// ## headings, no | tables", prompts.go). Gemini obeys that instruction; Claude Haiku
// 5.5 did not — 13 of 31 graded replies came back with **bold** around names, prices
// and languages (cmd/claudeeval against this deployment, 2026-10-08), and every
// transport here renders plain text, so a customer would have read the asterisks. A
// prompt rule is a request; this is the guarantee, and it lives here because this is
// already the single door every outbound reply walks through.
func SanitizeReply(s string) string {
	if s == "" {
		return ""
	}
	s = norm.NFC.String(s)
	// Break normalisation BEFORE the rune map: strings.Map is per-rune, so a CRLF
	// seen one rune at a time becomes two newlines — a paragraph break the model
	// never asked for, in every message that uses Windows line endings.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(dropInvisible, s)
	// NBSP is a space in a costume: most clients render it as a non-breaking gap,
	// so a wrapped reply looks like it has a hole in it. Keep the separation, lose
	// the special form.
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = khmerDigitsToASCII(s)
	s = stripChatMarkup(s)
	return strings.TrimSpace(tidyWhitespace(s))
}

// SanitizeReplyChunk is the streaming variant: the same character-level rules,
// minus everything that is only correct once the whole reply is known.
//
// It deliberately does NOT trim, collapse newlines or collapse spaces. A stream
// chunk routinely carries the space that separates two words, so trimming here
// would glue words together mid-conversation; the assembled reply is passed
// through SanitizeReply by the caller anyway, which is where the layout rules
// belong.
func SanitizeReplyChunk(s string) string {
	if s == "" {
		return ""
	}
	// Cheapest possible path for the overwhelmingly common case: chunks are
	// ASCII/khmer with no invisibles at all.
	if !strings.ContainsFunc(s, needsChunkWork) {
		return s
	}
	s = strings.Map(dropInvisible, s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	return khmerDigitsToASCII(s)
}

func needsChunkWork(r rune) bool {
	const ascii = 0x80
	if r < ascii {
		// Includes '\r': normalising line endings is a whole-reply rule, because a
		// chunk boundary can fall between a CR and its LF.
		return false
	}
	return r == '\u00a0' || r == '\u200b' || r == '\u200c' || r == '\u200d' ||
		r == '\u2060' || r == '\ufeff' || r == '\u00ad' ||
		(r >= '០' && r <= '៩') || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// dropInvisible removes characters that carry no visible glyph but break
// rendering, copying, searching or wrapping.
//
// Deletion, not substitution, is the whole point (see the package comment): a
// ZWSP inside "ជំនួយការ" must leave "ជំនួយការ", never "ជំនួយ ការ". Tab and newline
// survive because a reply legitimately has them; every other control character
// is dropped rather than allowed to reach a bubble where it renders as tofu or
// truncates the message in some clients.
func dropInvisible(r rune) rune {
	switch r {
	case '\n', '\t':
		return r
	case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff', '\u00ad':
		return -1
	case '\u00a0':
		// Kept here and turned into a plain space by the callers: returning ' '
		// directly would be wrong for the chunk path, which must not invent a
		// separator that trimming cannot later distinguish from a real one.
		return r
	}
	if r == '\r' {
		// Reached only through the whole-reply path, which collapses CRLF first;
		// a lone CR still becomes a break rather than surviving into the payload.
		return '\n'
	}
	if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
		return -1
	}
	return r
}

// khmerDigitsToASCII maps U+17E0–U+17E9 onto 0-9.
//
// The knowledge base writes every price, quantity and phone number in ASCII
// digits, and so does every documented reply. A model that answers "តម្លៃ ៣២ ដុល្លារ"
// next to a stored "$32.00" puts two numeral systems in one bubble — for a
// quotation that reads as a different number, and it cannot be copied into a
// search box or a reply that matches the corpus.
func khmerDigitsToASCII(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= '០' && r <= '៩' }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= '០' && r <= '៩' {
			return '0' + (r - '០')
		}
		return r
	}, s)
}

// stripChatMarkup removes the markdown a chat bubble must not show.
//
// Deliberately narrow, so ordinary text survives:
//
//   - runs of two or more asterisks (bold/italic markers) go;
//   - a line-leading # marker only when a space follows it, so the company address
//     "#777, Road No. 2" is untouched;
//   - whole code-fence lines (``` or ```json) go.
//
// A single asterisk stays: it may be arithmetic ("5 * 10"), and the reply rubric does
// not forbid it. A table separator ("| ---") stays too — the rubric still reports it
// if a provider emits one, and guessing at table layout would risk more than it fixes.
func stripChatMarkup(s string) string {
	if !strings.ContainsAny(s, "*#`") {
		return s
	}
	s = chatMarkupFence.ReplaceAllString(s, "")
	s = chatMarkupHeading.ReplaceAllString(s, "")
	return chatMarkupBold.ReplaceAllString(s, "")
}

var (
	// A whole line that is nothing but a fence, with an optional language tag.
	chatMarkupFence = regexp.MustCompile("(?m)^[ \\t]*`{3}[A-Za-z0-9_+#.-]*[ \\t]*$")
	// A heading marker only counts when a space follows it (#777 survives).
	chatMarkupHeading = regexp.MustCompile(`(?m)^#{1,6}[ \t]+`)
	// Bold and italic markers: two or more asterisks in a row.
	chatMarkupBold = regexp.MustCompile(`\*{2,}`)
)

// tidyWhitespace trims layout noise without touching structure: human text has
// paragraphs, and both the widget and the messengers render a blank line as a
// blank line.
func tidyWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			// One blank line is a paragraph break; two or more is a model
			// clearing its throat, and in a chat bubble it is just a gap.
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
