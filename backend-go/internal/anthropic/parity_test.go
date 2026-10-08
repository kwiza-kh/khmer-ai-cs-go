package anthropic

// parity_test.go pins what the Claude client does for one turn. Where the client
// must agree with the Gemini client it is checked against the same constants;
// where the Messages API differs, the test states what the API requires.

import (
	"context"
	"strings"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/gemini"
)

// withCatalog adds models to the catalog for one test. The catalog is package
// state, so the test puts the original back when it ends.
func withCatalog(t *testing.T, extra ...Model) {
	t.Helper()
	prev := catalog
	catalog = append(append([]Model(nil), catalog...), extra...)
	t.Cleanup(func() { catalog = prev })
}

func floatPtr(v float64) *float64 { return &v }

func prefixOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func suffixOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestSystemPromptFallsBackToTheBuiltInDefault(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL}) // no SystemPrompt

	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	system := st.text(t, "system")
	if !strings.HasPrefix(system, gemini.DefaultSystemPrompt) {
		t.Errorf("an empty configured prompt must fall back to the built-in default; system starts %q", prefixOf(system, 40))
	}
	if !strings.HasSuffix(system, gemini.LanguageLabel("en")) {
		t.Errorf("the language directive must follow the default prompt; system ends %q", suffixOf(system, 60))
	}
}

func TestPersonaOverrideReplacesTheConfiguredPrompt(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "k", SystemPrompt: "configured prompt", BaseURL: st.server.URL})

	if _, err := svc.ChatAs(context.Background(), "hi", nil, "en", "persona prompt"); err != nil {
		t.Fatalf("ChatAs: %v", err)
	}
	system := st.text(t, "system")
	if !strings.HasPrefix(system, "persona prompt") || strings.Contains(system, "configured prompt") {
		t.Errorf("a persona must replace the configured prompt; system starts %q", prefixOf(system, 80))
	}
}

func TestAuxCallsSendNoSystemPromptAndNoSampling(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("42", 3, 1, 0)
	svc := New(Config{APIKey: "k", Model: "claude-haiku-5-5", SystemPrompt: "customer persona", BaseURL: st.server.URL})

	out, ok := svc.GenerateFast(context.Background(), "classify this", 5*time.Second)
	if !ok || out != "42" {
		t.Fatalf("GenerateFast = (%q, %v), want (42, true)", out, ok)
	}
	if _, has := st.lastBody["system"]; has {
		t.Errorf("an auxiliary call must not carry the customer-service prompt: %v", st.lastBody["system"])
	}
	if _, has := st.lastBody["temperature"]; has {
		t.Error("haiku takes no sampling parameters, auxiliary calls included")
	}
	msgs := st.messages(t)
	if len(msgs) != 1 || msgs[0]["role"] != "user" || msgs[0]["content"] != "classify this" {
		t.Errorf("auxiliary body = %v, want one user turn carrying the prompt", msgs)
	}
}

func TestSamplingIsSentOnlyWhereTheCatalogAllowsIt(t *testing.T) {
	withCatalog(t, Model{ID: "test-sampling", DisplayName: "test", Sampling: true})
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "k", Model: "test-sampling", Temperature: floatPtr(0.4), BaseURL: st.server.URL})

	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got, _ := st.lastBody["temperature"].(float64); got != 0.4 {
		t.Errorf("chat temperature = %v, want 0.4 for a model that takes sampling", st.lastBody["temperature"])
	}
	if _, ok := svc.GenerateFast(context.Background(), "classify", time.Second); !ok {
		t.Fatal("GenerateFast failed")
	}
	if got, ok := st.lastBody["temperature"].(float64); !ok || got != 0 {
		t.Errorf("auxiliary temperature = %v, want an explicit 0 (greedy, as the Gemini aux path sends)", st.lastBody["temperature"])
	}
}

func TestThinkingIsOffForCatalogModelsOnly(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)

	svc := New(Config{APIKey: "k", Model: "claude-haiku-5-5", BaseURL: st.server.URL})
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	thinking, _ := st.lastBody["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want type disabled: adaptive thinking spends max_tokens and is billed as output", st.lastBody["thinking"])
	}

	// A model the catalog does not know gets the platform default, and no field
	// at all: an explicit "disabled" on a model that rejects it is a 400.
	unknown := New(Config{APIKey: "k", Model: "claude-unknown-9-9", BaseURL: st.server.URL})
	if _, err := unknown.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat (unknown model): %v", err)
	}
	if _, has := st.lastBody["thinking"]; has {
		t.Errorf("an uncatalogued model must not receive a thinking field: %v", st.lastBody["thinking"])
	}
}

