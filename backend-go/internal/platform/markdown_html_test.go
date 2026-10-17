package platform

import (
	"strings"
	"testing"
)

// The converter's contract: the Markdown subset the model emits becomes the HTML
// subset Telegram accepts, everything else is escaped text, and no input can
// produce a tag this function did not write.
func TestTelegramHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
		forbidden      string
	}{
		{"bold italic code", "**bold** and *it* and `c`", "<b>bold</b> and <i>it</i> and <code>c</code>", ""},
		{"underline and strike", "__u__ ~~s~~", "<u>u</u> <s>s</s>", ""},
		{"angle brackets are content", "a < b & c > d", "a &lt; b &amp; c &gt; d", "<b"},
		{"script stays text", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;", "<script>"},
		{"link", "[docs](https://x.y/a?b=1&c=2)", `<a href="https://x.y/a?b=1&amp;c=2">docs</a>`, ""},
		{"non-http link stays literal", "[x](javascript:alert(1))", "[x](javascript:alert(1))", "<a"},
		{"fenced code is not emphasis", "```\n**not bold**\n```", "<pre>**not bold**\n</pre>", "<b>"},
		{"heading", "## Title", "<b>Title</b>", ""},
		{"bullets", "- one\n- two", "• one\n• two", ""},
		{"unbalanced bold stays literal", "**unbalanced", "**unbalanced", "<b>"},
		{"quote marker", "> quoted", "quoted", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telegramHTML(tc.in)
			if got != tc.want {
				t.Fatalf("telegramHTML(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if tc.forbidden != "" && strings.Contains(got, tc.forbidden) {
				t.Fatalf("telegramHTML(%q) = %q, must not contain %q", tc.in, got, tc.forbidden)
			}
		})
	}
}

func TestTelegramErrorClassification(t *testing.T) {
	if !tgParseError(errString("Bad Request: can't parse entities: Unsupported start tag")) {
		t.Error("parse-entity refusal not recognised; the chunk would be dropped instead of retried")
	}
	if tgParseError(errString("Bad Request: chat not found")) {
		t.Error("an unrelated failure was classified as a formatting problem")
	}
	if !tgCaptionTooLong(errString("Bad Request: message caption is too long")) {
		t.Error("caption-length refusal not recognised")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
