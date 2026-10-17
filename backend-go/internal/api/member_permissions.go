package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// Member permissions — what an invited seat may do inside the owner's tenant.
//
// Keys are the wire contract: the owner console writes them (PUT
// /team/agents/{id}/permissions) and handlers read them through
// CurrentUser.Can. Adding a capability means adding a key here plus the handler
// check; the whitelist is what keeps a typo from silently granting nothing — or
// worse, from being stored while a lookalike default stays open.
//
// Only wired capabilities are listed. Billing, team management and Telegram
// notification routing stay owner-only (they move money, grant seats, or decide
// which chat receives every customer message) and are enforced with
// tenantAdminOnly at the route, not here.
//
// Every key with a *_view/*_manage pair follows the same rule: the default floor
// grants the view (an agent needs context to answer), never the write (a write
// changes the tenant's public surface).
const (
	PermInboxView     = "inbox_view"     // list and read the tenant's conversations
	PermInboxReply    = "inbox_reply"    // reply as the tenant (and tag/archive)
	PermInboxTakeover = "inbox_takeover" // take a conversation over from the AI
	PermInboxAssign   = "inbox_assign"   // assign conversations to other agents
	PermKnowledgeView = "knowledge_view" // search and read the knowledge base
	PermKnowledgeEdit = "knowledge_edit" // upload, edit and delete documents

	// Channels: the provider connections (Messenger/Instagram/Telegram/WhatsApp
	// and the website widget's public tokens). Manage writes sealed credentials,
	// so it is the highest-impact grant in this list — a member who swaps a Page
	// token redirects the tenant's Messenger to another page.
	PermChannelsView   = "channels_view"   // list connections, work queue, retries, connection check
	PermChannelsManage = "channels_manage" // connect/rotate/deactivate a channel, meta OAuth, WhatsApp signup
	PermWidgetView     = "widget_view"     // see the website-widget embed tokens
	PermWidgetManage   = "widget_manage"   // create/rotate/revoke them

	// Customers: 360 and notes.
	PermCustomersView = "customers_view"
	PermCustomersEdit = "customers_edit"

	// Scripts (话术): canned responses and macros — the reply library.
	PermScriptsView = "scripts_view"
	PermScriptsEdit = "scripts_edit"

	// Settings: business hours, personas, SLA, routing rules. These change what
	// the AI says and when it says it, so the view floor is deliberate and the
	// write is opt-in.
	PermSettingsView   = "settings_view"
	PermSettingsManage = "settings_manage"
)

// memberPermissionDefaults is the floor a seat gets when the owner has said
// nothing: enough to answer conversations and look up context, nothing that
// changes the tenant's public surface or its credentials. Keep this map in sync
// with the console — the UI renders exactly these keys.
var memberPermissionDefaults = map[string]bool{
	PermInboxView:     true,
	PermInboxReply:    true,
	PermInboxTakeover: true,
	PermInboxAssign:   false,
	PermKnowledgeView: true,
	PermKnowledgeEdit: false,

	PermChannelsView:   true,
	PermChannelsManage: false,
	PermWidgetView:     true,
	PermWidgetManage:   false,
	PermCustomersView:  true,
	PermCustomersEdit:  false,
	PermScriptsView:    true,
	PermScriptsEdit:    false,
	PermSettingsView:   true,
	PermSettingsManage: false,
}

func validPermissionKey(key string) bool {
	_, ok := memberPermissionDefaults[key]
	return ok
}

// effectivePermissions merges the owner's explicit grants over the defaults, so
// the console shows (and the handlers enforce) one resolved truth. Unknown keys
// are dropped rather than carried: a grant for a capability that no longer
// exists must not look like a live one.
func effectivePermissions(stored map[string]bool) map[string]bool {
	out := make(map[string]bool, len(memberPermissionDefaults))
	for key, def := range memberPermissionDefaults {
		out[key] = def
	}
	for key, granted := range stored {
		if validPermissionKey(key) {
			out[key] = granted
		}
	}
	return out
}

// decodePermissions reads the JSONB column. A malformed value degrades to "no
// explicit grants" (defaults apply) instead of failing the request: the column
// is written by this API, so a parse error means data drift, and locking a
// member out of their inbox over it would be the wrong failure direction.
func decodePermissions(raw []byte) map[string]bool {
	stored := map[string]bool{}
	if len(raw) == 0 {
		return stored
	}
	// A JSON `null` (or malformed bytes) leaves the destination untouched; both
	// mean "no explicit grants", not "grant nothing forever".
	if err := json.Unmarshal(raw, &stored); err != nil || stored == nil {
		return map[string]bool{}
	}
	return stored
}

