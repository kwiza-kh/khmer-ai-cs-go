package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/rag"
)

// The permissions feature has two halves that must hold together: a seat works
// INSIDE the owner's tenant (otherwise the seat is useless), and only the grants
// the owner gave (otherwise every seat is an admin). These tests pin both, plus
// the failure direction — a member must never fall back into the owner's data,
// and the tenant filter must stay on the session/doc queries.
//
// Gated on DATABASE_URL like the other tests that need real rows.
type memberProbe struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	app  *App
	ids  []int32
}

func newMemberProbe(t *testing.T) *memberProbe {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)
	p := &memberProbe{t: t, ctx: ctx, pool: pool, app: &App{
		Cfg:    &config.Config{},
		DB:     pool,
		RAG:    &rag.Service{DB: pool},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	t.Cleanup(p.cleanup)
	return p
}

func (p *memberProbe) user(name, role string) int32 {
	p.t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var id int32
	if err := p.pool.QueryRow(p.ctx,
		"INSERT INTO users (username, email, password_hash, role) VALUES ($1,$2,'probe',$3::user_role) RETURNING user_id",
		"__"+name+"_"+suffix, name+"_"+suffix+"@member-probe.invalid", role).Scan(&id); err != nil {
		p.t.Fatalf("seed %s: %v", name, err)
	}
	p.ids = append(p.ids, id)
	return id
}

func (p *memberProbe) seat(ownerID, agentID int32, perms map[string]bool, active bool) {
	p.t.Helper()
	if perms == nil {
		perms = map[string]bool{}
	}
	raw, err := json.Marshal(perms)
	if err != nil {
		p.t.Fatalf("encode permissions: %v", err)
	}
	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO agent_teams (owner_user_id, agent_user_id, display_name, is_active, permissions) VALUES ($1,$2,'probe',$3,$4::jsonb)",
		ownerID, agentID, active, string(raw)); err != nil {
		p.t.Fatalf("seed seat: %v", err)
	}
}

func (p *memberProbe) session(ownerID int32) string {
	p.t.Helper()
	var sid string
	if err := p.pool.QueryRow(p.ctx,
		"INSERT INTO sessions (user_id, platform, platform_user_id, status, language) VALUES ($1,'telegram','probe','active','en') RETURNING session_id::text",
		ownerID).Scan(&sid); err != nil {
		p.t.Fatalf("seed session: %v", err)
	}
	return sid
}

func (p *memberProbe) doc(ownerID int32, title string) {
	p.t.Helper()
	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO knowledge_documents (uploaded_by, title, content) VALUES ($1,$2,'probe body')",
		ownerID, title); err != nil {
		p.t.Fatalf("seed document: %v", err)
	}
}

func (p *memberProbe) cleanup() {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The surfaces wired by the permission matrix seed rows under the probe
	// tenants; delete them before the users they reference, or the user delete
	// fails on the foreign key and the probe leaves accounts behind.
	for _, q := range []string{
		"DELETE FROM canned_responses WHERE user_id = ANY($1::int[])",
		"DELETE FROM business_hours WHERE user_id = ANY($1::int[])",
		"DELETE FROM sla_policies WHERE user_id = ANY($1::int[])",
		"DELETE FROM routing_rules WHERE user_id = ANY($1::int[])",
		"DELETE FROM macros WHERE user_id = ANY($1::int[])",
		"DELETE FROM webhook_subscriptions WHERE user_id = ANY($1::int[])",
		"DELETE FROM customer_profiles WHERE user_id = ANY($1::int[])",
		"DELETE FROM faq_suggestions WHERE user_id = ANY($1::int[])",
		"DELETE FROM widget_tokens WHERE user_id = ANY($1::int[])",
		"DELETE FROM platform_configs WHERE user_id = ANY($1::int[])",
		"DELETE FROM tenant_billing WHERE user_id = ANY($1::int[])",
	} {
		_, _ = p.pool.Exec(c, q, p.ids)
	}
	_, _ = p.pool.Exec(c, "DELETE FROM agent_teams WHERE owner_user_id = ANY($1::int[]) OR agent_user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM agent_teams WHERE owner_user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM knowledge_documents WHERE uploaded_by = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM sessions WHERE user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM users WHERE user_id = ANY($1::int[])", p.ids)
}

