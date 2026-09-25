package usage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/redisstore"
)

// TestMain pins the provider for the whole package. Every limit resolution
// branches on GEMINI_PROVIDER, and the value that matters here is the one
// production runs with: UNSET, which resolves to studio — exactly what the
// tests below assume. A shell that has sourced a migrated host's .env-go
// exports GEMINI_PROVIDER=vertex, and without this the studio expectations
// would silently start measuring the vertex branch (a $10 default would look
// like a regression instead of a passing test).
func TestMain(m *testing.M) {
	_ = os.Unsetenv(providerEnv)
	os.Exit(m.Run())
}

// captureLogs points slog.Default() at a buffer for the duration of a test, so
// the guardrail's own account of why it shed a turn can be asserted.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// fixedSpend is a spendSource that always reports the same window sum.
func fixedSpend(v float64) spendSource {
	return func(context.Context, *pgxpool.Pool, *redisstore.Client) (float64, error) { return v, nil }
}

// failingSpend is a spendSource for the metering-outage path.
func failingSpend() spendSource {
	return func(context.Context, *pgxpool.Pool, *redisstore.Client) (float64, error) {
		return 0, errors.New("metering down")
	}
}

func strptr(s string) *string { return &s }

func TestBudgetFailsOpenWithoutMetering(t *testing.T) {
	// The gate exists to protect customers from a 429; a metering outage must
	// never be the thing that stops their replies. No DB (or DB + Redis both
	// down) therefore means "not over budget".
	spent, limit, over := Budget(context.Background(), nil, nil)
	if over {
		t.Fatal("a missing database must fail open")
	}
	if spent != 0 || limit != 10 {
		t.Errorf("Budget without metering = (%v, %v), want (0, 10)", spent, limit)
	}
}

func TestSpendLimitDefaultAndOverride(t *testing.T) {
	t.Setenv("GEMINI_SPEND_LIMIT_USD", "")
	if got := SpendLimitUSD(); got != 10 {
		t.Errorf("default limit = %v, want 10 (Tier 1)", got)
	}
	t.Setenv("GEMINI_SPEND_LIMIT_USD", "50")
	if got := SpendLimitUSD(); got != 50 {
		t.Errorf("override limit = %v, want 50 (Tier 2)", got)
	}
	// Zero (or negative) switches the gate off entirely: useful when the
	// account's real ceiling is unknown and alerting alone is wanted.
	t.Setenv("GEMINI_SPEND_LIMIT_USD", "0")
	if _, _, over := Budget(context.Background(), nil, nil); over {
		t.Error("limit 0 must disable the gate")
	}
}

func TestGateRatioBounds(t *testing.T) {
	t.Setenv("GEMINI_SPEND_GATE_RATIO", "")
	if got := GateRatio(); got != 0.85 {
		t.Errorf("default gate ratio = %v, want 0.85", got)
	}
	// Out-of-range values keep the default rather than shedding every turn
	// (ratio 0) or never shedding (ratio > 1).
	for _, bad := range []string{"0", "-1", "1.5", "nonsense"} {
		t.Setenv("GEMINI_SPEND_GATE_RATIO", bad)
		if got := GateRatio(); got != 0.85 {
			t.Errorf("ratio %q = %v, want the 0.85 default", bad, got)
		}
	}
}

// ---------------------------------------------------------------------------
// One variable, two meanings (see the header of spend.go).
// ---------------------------------------------------------------------------

