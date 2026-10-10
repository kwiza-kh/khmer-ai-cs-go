package usage

import (
	"math"
	"testing"
)

// DeepSeek prices in peak and off-peak tiers; the rate card holds the PEAK
// numbers, so a cost computed here is the worst case for the tokens given. That
// is deliberate: this number feeds a spend guardrail, where over-counting asks
// the operator to look at a dashboard and under-counting hides a budget.

func TestDeepSeekFlashBillsAtThePeakListRate(t *testing.T) {
	// deepseek-flash peak: $0.30 in, $1.20 out, $0.006 cached in (per 1M).
	usd(t, "deepseek flash", EstimateCostFor("deepseek-flash", 1000, 100, 0),
		1000/1e6*0.30+100/1e6*1.20)
}

func TestDeepSeekCacheHitsUseTheirOwnRate(t *testing.T) {
	// The cache-hit rate is not a fixed fraction of the input rate on this
	// platform, so the cached part must be priced by its own number.
	usd(t, "deepseek cached", EstimateCostFor("deepseek-flash", 10000, 0, 8000),
		2000/1e6*0.30+8000/1e6*0.006)
}

func TestDeepSeekProRatesAreHigherThanFlash(t *testing.T) {
	flash := EstimateCostFor("deepseek-flash", 1000, 100, 0)
	pro := EstimateCostFor("deepseek-v4-pro", 1000, 100, 0)
	usd(t, "deepseek pro", pro, 1000/1e6*1.32+100/1e6*3.96)
	if pro <= flash {
		t.Errorf("pro %.9f must cost more than flash %.9f", pro, flash)
	}
}

func TestAnUnrecognisedDeepSeekModelTakesTheHigherRate(t *testing.T) {
	// The legacy alias deepseek-v4-flash is billed by the API at the Flash price,
	// so "flash" wins over the fallback; a name with no tier at all takes the Pro
	// card, the over-count direction.
	usd(t, "legacy flash alias", EstimateCostFor("deepseek-v4-flash", 1000, 0, 0), 1000/1e6*0.30)
	usd(t, "unknown deepseek name", EstimateCostFor("deepseek-future-9", 1000, 0, 0), 1000/1e6*1.32)
}

func TestDeepSeekIsNotPricedAsGeminiOrClaude(t *testing.T) {
	deepseek := EstimateCostFor("deepseek-flash", 1000, 100, 0)
	gemini := EstimateCostFor("gemini-3.8-flash", 1000, 100, 0)
	claude := EstimateCostFor("claude-haiku-5-5", 1000, 100, 0)
	if math.Abs(deepseek-gemini) < 1e-9 || math.Abs(deepseek-claude) < 1e-9 {
		t.Fatalf("deepseek %.9f matches gemini %.9f / claude %.9f: its rate card is not in use", deepseek, gemini, claude)
	}
}

func TestTheStudioWallDoesNotApplyToDeepSeek(t *testing.T) {
	// No AI Studio tier ceiling exists for a DeepSeek call, so a leftover tier
	// number is refused and the default is no budget — the same reading Claude
	// gets, and the boot check is what refuses the leftover.
	if r := spendWallFor(t, "studio", "", "deepseek"); r.basis != BasisVertexNoBudget || r.limit != 0 {
		t.Errorf("studio + deepseek unset = %+v, want no budget", r)
	}
	if r := spendWallFor(t, "studio", "10", "deepseek"); r.fault == nil {
		t.Error("a Tier 1 number under a DeepSeek provider must fail the boot check")
	}
}
