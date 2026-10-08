package api

// model_providers_test.go pins the console's provider rules and the reload that serves
// them. The row reads are package seams, so these run without a database. Every rule in
// updateModelConfig is checked before the first write, which is what makes the refusals
// testable against an App with no pool.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/anthropic"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/llm"
)

// providerTestStubRow answers the update path's row read for one test.
func providerTestStubRow(t *testing.T, row modelRow) {
	t.Helper()
	original := modelRowFromDB
	modelRowFromDB = func(context.Context, *pgxpool.Pool, int32) (modelRow, error) { return row, nil }
	t.Cleanup(func() { modelRowFromDB = original })
}

// providerTestStubGeminiKeyElsewhere answers whether another Gemini row holds a key.
func providerTestStubGeminiKeyElsewhere(t *testing.T, has bool) {
	t.Helper()
	original := otherGeminiRowHasKeyFromDB
	otherGeminiRowHasKeyFromDB = func(context.Context, *pgxpool.Pool, int32) bool { return has }
	t.Cleanup(func() { otherGeminiRowHasKeyFromDB = original })
}

// providerTestStubDefault and providerTestStubGemini answer the two reload reads.
func providerTestStubDefault(t *testing.T, row llm.Row) {
	t.Helper()
	original := defaultModelConfigFromDB
	defaultModelConfigFromDB = func(context.Context, *pgxpool.Pool) (llm.Row, bool) { return row, true }
	t.Cleanup(func() { defaultModelConfigFromDB = original })
}

func providerTestStubGemini(t *testing.T, row llm.Row) {
	t.Helper()
	original := geminiModelConfigFromDB
	geminiModelConfigFromDB = func(context.Context, *pgxpool.Pool) (llm.Row, bool) { return row, true }
	t.Cleanup(func() { geminiModelConfigFromDB = original })
}

// providerTestUpdate runs the update handler as a platform admin and returns its error.
func providerTestUpdate(t *testing.T, app *App, configID int32, body string) error {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/models/1", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, &CurrentUser{UserID: 1, Role: "platform_admin"}))
	_, err := app.updateModelConfig(httptest.NewRecorder(), req, configID)
	return err
}

// providerTestWantRefusal asserts a 400 whose message contains want.
func providerTestWantRefusal(t *testing.T, err error, want string) {
	t.Helper()
	var apiErr *ApiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want a 400 refusal", err)
	}
	if !strings.Contains(apiErr.Message, want) {
		t.Errorf("message = %q, want it to contain %q", apiErr.Message, want)
	}
}

func TestSwitchingToClaudeNeedsItsOwnKey(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	providerTestStubRow(t, modelRow{provider: llm.ProviderGemini, apiKey: "sealed-gemini-key"})
	err := providerTestUpdate(t, &App{}, 1, `{"provider":"anthropic","model_name":"claude-haiku-5-5"}`)
	// The Gemini key is not an Anthropic key: it must not follow the row.
	providerTestWantRefusal(t, err, "需要同时填写 Anthropic API Key")
}

func TestClaudeRowsTakeOnlyCatalogModels(t *testing.T) {
	providerTestStubRow(t, modelRow{provider: llm.ProviderAnthropic, apiKey: "sealed"})
	providerTestWantRefusal(t, providerTestUpdate(t, &App{}, 1, `{"model_name":"claude-sonnet-5-5"}`), "未知的 Claude 模型")
}

func TestTheDirectTransportTakesNoRegion(t *testing.T) {
	providerTestStubRow(t, modelRow{provider: llm.ProviderAnthropic, apiKey: "sealed"})
	providerTestWantRefusal(t, providerTestUpdate(t, &App{}, 1, `{"vertex_region":"us"}`), "Claude 直连没有区域")
}

func TestAnUnknownProviderIsRefused(t *testing.T) {
	providerTestStubRow(t, modelRow{provider: llm.ProviderGemini})
	providerTestWantRefusal(t, providerTestUpdate(t, &App{}, 1, `{"provider":"openai"}`), "未知的服务商")
}

func TestLeavingTheOnlyStudioKeyRowIsRefused(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "studio")
	providerTestStubRow(t, modelRow{provider: llm.ProviderGemini, apiKey: "sealed-studio-key"})
	providerTestStubGeminiKeyElsewhere(t, false)
	err := providerTestUpdate(t, &App{}, 1, `{"provider":"anthropic","api_key":"sk-ant-pasted"}`)
	// Embeddings and retrieval read that key: it may not leave while nothing else holds one.
	// (A key comes with the switch, or the earlier key check would answer first.)
	providerTestWantRefusal(t, err, "唯一的 Gemini 凭据")
}

