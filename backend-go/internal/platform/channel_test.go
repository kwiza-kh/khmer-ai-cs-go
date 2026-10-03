package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// These tests exercise the Channel implementations end to end over HTTP: the
// provider base URLs are variables (clients.go) so an httptest server can stand
// in for Telegram, LINE, Zalo and the Graph API, and the SSRF-guarded media
// fetcher is swapped for a plain one so a loopback CDN works.

func overrideBase(t *testing.T, target *string, srv *httptest.Server) {
	t.Helper()
	old := *target
	*target = srv.URL
	t.Cleanup(func() { *target = old })
}

func overrideMediaFetch(t *testing.T) {
	t.Helper()
	old := downloadMediaBytes
	downloadMediaBytes = func(ctx context.Context, u string) ([]byte, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", err
		}
		return data, resp.Header.Get("Content-Type"), nil
	}
	t.Cleanup(func() { downloadMediaBytes = old })
}

type recordedCall struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

type recordingServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []recordedCall
}

// newRecordingServer answers every request through respond and records it.
func newRecordingServer(t *testing.T, respond func(r *http.Request, n int) (int, map[string]string, string)) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rs.mu.Lock()
		rs.calls = append(rs.calls, recordedCall{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		n := len(rs.calls)
		rs.mu.Unlock()
		status, headers, payload := respond(r, n)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *recordingServer) recorded() []recordedCall {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]recordedCall, len(rs.calls))
	copy(out, rs.calls)
	return out
}

func okJSON(payload string) func(*http.Request, int) (int, map[string]string, string) {
	return func(*http.Request, int) (int, map[string]string, string) {
		return http.StatusOK, map[string]string{"Content-Type": "application/json"}, payload
	}
}

func TestNewChannelDispatch(t *testing.T) {
	p := &Pipeline{}
	for _, tc := range []struct{ platform, wantType string }{
		{"telegram", "*platform.telegramChannel"},
		{"line", "*platform.lineChannel"},
		{"zalo", "*platform.zaloChannel"},
		{"meta", "*platform.metaChannel"},
		{"instagram", "*platform.metaChannel"},
		{"whatsapp", "*platform.metaChannel"},
	} {
		ch, err := NewChannel(p, &configCred{Platform: tc.platform})
		if err != nil {
			t.Fatalf("%s: %v", tc.platform, err)
		}
		if got := fmt.Sprintf("%T", ch); got != tc.wantType {
			t.Errorf("%s: channel type = %s, want %s", tc.platform, got, tc.wantType)
		}
		if got := ch.Caps().Platform; got != tc.platform {
			t.Errorf("%s: Caps().Platform = %s", tc.platform, got)
		}
	}
	if _, err := NewChannel(p, &configCred{Platform: "myspace"}); err == nil {
		t.Fatal("an unimplemented platform must return an error, not a half-built channel")
	}
	if _, err := NewChannel(p, nil); err == nil {
		t.Fatal("nil config must return an error")
	}
}

// A Telegram text longer than the 4096-rune cap must arrive as several
// sendMessage calls, with the inline keyboard on the first one only.
func TestTelegramChannelChunksTextAndButtonsOnlyOnFirstChunk(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"ok":true,"result":{"message_id":42}}`))
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "chat-1",
		Kind:        "text",
		Content:     strings.Repeat("x", 4097), // 2 chunks, no spaces to break on
		Buttons:     [][2]string{{"Yes", "yes"}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "chat-1:42" {
		t.Fatalf("provider id = %q, want chat-1:42", id)
	}

	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2 (one per chunk)", len(calls))
	}
	for i, c := range calls {
		if c.Path != "/botT/sendMessage" {
			t.Errorf("call %d path = %s", i, c.Path)
		}
		if c.Body["chat_id"] != "chat-1" {
			t.Errorf("call %d chat_id = %v", i, c.Body["chat_id"])
		}
	}
	if calls[0].Body["reply_markup"] == nil {
		t.Error("first chunk must carry the inline keyboard")
	}
	if calls[1].Body["reply_markup"] != nil {
		t.Error("later chunks must not repeat the inline keyboard")
	}
}

func TestTelegramChannelSendAudioUsesAudioEndpoint(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"ok":true,"result":{"message_id":7}}`))
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "chat-9",
		Kind:        "media",
		MediaType:   "audio",
		MediaURL:    "https://cdn.example/voice.ogg",
		Content:     "caption",
	})
	if err != nil {
		t.Fatalf("send audio: %v", err)
	}
	if id != "chat-9:7" {
		t.Fatalf("provider id = %q", id)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Path != "/botT/sendAudio" {
		t.Fatalf("calls = %+v, want a single sendAudio", calls)
	}
	if calls[0].Body["audio"] != "https://cdn.example/voice.ogg" {
		t.Errorf("audio field = %v", calls[0].Body["audio"])
	}
}