func TestSpendLimitResolutionPerProvider(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		limitEnv  *string // nil = variable unset
		wantLimit float64
		wantBasis SpendBasis
		wantFault bool
	}{
		// studio: the historical resolution, unchanged. The limit is Google's
		// tier ceiling, so a tier number is the value that belongs here.
		{"studio unset keeps the Tier 1 default", "studio", nil, 10, BasisStudioTierCeiling, false},
		{"studio accepts a Tier ceiling (it IS the wall)", "studio", strptr("10"), 10, BasisStudioTierCeiling, false},
		{"studio accepts the Tier 2 ceiling", "studio", strptr("50"), 50, BasisStudioTierCeiling, false},
		{"studio keeps its lenient parse", "studio", strptr("nonsense"), 10, BasisStudioTierCeiling, false},
		{"studio negative disables", "studio", strptr("-5"), -5, BasisStudioTierCeiling, false},

		// vertex: no upstream wall, so nothing is inherited and nothing is
		// guessed. Unset means "no budget", which is the honest default.
		{"vertex unset arms nothing", "vertex", nil, 0, BasisVertexNoBudget, false},
		{"vertex empty arms nothing", "vertex", strptr(""), 0, BasisVertexNoBudget, false},
		{"vertex zero arms nothing", "vertex", strptr("0"), 0, BasisVertexNoBudget, false},
		{"vertex honours a deliberate budget", "vertex", strptr("25"), 25, BasisVertexSelfBudget, false},
		{"vertex refuses the Tier 1 leftover", "vertex", strptr("10"), 0, BasisVertexLegacyRefused, true},
		{"vertex refuses the Tier 2 leftover", "vertex", strptr("50"), 0, BasisVertexLegacyRefused, true},
		{"vertex refuses the Tier 3 leftover", "vertex", strptr("200"), 0, BasisVertexLegacyRefused, true},
		{"vertex refuses 10 written as 10.00", "vertex", strptr("10.00"), 0, BasisVertexLegacyRefused, true},
		{"vertex refuses 10 written as 1e1", "vertex", strptr("1e1"), 0, BasisVertexLegacyRefused, true},
		{"vertex refuses a non-numeric value", "vertex", strptr("10x"), 0, BasisVertexInvalidRefused, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(providerEnv, tc.provider)
			if tc.limitEnv != nil {
				t.Setenv(spendLimitEnv, *tc.limitEnv)
			} else {
				_ = os.Unsetenv(spendLimitEnv) // t.Setenv cannot unset
			}
			if got := SpendLimitUSD(); got != tc.wantLimit {
				t.Errorf("SpendLimitUSD() = %v, want %v", got, tc.wantLimit)
			}
			if got := SpendLimitBasis(); got != tc.wantBasis {
				t.Errorf("SpendLimitBasis() = %q, want %q", got, tc.wantBasis)
			}
			err := ValidateSpendConfig()
			if tc.wantFault {
				if err == nil {
					t.Fatal("ValidateSpendConfig() = nil, want a boot failure for this vertex configuration")
				}
				// The message has to name the variable, or the operator cannot
				// act on it.
				if !strings.Contains(err.Error(), spendLimitEnv) {
					t.Errorf("boot error must name %s: %v", spendLimitEnv, err)
				}
			} else if err != nil {
				t.Errorf("ValidateSpendConfig() = %v, want nil", err)
			}
		})
	}
}

// TestSameValueMeansDifferentThingsPerProvider is the crux of the increment:
// GEMINI_SPEND_LIMIT_USD=10 is Google's Tier 1 wall on studio and a leftover
// that must not be trusted on vertex.
func TestSameValueMeansDifferentThingsPerProvider(t *testing.T) {
	t.Setenv(spendLimitEnv, "10")

	t.Setenv(providerEnv, "studio")
	if got := SpendLimitUSD(); got != 10 {
		t.Fatalf("studio limit = %v, want 10 (the mirrored Tier 1 wall)", got)
	}
	if err := ValidateSpendConfig(); err != nil {
		t.Errorf("studio must never fail the new boot check: %v", err)
	}
	if got := SpendLimitBasis(); got != BasisStudioTierCeiling {
		t.Errorf("studio basis = %q, want %q", got, BasisStudioTierCeiling)
	}

	t.Setenv(providerEnv, "vertex")
	if got := SpendLimitUSD(); got != 0 {
		t.Errorf("vertex limit = %v, want 0 — the same 10 has no meaning here", got)
	}
	if err := ValidateSpendConfig(); err == nil {
		t.Error("vertex + 10 must be reported as a configuration error at boot")
	}
	if got := SpendLimitBasis(); got != BasisVertexLegacyRefused {
		t.Errorf("vertex basis = %q, want %q", got, BasisVertexLegacyRefused)
	}
}

// TestVertexRefusalNeverShedsATurn pins the safety property that makes this
// safe even if the boot check is never wired into main: a threshold that means
// nothing under vertex must not be the reason a customer's turn is refused —
// and the window is not even measured to decide that.
func TestVertexRefusalNeverShedsATurn(t *testing.T) {
	t.Setenv(providerEnv, "vertex")
	t.Setenv(spendLimitEnv, "10")

	measured := 0
	counting := spendSource(func(context.Context, *pgxpool.Pool, *redisstore.Client) (float64, error) {
		measured++
		return 1e6, nil // far above any threshold this file could have inherited
	})
	spent, limit, over := budget(context.Background(), nil, nil, counting, newThrottle())
	if over {
		t.Error("a refused leftover limit must not shed a turn the upstream would have accepted")
	}
	if spent != 0 || limit != 0 {
		t.Errorf("budget = (%v, %v), want (0, 0) — no ceiling is armed", spent, limit)
	}
	if measured != 0 {
		t.Errorf("the window was measured %d times for a refused limit; refusal must short-circuit", measured)
	}
}

func TestVertexSelfBudgetStillSheds(t *testing.T) {
	t.Setenv(providerEnv, "vertex")
	t.Setenv(spendLimitEnv, "25")
	t.Setenv(gateRatioEnv, "0.85")

	// Comfortably under the gate: a self-budget must not shed early either.
	if _, _, over := budget(context.Background(), nil, nil, fixedSpend(1), newThrottle()); over {
		t.Error("$1 of $25 must not shed")
	}
	spent, limit, over := budget(context.Background(), nil, nil, fixedSpend(25*0.85), newThrottle())
	if !over {
		t.Fatalf("$%v of $%v must shed", spent, limit)
	}
	if limit != 25 {
		t.Errorf("limit = %v, want the configured 25", limit)
	}
}

