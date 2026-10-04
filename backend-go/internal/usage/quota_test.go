package usage

import (
	"strings"
	"testing"
)

// Enterprise is unmetered by construction: the plan exists precisely so a
// negotiated customer is not stopped by a counter. Everything else is capped —
// that is the whole point of turning enforcement on, so the default for an
// unknown plan string has to be "metered".
func TestMessageQuotaIsMeteredPerPlan(t *testing.T) {
	cases := []struct {
		plan string
		want bool
	}{
		{PlanEnterprise, false},
		{PlanFree, true},
		{PlanPro, true},
		{"", true},
		{"some-future-plan", true},
	}
	for _, tc := range cases {
		if got := messageQuotaIsMetered(tc.plan); got != tc.want {
			t.Errorf("messageQuotaIsMetered(%q) = %v, want %v", tc.plan, got, tc.want)
		}
	}
}

// The statements are string constants rather than builder calls, so nothing but
// this test stops a future edit from interpolating a value into one. An
// interpolated tenant id would still work in tests and quietly become the
// injection sink the parameterization exists to prevent.
func TestMessageQuotaStatementsAreParameterized(t *testing.T) {
	stmts := map[string]string{
		"init":            sqlMessageQuotaInit,
		"rollover":        sqlMessageQuotaRollover,
		"lookup":          sqlMessageQuotaLookup,
		"increment":       sqlMessageQuotaIncrement,
		"incrementCapped": sqlMessageQuotaIncrementCapped,
	}
	for name, stmt := range stmts {
		if !strings.Contains(stmt, "$1") {
			t.Errorf("%s: statement has no $1 placeholder: %q", name, stmt)
		}
		if strings.Contains(stmt, "%s") || strings.Contains(stmt, "'+") {
			t.Errorf("%s: statement looks interpolated: %q", name, stmt)
		}
	}
	// The gate is only atomic while the cap lives in the same UPDATE as the
	// increment; a SELECT-then-UPDATE refactor would reintroduce the race.
	if !strings.Contains(sqlMessageQuotaIncrementCapped, "monthly_message_quota > messages_used") {
		t.Errorf("capped increment no longer compares the cap and the counter: %q", sqlMessageQuotaIncrementCapped)
	}
}