// as resolves a caller the way the auth middleware does: the same lookup, then the
// same applyMembership call, against real rows.
func (p *memberProbe) as(userID int32) *CurrentUser {
	p.t.Helper()
	user := &CurrentUser{UserID: userID, Username: "probe", Role: "user"}
	var ownerID *int32
	var rawPerms []byte
	if err := p.pool.QueryRow(p.ctx,
		"SELECT t.owner_user_id, t.permissions FROM users u LEFT JOIN agent_teams t "+
			"  ON t.agent_user_id = u.user_id AND t.is_active = true WHERE u.user_id = $1",
		userID).Scan(&ownerID, &rawPerms); err != nil {
		p.t.Fatalf("membership lookup: %v", err)
	}
	applyMembership(user, ownerID, rawPerms)
	return user
}

func (p *memberProbe) call(user *CurrentUser, body string, fn func(http.ResponseWriter, *http.Request) (any, error)) (any, error) {
	p.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probe", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey, user))
	return fn(httptest.NewRecorder(), req)
}

func (p *memberProbe) expect(err error, status int) {
	p.t.Helper()
	if status == 0 {
		if err != nil {
			p.t.Fatalf("want success, got: %v", err)
		}
		return
	}
	apiErr, ok := err.(*ApiError)
	if !ok {
		p.t.Fatalf("want *ApiError %d, got %T: %v", status, err, err)
	}
	if apiErr.Status != status {
		p.t.Fatalf("status = %d (%s), want %d", apiErr.Status, apiErr.Message, status)
	}
}

func TestMemberResolutionAndDefaults(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("owner", "user")
	member := p.user("member", "user")
	p.seat(owner, member, map[string]bool{}, true)

	resolved := p.as(member)
	if !resolved.IsMember {
		t.Fatal("an active seat did not resolve as a member")
	}
	if resolved.Tenant() != owner {
		t.Errorf("Tenant() = %d, want the owner %d", resolved.Tenant(), owner)
	}
	// Defaults: enough to answer conversations, nothing that changes the surface.
	for _, perm := range []string{PermInboxView, PermInboxReply, PermInboxTakeover, PermKnowledgeView} {
		if !resolved.Can(perm) {
			t.Errorf("default denies %s; a fresh seat must be able to work", perm)
		}
	}
	for _, perm := range []string{PermInboxAssign, PermKnowledgeEdit} {
		if resolved.Can(perm) {
			t.Errorf("default grants %s; it must be opt-in", perm)
		}
	}

	// An owner is not a member and keeps full run of their own tenant.
	ownerUser := p.as(owner)
	if ownerUser.IsMember || ownerUser.Tenant() != owner {
		t.Errorf("owner resolved as IsMember=%v Tenant=%d", ownerUser.IsMember, ownerUser.Tenant())
	}
	if !ownerUser.Can(PermKnowledgeEdit) {
		t.Error("owner must not be gated by member permissions")
	}

	// A deactivated seat stops being a member (fail closed, back to own tenant).
	if _, err := p.pool.Exec(p.ctx, "UPDATE agent_teams SET is_active = false WHERE agent_user_id = $1", member); err != nil {
		t.Fatalf("deactivate seat: %v", err)
	}
	if inactive := p.as(member); inactive.IsMember || inactive.Tenant() != member {
		t.Errorf("inactive seat resolved as IsMember=%v Tenant=%d", inactive.IsMember, inactive.Tenant())
	}
}

