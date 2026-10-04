package api

import "testing"

// The bug this pins: the widget's ?lang= (and the console's own default) is the
// language of the interface, but it used to be threaded through as the reply
// language — so a customer writing Chinese into a Khmer-configured widget got a
// Khmer answer end to end. Production 2026-10-04: "你好" → "សូមអរគុណ! …", and
// "hello" → the same Khmer small-talk template.
func TestReplyLanguageFollowsTheCustomer(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		preference string // users.language ("auto" is stored as "")
		hint       string // widget ?lang= / request language
		want       string
	}{
		{"chinese message in a km widget", "你好", "", "km", "zh"},
		{"chinese question in a km widget", "你好，EPS 板多少钱？", "", "km", "zh"},
		{"chinese in an en widget", "请给我报价", "", "en", "zh"},
		{"english in a km widget", "hello", "", "km", "en"},
		{"khmer in a zh widget", "តម្លៃប៉ុន្មាន?", "", "zh", "km"},
		{"script beats the hint", "你好", "", "en", "zh"},

		// No script at all → the interface language is the only guess available.
		{"digits fall back to the widget language", "123", "", "en", "en"},
		{"empty falls back to the widget language", "", "", "zh", "zh"},
		{"question marks fall back to the default", "??", "", "km", "km"},
		{"punctuation falls back to the default", "!!!", "", "", "km"},
		{"bogus hint falls back to the default", "12345", "", "fr", "km"},

		// A deliberate merchant setting outranks the customer's script: that is
		// what the console's AI language preference means.
		{"merchant pins khmer", "你好", "km", "zh", "km"},
		{"merchant pins chinese", "សូមស្វាគមន៍", "zh", "km", "zh"},
		{"unset preference is not a pin", "你好", "auto", "km", "zh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyLanguage(tc.content, tc.preference, tc.hint); got != tc.want {
				t.Errorf("replyLanguage(%q, pref=%q, hint=%q) = %q, want %q",
					tc.content, tc.preference, tc.hint, got, tc.want)
			}
		})
	}
}

// The three callers eventually feed the result to SmallTalkReply /
// HandoffAcknowledgement / JunkAcknowledgement, whose switch falls back to
// Khmer for anything unknown — a value this function could never produce, but
// a test is cheaper than remembering that.
func TestReplyLanguageAlwaysReturnsASupportedTag(t *testing.T) {
	for _, content := range []string{"你好", "hello", "សូមស្វាគមន៍", "??", "", "12345"} {
		for _, pref := range []string{"", "auto", "km", "en", "zh"} {
			for _, hint := range []string{"", "km", "en", "zh", "nonsense"} {
				if got := replyLanguage(content, pref, hint); !isReplyLanguage(got) {
					t.Fatalf("replyLanguage(%q,%q,%q) = %q, not a supported tag", content, pref, hint, got)
				}
			}
		}
	}
}