func TestMeteringFailureFailsOpenOnBothProviders(t *testing.T) {
	for _, provider := range []string{"studio", "vertex"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv(providerEnv, provider)
			t.Setenv(spendLimitEnv, "25")
			spent, limit, over := budget(context.Background(), nil, nil, failingSpend(), newThrottle())
			if over {
				t.Error("a metering outage must never stop customer replies")
			}
			if spent != 0 || limit != 25 {
				t.Errorf("budget = (%v, %v), want (0, 25)", spent, limit)
			}
		})
	}
}

// TestProviderMirrorsGeminiProviderResolution pins the duplicated rule to the
// one internal/gemini implements: only an explicit "vertex" leaves studio.
func TestProviderMirrorsGeminiProviderResolution(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"studio", false},
		{"STUDIO", false},
		{"vertex", true},
		{"VeRtEx", true},
		{" vertex ", true},
		{"vertx", false},
		{"vertex-us", false},
	} {
		t.Setenv(providerEnv, tc.value)
		if got := providerIsVertex(); got != tc.want {
			t.Errorf("GEMINI_PROVIDER=%q → vertex=%v, want %v", tc.value, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Observability: which ceiling tripped, and why that is the whole point.
// ---------------------------------------------------------------------------

func TestShedLogSaysWhichCeilingTripped(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		limitEnv   string
		wantBasis  string
		wantPhrase string
	}{
		{"studio sheds on Google's wall", "studio", "10", string(BasisStudioTierCeiling), "upstream wall"},
		{"vertex sheds on our own budget", "vertex", "25", string(BasisVertexSelfBudget), "self-imposed budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(providerEnv, tc.provider)
			t.Setenv(spendLimitEnv, tc.limitEnv)
			t.Setenv(gateRatioEnv, "1") // trip exactly at the limit
			out := captureLogs(t)
			limit, _ := strconv.ParseFloat(tc.limitEnv, 64)
			log := newThrottle()

			if _, _, over := budget(context.Background(), nil, nil, fixedSpend(limit), log); !over {
				t.Fatal("spend at 100% of the limit must shed")
			}
			logged := out.String()
			if !strings.Contains(logged, "basis="+tc.wantBasis) {
				t.Errorf("log must name the basis %q:\n%s", tc.wantBasis, logged)
			}
			if !strings.Contains(logged, tc.wantPhrase) {
				t.Errorf("log must explain the ceiling (%q):\n%s", tc.wantPhrase, logged)
			}
			if !strings.Contains(logged, "level=WARN") {
				t.Errorf("a shed turn is warn-level:\n%s", logged)
			}

			// A storm inside the window is one line a minute, not one per turn.
			for i := 0; i < 5; i++ {
				_, _, _ = budget(context.Background(), nil, nil, fixedSpend(limit), log)
			}
			if n := strings.Count(out.String(), "spend gate shed a turn"); n != 1 {
				t.Errorf("shed lines = %d, want 1 (throttled)", n)
			}
		})
	}
}

// TestRefusedLimitIsAudibleWithoutTheBootCheck — the boot check lives in
// main.go's hands; this error line does not, so a deployment that never wired
// the check still says out loud that its guardrail is off.
func TestRefusedLimitIsAudibleWithoutTheBootCheck(t *testing.T) {
	t.Setenv(providerEnv, "vertex")
	t.Setenv(spendLimitEnv, "50")
	out := captureLogs(t)

	if _, _, over := budget(context.Background(), nil, nil, fixedSpend(1e6), newThrottle()); over {
		t.Fatal("a refused limit must not shed")
	}
	logged := out.String()
	if !strings.Contains(logged, "level=ERROR") {
		t.Errorf("refusal must be error-level:\n%s", logged)
	}
	if !strings.Contains(logged, string(BasisVertexLegacyRefused)) {
		t.Errorf("refusal must carry the basis:\n%s", logged)
	}
	if !strings.Contains(logged, "NOT armed") {
		t.Errorf("refusal must say the gate is off:\n%s", logged)
	}
	if strings.Contains(logged, "spend gate shed a turn") {
		t.Errorf("a refused limit must not log a shed:\n%s", logged)
	}
}

func TestThrottleAllowsOneLinePerInterval(t *testing.T) {
	log := newThrottle()
	now := time.Now()
	if !log.allowAt("shed", time.Minute, now) {
		t.Fatal("the first line must go out")
	}
	if log.allowAt("shed", time.Minute, now.Add(30*time.Second)) {
		t.Error("a second line inside the interval must be suppressed")
	}
	if !log.allowAt("shed", time.Minute, now.Add(time.Minute)) {
		t.Error("the next interval must be allowed")
	}
	if !log.allowAt("refused", time.Minute, now) {
		t.Error("keys must throttle independently")
	}
}
