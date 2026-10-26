package deepseek

// The persona override on the streaming path. Chat-completions carries the system
// prompt as the FIRST message, so that is where the assertion lands: the persona
// prompt replaces the configured one, and SafetyRules still ride along.

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
func TestChatStreamAsSendsThePersonaPrompt(t *testing.T) {
	st := newStub(t)
	st.stream = strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	svc := New(Config{APIKey: "k", Model: "deepseek-chat", SystemPrompt: "tenant house prompt", BaseURL: st.server.URL})
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

	msgs := st.messages(t)
	if len(msgs) == 0 {
		t.Fatal("no messages in the request")
	}
	role, _ := msgs[0]["role"].(string)
	system, _ := msgs[0]["content"].(string)
	if role != "system" {
		t.Fatalf("first message role = %q, want the system turn", role)
	}
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
		`data: {"choices":[{"delta":{"content":"ok"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	svc := New(Config{APIKey: "k", Model: "deepseek-chat", SystemPrompt: "tenant house prompt", BaseURL: st.server.URL})
	if _, err := svc.ChatStream(context.Background(), "hi", nil, "en", nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	msgs := st.messages(t)
	if len(msgs) == 0 {
		t.Fatal("no messages in the request")
	}
	system, _ := msgs[0]["content"].(string)
	if !strings.Contains(system, "tenant house prompt") {
		t.Fatalf("system = %q, want the configured prompt when there is no persona", system)
	}
}