func TestReloadServesClaudeFromTheDefaultRowAndGeminiFromItsOwn(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", "")
	sealer := newTestSealer(t)
	sealedKey, err := sealer.Encrypt("sk-ant-plain")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	providerTestStubDefault(t, llm.Row{ConfigID: 2, Provider: llm.ProviderAnthropic, APIKey: sealedKey, ModelName: "claude-haiku-5-5", MaxTokens: 700})
	providerTestStubGemini(t, llm.Row{ConfigID: 1, Provider: llm.ProviderGemini, ModelName: "gemini-3.8-flash", MaxTokens: 256})

	serving := gemini.New("", "gemini-old", 64)
	app := &App{Gemini: serving, Sealer: sealer, Logger: slog.Default(), LLM: llm.NewRouter(serving)}
	app.reloadServingFromDB(context.Background())

	if got := app.LLM.Provider(); got != llm.ProviderAnthropic {
		t.Fatalf("provider in force = %q, want anthropic", got)
	}
	claude := app.LLM.Claude()
	if claude == nil || !claude.IsConfigured() || claude.ModelName() != "claude-haiku-5-5" {
		t.Fatalf("the Claude client was not built from the default row: %+v", claude)
	}
	// Gemini keeps its own row's settings: embeddings and retrieval run on it.
	if got := serving.ModelName(); got != "gemini-3.8-flash" {
		t.Errorf("the Gemini client took model %q, want its own row's gemini-3.8-flash", got)
	}
	if got := app.claudeKey(llm.Row{Provider: llm.ProviderAnthropic, APIKey: sealedKey}); got != "sk-ant-plain" {
		t.Errorf("the Claude key reached the client as %q, want the unsealed key", got)
	}
}

func TestReloadWithAGeminiDefaultLeavesClaudeAlone(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	providerTestStubDefault(t, llm.Row{ConfigID: 1, Provider: llm.ProviderGemini, ModelName: "gemini-3.8-flash", MaxTokens: 256})

	serving := gemini.New("", "m", 64)
	app := &App{Gemini: serving, Logger: slog.Default(), LLM: llm.NewRouter(serving)}
	app.reloadServingFromDB(context.Background())

	if app.LLM.Provider() != llm.ProviderGemini {
		t.Errorf("provider in force = %q, want gemini", app.LLM.Provider())
	}
	if app.LLM.Claude() != nil {
		t.Error("a Gemini default must not build a Claude client")
	}
	if serving.ModelName() != "gemini-3.8-flash" {
		t.Errorf("the Gemini client took model %q, want the default row's", serving.ModelName())
	}
}

func TestServingFollowsTheRouterAndFallsBackToGemini(t *testing.T) {
	g := gemini.New("", "m", 64)
	if got := (&App{Gemini: g}).serving(); got != llm.Model(g) {
		t.Error("with no router wired, generation must use the Gemini client")
	}
	router := llm.NewRouter(g)
	router.SetClaude(anthropic.New(anthropic.Config{APIKey: "k", Model: "claude-haiku-5-5"}))
	router.SetProvider(llm.ProviderAnthropic)
	if _, ok := (&App{Gemini: g, LLM: router}).serving().(*anthropic.Service); !ok {
		t.Error("with anthropic in force, generation must use the Claude client")
	}
}

func TestClaudeModelListIsTheCatalogAndAllAvailable(t *testing.T) {
	out, err := claudeAvailableModels(llm.Row{Provider: llm.ProviderAnthropic, Region: "asia-southeast1"}, "")
	if err != nil {
		t.Fatalf("claudeAvailableModels: %v", err)
	}
	models := out.([]map[string]any)
	if len(models) != len(anthropic.Catalog()) {
		t.Fatalf("listed %d models, want the %d in the catalog", len(models), len(anthropic.Catalog()))
	}
	for _, m := range models {
		// Claude is served straight from Anthropic: no location can be missing a model.
		if m["available"] != true {
			t.Errorf("%v must be available", m["name"])
		}
	}
}

func TestTestingAClaudeRowNeedsItsKeyFirst(t *testing.T) {
	_, err := (&App{}).testClaudeConfig(context.Background(), llm.Row{Provider: llm.ProviderAnthropic, ModelName: "claude-haiku-5-5"})
	providerTestWantRefusal(t, err, "未设置 API Key")
}

func TestClaudeKeyIsUnsealed(t *testing.T) {
	sealer := newTestSealer(t)
	sealed, err := sealer.Encrypt("sk-ant")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	app := &App{Sealer: sealer}
	if got := app.claudeKey(llm.Row{Provider: llm.ProviderAnthropic, APIKey: sealed}); got != "sk-ant" {
		t.Errorf("direct key = %q, want the unsealed key", got)
	}
}