func TestTelegramChannelTyping(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"ok":true,"result":true}`))
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	if err := ch.Typing(context.Background(), "chat-2"); err != nil {
		t.Fatalf("typing: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Path != "/botT/sendChatAction" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Body["action"] != "typing" {
		t.Errorf("action = %v", calls[0].Body["action"])
	}
	// An empty recipient must not reach the provider at all.
	if err := ch.Typing(context.Background(), ""); err != nil {
		t.Fatalf("empty recipient: %v", err)
	}
	if got := len(rs.recorded()); got != 1 {
		t.Fatalf("empty recipient issued a request (%d total)", got)
	}
}

func TestTelegramChannelDownloadMedia(t *testing.T) {
	rs := newRecordingServer(t, func(r *http.Request, _ int) (int, map[string]string, string) {
		if strings.HasPrefix(r.URL.Path, "/file/botT/") {
			return http.StatusOK, map[string]string{"Content-Type": "audio/ogg"}, "OGGDATA"
		}
		return http.StatusOK, map[string]string{"Content-Type": "application/json"},
			`{"ok":true,"result":{"file_path":"voice/1.ogg"}}`
	})
	overrideBase(t, &telegramBase, rs.srv)
	overrideMediaFetch(t)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	data, mime, err := ch.DownloadMedia(context.Background(), "file-1", "", "audio/ogg")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(data) != "OGGDATA" {
		t.Fatalf("data = %q", data)
	}
	if !strings.Contains(mime, "audio/ogg") {
		t.Fatalf("mime = %q", mime)
	}
	calls := rs.recorded()
	if calls[0].Path != "/botT/getFile" || calls[0].Body["file_id"] != "file-1" {
		t.Fatalf("getFile call = %+v", calls[0])
	}
	if len(calls) != 2 || calls[1].Path != "/file/botT/voice/1.ogg" {
		t.Fatalf("second call = %+v", calls)
	}
	// No provider id and no URL: nothing to fetch, and no request either.
	data, _, err = ch.DownloadMedia(context.Background(), "", "", "")
	if err != nil || data != nil {
		t.Fatalf("empty media must be a no-op, got %q/%v", data, err)
	}
	if got := len(rs.recorded()); got != 2 {
		t.Fatalf("empty media issued a request (%d total)", got)
	}
}

func TestFeedbackButtonsHelper(t *testing.T) {
	d := &outboundDelivery{LastMessageID: 5, Payload: map[string]any{"feedback": true}}
	got, attached := feedbackButtons(d, nil)
	if !attached || len(got) != 2 {
		t.Fatalf("feedback = %v/%v", got, attached)
	}
	if got[0][1] != "fb:5:1" || got[1][1] != "fb:5:-1" {
		t.Fatalf("callback data = %v", got)
	}
	// The labels are the actual emoji, not HTML entities or mojibake: a tool that
	// guesses the wrong text encoding turns them into garbage silently.
	if got[0][0] != "👍" || got[1][0] != "👎" {
		t.Fatalf("button labels = %q/%q", got[0][0], got[1][0])
	}
	if _, attached := feedbackButtons(d, [][2]string{{"x", "y"}}); attached {
		t.Error("an explicit keyboard must win over the CSAT pair")
	}
	if _, attached := feedbackButtons(&outboundDelivery{LastMessageID: 0, Payload: d.Payload}, nil); attached {
		t.Error("with no previous message there is nothing to vote on")
	}
	if _, attached := feedbackButtons(&outboundDelivery{LastMessageID: 5}, nil); attached {
		t.Error("feedback must be opt-in per delivery")
	}
	if _, attached := feedbackButtons(nil, nil); attached {
		t.Error("a nil delivery must not panic")
	}
}

func TestLineChannelRejectsMediaAndPushesChunks(t *testing.T) {
	rs := newRecordingServer(t, func(*http.Request, int) (int, map[string]string, string) {
		return http.StatusOK, map[string]string{"X-Line-Request-Id": "req-1"}, ""
	})
	overrideBase(t, &lineBase, rs.srv)

	ch := &lineChannel{p: &Pipeline{}, cfg: &configCred{Platform: "line", AccessToken: "tok"}}
	if _, err := ch.Send(context.Background(), ChannelMessage{
		RecipientID: "U1", Kind: "media", MediaType: "image", MediaURL: "https://cdn/x.png",
	}); err == nil {
		t.Fatal("LINE media must still be rejected (the capability table declares text-only)")
	}
	if got := len(rs.recorded()); got != 0 {
		t.Fatalf("rejected media still reached the provider (%d requests)", got)
	}

	id, err := ch.Send(context.Background(), ChannelMessage{
		RecipientID: "U1", Kind: "text", Content: strings.Repeat("y", 5001),
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if id != "req-1" {
		t.Fatalf("request id = %q", id)
	}
	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2 chunks for 5001 runes at a 5000 cap", len(calls))
	}
	for i, c := range calls {
		if c.Path != "/v2/bot/message/push" {
			t.Errorf("call %d path = %s", i, c.Path)
		}
		if c.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("call %d auth = %q", i, c.Header.Get("Authorization"))
		}
		if c.Body["to"] != "U1" {
			t.Errorf("call %d to = %v", i, c.Body["to"])
		}
	}
}

func TestLineChannelTyping(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{}`))
	overrideBase(t, &lineBase, rs.srv)

	ch := &lineChannel{p: &Pipeline{}, cfg: &configCred{Platform: "line", AccessToken: "tok"}}
	if err := ch.Typing(context.Background(), "U1"); err != nil {
		t.Fatalf("typing: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Path != "/v2/bot/chat/U1/typing" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestZaloChannelSendsImageAttachment(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"error":0,"data":{"msg_id":"z-1"}}`))
	overrideBase(t, &zaloBase, rs.srv)

	ch := &zaloChannel{p: &Pipeline{}, cfg: &configCred{Platform: "zalo", AccessToken: "tok"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		RecipientID: "u-9", Kind: "media", MediaType: "image", MediaURL: "https://cdn/x.png",
	})
	if err != nil {
		t.Fatalf("send image: %v", err)
	}
	if id != "z-1" {
		t.Fatalf("msg id = %q", id)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Path != "/v3.0/oa/message/cs" {
		t.Fatalf("calls = %+v", calls)
	}
	msg, _ := calls[0].Body["message"].(map[string]any)
	if msg["type"] != "image" {
		t.Fatalf("message = %+v, want an image attachment", msg)
	}
}

func TestZaloChannelTypingIsANoop(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"error":0}`))
	overrideBase(t, &zaloBase, rs.srv)

	ch := &zaloChannel{p: &Pipeline{}, cfg: &configCred{Platform: "zalo", AccessToken: "tok"}}
	if err := ch.Typing(context.Background(), "u-9"); err != nil {
		t.Fatalf("typing: %v", err)
	}
	if got := len(rs.recorded()); got != 0 {
		t.Fatalf("Zalo has no typing endpoint, but %d request(s) were sent", got)
	}
}

func TestMetaChannelWhatsAppSendsToMessagesEndpoint(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"wamid.1"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	ch := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "whatsapp", AccessToken: "tok", PageID: "WABA-1"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:         &outboundDelivery{},
		RecipientID:      "15551234",
		Kind:             "template",
		TemplateName:     "hello_world",
		TemplateLanguage: "en_US",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "wamid.1" {
		t.Fatalf("provider id = %q", id)
	}
	calls := rs.recorded()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if !strings.HasSuffix(calls[0].Path, "/messages") || !strings.Contains(calls[0].Path, "WABA-1") {
		t.Fatalf("path = %s, want …/WABA-1/messages", calls[0].Path)
	}
}

func TestMetaChannelTypingIsSkippedOnWhatsApp(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	wa := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "whatsapp", AccessToken: "tok", PageID: "WABA-1"}}
	if err := wa.Typing(context.Background(), "15551234"); err != nil {
		t.Fatalf("whatsapp typing: %v", err)
	}
	if got := len(rs.recorded()); got != 0 {
		t.Fatalf("WhatsApp has no sender action, but %d request(s) were sent", got)
	}

	msg := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "meta", AccessToken: "tok", PageID: "PAGE-1"}}
	if err := msg.Typing(context.Background(), "psid-1"); err != nil {
		t.Fatalf("messenger typing: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 1 {
		t.Fatalf("messenger typing calls = %d", len(calls))
	}
	if !strings.Contains(calls[0].Path, "PAGE-1") || calls[0].Body["sender_action"] != "typing_on" {
		t.Fatalf("messenger typing call = %+v", calls[0])
	}
}

// Capabilities must follow the config row, not the channel type: the three
// Graph-API channels share one implementation but not one window.
func TestChannelCapsFollowThePlatform(t *testing.T) {
	p := &Pipeline{}
	for platform, wantWindowless := range map[string]bool{
		"telegram": true, "line": true, "zalo": true,
		"meta": false, "instagram": false, "whatsapp": false,
	} {
		ch, err := NewChannel(p, &configCred{Platform: platform})
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
		if got := ch.Caps().Windowless; got != wantWindowless {
			t.Errorf("%s: Windowless = %v, want %v", platform, got, wantWindowless)
		}
	}
}
