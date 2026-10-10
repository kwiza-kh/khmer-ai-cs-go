package deepseek

// The tests pin what the client sends and how it reads what comes back. The
// stub is a chat-completions server: every assertion is about our own wire
// format, with no network and no credentials.

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

type stub struct {
	t        *testing.T
	server   *httptest.Server
	lastPath string
	lastAuth string
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
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q}}],`+
		`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"prompt_cache_hit_tokens":%d,"prompt_cache_miss_tokens":%d}}`,
		text, in, out, cached, in-cached)
}

func floatPtr(v float64) *float64 { return &v }

func TestChatMapsOurTurnOntoChatCompletions(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("សូស្តី!", 120, 7, 40)
	temp := 0.7
	svc := New(Config{
		APIKey:       "sk-ds-test",
		Model:        "deepseek-flash",
		SystemPrompt: "You are a shop assistant.",
		MaxTokens:    512,
		Temperature:  &temp,
		BaseURL:      st.server.URL,
	})

	history := []gemini.HistoryItem{
		{Role: "user", Content: "hello"},
		{Role: "agent", Content: "three left"},
		{Role: "model", Content: "thanks"},
		{Role: "system", Content: "dropped: no such role in the history mapping"},
	}
	res, err := svc.Chat(context.Background(), "how much?", history, "km")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if st.lastPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", st.lastPath)
	}
	if st.lastAuth != "Bearer sk-ds-test" {
		t.Errorf("Authorization = %q, want the bearer key", st.lastAuth)
	}
	if got, _ := st.lastBody["model"].(string); got != "deepseek-flash" {
		t.Errorf("model = %q", got)
	}
	if got, _ := st.lastBody["max_tokens"].(float64); int(got) != 512 {
		t.Errorf("max_tokens = %v, want 512", st.lastBody["max_tokens"])
	}
	if got, _ := st.lastBody["temperature"].(float64); got != 0.7 {
		t.Errorf("temperature = %v, want 0.7", st.lastBody["temperature"])
	}
	if res.PromptTokens != 120 || res.OutputTokens != 7 || res.CachedTokens != 40 {
		t.Errorf("usage = prompt %d / output %d / cached %d, want 120 / 7 / 40", res.PromptTokens, res.OutputTokens, res.CachedTokens)
	}

	// system is the first message and carries the language directive; the staff
	// reply keeps the Gemini client's marker and joins the preceding user turn.
	want := []struct{ role, content string }{
		{"system", "You are a shop assistant.\n\n[Language Preference] " + gemini.LanguageLabel("km")},
		{"user", "hello\n\n[Human agent reply] three left"},
		{"assistant", "thanks"},
		{"user", "how much?"},
	}
	msgs := st.messages(t)
	if len(msgs) != len(want) {
		t.Fatalf("messages = %d, want %d: %v", len(msgs), len(want), msgs)
	}
	for i, w := range want {
		if msgs[i]["role"] != w.role || msgs[i]["content"] != w.content {
			t.Errorf("message %d = %v/%v, want %s/%q", i, msgs[i]["role"], msgs[i]["content"], w.role, w.content)
		}
	}
}

func TestSystemPromptFallsBackToTheBuiltInDefault(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL}) // no SystemPrompt

	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	msgs := st.messages(t)
	if len(msgs) == 0 || msgs[0]["role"] != "system" {
		t.Fatalf("first message = %v, want a system message", msgs)
	}
	system, _ := msgs[0]["content"].(string)
	if !strings.HasPrefix(system, gemini.DefaultSystemPrompt) || !strings.HasSuffix(system, gemini.LanguageLabel("en")) {
		t.Errorf("system = %q; want the built-in default followed by the language directive", system)
	}
}

func TestPersonaOverrideReplacesTheConfiguredPrompt(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "k", SystemPrompt: "configured prompt", BaseURL: st.server.URL})

	if _, err := svc.ChatAs(context.Background(), "hi", nil, "en", "persona prompt"); err != nil {
		t.Fatalf("ChatAs: %v", err)
	}
	system, _ := st.messages(t)[0]["content"].(string)
	if !strings.HasPrefix(system, "persona prompt") || strings.Contains(system, "configured prompt") {
		t.Errorf("a persona must replace the configured prompt; system = %q", system)
	}
}

func TestThinkingIsDisabledForCatalogModelsOnly(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)

	svc := New(Config{APIKey: "k", Model: "deepseek-flash", BaseURL: st.server.URL})
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	thinking, _ := st.lastBody["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want type disabled: V4 thinks by default, spends output tokens and adds latency", st.lastBody["thinking"])
	}

	// A model the catalog does not know gets no field at all: the console refuses
	// such an id at save time, and an invented "disabled" could be a 400.
	unknown := New(Config{APIKey: "k", Model: "deepseek-unknown-9-9", BaseURL: st.server.URL})
	if _, err := unknown.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat (unknown model): %v", err)
	}
	if _, has := st.lastBody["thinking"]; has {
		t.Errorf("an uncatalogued model must not receive a thinking field: %v", st.lastBody["thinking"])
	}
}

func TestAuxCallsSendNoSystemPromptAndGreedySampling(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("42", 3, 1, 0)
	svc := New(Config{APIKey: "k", Model: "deepseek-flash", SystemPrompt: "customer persona", BaseURL: st.server.URL})

	out, ok := svc.GenerateFast(context.Background(), "classify this", 5*time.Second)
	if !ok || out != "42" {
		t.Fatalf("GenerateFast = (%q, %v), want (42, true)", out, ok)
	}
	msgs := st.messages(t)
	if len(msgs) != 1 || msgs[0]["role"] != "user" || msgs[0]["content"] != "classify this" {
		t.Errorf("auxiliary body = %v, want one user turn carrying the prompt", msgs)
	}
	if got, has := st.lastBody["temperature"].(float64); !has || got != 0 {
		t.Errorf("auxiliary temperature = %v, want an explicit 0 (greedy, as the Gemini aux path sends)", st.lastBody["temperature"])
	}
}

func TestReasoningContentIsNotReplyText(t *testing.T) {
	st := newStub(t)
	st.body = `{"choices":[{"message":{"reasoning_content":"private reasoning","content":"the answer"}}],` +
		`"usage":{"prompt_tokens":5,"completion_tokens":9,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":5}}`
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	res, err := svc.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Reply != "the answer" {
		t.Errorf("reply = %q, want only the content field", res.Reply)
	}
}

func TestPromptCountsEveryCachedPart(t *testing.T) {
	st := newStub(t)
	// prompt_tokens is the documented total (hit + miss); the cached part must be
	// carried separately because the cost arithmetic subtracts it.
	st.body = `{"choices":[{"message":{"content":"ok"}}],` +
		`"usage":{"prompt_tokens":260,"completion_tokens":3,"prompt_cache_hit_tokens":200,"prompt_cache_miss_tokens":60}}`
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	res, err := svc.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.PromptTokens != 260 || res.CachedTokens != 200 || res.OutputTokens != 3 {
		t.Errorf("usage = prompt %d / cached %d / output %d, want 260 / 200 / 3", res.PromptTokens, res.CachedTokens, res.OutputTokens)
	}
}

func TestStreamDeliversDeltasAndReadsUsageFromTheLastChunk(t *testing.T) {
	st := newStub(t)
	st.stream = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"សូ\"},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"ស្តី\"},\"finish_reason\":null}],\"usage\":null}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":9,\"prompt_cache_hit_tokens\":4,\"prompt_cache_miss_tokens\":13}}\n\n" +
		"data: [DONE]\n\n"
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	var tokens []string
	res, err := svc.ChatStream(context.Background(), "hi", nil, "km", func(tok string) { tokens = append(tokens, tok) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if res.Reply != "សូស្តី" {
		t.Errorf("reply = %q, want the concatenated deltas", res.Reply)
	}
	if strings.Join(tokens, "") != res.Reply {
		t.Errorf("onToken saw %v, want the same text as the reply %q", tokens, res.Reply)
	}
	if res.PromptTokens != 17 || res.CachedTokens != 4 || res.OutputTokens != 9 {
		t.Errorf("usage = prompt %d / cached %d / output %d, want 17 / 4 / 9", res.PromptTokens, res.CachedTokens, res.OutputTokens)
	}
}

func TestAStreamWithoutDoneIsAFailureNotAShortAnswer(t *testing.T) {
	st := newStub(t)
	st.stream = "data: {\"choices\":[{\"delta\":{\"content\":\"half\"},\"finish_reason\":null}],\"usage\":null}\n\n"
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	res, err := svc.ChatStream(context.Background(), "hi", nil, "en", nil)
	if err == nil {
		t.Fatal("a stream that ended without [DONE] returned a truncated answer as success")
	}
	if res.Reply != "half" {
		t.Errorf("partial text should still be reported for billing, got %q", res.Reply)
	}
}

func TestAStreamErrorFrameFailsTheTurn(t *testing.T) {
	st := newStub(t)
	st.stream = "data: {\"error\":{\"message\":\"rate limited\",\"type\":\"rate_limit_error\"}}\n\n"
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	if _, err := svc.ChatStream(context.Background(), "hi", nil, "en", nil); err == nil {
		t.Fatal("a mid-stream error frame was swallowed")
	}
}

func TestHTTPErrorCarriesTheBody(t *testing.T) {
	st := newStub(t)
	st.status = http.StatusUnauthorized
	st.body = `{"error":{"message":"Authentication Fails","type":"authentication_error"}}`
	svc := New(Config{APIKey: "bad", Model: "deepseek-flash", BaseURL: st.server.URL})

	_, err := svc.Chat(context.Background(), "hi", nil, "en")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Authentication Fails") {
		t.Fatalf("error = %v, want the status and the API's message", err)
	}
}

func TestNothingToSendIsRefusedBeforeTheWire(t *testing.T) {
	st := newStub(t)
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	if _, err := svc.Chat(context.Background(), "   ", nil, "en"); err == nil {
		t.Fatal("an empty turn must fail before any request")
	}
	if st.lastPath != "" {
		t.Errorf("an empty turn reached the wire: %q", st.lastPath)
	}
}

func TestAnUnconfiguredClientFailsLoudly(t *testing.T) {
	svc := New(Config{Model: "deepseek-flash"}) // no key, no base URL
	if svc.IsConfigured() {
		t.Error("a client with no key reported itself configured")
	}
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err == nil {
		t.Error("an unconfigured client answered a call")
	}
}

func TestCatalogIsTheVerifiedModelList(t *testing.T) {
	for _, id := range []string{"deepseek-flash", "deepseek-v4-pro"} {
		m, ok := Lookup(id)
		if !ok || !m.Sampling || !m.ThinkingOff {
			t.Errorf("Lookup(%q) = %+v, %v; want a catalogued model that takes sampling and has thinking off", id, m, ok)
		}
	}
	for _, id := range []string{"deepseek-v4-flash", "deepseek-flash-latest", "DEEPSEEK-FLASH", ""} {
		if _, ok := Lookup(id); ok {
			t.Errorf("Lookup(%q) found an entry; only the exact documented ids are catalog models", id)
		}
	}
	// Catalog returns a copy: a caller cannot edit the verified list.
	c := Catalog()
	c[0].ID = "tampered"
	if _, ok := Lookup("deepseek-flash"); !ok {
		t.Error("mutating the slice Catalog returned changed the catalog")
	}
}

func TestReconfigureSwapsTheWholeConfiguration(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "old", Model: "deepseek-flash", BaseURL: st.server.URL})

	svc.Reconfigure(Config{APIKey: "new", Model: "deepseek-flash", SystemPrompt: "second prompt", MaxTokens: 77, BaseURL: st.server.URL})
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat after reconfigure: %v", err)
	}
	if st.lastAuth != "Bearer new" {
		t.Errorf("Authorization = %q, want the reconfigured key", st.lastAuth)
	}
	if got, _ := st.lastBody["max_tokens"].(float64); int(got) != 77 {
		t.Errorf("max_tokens = %v, want 77", st.lastBody["max_tokens"])
	}
	system, _ := st.messages(t)[0]["content"].(string)
	if !strings.HasPrefix(system, "second prompt") {
		t.Errorf("system = %q, want the reconfigured prompt", system)
	}
}

func TestAuxUsageIsReportedToThePlatformObserver(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 11, 3, 2)
	svc := New(Config{APIKey: "k", Model: "deepseek-flash", BaseURL: st.server.URL})

	var seenModel string
	var seenPrompt, seenCompletion, seenCached int
	prev := gemini.AuxUsageObserver
	gemini.AuxUsageObserver = func(_ context.Context, model string, prompt, completion, cached int) {
		seenModel, seenPrompt, seenCompletion, seenCached = model, prompt, completion, cached
	}
	t.Cleanup(func() { gemini.AuxUsageObserver = prev })

	if _, ok := svc.GenerateFast(context.Background(), "classify", time.Second); !ok {
		t.Fatal("GenerateFast failed")
	}
	if seenModel != "deepseek-flash" || seenPrompt != 11 || seenCompletion != 3 || seenCached != 2 {
		t.Errorf("observer saw %s/%d/%d/%d, want deepseek-flash/11/3/2", seenModel, seenPrompt, seenCompletion, seenCached)
	}
}
