package api

import (
	"os"
	"strings"
	"testing"
)

// The bug this pins: "role == admin" was used as "tenant owner", so a
// self-service or SSO-provisioned merchant (role "user") could not add a single
// agent to their own team, and Pro's five-seat allowance was unusable for exactly
// the tenants that buy self-serve.
//
// The matrix is small but each row matters: an agent of another tenant must not
// gain owner powers, and that is the one thing the old role check did buy.
func TestTenantOwnerDecision(t *testing.T) {
	cases := []struct {
		name            string
		isPlatformAdmin bool
		isAgent         bool
		want            bool
	}{
		{name: "platform admin", isPlatformAdmin: true, isAgent: false, want: true},
		{name: "platform admin who is also an agent elsewhere", isPlatformAdmin: true, isAgent: true, want: true},
		{name: "self-service owner (role user, not an agent)", isPlatformAdmin: false, isAgent: false, want: true},
		{name: "agent of another tenant", isPlatformAdmin: false, isAgent: true, want: false},
		// Audit P1-5: this row used to be "want: true" because the role string
		// won over membership, and that is exactly the bypass.
		{name: "tenant admin who is a seat elsewhere", isPlatformAdmin: false, isAgent: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tenantOwnerAllowed(tc.isPlatformAdmin, tc.isAgent); got != tc.want {
				t.Errorf("tenantOwnerAllowed(isPlatformAdmin=%v, isAgent=%v) = %v, want %v",
					tc.isPlatformAdmin, tc.isAgent, got, tc.want)
			}
		})
	}
}

// The route guard is the one place that decides who may touch tenant
// administration, so pin the decision at the source: the role-based guard must
// not come back, because it silently locks out every self-service merchant.
func TestTenantRoutesUseTheOwnerGuard(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "a.adminOnly(") {
		t.Error("router.go still wires the role-based adminOnly guard")
	}
	if strings.Contains(src, "user.IsAdmin()") {
		t.Error("router.go still decides by role; use a.isTenantOwner")
	}
	// Two tenant gates exist, and every tenant surface must be behind one of
	// them: tenantAdminOnly for owner-only surfaces (money, seats, credentials
	// the matrix does not grant) and permOnly for the capabilities the owner can
	// delegate. The failure this scan exists to catch is a route that slips in
	// unwrapped — canned responses and business hours shipped that way, and a
	// seat's write landed under its own (empty) user_id while the API answered
	// 200.
	tenantPrefixes := []string{
		"/api/v1/customers", "/api/v1/widgets",
		"/api/v1/platforms/configs", "/api/v1/platforms/meta/", "/api/v1/platforms/whatsapp/",
		"/api/v1/sla", "/api/v1/routing", "/api/v1/macros",
		"/api/v1/webhooks/subscriptions",
		"/api/v1/settings/business-hours", "/api/v1/settings/canned-responses",
		"/api/v1/settings/faq/suggestions", "/api/v1/settings/telegram-notify",
		"/api/v1/personas", "/api/v1/persona-bindings",
		"/api/v1/admin/users", "/api/v1/admin/analytics/", "/api/v1/admin/agent-performance",
		"/api/v1/admin/intent-analytics", "/api/v1/admin/integrations/status",
		// /api/v1/reports/* streams CSV and cannot hand an error back to a.handle,
		// so the owner check lives in reportCSV (requireOwnerOfTenant) rather than
		// at the route. Pinned by the handler's own refuse-a-seat path.
		// /api/v1/team/* is deliberately absent: those handlers gate inside
		// requireOwnerOfTenant (they must also protect the invite-accept path,
		// which is reachable before a seat exists). Their refusal is pinned by
		// TestOwnerOnlyWrapperRefusesMembers and TestMemberStaysInsideItsTenant.
	}
	checked := 0
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "authed.Handle") {
			continue
		}
		tenantRoute := false
		for _, p := range tenantPrefixes {
			if strings.Contains(trimmed, p) {
				tenantRoute = true
				break
			}
		}
		if !tenantRoute {
			continue
		}
		checked++
		if !strings.Contains(trimmed, "a.permOnly(") && !strings.Contains(trimmed, "a.tenantAdminOnly(") {
			t.Errorf("tenant route is not behind a tenant gate: %s", trimmed)
		}
	}
	if checked < 40 {
		t.Fatalf("only %d tenant routes recognised; the scan stopped matching the router", checked)
	}
	// The platform console keeps its own, stricter guard: that is where money and
	// other tenants' data actually move.
	if n := strings.Count(src, "a.platformAdminOnly("); n < 8 {
		t.Errorf("router.go wires platformAdminOnly %d times, want >= 8", n)
	}
}

// The membership lookup is the part that must not silently invert: it has to read
// agent_teams by the AGENT column (who belongs to whom), scoped to active rows,
// or a member would look like an owner.
func TestCallerIsAgentStatementLooksAtTheAgentColumn(t *testing.T) {
	if !strings.Contains(sqlCallerIsAgent, "agent_teams") {
		t.Fatalf("statement does not read agent_teams: %q", sqlCallerIsAgent)
	}
	if !strings.Contains(sqlCallerIsAgent, "agent_user_id = $1") {
		t.Errorf("statement must key on the agent column and the caller: %q", sqlCallerIsAgent)
	}
	if !strings.Contains(sqlCallerIsAgent, "is_active") {
		t.Errorf("statement must ignore revoked memberships: %q", sqlCallerIsAgent)
	}
	if strings.Contains(sqlCallerIsAgent, "owner_user_id = $1") {
		t.Errorf("statement is asking the wrong direction (owner column): %q", sqlCallerIsAgent)
	}
}

// funcBody returns the source of one function, so the two matrix fixes can be
// pinned at the place that enforces them (the repo's other guard tests scan the
// router the same way). A behavioural test would need a member session, and the
// security property must hold whether or not a DB-backed test is skipped.
func funcBody(t *testing.T, file, signature string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(raw)
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("%s not found in %s", signature, file)
	}
	rest := src[start+len(signature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// Audit P1-5, hole 2: any seat could remove its colleagues because the DELETE
// scoped rows by tenant but never checked who was deleting.
func TestRemoveTeamAgentRunsTheOwnerGate(t *testing.T) {
	if !strings.Contains(funcBody(t, "misc2_handlers.go", "func (a *App) removeTeamAgent"), "requireOwnerOfTenant(user)") {
		t.Error("removeTeamAgent must run requireOwnerOfTenant before deleting a seat")
	}
}

// Audit P1-5, hole 2: promoting a seat to role admin used to bypass the matrix.
// The role editor must refuse that for an active agent.
func TestSeatCannotBePromotedToRoleAdmin(t *testing.T) {
	body := funcBody(t, "admin_handlers.go", "func (a *App) updateUserRole")
	if !strings.Contains(body, "sqlCallerIsAgent") {
		t.Error("updateUserRole must check agent_teams membership before granting role admin")
	}
	if !strings.Contains(body, "席位账号不能设为 admin") {
		t.Error("updateUserRole must reject promoting a seat to admin with an explicit message")
	}
}
