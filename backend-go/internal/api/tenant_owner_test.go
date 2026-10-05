package api

import (
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
		name    string
		isAdmin bool
		isAgent bool
		want    bool
	}{
		{name: "platform/tenant admin by role", isAdmin: true, isAgent: false, want: true},
		{name: "admin who is also an agent elsewhere", isAdmin: true, isAgent: true, want: true},
		{name: "self-service owner (role user, not an agent)", isAdmin: false, isAgent: false, want: true},
		{name: "agent of another tenant", isAdmin: false, isAgent: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tenantOwnerAllowed(tc.isAdmin, tc.isAgent); got != tc.want {
				t.Errorf("tenantOwnerAllowed(isAdmin=%v, isAgent=%v) = %v, want %v",
					tc.isAdmin, tc.isAgent, got, tc.want)
			}
		})
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
