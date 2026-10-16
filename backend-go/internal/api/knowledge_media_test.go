package api

import (
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/storager2"
)

// A ".jpg" that is really something else must not be stored as an image, and the
// stored object must be content-addressed inside the tenant's own prefix.
func TestKBImageMime(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0, 0}, "image/jpeg"},
		{"png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...), "image/png"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 4)...), "image/webp"},
		{"pdf renamed to .jpg", []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n000\n"), ""},
		{"empty", nil, ""},
		{"html renamed to .png", []byte("<html><body>hi</body></html>"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kbImageMime(tc.data); got != tc.want {
				t.Fatalf("kbImageMime = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKBAssetKeyIsContentAddressedPerTenant(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\npayload")
	key := kbAssetKey(7, data, "image/png")
	if !strings.HasPrefix(key, "kb/7/") || !strings.HasSuffix(key, ".png") {
		t.Fatalf("key = %q, want kb/<tenant>/<hash>.png", key)
	}
	if again := kbAssetKey(7, data, "image/png"); again != key {
		t.Fatalf("same bytes produced %q then %q: re-uploading one photo must not leave two objects", key, again)
	}
	if other := kbAssetKey(8, data, "image/png"); other == key {
		t.Fatalf("two tenants share the key %q", key)
	}
	if changed := kbAssetKey(7, []byte("\x89PNG\r\n\x1a\nother"), "image/png"); changed == key {
		t.Fatalf("different bytes share the key %q", key)
	}
	// The extension comes from the sniffed mime, never from the client's filename:
	// a ".jpg" holding PNG bytes is stored as .png and served as such.
	if got := kbAssetKey(7, data, "image/jpeg"); !strings.HasSuffix(got, ".jpg") {
		t.Fatalf("key = %q, want the mime's extension", got)
	}
}

// Without R2 the document must still be usable: the key stays, the URL does not
// appear, and nothing panics.
func TestAttachKnowledgeMediaURLWithoutR2(t *testing.T) {
	a := &App{}
	doc := map[string]any{"doc_id": int32(1), "media_key": "kb/1/x.png"}
	a.attachKnowledgeMediaURL(doc)
	if _, ok := doc["media_url"]; ok {
		t.Fatal("no media_url can be signed without R2; the field must stay absent")
	}
	if doc["media_key"] != "kb/1/x.png" {
		t.Fatal("the stored key must survive for support and re-resolution")
	}
	// A text document (no key) is untouched.
	plain := map[string]any{"doc_id": int32(2)}
	a.attachKnowledgeMediaURL(plain)
	if len(plain) != 1 {
		t.Fatalf("plain document grew fields: %v", plain)
	}
}

// The scanner hands the column over as *string (nullable); the upload path as a
// plain string. Both must resolve, or the library shows the picture only in the
// upload response and never again (caught by the first production smoke).
func TestAttachKnowledgeMediaURLHandlesPointerKey(t *testing.T) {
	key := "kb/3/abc.png"
	a := &App{Media: storager2.New("acct", "key", "secret", "bucket", "https://cdn.example.com")}
	for _, doc := range []map[string]any{
		{"doc_id": int32(1), "media_key": &key},
		{"doc_id": int32(1), "media_key": key},
	} {
		a.attachKnowledgeMediaURL(doc)
		if doc["media_url"] != "https://cdn.example.com/kb/3/abc.png" {
			t.Fatalf("media_url = %v for %T key", doc["media_url"], doc["media_key"])
		}
	}
	// A NULL column (the common case for text documents) must not resolve.
	doc := map[string]any{"doc_id": int32(2), "media_key": (*string)(nil)}
	a.attachKnowledgeMediaURL(doc)
	if _, ok := doc["media_url"]; ok {
		t.Fatal("a NULL media_key must not produce a URL")
	}
}

// With a public base configured the stored key resolves to a permanent URL —
// the address the channel provider fetches when it sends the picture. With no
// client there is nothing to send, and callers must degrade to text.
func TestMediaURLFor(t *testing.T) {
	a := &App{Media: storager2.New("acct", "key", "secret", "bucket", "https://cdn.example.com")}
	if got := a.mediaURLFor("kb/1/x.png"); got != "https://cdn.example.com/kb/1/x.png" {
		t.Fatalf("mediaURLFor = %q", got)
	}
	if got := a.mediaURLFor(""); got != "" {
		t.Fatalf("empty key produced %q", got)
	}
	if got := (&App{}).mediaURLFor("kb/1/x.png"); got != "" {
		t.Fatalf("a missing client produced %q", got)
	}
}
