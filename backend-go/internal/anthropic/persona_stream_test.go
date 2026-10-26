package anthropic

import (
	"context"
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
)

// ChatStreamAs is the streaming half of the persona override: the console's /chat,
// /chat/stream, /ai-test and the website widget all stream, so without it a persona
// bound to a console session or to a widget conversation applied everywhere except
// the screens an operator checks it on.
//
// Same rule as ChatAs, asserted on the wire: the override REPLACES the configured
// prompt (it is not concatenated into it), and SafetyRules still ride along.
func TestChatStreamAsSendsThePersonaPrompt(t *testing.T) {
	st := newStub(t)
	st.stream = strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	svc := New(Config{APIKey: "k", Model: "claude-haiku-5-5", SystemPrompt: "tenant house prompt", BaseURL: st.server.URL})
	var got []string
	res, err := svc.ChatStreamAs(context.Background(), "hi", nil, "en", "你是轮胎客服。", func(tok string) {
		got = append(got, tok)
	})
	if err != nil {
		t.Fatalf("ChatStreamAs: %v", err)
	}
	if strings.Join(got, "") != "Hello" || res.Reply != "Hello" {
		t.Fatalf("deltas = %v, reply = %q; the persona override must not change how the stream is read", got, res.Reply)
	}
	if stream, _ := st.lastBody["stream"].(bool); !stream {
		t.Error("ChatStreamAs must still ask for stream:true")
	}

	system := st.text(t, "system")
	if !strings.Contains(system, "你是轮胎客服。") {
		t.Fatalf("system = %q, want the persona prompt", system)
	}
	if strings.Contains(system, "tenant house prompt") {
		t.Fatalf("system = %q; the persona must REPLACE the configured prompt, not concatenate it", system)
	}
	if !strings.Contains(system, gemini.SafetyRules) {
		t.Fatal("the safety rules must ride with every persona prompt; a persona cannot switch them off")
	}
}

// The compatibility promise: ChatStream with no override sends what it always sent.
func TestChatStreamKeepsTheConfiguredPrompt(t *testing.T) {
	st := newStub(t)
	st.stream = strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	svc := New(Config{APIKey: "k", Model: "claude-haiku-5-5", SystemPrompt: "tenant house prompt", BaseURL: st.server.URL})
	if _, err := svc.ChatStream(context.Background(), "hi", nil, "en", nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if system := st.text(t, "system"); !strings.Contains(system, "tenant house prompt") {
		t.Fatalf("system = %q, want the configured prompt when there is no persona", system)
	}
}
