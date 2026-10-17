package api

import "testing"

// The server refuses an attachment the channel cannot carry. The composer hides
// most of these, but a seat can hit the API directly, and Zalo's Send silently
// drops a document while LINE fails the delivery after the reply was accepted.
func TestReplyMediaUnsupported(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		payload  map[string]any
		want     string
	}{
		{"no payload", "telegram", nil, ""},
		{"text reply has no media", "telegram", map[string]any{"kind": "text"}, ""},
		{"telegram document", "telegram", map[string]any{"kind": "media", "media_type": "document"}, ""},
		{"telegram video", "telegram", map[string]any{"kind": "media", "media_type": "video"}, ""},
		{"line image is not supported", "line", map[string]any{"kind": "media", "media_type": "image"}, "image"},
		{"zalo document is silently dropped", "zalo", map[string]any{"kind": "media", "media_type": "document"}, "document"},
		{"zalo video", "zalo", map[string]any{"kind": "media", "media_type": "video"}, "video"},
		{"instagram document", "instagram", map[string]any{"kind": "media", "media_type": "document"}, "document"},
		{"meta file", "meta", map[string]any{"kind": "media", "media_type": "file"}, ""},
		{"whatsapp audio", "whatsapp", map[string]any{"kind": "media", "media_type": "audio"}, ""},
		{"web document", "web", map[string]any{"kind": "media", "media_type": "document"}, "document"},
		{
			"one bad item in a list",
			"telegram",
			map[string]any{"kind": "media", "media_items": []any{
				map[string]any{"url": "https://a.example/1.jpg", "type": "image"},
				map[string]any{"url": "https://a.example/2.pdf", "type": "document"},
			}},
			"",
		},
		{
			"a list on line",
			"line",
			map[string]any{"kind": "media", "media_items": []any{
				map[string]any{"url": "https://a.example/1.jpg", "type": "image"},
			}},
			"image",
		},
		{
			"unknown type is left to the provider",
			"telegram",
			map[string]any{"kind": "media", "media_type": "sticker"},
			"",
		},
	}
	for _, tc := range cases {
		if got := replyMediaUnsupported(tc.platform, tc.payload); got != tc.want {
			t.Errorf("%s: replyMediaUnsupported = %q, want %q", tc.name, got, tc.want)
		}
	}
}
