package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Text-to-image: a length threshold plus a channel check decide whether a reply
// becomes an image, the remote renderer turns it into bytes, and the object store
// hands back a URL the channel can fetch. Any failure leaves the text alone.

func TestT2IConfigFromEnv(t *testing.T) {
	t.Setenv("T2I_ENDPOINT", "")
	t.Setenv("T2I_ENABLED", "")
	t.Setenv("T2I_WORD_THRESHOLD", "")
	t.Setenv("T2I_TIMEOUT_MS", "")
	if cfg := T2IConfigFromEnv(); cfg.Enabled || cfg.Endpoint != "" {
		t.Fatalf("no endpoint must leave the feature off: %+v", cfg)
	}
	if cfg := T2IConfigFromEnv(); cfg.Threshold != T2IThresholdDefault {
		t.Fatalf("threshold = %d, want the default %d", cfg.Threshold, T2IThresholdDefault)
	}

	// Setting the endpoint is enough to turn it on.
	t.Setenv("T2I_ENDPOINT", "https://render.example/t2i")
	cfg := T2IConfigFromEnv()
	if !cfg.Enabled || cfg.Endpoint != "https://render.example/t2i" {
		t.Fatalf("endpoint must enable the feature: %+v", cfg)
	}

	// The explicit off switch wins over a configured endpoint.
	t.Setenv("T2I_ENABLED", "false")
	if cfg := T2IConfigFromEnv(); cfg.Enabled {
		t.Fatal("T2I_ENABLED=false must disable the feature")
	}
	t.Setenv("T2I_ENABLED", "yes")
	t.Setenv("T2I_WORD_THRESHOLD", "40")
	t.Setenv("T2I_TIMEOUT_MS", "2500")
	cfg = T2IConfigFromEnv()
	if !cfg.Enabled || cfg.Threshold != 40 || cfg.Timeout != 2500*time.Millisecond {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	// A malformed number falls back rather than disabling the feature.
	t.Setenv("T2I_WORD_THRESHOLD", "abc")
	t.Setenv("T2I_TIMEOUT_MS", "-1")
	cfg = T2IConfigFromEnv()
	if cfg.Threshold != T2IThresholdDefault || cfg.Timeout != 15*time.Second {
		t.Fatalf("malformed values must fall back: %+v", cfg)
	}
}

func TestShouldRender(t *testing.T) {
	cfg := T2IConfig{Enabled: true, Endpoint: "https://render.example/t2i", Threshold: 100}
	long := strings.Repeat("x", 100)
	short := strings.Repeat("x", 99)

	cases := []struct {
		name   string
		text   string
		cfg    T2IConfig
		portal string
		want   bool
	}{
		{"disabled", long, T2IConfig{Threshold: 100}, "zalo", false},
		{"below threshold", short, cfg, "zalo", false},
		{"at threshold", long, cfg, "zalo", true},
		{"zalo carries images", long, cfg, "zalo", true},
		{"whatsapp carries images", long, cfg, "whatsapp", true},
		{"meta carries images", long, cfg, "meta", true},
		{"instagram carries images", long, cfg, "instagram", true},
		// Telegram has no image send path (see the capability table), so an image
		// would be delivered as text — worse than the text.
		{"telegram has no image path", long, cfg, "telegram", false},
		{"line is text only", long, cfg, "line", false},
		// The widget renders Markdown itself; an image would be a downgrade.
		{"web renders markdown", long, cfg, "web", false},
	}
	for _, tc := range cases {
		if got := tc.cfg.ShouldRender(tc.text, CapabilitiesFor(tc.portal)); got != tc.want {
			t.Errorf("%s: ShouldRender = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The capability table must describe what the channel implementations can really
// send, because text-to-image now acts on it.
func TestCapabilitiesMatchTheImagePathsThatExist(t *testing.T) {
	if CapabilitiesFor("telegram").Media.Supports(MediaImage) {
		t.Error("telegram has no image branch in telegramChannel.Send and must not claim one")
	}
	if !CapabilitiesFor("zalo").Media.Supports(MediaImage) {
		t.Error("zalo sends images")
	}
	for _, p := range []string{"telegram", "line", "zalo", "whatsapp", "meta", "instagram"} {
		if !CapabilitiesFor(p).FlattensMarkdown {
			t.Errorf("%s delivers plain text and must declare it", p)
		}
	}
	if CapabilitiesFor("web").FlattensMarkdown {
		t.Error("the widget renders Markdown; an image is a downgrade there")
	}
}

func TestHTTPRendererPostsTextAndReturnsImage(t *testing.T) {
	var gotText, gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotText, _ = body["text"].(string)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	t.Cleanup(srv.Close)

	r := &HTTPRenderer{Endpoint: srv.URL, Token: "tok", Timeout: time.Second}
	data, ct, err := r.Render(context.Background(), "hello សូមស្វាគមន៍ 你好")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(data) != "PNGDATA" || ct != "image/png" {
		t.Fatalf("data/type = %q/%q", data, ct)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s", gotMethod)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	// The text must survive the wire byte-for-byte: Khmer and CJK are the whole
	// reason this platform needs an image in the first place.
	if gotText != "hello សូមស្វាគមន៍ 你好" {
		t.Errorf("text reached the renderer as %q", gotText)
	}
}

func TestHTTPRendererRejectsBadResponses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"non-200", http.StatusBadGateway, "upstream down", "HTTP 502"},
		{"empty", http.StatusOK, "", "no image"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		r := &HTTPRenderer{Endpoint: srv.URL, Timeout: time.Second}
		_, _, err := r.Render(context.Background(), "text")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestHTTPRendererHonoursItsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	r := &HTTPRenderer{Endpoint: srv.URL, Timeout: 150 * time.Millisecond}
	start := time.Now()
	_, _, err := r.Render(context.Background(), "text")
	if err == nil {
		t.Fatal("a hanging renderer must time out")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("timeout took %s; the renderer budget did not apply", took)
	}
}

type fakeRenderer struct {
	data []byte
	ct   string
	err  error
	got  string
}

func (f *fakeRenderer) Render(_ context.Context, text string) ([]byte, string, error) {
	f.got = text
	return f.data, f.ct, f.err
}

type fakeUploader struct {
	key, contentType string
	data             []byte
	err              error
}

func (f *fakeUploader) PutObject(_ context.Context, key string, data []byte, contentType string) error {
	if f.err != nil {
		return f.err
	}
	f.key, f.contentType, f.data = key, contentType, data
	return nil
}

func (f *fakeUploader) PublicOrPresigned(key string, _ time.Duration) string {
	return "https://cdn.example/" + key
}

func TestRenderReplyToImageUploadsAndReturnsURL(t *testing.T) {
	cfg := T2IConfig{Enabled: true, Endpoint: "https://render.example/t2i"}
	up := &fakeUploader{}
	rend := &fakeRenderer{data: []byte("PNG"), ct: "image/png"}

	url, err := RenderReplyToImage(context.Background(), cfg, rend, up, "t2i/7/1.png", "the reply")
	if err != nil {
		t.Fatalf("RenderReplyToImage: %v", err)
	}
	if url != "https://cdn.example/t2i/7/1.png" {
		t.Fatalf("url = %q", url)
	}
	if up.key != "t2i/7/1.png" || up.contentType != "image/png" || string(up.data) != "PNG" {
		t.Fatalf("uploaded %q/%q/%q", up.key, up.contentType, up.data)
	}
	if rend.got != "the reply" {
		t.Fatalf("renderer got %q", rend.got)
	}

	// Every failure mode is reported rather than silently producing an empty URL.
	for name, tc := range map[string]struct {
		rend T2IRenderer
		up   MediaUploader
	}{
		"render error": {&fakeRenderer{err: errors.New("boom")}, up},
		"empty image":  {&fakeRenderer{data: nil}, up},
		"upload error": {&fakeRenderer{data: []byte("PNG")}, &fakeUploader{err: errors.New("bucket down")}},
		"no uploader":  {&fakeRenderer{data: []byte("PNG")}, nil},
	} {
		if _, err := RenderReplyToImage(context.Background(), cfg, tc.rend, tc.up, "k", "t"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The whole point of the fallback: a broken render service must not cost the
// customer their answer.
func TestMaybeRenderReplyFallsBackToText(t *testing.T) {
	t.Setenv("T2I_ENDPOINT", "https://render.example/t2i")
	long := strings.Repeat("x", 400)
	cfg := &configCred{UserID: 7, Platform: "zalo"}

	for name, p := range map[string]*Pipeline{
		"renderer fails": {T2I: &fakeRenderer{err: errors.New("boom")}, mediaUploader: &fakeUploader{}, Logger: quietLogger()},
		"upload fails":   {T2I: &fakeRenderer{data: []byte("PNG")}, mediaUploader: &fakeUploader{err: errors.New("down")}, Logger: quietLogger()},
		"no uploader":    {T2I: &fakeRenderer{data: []byte("PNG")}, Logger: quietLogger()},
	} {
		content, payload := p.maybeRenderReply(context.Background(), cfg, long, nil)
		if content != long {
			t.Errorf("%s: content = %q, want the original text", name, content)
		}
		if payload != nil {
			t.Errorf("%s: payload = %v, want it untouched", name, payload)
		}
	}
}

func TestMaybeRenderReplyConvertsALongReply(t *testing.T) {
	t.Setenv("T2I_ENDPOINT", "https://render.example/t2i")
	long := strings.Repeat("x", 400)
	up := &fakeUploader{}
	p := &Pipeline{T2I: &fakeRenderer{data: []byte("PNG"), ct: "image/png"}, mediaUploader: up, Logger: quietLogger()}

	content, payload := p.maybeRenderReply(context.Background(), &configCred{UserID: 7, Platform: "zalo"}, long, nil)
	if content != "" {
		t.Fatalf("content = %q, want an empty caption", content)
	}
	if payload["kind"] != "media" || payload["media_type"] != "image" {
		t.Fatalf("payload = %v", payload)
	}
	url, _ := payload["media_url"].(string)
	if !strings.HasPrefix(url, "https://cdn.example/t2i/7/") {
		t.Fatalf("media_url = %q", url)
	}
	if !strings.HasSuffix(up.key, ".png") {
		t.Fatalf("object key = %q", up.key)
	}
}

// A template, a voice note or an already-attached image carries its own kind and
// must not be replaced by a picture of its caption.
func TestMaybeRenderReplyLeavesNonTextPayloadsAlone(t *testing.T) {
	t.Setenv("T2I_ENDPOINT", "https://render.example/t2i")
	long := strings.Repeat("x", 400)
	p := &Pipeline{T2I: &fakeRenderer{data: []byte("PNG")}, mediaUploader: &fakeUploader{}, Logger: quietLogger()}

	for _, kind := range []string{"template", "media", "buttons"} {
		payload := map[string]any{"kind": kind}
		content, got := p.maybeRenderReply(context.Background(), &configCred{UserID: 7, Platform: "zalo"}, long, payload)
		if content != long || got["kind"] != kind {
			t.Errorf("kind=%s was rewritten to %v/%v", kind, content, got)
		}
	}
}

// A short reply is not worth an image even on a capable channel.
func TestMaybeRenderReplyKeepsShortRepliesAsText(t *testing.T) {
	t.Setenv("T2I_ENDPOINT", "https://render.example/t2i")
	p := &Pipeline{T2I: &fakeRenderer{data: []byte("PNG")}, mediaUploader: &fakeUploader{}, Logger: quietLogger()}

	content, payload := p.maybeRenderReply(context.Background(), &configCred{UserID: 7, Platform: "zalo"}, "thanks!", nil)
	if content != "thanks!" || payload != nil {
		t.Fatalf("short reply was rewritten: %q/%v", content, payload)
	}
}

// The HTTP renderer is the production renderer: prove the whole path works over a
// real socket, from enqueue decision to stored object.
func TestRenderReplyToImageOverHTTP(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		seen, _ = body["text"].(string)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("REALPNG"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("T2I_ENDPOINT", srv.URL)

	up := &fakeUploader{}
	p := &Pipeline{mediaUploader: up, Logger: quietLogger()}
	long := strings.Repeat("y", 200)
	content, payload := p.maybeRenderReply(context.Background(), &configCred{UserID: 9, Platform: "whatsapp"}, long, nil)

	if content != "" || payload == nil {
		t.Fatalf("expected an image delivery, got %q/%v", content, payload)
	}
	if seen != long {
		t.Fatalf("renderer received %d chars, want %d", len(seen), len(long))
	}
	if string(up.data) != "REALPNG" || up.contentType != "image/png" {
		t.Fatalf("stored %q/%q", up.data, up.contentType)
	}
}
