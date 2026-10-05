package api

import (
	"context"
)

// Who may manage a tenant's people and destroy its data.
//
// Every row in users IS a tenant: sessions, knowledge, channels and billing are
// all keyed by user_id, so the tenant's owner is that row itself. The role string
// says how the account came to exist, not what it may do — self-service signup and
// SSO provisioning both produce role "user" (production: user 10 / kw1za), while
// the console and the seeded admin produce "admin" / "platform_admin". Treating
// "role == admin" as "tenant owner" therefore locked self-service merchants out of
// their own team screen (and out of hard-deleting their own sessions).
//
// What IsAdmin() did buy is worth keeping: an agent claimed into someone else's
// team must not act as an owner. That is what agent_teams membership records, so
// the owner test is "not somebody else's agent (and not revoked)" plus the
// historical admin carve-out.
const sqlCallerIsAgent = "SELECT EXISTS (SELECT 1 FROM agent_teams WHERE agent_user_id = $1 AND is_active = true)"

// tenantOwnerAllowed is the whole decision, kept pure so the matrix can be tested
// without a database: admins by role, and anyone who is not currently an agent of
// another tenant.
func tenantOwnerAllowed(isAdmin, isAgent bool) bool {
	return isAdmin || !isAgent
}

// isTenantOwner reports whether the caller may manage this tenant's team or
// permanently delete its sessions.
//
// The lookup fails closed: it guards destructive and team-management actions, and
// a database hiccup must not hand an agent owner-level powers.
func (a *App) isTenantOwner(ctx context.Context, user *CurrentUser) bool {
	if user.IsAdmin() {
		return true
	}
	var isAgent bool
	if err := a.DB.QueryRow(ctx, sqlCallerIsAgent, user.UserID).Scan(&isAgent); err != nil {
		a.Logger.Warn("tenant-owner check failed; refusing", "user_id", user.UserID, "error", err.Error())
		return false
	}
	return tenantOwnerAllowed(false, isAgent)
}
