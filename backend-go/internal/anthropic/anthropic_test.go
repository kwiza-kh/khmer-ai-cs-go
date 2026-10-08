package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/gemini"
)

// stub is a Messages API server: it records the last request and answers with a
// canned body/stream, so every assertion here is about what WE send and how we
// read what comes back — no network, no credentials.
type stub struct {
	t        *testing.T
	server   *httptest.Server
	lastPath string
	lastAuth string
	lastKey  string
	lastVer  string
	lastBody map[string]any
	status   int
	body     string
	stream   string
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{t: t, status: http.StatusOK}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastPath = r.URL.Path
		s.lastAuth = r.Header.Get("Authorization")
		s.lastKey = r.Header.Get("x-api-key")
		s.lastVer = r.Header.Get("anthropic-version")
		// Decode into a fresh map: decoding into the previous request's map keeps
		// the keys that request set, and a test would read them as this one's.
		s.lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&s.lastBody)
		w.Header().Set("Content-Type", "application/json")
		if s.stream != "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.WriteHeader(s.status)
		if s.stream != "" {
			_, _ = w.Write([]byte(s.stream))
			return
		}
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stub) text(t *testing.T, field string) string {
	t.Helper()
	v, _ := s.lastBody[field].(string)
	return v
}

func (s *stub) messages(t *testing.T) []map[string]any {
	t.Helper()
	raw, _ := s.lastBody["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func responseJSON(text string, in, out, cached int) string {
	return fmt.Sprintf(`{"content":[{"type":"text","text":%q}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d}}`,
		text, in, out, cached)
}

func TestChatMapsOurTurnOntoTheMessagesAPI(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("សូស្តី!", 120, 7, 40)
	temp := 0.7
	svc := New(Config{
		APIKey:       "sk-ant-test",
		Model:        "claude-haiku-5-5",
		SystemPrompt: "You are a shop assistant.",
		MaxTokens:    512,
		Temperature:  &temp,
		BaseURL:      st.server.URL,
	})

	history := []gemini.HistoryItem{
		{Role: "user", Content: "hello"},
		{Role: "model", Content: "hi"}, // Gemini's assistant role must be translated
		{Role: "system", Content: "ignored"},
	}
	res, err := svc.Chat(context.Background(), "how much?", history, "km")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if st.lastPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", st.lastPath)
	}
	if st.lastKey != "sk-ant-test" {
		t.Errorf("x-api-key = %q", st.lastKey)
	}
	if st.lastVer != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", st.lastVer, anthropicVersion)
	}
	if got := st.text(t, "model"); got != "claude-haiku-5-5" {
		t.Errorf("model = %q", got)
	}
	if got, _ := st.lastBody["max_tokens"].(float64); int(got) != 512 {
		t.Errorf("max_tokens = %v, want 512", st.lastBody["max_tokens"])
	}
	// Haiku 5.5 answers 400 to a non-default temperature on every call, so the
	// configured 0.7 stays in the row and is never sent to this model.
	if _, sent := st.lastBody["temperature"]; sent {
		t.Errorf("temperature sent to a model that does not take sampling parameters: %v", st.lastBody["temperature"])
	}
	system := st.text(t, "system")
	if !strings.Contains(system, "shop assistant") {
		t.Errorf("system lost the configured prompt: %q", system)
	}
	// The reply language must still follow the customer (the 2026-09-14 fix).
	if !strings.Contains(system, gemini.LanguageLabel("km")) {
		t.Errorf("system missing the language directive: %q", system)
	}
	msgs := st.messages(t)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (system turn dropped, assistant renamed)", len(msgs))
	}
	if msgs[0]["role"] != "user" || msgs[1]["role"] != "assistant" || msgs[2]["role"] != "user" {
		t.Errorf("roles = %v/%v/%v, want user/assistant/user", msgs[0]["role"], msgs[1]["role"], msgs[2]["role"])
	}
	if msgs[2]["content"] != "how much?" {
		t.Errorf("last message = %v, want the new turn", msgs[2]["content"])
	}

	if res.Reply != "សូស្តី!" {
		t.Errorf("reply = %q", res.Reply)
	}
	// The API reports 120 uncached input tokens and 40 read from the cache. The
	// prompt count includes the cached part, as it does for Gemini: 160.
	if res.PromptTokens != 160 || res.OutputTokens != 7 || res.CachedTokens != 40 {
		t.Errorf("usage = %d/%d/%d, want 160/7/40", res.PromptTokens, res.OutputTokens, res.CachedTokens)
	}
}

