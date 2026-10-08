package llm

import (
	"context"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/anthropic"
	"khmer-ai-cs-go/internal/gemini"
)

func TestDefaultRouterServesGemini(t *testing.T) {
	g := gemini.New("", "gemini-3.8-flash", 128)
	r := NewRouter(g)
	if r.Provider() != ProviderGemini {
		t.Fatalf("a new router serves %q, want gemini: every existing row means Gemini", r.Provider())
	}
	if m, ok := r.Model().(*gemini.Service); !ok || m != g {
		t.Fatalf("Model() = %T, want the Gemini client the router was built with", r.Model())
	}
}

func TestClaudeProviderServesClaude(t *testing.T) {
	c := anthropic.New(anthropic.Config{Transport: anthropic.TransportAPI, APIKey: "k", Model: "claude-haiku-5-5"})
	r := NewRouter(gemini.New("", "m", 128))
	r.SetClaude(c)
	r.SetProvider(ProviderAnthropic)
	if m, ok := r.Model().(*anthropic.Service); !ok || m != c {
		t.Fatalf("Model() = %T, want the Claude client once anthropic is in force", r.Model())
	}
	if !r.Model().IsConfigured() {
		t.Error("a Claude client with a key must report itself configured")
	}
}

func TestAClaudeProviderWithoutAClientFailsLoudlyNeverGemini(t *testing.T) {
	// The Gemini client here is in mock mode, which answers with a template and a nil
	// error. That is exactly the answer a Claude outage must never get.
	r := NewRouter(gemini.New("", "m", 128))
	r.SetProvider(ProviderAnthropic)
	m := r.Model()
	if _, ok := m.(*gemini.Service); ok {
		t.Fatal("a Claude provider with no client fell back to Gemini")
	}
	if _, err := m.Chat(context.Background(), "hi", nil, "en"); err == nil {
		t.Error("Chat on a missing Claude client must return an error")
	}
	if _, ok := m.GenerateFast(context.Background(), "x", time.Second); ok {
		t.Error("GenerateFast on a missing Claude client must report failure")
	}
	if m.IsConfigured() {
		t.Error("a missing Claude client must not report itself configured")
	}
}

func TestAnUnconfiguredClaudeClientFailsTheCall(t *testing.T) {
	c := anthropic.New(anthropic.Config{Transport: anthropic.TransportAPI, Model: "claude-haiku-5-5"}) // no key
	r := NewRouter(gemini.New("", "m", 128))
	r.SetClaude(c)
	r.SetProvider(ProviderAnthropic)
	if _, err := r.Model().Chat(context.Background(), "hi", nil, "en"); err == nil {
		t.Error("an unconfigured Claude client answered a call")
	}
	if r.Model().IsConfigured() {
		t.Error("an unconfigured Claude client reported itself configured")
	}
}

func TestUnknownProviderServesGemini(t *testing.T) {
	g := gemini.New("", "m", 128)
	r := NewRouter(g)
	r.SetProvider("openai")
	if r.Provider() != ProviderGemini {
		t.Errorf("an unrecognised provider is served as %q, want gemini", r.Provider())
	}
	if m, ok := r.Model().(*gemini.Service); !ok || m != g {
		t.Error("an unrecognised provider must route to the Gemini client")
	}
}

func TestSwitchingProvidersTakesEffectOnTheNextCall(t *testing.T) {
	g := gemini.New("", "m", 128)
	c := anthropic.New(anthropic.Config{Transport: anthropic.TransportAPI, APIKey: "k", Model: "claude-haiku-5-5"})
	r := NewRouter(g)
	r.SetClaude(c)

	r.SetProvider(ProviderAnthropic)
	if _, ok := r.Model().(*anthropic.Service); !ok {
		t.Fatal("switching to anthropic did not take effect")
	}
	r.SetProvider(ProviderGemini)
	if _, ok := r.Model().(*gemini.Service); !ok {
		t.Fatal("switching back to gemini did not take effect")
	}
}

