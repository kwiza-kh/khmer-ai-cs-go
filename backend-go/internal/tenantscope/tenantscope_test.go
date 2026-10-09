package tenantscope

import (
	"fmt"
	"strings"
	"testing"
)

// TestTenantIsolation is the gate: every SQL statement touching tenant data
// must bind it (see the package comment for the accepted shapes). Run with
// `go test ./internal/tenantscope/`; it needs no database, so it cannot skip
// the way the DATABASE_URL-gated tests do.
func TestTenantIsolation(t *testing.T) {
	rep, err := Analyze("../..")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	t.Logf("%d statements, %d touch tenant data (%d bound in-statement, %d under a funnel, %d marked, %d excepted)",
		rep.Statements, rep.Tenant, rep.Bound, rep.Funnelled, rep.Marked, rep.Excepted)

	if len(rep.Gaps) > 0 {
		var b strings.Builder
		for _, g := range rep.Gaps {
			fmt.Fprintf(&b, "  %s\n", g)
		}
		t.Errorf("%d SQL statement(s) reach tenant data with no visible tenant binding:\n%s\n"+
			"Fix by naming the tenant column in the statement, running a reviewed funnel "+
			"(ensureSessionAccess/ensureConfigOwner) before it, or adding a reviewed exception "+
			"to internal/tenantscope/exceptions.go with the binding it relies on",
			len(rep.Gaps), b.String())
	}
	if len(rep.Stale) > 0 {
		t.Errorf("%d stale tenant-scope exception(s) — the function they cover no longer issues an unscoped statement:\n  %s\n"+
			"Delete them so the list keeps documenting live decisions", len(rep.Stale), strings.Join(rep.Stale, "\n  "))
	}
}