func TestOwnerSetsMemberPermissions(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("perm_owner", "user")
	member := p.user("perm_member", "user")
	p.seat(owner, member, map[string]bool{}, true)

	var teamID int32
	if err := p.pool.QueryRow(p.ctx, "SELECT team_id FROM agent_teams WHERE agent_user_id = $1", member).Scan(&teamID); err != nil {
		t.Fatalf("read seat: %v", err)
	}

	ownerUser := p.as(owner)
	memberUser := p.as(member)

	// A member cannot grant themselves anything.
	_, err := p.call(memberUser, `{"permissions":{"knowledge_edit":true}}`, func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.updateMemberPermissions(w, r, teamID)
	})
	p.expect(err, http.StatusForbidden)

	// Unknown keys are rejected, so a typo cannot store a grant no handler reads.
	_, err = p.call(ownerUser, `{"permissions":{"root":true}}`, func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.updateMemberPermissions(w, r, teamID)
	})
	p.expect(err, http.StatusBadRequest)

	// Revoke a default and grant an opt-in; the member sees both on the next resolve.
	if _, err := p.call(ownerUser, `{"permissions":{"inbox_reply":false,"knowledge_edit":true}}`, func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.updateMemberPermissions(w, r, teamID)
	}); err != nil {
		t.Fatalf("owner update failed: %v", err)
	}
	after := p.as(member)
	if after.Can(PermInboxReply) {
		t.Error("inbox_reply stayed granted after the owner revoked it")
	}
	if !after.Can(PermKnowledgeEdit) {
		t.Error("knowledge_edit was not granted")
	}
	if !after.Can(PermInboxView) {
		t.Error("an unrelated default (inbox_view) changed")
	}

	// The roster shows the effective set, which is what the console toggles render.
	res, err := p.call(ownerUser, "", p.app.listTeam)
	if err != nil {
		t.Fatalf("listTeam: %v", err)
	}
	rows, _ := res.([]map[string]any)
	found := false
	for _, row := range rows {
		if id, _ := row["agent_user_id"].(int); id == int(member) {
			found = true
			perms, _ := row["permissions"].(map[string]bool)
			if perms[PermInboxReply] {
				t.Error("listTeam reports inbox_reply as granted after revocation")
			}
			if !perms[PermKnowledgeEdit] {
				t.Error("listTeam does not report the granted knowledge_edit")
			}
		}
	}
	if !found {
		t.Fatal("the member is missing from listTeam")
	}
}

func TestMemberSeesTenantDataWithinGrantedPermissions(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("data_owner", "user")
	member := p.user("data_member", "user")
	p.seat(owner, member, map[string]bool{}, true) // defaults: view+reply, no assign/edit
	sid := p.session(owner)
	p.doc(owner, "tenant policy")

	memberUser := p.as(member)

	// Inbox: the seat sees the tenant's conversations (its whole purpose).
	res, err := p.call(memberUser, "", p.app.listInbox)
	if err != nil {
		t.Fatalf("member listInbox: %v", err)
	}
	inbox, _ := res.(map[string]any)
	items, _ := inbox["data"].([]map[string]any)
	if len(items) == 0 {
		t.Fatal("a member with inbox_view saw an empty inbox; the tenant filter did not switch")
	}

	// Knowledge read is a default; the edit gate is not.
	res, err = p.call(memberUser, "", p.app.listKnowledge)
	if err != nil {
		t.Fatalf("member listKnowledge: %v", err)
	}
	docs, _ := res.(map[string]any)
	if total, _ := docs["total"].(int64); total == 0 {
		t.Error("a member with knowledge_view did not see the tenant's documents")
	}
	_, err = p.call(memberUser, `{"title":"x","content":"y"}`, p.app.uploadKnowledge)
	p.expect(err, http.StatusForbidden)

	// Session access follows the per-action permission, not just membership.
	if err := p.app.ensureSessionAccess(p.ctx, sid, memberUser, PermInboxReply); err != nil {
		t.Fatalf("member with inbox_reply was refused: %v", err)
	}
	if err := p.app.ensureSessionAccess(p.ctx, sid, memberUser, PermInboxAssign); err == nil {
		t.Error("member without inbox_assign passed the assign gate")
	}

	// Revoke the view permission: the same call is refused.
	if _, err := p.pool.Exec(p.ctx, "UPDATE agent_teams SET permissions = '{\"inbox_view\":false}'::jsonb WHERE agent_user_id = $1", member); err != nil {
		t.Fatalf("revoke inbox_view: %v", err)
	}
	revoked := p.as(member)
	if _, err := p.call(revoked, "", p.app.listInbox); err == nil {
		t.Error("inbox_view was revoked and listInbox still succeeded")
	} else {
		p.expect(err, http.StatusForbidden)
	}
}