func TestHumanAgentTurnsKeepTheirMarkerAndConsecutiveTurnsJoin(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	history := []gemini.HistoryItem{
		{Role: "user", Content: "is it in stock?"},
		{Role: "agent", Content: "yes, three left"},
		{Role: "model", Content: "thanks for waiting"},
		{Role: "model", Content: "anything else?"},
		{Role: "system", Content: "dropped: no such role on this API"},
	}
	if _, err := svc.Chat(context.Background(), "reserve one", history, "en"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// The marker text is the Gemini client's own ("[Human agent reply] "): a staff
	// reply must read the same to the model on either provider.
	want := []struct{ role, content string }{
		{"user", "is it in stock?\n\n[Human agent reply] yes, three left"},
		{"assistant", "thanks for waiting\n\nanything else?"},
		{"user", "reserve one"},
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

func TestPromptCountsEveryCachedPart(t *testing.T) {
	st := newStub(t)
	st.body = `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":200,"cache_creation_input_tokens":50}}`
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	res, err := svc.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// Uncached input 10 + cache read 200 + cache write 50: the cost arithmetic
	// subtracts the cached part from the prompt, so all of it must be in the prompt.
	if res.PromptTokens != 260 || res.CachedTokens != 200 || res.OutputTokens != 3 {
		t.Errorf("usage = prompt %d / cached %d / output %d, want 260 / 200 / 3", res.PromptTokens, res.CachedTokens, res.OutputTokens)
	}
}

func TestThinkingBlocksAreNotReplyText(t *testing.T) {
	st := newStub(t)
	st.body = `{"content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"the answer"}],"usage":{"input_tokens":5,"output_tokens":9}}`
	svc := New(Config{APIKey: "k", BaseURL: st.server.URL})

	res, err := svc.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Reply != "the answer" {
		t.Errorf("reply = %q, want only the text block", res.Reply)
	}
}

func TestCatalogIsTheVerifiedModelList(t *testing.T) {
	m, ok := Lookup("claude-haiku-5-5")
	if !ok || m.Sampling || !m.ThinkingOff {
		t.Fatalf("Lookup(claude-haiku-5-5) = %+v, %v; want a catalog entry with sampling off and thinking off", m, ok)
	}
	for _, id := range []string{"claude-haiku-4-5", "claude-haiku-5-5-latest", "CLAUDE-HAIKU-5-5", ""} {
		if _, ok := Lookup(id); ok {
			t.Errorf("Lookup(%q) found an entry; only the exact documented id is a catalog model", id)
		}
	}
	// Catalog returns a copy: a caller cannot edit the verified list.
	c := Catalog()
	c[0].ID = "tampered"
	if _, ok := Lookup("claude-haiku-5-5"); !ok {
		t.Error("mutating the slice Catalog returned changed the catalog")
	}
}

func TestReconfigureSwapsTheWholeConfiguration(t *testing.T) {
	st := newStub(t)
	st.body = responseJSON("ok", 1, 1, 0)
	svc := New(Config{APIKey: "old", Model: "claude-haiku-5-5", BaseURL: st.server.URL})

	svc.Reconfigure(Config{APIKey: "new", Model: "claude-haiku-5-5", SystemPrompt: "second prompt", MaxTokens: 77, BaseURL: st.server.URL})
	if _, err := svc.Chat(context.Background(), "hi", nil, "en"); err != nil {
		t.Fatalf("Chat after reconfigure: %v", err)
	}
	if st.lastKey != "new" {
		t.Errorf("x-api-key = %q, want the reconfigured key", st.lastKey)
	}
	if got, _ := st.lastBody["max_tokens"].(float64); int(got) != 77 {
		t.Errorf("max_tokens = %v, want 77", st.lastBody["max_tokens"])
	}
	if !strings.HasPrefix(st.text(t, "system"), "second prompt") {
		t.Errorf("system = %q, want the reconfigured prompt", prefixOf(st.text(t, "system"), 40))
	}
}
