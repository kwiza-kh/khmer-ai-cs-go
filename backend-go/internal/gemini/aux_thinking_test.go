package gemini

import "testing"

// The auxiliary calls (transcription, OCR, image description, the fast helper
// generations) must not think. On 2026-10-16 a Messenger voice note intermittently
// took 65s end to end while the identical pipeline answered another note in 3.5s:
// the chat path had a thinking budget, these calls had the model's defaults.
func TestAuxGenerationConfigDisablesThinking(t *testing.T) {
	t.Setenv("GEMINI_THINKING_BUDGET", "")
	cfg := auxGenerationConfig(512, 0.0)
	tc, ok := cfg["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("auxGenerationConfig has no thinkingConfig: %+v", cfg)
	}
	if got := tc["thinkingBudget"]; got != 0 {
		t.Fatalf("thinkingBudget = %v, want 0 for an auxiliary read", got)
	}
	if cfg["temperature"] != 0.0 || cfg["maxOutputTokens"] != 512 {
		t.Fatalf("config = %+v, want a deterministic capped read", cfg)
	}
}

// An operator who sets the knob still gets it — the default is 0, not a hard 0.
func TestAuxGenerationConfigHonoursTheKnob(t *testing.T) {
	t.Setenv("GEMINI_THINKING_BUDGET", "256")
	cfg := auxGenerationConfig(64, 0.0)
	tc, _ := cfg["thinkingConfig"].(map[string]any)
	if got := tc["thinkingBudget"]; got != 256 {
		t.Fatalf("thinkingBudget = %v, want the configured 256", got)
	}
}

// The chat path keeps its own default (send nothing, let the model decide): a
// slower better answer is a fair trade for a reply, and only for a reply.
func TestChatThinkingBudgetStaysUnsetByDefault(t *testing.T) {
	t.Setenv("GEMINI_THINKING_BUDGET", "")
	if b := thinkingBudget(); b != -1 {
		t.Fatalf("thinkingBudget() = %d, want -1 (nothing sent) so the chat path is unchanged", b)
	}
	cfg := generationConfig(1024, nil)
	if _, present := cfg["thinkingConfig"]; present {
		t.Fatalf("chat config gained a thinkingConfig: %+v", cfg)
	}
}