func TestMemberStaysInsideItsTenant(t *testing.T) {
	p := newMemberProbe(t)
	ownerA := p.user("iso_owner_a", "user")
	ownerB := p.user("iso_owner_b", "user")
	member := p.user("iso_member", "user")
	// Every permission granted — isolation must not depend on the matrix.
	all := map[string]bool{}
	for key := range memberPermissionDefaults {
		all[key] = true
	}
	p.seat(ownerA, member, all, true)
	sidA := p.session(ownerA)
	sidB := p.session(ownerB)

	memberUser := p.as(member)
	if err := p.app.ensureSessionAccess(p.ctx, sidA, memberUser, PermInboxView); err != nil {
		t.Fatalf("member refused its own tenant's session: %v", err)
	}
	if err := p.app.ensureSessionAccess(p.ctx, sidB, memberUser, PermInboxView); err == nil {
		t.Fatal("member reached another tenant's session")
	} else {
		p.expect(err, http.StatusNotFound)
	}

	res, err := p.call(memberUser, "", p.app.listInbox)
	if err != nil {
		t.Fatalf("member listInbox: %v", err)
	}
	inbox, _ := res.(map[string]any)
	for _, row := range inbox["data"].([]map[string]any) {
		if got, _ := row["session_id"].(string); got == sidB {
			t.Fatal("listInbox leaked another tenant's session to a member")
		}
	}
}

func TestOwnerOnlyWrapperRefusesMembers(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("wrap_owner", "user")
	member := p.user("wrap_member", "user")
	p.seat(owner, member, map[string]bool{}, true)

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	guarded := p.app.tenantAdminOnly(next)

	for _, tc := range []struct {
		user    *CurrentUser
		reached bool
	}{
		{p.as(member), false}, // credentials/public tokens stay owner-only
		{p.as(owner), true},
	} {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/configs", nil)
		req = req.WithContext(context.WithValue(req.Context(), userKey, tc.user))
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		if tc.reached && !reached {
			t.Errorf("owner was refused by the owner-only wrapper (status %d)", rec.Code)
		}
		if !tc.reached && (reached || rec.Code != http.StatusForbidden) {
			t.Errorf("member reached an owner-only route (status %d, reached=%v)", rec.Code, reached)
		}
	}
}

// The surface matrix is data the console renders. If the whitelist and the
// defaults drift apart, the dialog shows a toggle that grants nothing — or a
// capability exists that no toggle can reach. Pin both, plus the rule that the
// write half is never on by default.
func TestSurfacePermissionsDefaultToReadOnly(t *testing.T) {
	want := map[string]bool{
		PermChannelsView: true, PermChannelsManage: false,
		PermWidgetView: true, PermWidgetManage: false,
		PermCustomersView: true, PermCustomersEdit: false,
		PermScriptsView: true, PermScriptsEdit: false,
		PermSettingsView: true, PermSettingsManage: false,
	}
	for key, granted := range want {
		if !validPermissionKey(key) {
			t.Errorf("%s is not in the whitelist; the console cannot grant it", key)
		}
		if memberPermissionDefaults[key] != granted {
			t.Errorf("default for %s = %v, want %v", key, memberPermissionDefaults[key], granted)
		}
	}
	// 6 inbox/knowledge keys + 10 surface keys. The console renders exactly the
	// whitelist, so a new key must land here and in the dialog together.
	if len(memberPermissionDefaults) != 16 {
		t.Errorf("matrix has %d keys, want 16 — update the console dialog with any change here", len(memberPermissionDefaults))
	}
	// The default floor must not grant a write on any surface.
	for _, write := range []string{PermChannelsManage, PermWidgetManage, PermCustomersEdit, PermScriptsEdit, PermSettingsManage, PermKnowledgeEdit, PermInboxAssign} {
		if memberPermissionDefaults[write] {
			t.Errorf("%s is a write and must be opt-in, but the default grants it", write)
		}
	}
}

// permOnly is the route-level half. It must pass owners and the platform admin,
// and answer a member exactly like the handler-side requirePermission would.
func TestPermOnlyGate(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("gate_owner", "user")
	member := p.user("gate_member", "user")
	p.seat(owner, member, map[string]bool{PermCustomersEdit: true}, true)

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	call := func(user *CurrentUser, perm string) int {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
		req = req.WithContext(context.WithValue(req.Context(), userKey, user))
		rec := httptest.NewRecorder()
		p.app.permOnly(perm)(next).ServeHTTP(rec, req)
		return rec.Code
	}

	memberUser := p.as(member)
	if code := call(memberUser, PermCustomersEdit); code != http.StatusOK || !reached {
		t.Errorf("granted customers_edit was refused (status %d)", code)
	}
	if code := call(memberUser, PermScriptsEdit); code != http.StatusForbidden || reached {
		t.Errorf("ungranted scripts_edit reached the handler (status %d)", code)
	}
	// A default-granted view passes without an explicit grant.
	if code := call(memberUser, PermCustomersView); code != http.StatusOK {
		t.Errorf("default customers_view was refused (status %d)", code)
	}
	if code := call(p.as(owner), PermScriptsEdit); code != http.StatusOK || !reached {
		t.Errorf("owner was refused by the permission gate (status %d)", code)
	}
	// No user in context: 401, never a silent pass.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
	rec := httptest.NewRecorder()
	p.app.permOnly(PermCustomersView)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated request got status %d, want 401", rec.Code)
	}
}