func TestChatStreamEmitsDeltasThenUsage(t *testing.T) {
	st := newStub(t)
	st.stream = strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":88,"cache_read_input_tokens":12}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	svc := New(Config{APIKey: "k", Model: "claude-haiku-5-5", BaseURL: st.server.URL})
	var got []string
	res, err := svc.ChatStream(context.Background(), "hi", nil, "en", func(tok string) { got = append(got, tok) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if strings.Join(got, "") != "Hello" || res.Reply != "Hello" {
		t.Errorf("deltas = %v, reply = %q", got, res.Reply)
	}
	// 88 uncached + 12 cached: the prompt count includes the cached part.
	if res.PromptTokens != 100 || res.OutputTokens != 5 || res.CachedTokens != 12 {
		t.Errorf("usage = %d/%d/%d, want 100/5/12", res.PromptTokens, res.OutputTokens, res.CachedTokens)
	}
	if stream, _ := st.lastBody["stream"].(bool); !stream {
		t.Error("direct transport must ask for stream:true")
	}
}

func TestChatStreamEndingEarlyIsAnError(t *testing.T) {
	st := newStub(t)
	st.stream = strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"trunc"}}`,
		``,
	}, "\n")
	svc := New(Config{APIKey: "k", Model: "m", BaseURL: st.server.URL})
	if _, err := svc.ChatStream(context.Background(), "hi", nil, "en", nil); err == nil {
		t.Fatal("a stream without message_stop must not look like a complete reply")
	}
}

func TestGenerateFastReportsAuxUsage(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("  short answer  ", 30, 4, 0)
	svc := New(Config{APIKey: "k", Model: "claude-haiku-5-5", BaseURL: st.server.URL})

	var seenModel string
	var seenIn, seenOut int
	prev := gemini.AuxUsageObserver
	gemini.AuxUsageObserver = func(ctx context.Context, model string, prompt, completion, cached int) {
		seenModel, seenIn, seenOut = model, prompt, completion
	}
	t.Cleanup(func() { gemini.AuxUsageObserver = prev })

	out, ok := svc.GenerateFast(context.Background(), "summarise", 5*time.Second)
	if !ok || strings.TrimSpace(out) != "short answer" {
		t.Fatalf("GenerateFast = (%q, %v)", out, ok)
	}
	if seenModel != "claude-haiku-5-5" || seenIn != 30 || seenOut != 4 {
		t.Errorf("aux usage = (%q, %d, %d), want (claude-haiku-5-5, 30, 4)", seenModel, seenIn, seenOut)
	}
}

func TestUnconfiguredProviderFailsLoudly(t *testing.T) {
	svc := New(Config{Model: "claude-haiku-5-5"})
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err == nil {
		t.Fatal("an unconfigured provider returned a reply; it must fail loudly, never silently mock")
	}
	if _, ok := svc.GenerateFast(context.Background(), "hi", time.Second); ok {
		t.Fatal("GenerateFast reported success while unconfigured")
	}
}

func TestHotReloadSwapsModelAndKey(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "old", Model: "claude-haiku-5-5", BaseURL: st.server.URL})
	svc.HotReload("new-key", "claude-sonnet-5-5", "sys", 1234)
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat after reload: %v", err)
	}
	if st.lastKey != "new-key" {
		t.Errorf("x-api-key = %q, want the reloaded key", st.lastKey)
	}
	if got := st.text(t, "model"); got != "claude-sonnet-5-5" {
		t.Errorf("model = %q, want the reloaded model", got)
	}
	if got, _ := st.lastBody["max_tokens"].(float64); int(got) != 1234 {
		t.Errorf("max_tokens = %v, want 1234", st.lastBody["max_tokens"])
	}
}