func TestInstallClaudeBuildsThenReconfigures(t *testing.T) {
	r := NewRouter(gemini.New("", "m", 128))
	r.InstallClaude(Row{Provider: ProviderAnthropic, ModelName: "claude-haiku-5-5", MaxTokens: 512}, "key-1")
	first := r.Claude()
	if first == nil || !first.IsConfigured() || first.ModelName() != "claude-haiku-5-5" {
		t.Fatalf("InstallClaude did not build a configured client: %+v", first)
	}

	r.InstallClaude(Row{Provider: ProviderAnthropic, ModelName: "claude-haiku-5-5"}, "")
	if r.Claude() != first {
		t.Error("a reload must reconfigure the client in place, not replace it under callers holding it")
	}
	if first.IsConfigured() {
		t.Error("an empty key on reload must leave the client unconfigured, not keep a stale one")
	}
}

func TestClaudeConfigMapsTheRow(t *testing.T) {
	t.Setenv("GEMINI_VERTEX_SA_FILE", "/secret/sa.json")
	t.Setenv("GEMINI_VERTEX_PROJECT", " proj-9 ")
	temp := 0.7
	vertexRow := Row{Provider: ProviderAnthropicVertex, ModelName: "claude-haiku-5-5", SystemPrompt: "p", MaxTokens: 900, Region: "us", Temperature: &temp}
	cfg := ClaudeConfig(vertexRow, "ignored-on-vertex")
	if cfg.Transport != anthropic.TransportVertex || cfg.Region != "us" || cfg.SAFile != "/secret/sa.json" || cfg.Project != "proj-9" {
		t.Errorf("vertex config = %+v, want the row's region and the environment's service account and project", cfg)
	}
	if cfg.APIKey != "" {
		t.Error("a key must never reach the Vertex transport")
	}
	if cfg.MaxTokens != 900 || cfg.Temperature == nil || *cfg.Temperature != 0.7 {
		t.Errorf("generation settings lost in the mapping: %+v", cfg)
	}

	direct := ClaudeConfig(Row{Provider: ProviderAnthropic, ModelName: "claude-haiku-5-5", Region: "us"}, "sk-ant")
	if direct.Transport != anthropic.TransportAPI || direct.APIKey != "sk-ant" {
		t.Errorf("direct config = %+v, want the API transport with the key", direct)
	}
	if direct.Region != "" || direct.SAFile != "" {
		t.Error("the direct transport must not inherit a region or a service account")
	}
}

func TestCredentialSourceNamesTheSecretPerProvider(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	if got := CredentialSource(ProviderAnthropic); got != "api_key" {
		t.Errorf("anthropic = %q, want api_key", got)
	}
	if got := CredentialSource(ProviderAnthropicVertex); got != "service_account" {
		t.Errorf("anthropic-vertex = %q, want service_account", got)
	}
	if got := CredentialSource(ProviderGemini); got != "service_account" {
		t.Errorf("gemini on vertex = %q, want service_account", got)
	}
	t.Setenv("GEMINI_PROVIDER", "studio")
	if got := CredentialSource(ProviderGemini); got != "api_key" {
		t.Errorf("gemini on studio = %q, want api_key", got)
	}
}

func TestProviderVocabulary(t *testing.T) {
	for _, p := range []string{ProviderGemini, ProviderAnthropic, ProviderAnthropicVertex} {
		if !Valid(p) {
			t.Errorf("%q must be a storable provider", p)
		}
	}
	for _, p := range []string{"", "openai", "Anthropic", "claude", "anthropic-api"} {
		if Valid(p) {
			t.Errorf("%q must not be storable", p)
		}
	}
	if !IsClaude(ProviderAnthropic) || !IsClaude(ProviderAnthropicVertex) || IsClaude(ProviderGemini) || IsClaude("") {
		t.Error("IsClaude must select exactly the two Claude providers")
	}
}
