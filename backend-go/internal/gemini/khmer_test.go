package gemini

import (
	"strings"
	"testing"
)

// Khmer sentence from the production knowledge base (kb/docs/SVN-021-faq.md),
// used verbatim so the "nothing is mangled" assertions are about real text and
// not about a string this test invented.
const khmerKBLine = "ដុំ EPS-B តម្លៃ $30.00/m³ សម្រាប់ 10 kg/m³ និង $43.00/m³ សម្រាប់ 15 kg/m³។"

func TestSanitizeReply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "zero width space inside a khmer word is deleted, not spaced",
			in:   "ជំនួយ​ការ",
			want: "ជំនួយការ",
		},
		{
			name: "zwnj/zwj/bom/word-joiner/soft-hyphen are deleted",
			in:   "a\u200cb\u200dc\ufeffd\u2060e\u00adf",
			want: "abcdef",
		},
		{
			name: "nbsp becomes a plain space",
			in:   "តម្លៃ\u00a0$32.00",
			want: "តម្លៃ $32.00",
		},
		{
			name: "khmer digits become ascii",
			in:   "តម្លៃ ៣២ ដុល្លារ · MOQ ២០ m³",
			want: "តម្លៃ 32 ដុល្លារ · MOQ 20 m³",
		},
		{
			name: "control characters are dropped but newline and tab survive",
			in:   "សួស្តី\x00\x07​\n\tបង\x1f",
			want: "សួស្តី\n\tបង",
		},
		{
			name: "carriage returns become newlines",
			in:   "one\r\ntwo\rthree",
			want: "one\ntwo\nthree",
		},
		{
			name: "trailing spaces per line are stripped",
			in:   "line one   \nline two\t",
			want: "line one\nline two",
		},
		{
			name: "runs of blank lines collapse to one",
			in:   "a\n\n\n\n\nb",
			want: "a\n\nb",
		},
		{
			name: "ends are trimmed",
			in:   "\n\n  សួស្តី  \n\n",
			want: "សួស្តី",
		},
		{
			name: "real kb sentence is untouched",
			in:   khmerKBLine,
			want: khmerKBLine,
		},
		{
			name: "english reply is untouched",
			in:   "We ship EPS panels in 50/75/100 mm.",
			want: "We ship EPS panels in 50/75/100 mm.",
		},
		{
			name: "empty stays empty",
			in:   "",
			want: "",
		},
		{
			name: "invisible-only reply becomes empty",
			in:   "\u200b\ufeff\u200d",
			want: "",
		},
	}
	for _, tc := range cases {
		got := SanitizeReply(tc.in)
		if got != tc.want {
			t.Errorf("%s\n  in:   %q\n  got:  %q\n  want: %q", tc.name, tc.in, got, tc.want)
		}
		if again := SanitizeReply(got); again != got {
			t.Errorf("%s: not idempotent — %q then %q", tc.name, got, again)
		}
	}
}

// TestSanitizeReplyLeavesKhmerBytesAlone — the sanitizer must never reorder or
// drop combining marks: Khmer vowels and signs are what make two replies read as
// different words, and a "cleanup" that touches them is corruption, not hygiene.
// The Khmer half of the NFC pass is a no-op by construction (Khmer has no
// canonical compositions), which is exactly why it is safe to run on a reply at
// all — the Latin case below proves the pass is not dead code.
func TestSanitizeReplyLeavesKhmerBytesAlone(t *testing.T) {
	// The KB sentence with the invisible characters a model sometimes injects
	// inside a word: removing them must give the original bytes back.
	messy := strings.ReplaceAll(khmerKBLine, "ត", "ត\u200b")
	if got := SanitizeReply(messy); got != khmerKBLine {
		t.Fatalf("round trip lost or changed Khmer text\n got: %q\nwant: %q", got, khmerKBLine)
	}
	if got, want := SanitizeReply("cafe\u0301"), "caf\u00e9"; got != want {
		t.Errorf("NFC pass is not running: got %q want %q", got, want)
	}
}

func TestSanitizeReplyChunk(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing space of a chunk is preserved",
			in:   "តម្លៃ ",
			want: "តម្លៃ ",
		},
		{
			name: "leading newline of a chunk is preserved",
			in:   "\nដុំ",
			want: "\nដុំ",
		},
		{
			name: "blank lines are NOT collapsed (chunk boundaries are not paragraphs)",
			in:   "\n\n",
			want: "\n\n",
		},
		{
			name: "invisibles are still removed",
			in:   "ជំនួយ\u200bការ",
			want: "ជំនួយការ",
		},
		{
			name: "khmer digits are still mapped",
			in:   "២០ m³",
			want: "20 m³",
		},
		{
			name: "nbsp still becomes a space",
			in:   "a\u00a0b",
			want: "a b",
		},
		{
			name: "plain ascii chunk is returned untouched",
			in:   "hello ",
			want: "hello ",
		},
	}
	for _, tc := range cases {
		if got := SanitizeReplyChunk(tc.in); got != tc.want {
			t.Errorf("%s\n  in:   %q\n  got:  %q\n  want: %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestChunkStreamAssemblesToTheSameTextAsTheWholeReply — the cheap chunk path
// and the full path must agree once the chunks are concatenated, or the customer
// sees one thing while the stored row (and the reply cache) holds another.
func TestChunkStreamAssemblesToTheSameTextAsTheWholeReply(t *testing.T) {
	chunks := []string{"តម្លៃ EPS-S ", "គិតតាមដង់ស៊ី", "តេ៖ 10 kg/m³ = $32.00 ", "\n12 kg/m³ = $38.00\u200b។"}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(SanitizeReplyChunk(c))
	}
	whole := strings.Join(chunks, "")
	if got, want := SanitizeReply(b.String()), SanitizeReply(whole); got != want {
		t.Fatalf("streamed assembly differs from the whole reply\n got: %q\nwant: %q", got, want)
	}
}
