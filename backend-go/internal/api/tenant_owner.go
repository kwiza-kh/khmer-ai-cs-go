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
// without a database.
//
// Membership dominates the role string. The previous version let "role == admin"
// win over membership, so a seat whose role was edited to admin bypassed the
// member permission matrix entirely (audit P1-5: roster rows, billing and reports
// were reachable from a seat). Only the platform super-role is exempt — it is a
// cross-tenant operator, never somebody else's agent.
func tenantOwnerAllowed(isPlatformAdmin, isAgent bool) bool {
	return isPlatformAdmin || !isAgent
}

// isTenantOwner reports whether the caller may manage this tenant's team or
// permanently delete its sessions.
func (a *App) isTenantOwner(ctx context.Context, user *CurrentUser) bool {
	return a.tenantOwnerFor(ctx, user.UserID, user.IsPlatformAdmin())
}

// isTenantOwnerID is isTenantOwner for callers that hold only what they read from
// the database (login, register, profile) rather than a request's CurrentUser.
func (a *App) isTenantOwnerID(ctx context.Context, userID int32, role string) bool {
	return a.tenantOwnerFor(ctx, userID, role == "platform_admin")
}

// tenantOwnerFor is the whole check. The membership lookup fails closed: it
// guards destructive and team-management actions, and a database hiccup must not
// hand an agent owner-level powers.
func (a *App) tenantOwnerFor(ctx context.Context, userID int32, isPlatformAdmin bool) bool {
	if isPlatformAdmin {
		return true
	}
	var isAgent bool
	if err := a.DB.QueryRow(ctx, sqlCallerIsAgent, userID).Scan(&isAgent); err != nil {
		a.Logger.Warn("tenant-owner check failed; refusing", "user_id", userID, "error", err.Error())
		return false
	}
	return tenantOwnerAllowed(false, isAgent)
}