// The regression this matrix would otherwise reintroduce: a seat's write must
// land in the owner's tenant, and its reads must see the owner's rows. Before
// the handlers bound user.Tenant(), a member's create landed under the member's
// own (empty) user_id — the write looked successful and nobody else saw it.
func TestMemberSurfaceWritesLandInTenant(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("surf_owner", "user")
	member := p.user("surf_member", "user")
	p.seat(owner, member, map[string]bool{
		PermScriptsEdit:    true,
		PermSettingsManage: true,
		PermCustomersEdit:  true,
	}, true)

	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO business_hours (user_id, weekday, open_time, close_time, is_active) VALUES ($1, 3, '09:00', '17:00', true)", owner); err != nil {
		t.Fatalf("seed business hours: %v", err)
	}
	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO canned_responses (user_id, title, body, language, is_active) VALUES ($1, 'greet', 'hello', 'en', true)", owner); err != nil {
		t.Fatalf("seed canned response: %v", err)
	}
	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO customer_profiles (user_id, platform, platform_user_id, display_name) VALUES ($1, 'telegram', 'probe-cust', 'Probe')", owner); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	memberUser := p.as(member)

	// Reads resolve to the owner's rows, not the empty tenant a raw user_id would name.
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request) (any, error){
		"business hours":   p.app.listBusinessHours,
		"canned responses": p.app.listCannedResponses,
		"customers":        p.app.listCustomers,
	} {
		res, err := p.call(memberUser, "", fn)
		if err != nil {
			t.Fatalf("member %s: %v", name, err)
		}
		rows, _ := res.([]map[string]any)
		if len(rows) == 0 {
			t.Errorf("member %s read landed on the member's own empty tenant", name)
		}
	}

	// A write goes to the tenant, and created_by keeps the acting seat.
	res, err := p.call(memberUser, `{"title":"probe","body":"probe"}`, p.app.createCannedResponse)
	if err != nil {
		t.Fatalf("member createCannedResponse: %v", err)
	}
	id, _ := res.(map[string]any)["id"].(int)
	if id == 0 {
		t.Fatalf("createCannedResponse returned no id: %#v", res)
	}
	var ownerID int32
	var createdBy *int32
	if err := p.pool.QueryRow(p.ctx,
		"SELECT user_id, created_by FROM canned_responses WHERE id = $1", id).Scan(&ownerID, &createdBy); err != nil {
		t.Fatalf("read created row: %v", err)
	}
	if ownerID != owner {
		t.Errorf("row user_id = %d, want the tenant owner %d", ownerID, owner)
	}
	if createdBy == nil || *createdBy != member {
		t.Errorf("created_by = %v, want the acting seat %d", createdBy, member)
	}
}

// The login/profile payload is the console's only view of the effective set.
// It must match what Can() enforces, and owners must get no map at all.
func TestMemberIdentityPayload(t *testing.T) {
	p := newMemberProbe(t)
	owner := p.user("ident_owner", "user")
	member := p.user("ident_member", "user")
	p.seat(owner, member, map[string]bool{PermKnowledgeEdit: true, PermScriptsView: false}, true)

	isMember, perms := p.app.memberIdentity(p.ctx, member)
	if !isMember {
		t.Fatal("an active seat did not resolve as a member")
	}
	if !perms[PermKnowledgeEdit] {
		t.Error("the payload dropped the explicit knowledge_edit grant")
	}
	if perms[PermScriptsView] {
		t.Error("the payload ignored the explicit scripts_view revocation")
	}
	if !perms[PermInboxView] {
		t.Error("the payload is missing a default (inbox_view)")
	}
	if isMember, perms := p.app.memberIdentity(p.ctx, owner); isMember || perms != nil {
		t.Errorf("owner resolved as a member payload (isMember=%v, perms=%v)", isMember, perms)
	}
}
