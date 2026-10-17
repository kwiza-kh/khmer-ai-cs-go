package platform

import (
	"regexp"
	"strings"
)

// telegramHTML converts the Markdown subset the model emits into the HTML subset
// Telegram accepts (<b>, <i>, <u>, <s>, <code>, <pre>, <a>).
//
// Safety rule: the whole string is HTML-escaped BEFORE any tag is inserted, and
// only tags this function writes can appear in the output — so a prompt-injected
// "<script>" reaches the customer as visible text, never as markup, and no
// unbalanced Markdown can break the parser.
//
// Code spans and fenced blocks are extracted first so their contents are never
// re-interpreted as emphasis (a code sample containing `**` must stay literal).
// Constructs that do not pair up inside one chunk stay literal: `SplitChannelText`
// cuts at whitespace, so a bold span can straddle two messages, and inventing a
// tag to close it would be worse than showing the asterisks.
var (
	tgFenceRe  = regexp.MustCompile("(?s)```[a-zA-Z0-9_+.-]*\\n?(.*?)```")
	tgCodeRe   = regexp.MustCompile("`([^`\\n]+)`")
	tgHeadRe   = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]*(.+)$`)
	tgBulletRe = regexp.MustCompile(`(?m)^[ \t]*[-*+][ \t]+`)
	tgQuoteRe  = regexp.MustCompile(`(?m)^[ \t]{0,3}&gt;[ \t]?`)
	tgLinkRe   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	tgBoldRe   = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	tgUnderRe  = regexp.MustCompile(`__([^_\n]+)__`)
	tgStrikeRe = regexp.MustCompile(`~~([^~\n]+)~~`)
	tgItalicRe = regexp.MustCompile(`\*([^*\n]+)\*`)
)

const tgCodeSentinel = "\x00"

func escapeTelegramHTML(s string) string {
	// `"` is escaped too so it is safe inside an href attribute.
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func telegramHTML(markdown string) string {
	text := escapeTelegramHTML(markdown)

	// Code first: pull it out so emphasis rules cannot touch it.
	var code []string
	text = tgFenceRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := tgFenceRe.FindStringSubmatch(m)[1]
		code = append(code, "<pre>"+inner+"</pre>")
		return tgCodeSentinel + "\x01"
	})
	text = tgCodeRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := tgCodeRe.FindStringSubmatch(m)[1]
		code = append(code, "<code>"+inner+"</code>")
		return tgCodeSentinel + "\x02"
	})

	// Block-level shapes first, so a list marker is not mistaken for emphasis.
	text = tgHeadRe.ReplaceAllString(text, "<b>$1</b>")
	text = tgBulletRe.ReplaceAllString(text, "• ")
	text = tgQuoteRe.ReplaceAllString(text, "")
	text = tgLinkRe.ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = tgBoldRe.ReplaceAllString(text, "<b>$1</b>")
	text = tgUnderRe.ReplaceAllString(text, "<u>$1</u>")
	text = tgStrikeRe.ReplaceAllString(text, "<s>$1</s>")
	text = tgItalicRe.ReplaceAllString(text, "<i>$1</i>")

	// Restore code, in the order the stashes were created (both walk forward).
	for _, wrapped := range code {
		if strings.HasPrefix(wrapped, "<pre>") {
			text = strings.Replace(text, tgCodeSentinel+"\x01", wrapped, 1)
		} else {
			text = strings.Replace(text, tgCodeSentinel+"\x02", wrapped, 1)
		}
	}
	return text
}

// tgCaptionTooLong recognises the caption cap; the caller sends the picture bare
// and the text after it instead of truncating the answer.
func tgCaptionTooLong(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "caption is too long")
}

// tgParseError recognises Telegram's "the entity parser choked on this" family.
// The caller retries the chunk as plain text: a formatting bug must cost the
// bold, never the answer.
func tgParseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "can't parse entities") ||
		strings.Contains(msg, "can't find end tag") ||
		strings.Contains(msg, "unsupported start tag") ||
		strings.Contains(msg, "unmatched end tag")
}
