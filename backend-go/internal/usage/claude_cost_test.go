package usage

import (
	"math"
	"strings"
	"testing"
)

// usd compares two dollar amounts at the precision that matters for a dashboard.
func usd(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.9f, want %.9f", label, got, want)
	}
}

func TestClaudeShortPromptBillsAtTheListRate(t *testing.T) {
	// claude-haiku-5-5 list price for prompts up to 100k tokens: $0.10 in, $0.50 out.
	usd(t, "claude short", EstimateCostFor("claude-haiku-5-5", 1000, 100, 0),
		1000/1e6*0.10+100/1e6*0.50)
}

func TestClaudeCacheReadsBillAtATenthOfInput(t *testing.T) {
	// 2000 uncached at the input rate, 8000 read from the cache at a tenth of it.
	usd(t, "claude cached", EstimateCostFor("claude-haiku-5-5", 10000, 0, 8000),
		2000/1e6*0.10+8000/1e6*0.10*0.10)
}

func TestClaudeLongPromptBillsEveryTokenAtTheLongRate(t *testing.T) {
	// Over 100k tokens the whole request moves to $0.50 in / $2.50 out, and cache
	// reads to a tenth of that input rate.
	usd(t, "claude long", EstimateCostFor("claude-haiku-5-5", 200000, 1000, 100000),
		100000/1e6*0.50+100000/1e6*0.50*0.10+1000/1e6*2.50)
}

func TestClaudeLongRateStartsAboveOneHundredThousandTokens(t *testing.T) {
	atBoundary := EstimateCostFor("claude-haiku-5-5", 100000, 0, 0)
	overBoundary := EstimateCostFor("claude-haiku-5-5", 100001, 0, 0)
	usd(t, "exactly 100k", atBoundary, 100000/1e6*0.10)
	if overBoundary < 100001/1e6*0.50-1e-9 {
		t.Errorf("100001 tokens = %.9f, want the long rate", overBoundary)
	}
}

func TestClaudeIsNotPricedAsGemini(t *testing.T) {
	claude := EstimateCostFor("claude-haiku-5-5", 1000, 100, 0)
	gemini := EstimateCostFor("gemini-3.8-flash", 1000, 100, 0)
	if math.Abs(claude-gemini) < 1e-9 {
		t.Fatalf("claude and gemini cost the same %.9f: the Claude rate card is not in use", claude)
	}
}

func TestEmbeddingPricingIsUnchanged(t *testing.T) {
	usd(t, "embedding", EstimateCostFor("gemini-embedding-001", 1000, 0, 0), 1000/1e6*0.15)
}

// spendWallFor resolves the ceiling under one environment and one serving provider.
func spendWallFor(t *testing.T, provider, limitEnv, serving string) resolvedLimit {
	t.Helper()
	t.Setenv(providerEnv, provider)
	t.Setenv(spendLimitEnv, limitEnv)
	SetServingProvider(serving)
	t.Cleanup(func() { SetServingProvider("") })
	return resolveSpendLimit()
}

func TestAStudioTierCeilingGuardsGeminiOnly(t *testing.T) {
	if r := spendWallFor(t, "studio", "", "gemini"); r.basis != BasisStudioTierCeiling || r.limit != 10 {
		t.Errorf("studio + gemini unset = %+v, want the Tier 1 ceiling of 10", r)
	}
	// The same studio transport serving Claude: no AI Studio wall exists for
	// Claude, so the default is no budget, not Google's $10.
	if r := spendWallFor(t, "studio", "", "anthropic"); r.basis != BasisVertexNoBudget || r.limit != 0 {
		t.Errorf("studio + claude unset = %+v, want no budget", r)
	}
}

func TestATierNumberIsRefusedWhenClaudeServes(t *testing.T) {
	// A leftover AI Studio tier number must not become a Claude budget that sheds
	// customer turns at a threshold Google never set. The boot check refuses it.
	r := spendWallFor(t, "studio", "10", "anthropic-vertex")
	if r.fault == nil {
		t.Fatal("a Tier 1 number under a Claude provider must fail the boot check")
	}
	if !strings.Contains(r.fault.Error(), "anthropic-vertex") {
		t.Errorf("the refusal must name the serving provider, got: %v", r.fault)
	}
	if r.basis != BasisVertexLegacyRefused {
		t.Errorf("basis = %s, want %s", r.basis, BasisVertexLegacyRefused)
	}
}

func TestADeliberateBudgetStillArmsUnderClaude(t *testing.T) {
	r := spendWallFor(t, "studio", "25", "anthropic")
	if r.fault != nil || r.limit != 25 || r.basis != BasisVertexSelfBudget {
		t.Errorf("a non-tier budget under claude = %+v, want a self-imposed 25", r)
	}
}

func TestAnUnknownServingProviderIsGemini(t *testing.T) {
	// The reading every model_configs row has always had: a value this package does
	// not recognise serves as Gemini, so the studio wall keeps applying to it.
	if r := spendWallFor(t, "studio", "", "some-future-provider"); r.basis != BasisStudioTierCeiling {
		t.Errorf("unknown provider = %+v, want the Gemini reading", r)
	}
}