// applyMembership turns a seat lookup into the caller's effective identity:
// ownerID non-nil means "active seat in that tenant", nil means the caller is
// an independent tenant (or the platform admin, who keeps their own scope).
func applyMembership(user *CurrentUser, ownerID *int32, rawPerms []byte) {
	if user == nil {
		return
	}
	user.TenantID = user.UserID
	user.IsMember = false
	user.Permissions = nil
	if ownerID == nil || user.IsPlatformAdmin() {
		return
	}
	user.IsMember = true
	user.TenantID = *ownerID
	user.Permissions = decodePermissions(rawPerms)
}

// The auth middleware folds the seat lookup into the users query it already runs.
// lookup into the users query it already runs and calls applyMembership directly.
//

// requirePermission is the handler-side gate. Owners and platform admins always
// pass; a member needs the key granted (explicitly, or by default).
func requirePermission(user *CurrentUser, perm string) error {
	if user == nil {
		return ErrUnauthorized("未提供认证令牌")
	}
	if !user.Can(perm) {
		return ErrForbidden("没有该操作的权限")
	}
	return nil
}

// requireOwnerOfTenant refuses members outright: credentials and public tokens
// belong to the tenant's owner, and this endpoint has no grantable form (yet).
func requireOwnerOfTenant(user *CurrentUser) error {
	if user == nil {
		return ErrUnauthorized("未提供认证令牌")
	}
	if user.IsMember && !user.IsPlatformAdmin() {
		return ErrForbidden("只有店主可以管理该设置")
	}
	return nil
}

// permOnly gates a tenant route on one member permission key. Owners and the
// platform admin pass (Can returns true for them); a member needs the grant.
//
// This is the replacement for tenantAdminOnly on surfaces the matrix now covers.
// The gate is the route-level half; the handler's other half is addressing the
// tenant by user.Tenant() rather than the caller's own id, or a member's write
// would land in an empty tenant of their own.
func (a *App) permOnly(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFrom(r)
			if !ok || user == nil {
				WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "未提供认证令牌"})
				return
			}
			if !user.Can(perm) {
				WriteJSON(w, http.StatusForbidden, map[string]string{"error": "没有该操作的权限"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// memberIdentity resolves a seat for payloads that describe the caller to the
// console (login, profile). The auth middleware runs the same lookup on every
// request; this is the login-time twin. It returns the EFFECTIVE set — defaults
// merged over the owner's grants — so the console renders exactly what the
// handlers will enforce, rather than a second truth that can drift.
func (a *App) memberIdentity(ctx context.Context, userID int32) (bool, map[string]bool) {
	var ownerID *int32
	var raw []byte
	err := a.DB.QueryRow(ctx,
		"SELECT owner_user_id, permissions FROM agent_teams WHERE agent_user_id = $1 AND is_active = true",
		userID).Scan(&ownerID, &raw)
	if err != nil || ownerID == nil {
		return false, nil
	}
	return true, effectivePermissions(decodePermissions(raw))
}

// updateMemberPermissionsRequest is the console payload: the full effective set
// for one seat, not a delta — the dialog always sends what the toggles show.
type updateMemberPermissionsRequest struct {
	Permissions map[string]bool `json:"permissions"`
}

// updateMemberPermissions — PUT /api/v1/team/agents/{id}/permissions. Owner
// only; the target must be a seat of the caller's own tenant.
func (a *App) updateMemberPermissions(w http.ResponseWriter, r *http.Request, teamID int32) (any, error) {
	user, _ := UserFrom(r)
	if !a.isTenantOwner(r.Context(), user) {
		return nil, ErrForbidden("只有租户所有者可以设置成员权限")
	}
	var req updateMemberPermissionsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	for key := range req.Permissions {
		if !validPermissionKey(key) {
			return nil, ErrBadRequest("未知的权限项")
		}
	}
	raw, err := json.Marshal(req.Permissions)
	if err != nil {
		return nil, ErrInternal("保存失败")
	}
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE agent_teams SET permissions = $1::jsonb WHERE team_id = $2 AND owner_user_id = $3",
		string(raw), teamID, user.UserID)
	if err != nil {
		return nil, ErrInternal("保存失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("成员不存在")
	}
	return map[string]any{"message": "已更新", "permissions": effectivePermissions(req.Permissions)}, nil
}
