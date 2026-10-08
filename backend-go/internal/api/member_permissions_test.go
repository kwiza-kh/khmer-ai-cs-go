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
