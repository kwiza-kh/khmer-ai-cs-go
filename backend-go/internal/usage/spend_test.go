package usage

import (
	"context"
	"testing"
)

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
