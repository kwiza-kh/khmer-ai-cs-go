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

// TestParseFeedbackCallback separates the two kinds of inline-keyboard press
// that share one Telegram update type. Only "fb:<msgID>:<±1>" is a CSAT vote;
// everything else is a customer tapping a reply button the operator sent, and
// the webhook now routes those into the pipeline instead of returning early.
// Reading a button press as a vote would be silent data loss in the other
// direction (the rating lands on the wrong message, or nowhere).
func TestParseFeedbackCallback(t *testing.T) {
	cases := []struct {
		in     string
		msgID  int64
		rating int
		ok     bool
	}{
		{"fb:42:1", 42, 1, true},
		{"fb:42:-1", 42, -1, true},
		{"fb:42:0", 0, 0, false},
		{"fb:abc:1", 0, 0, false},
		{"fb:42:1:extra", 0, 0, false},
		{"fb:42", 0, 0, false},
		// The operator's own buttons: a bare payload must NOT parse as a vote,
		// or the tap is swallowed by the CSAT handler (2026-10-12).
		{"1", 0, 0, false},
		{"2", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, tc := range cases {
		msgID, rating, ok := parseFeedbackCallback(tc.in)
		if ok != tc.ok || (ok && (msgID != tc.msgID || rating != tc.rating)) {
			t.Errorf("parseFeedbackCallback(%q) = (%d, %d, %v), want (%d, %d, %v)",
				tc.in, msgID, rating, ok, tc.msgID, tc.rating, tc.ok)
		}
		if isFeedbackCallback(tc.in) != tc.ok {
			t.Errorf("isFeedbackCallback(%q) disagrees with parseFeedbackCallback", tc.in)
		}
	}
}
