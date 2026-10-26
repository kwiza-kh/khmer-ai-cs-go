package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ChatStreamAs is the console/widget streaming path under a persona. Two things
// have to be true on the wire, and neither is visible from the caller's side:
// the persona prompt replaces the tenant prompt (with SafetyRules still appended),
// and the explicit context cache is skipped — a cached prefix was registered for
// the BASE prompt, and the API rejects a request carrying both.
func TestChatStreamAsSendsThePersonaPrompt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{"Hel", "lo"} {
			_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"` + chunk + `"}]}}]}` + "\n\n"))
		}
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_PROVIDER", "studio")
	t.Setenv("GEMINI_FAST_MODEL", "")

	s := New("test-key", "gemini-test", 256)
	var streamed strings.Builder
	res, err := s.ChatStreamAs(context.Background(), "hi", nil, "en", "你是轮胎客服。", func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("ChatStreamAs: %v", err)
	}
	if res.Reply != "Hello" {
		t.Fatalf("reply = %q, want the assembled stream", res.Reply)
	}
	if streamed.String() != "Hello" {
		t.Fatalf("streamed = %q, want the same deltas the reply was assembled from", streamed.String())
	}

	instruction := systemInstructionOf(t, body)
	if !strings.Contains(instruction, "你是轮胎客服。") {
		t.Fatalf("systemInstruction = %q, want the persona prompt", instruction)
	}
	if !strings.Contains(instruction, SafetyRules) {
		t.Fatal("the safety rules must ride with every persona prompt; a persona cannot switch them off")
	}
	if strings.Contains(instruction, DefaultSystemPrompt) {
		t.Fatal("the tenant prompt must be REPLACED by the persona, not merged with it")
	}
	if _, present := body["cachedContent"]; present {
		t.Fatal("a persona turn must not carry the explicit context cache")
	}
}

// The compatibility promise: no persona means the request the deployment already
// sent — the configured prompt, unchanged.
func TestChatStreamWithoutAPersonaKeepsTheTenantPrompt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}` + "\n\n"))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_PROVIDER", "studio")
	t.Setenv("GEMINI_FAST_MODEL", "")

	s := New("test-key", "gemini-test", 256)
	s.SetSystemPrompt("tenant house prompt")
	if _, err := s.ChatStream(context.Background(), "hi", nil, "en", func(string) {}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	instruction := systemInstructionOf(t, body)
	if !strings.Contains(instruction, "tenant house prompt") {
		t.Fatalf("systemInstruction = %q, want the configured tenant prompt", instruction)
	}
}

func systemInstructionOf(t *testing.T, body map[string]any) string {
	t.Helper()
	si, _ := body["systemInstruction"].(map[string]any)
	parts, _ := si["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("no systemInstruction.parts in the request: %v", body)
	}
	first, _ := parts[0].(map[string]any)
	text, _ := first["text"].(string)
	return text
}
