package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestChatAsSendsThePersonaPromptInsteadOfTheContextCache covers the two halves
// of a persona turn that fail silently rather than loudly: the prompt must be the
// persona's, and the cachedContents resource — registered against the BASE prompt
// and sent in place of systemInstruction — must not travel with it.
func TestChatAsSendsThePersonaPromptInsteadOfTheContextCache(t *testing.T) {
	const cacheName = "projects/proj-1/locations/asia-southeast1/cachedContents/persona1"
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			return http.StatusOK, `{"name":"` + cacheName + `"}`
		}
		return generateOK("gemini-3.5-flash")
	})
	t.Setenv("GEMINI_CACHE_TTL", "3600")
	t.Setenv("GEMINI_CACHE_MIN_TOKENS", "0")

	s = New("", "gemini-3.5-flash", 128)
	// Unique to this test: the cache registry is process-global.
	s.SetSystemPrompt("persona-prompt-test " + strings.Repeat("x", 64))

	// Baseline: with the cache on, a plain turn does ride the cached prefix.
	if _, err := s.Chat(context.Background(), "hi", nil, "km"); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var plain map[string]any
	if err := json.Unmarshal(platform.last(t).Body, &plain); err != nil {
		t.Fatal(err)
	}
	if plain["cachedContent"] != cacheName {
		t.Fatalf("baseline turn did not use the cache: %s", platform.last(t).Body)
	}

	const persona = "You are Sokha, the tire desk."
	if _, err := s.ChatAs(context.Background(), "hi", nil, "km", persona); err != nil {
		t.Fatalf("ChatAs: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(platform.last(t).Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["cachedContent"]; present {
		t.Errorf("a persona turn carried the base prompt's cache: %s", platform.last(t).Body)
	}
	si, _ := body["systemInstruction"].(map[string]any)
	parts, _ := si["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("systemInstruction = %v, want exactly one part", si)
	}
	text := fmt.Sprint(parts[0].(map[string]any)["text"])
	if !strings.HasPrefix(text, persona) {
		t.Errorf("systemInstruction = %q, want the persona prompt first", text)
	}
	if !strings.Contains(text, LanguageLabel("km")) {
		t.Errorf("systemInstruction = %q, want the language preference kept", text)
	}
	if strings.Contains(text, "persona-prompt-test") {
		t.Error("the tenant prompt leaked into a persona turn")
	}
}

// TestSystemInstructionForFallsBackToTheConfiguredPrompt — an empty override has
// to be byte-identical to the prompt every deployment sent before personas, or an
// unbindable persona would silently change the behaviour of every tenant that
// never asked for one.
func TestSystemInstructionForFallsBackToTheConfiguredPrompt(t *testing.T) {
	s := New("k", "gemini-3.5-flash", 0)
	s.SetSystemPrompt("tenant prompt")
	if got, want := s.systemInstructionFor("km", ""), s.systemInstruction("km"); got != want {
		t.Errorf("empty override = %q, want the configured prompt %q", got, want)
	}
	if got := s.systemInstructionFor("km", "persona prompt"); !strings.HasPrefix(got, "persona prompt") {
		t.Errorf("override = %q, want the persona prompt first", got)
	}
}
