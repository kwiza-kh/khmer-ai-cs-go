package platform

import "testing"

// TestParseTelegramMessageID pins the provider_message_id format written by
// TelegramProviderMessageID ("<chat_id>:<message_id>") — the demote-previous-
// keyboard path parses it back, and a silent mismatch would leave the old
// 👍/👎 keyboard live forever.
func TestParseTelegramMessageID(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"123456789:42", 42, true},
		{"-1001234567890:7", 7, true},
		{"123456789:", 0, false},
		{"123456789", 0, false},
		{"", 0, false},
		{":", 0, false},
		{"123:abc", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseTelegramMessageID(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseTelegramMessageID(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
