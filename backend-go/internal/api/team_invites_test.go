package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
)

// The invite flow exists because binding an account by user_id was both
// unusable (nobody is shown their id) and unsafe (any owner could claim a
// freshly registered account, then change its role or disable it). These tests
// pin the properties that make it a replacement:
//
//   - acceptance binds the CALLER's own id — the request body only carries the
//     code, so there is no id to aim at;
//   - a link carries its own expiry and capacity, and the capacity is enforced
//     under a row lock (never more joins than max_uses);
//   - a rejected accept (seat full, already seated) rolls everything back, so
//     the link survives instead of being burned;
//   - the history keeps every link — including revoked ones — with who joined
//     through it, which is the audit trail the owner asked for;
//   - an independent tenant, a platform admin, and the owner themselves cannot
//     be seated through a link.
//
// Gated on DATABASE_URL like the other tests that need real unique indexes.
type inviteProbe struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	app  *App
	ids  []int32
}

func newInviteProbe(t *testing.T) *inviteProbe {
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
	p := &inviteProbe{t: t, ctx: ctx, pool: pool, app: &App{
		Cfg:    &config.Config{Server: config.ServerConfig{PublicAPIURL: "https://cs.example.test/api/v1"}},
		DB:     pool,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	t.Cleanup(p.cleanup)
	return p
}

// user seeds a plain account and remembers it for cleanup. Role is a parameter
// because the platform-admin refusal is one of the behaviours under test.
func (p *inviteProbe) user(name, role string) int32 {
	p.t.Helper()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var id int32
	if err := p.pool.QueryRow(p.ctx,
		"INSERT INTO users (username, email, password_hash, role) VALUES ($1,$2,'probe',$3::user_role) RETURNING user_id",
		"__"+name+"_"+suffix, name+"_"+suffix+"@invite-probe.invalid", role).Scan(&id); err != nil {
		p.t.Fatalf("seed %s: %v", name, err)
	}
	p.ids = append(p.ids, id)
	return id
}

// setPlan gives an owner more than the free tier's single seat, which is what
// multi-use links need to be exercised.
func (p *inviteProbe) setPlan(userID int32, plan string) {
	p.t.Helper()
	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO tenant_billing (user_id, plan) VALUES ($1,$2) ON CONFLICT (user_id) DO UPDATE SET plan = EXCLUDED.plan",
		userID, plan); err != nil {
		p.t.Fatalf("set plan %s: %v", plan, err)
	}
}

func (p *inviteProbe) username(id int32) string {
	p.t.Helper()
	var name string
	if err := p.pool.QueryRow(p.ctx, "SELECT username FROM users WHERE user_id = $1", id).Scan(&name); err != nil {
		p.t.Fatalf("read username: %v", err)
	}
	return name
}

func (p *inviteProbe) cleanup() {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = p.pool.Exec(c, "DELETE FROM agent_teams WHERE owner_user_id = ANY($1::int[]) OR agent_user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM team_invites WHERE owner_user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM tenant_billing WHERE user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM users WHERE user_id = ANY($1::int[])", p.ids)
}

// call invokes a handler as the given account, the same way the router does
// after the auth middleware.
func (p *inviteProbe) call(userID int32, role, body string, fn func(http.ResponseWriter, *http.Request) (any, error)) (any, error) {
	p.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/team/invites", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), userKey,
		&CurrentUser{UserID: userID, Username: "probe", Role: role}))
	return fn(httptest.NewRecorder(), req)
}

// createInviteOpts mints a link as the owner, returning its id and the code the
// invitee would receive (parsed out of the URL the handler returns, so the URL
// shape itself is part of what is tested). Zero values mean the handler's
// defaults (7 days, one use).
func (p *inviteProbe) createInviteOpts(ownerID int32, role, displayName string, skills []string, maxUses, expiresInHours int) (int64, string) {
	p.t.Helper()
	if skills == nil {
		skills = []string{}
	}
	raw, err := json.Marshal(map[string]any{
		"display_name": displayName, "skills": skills,
		"max_uses": maxUses, "expires_in_hours": expiresInHours,
	})
	if err != nil {
		p.t.Fatalf("encode invite body: %v", err)
	}
	res, err := p.call(ownerID, role, string(raw), p.app.createTeamInvite)
	if err != nil {
		p.t.Fatalf("createInvite: %v", err)
	}
	entry, ok := res.(map[string]any)
	if !ok {
		p.t.Fatalf("createInvite returned %T, want a map", res)
	}
	rawURL, _ := entry["url"].(string)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		p.t.Fatalf("invite url %q does not parse: %v", rawURL, err)
	}
	code := parsed.Query().Get("code")
	if !strings.HasPrefix(code, "ti_") {
		p.t.Fatalf("invite code = %q, want a ti_ token", code)
	}
	id, _ := entry["invite_id"].(int64)
	return id, code
}

func (p *inviteProbe) createInvite(ownerID int32, role, displayName string, skills []string) (int64, string) {
	p.t.Helper()
	return p.createInviteOpts(ownerID, role, displayName, skills, 0, 0)
}

func (p *inviteProbe) accept(userID int32, role, code string) error {
	p.t.Helper()
	_, err := p.call(userID, role, `{"code":"`+code+`"}`, p.app.acceptTeamInvite)
	return err
}

func (p *inviteProbe) status(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("want success, got: %v", err)
		}
		return
	}
	apiErr, ok := err.(*ApiError)
	if !ok {
		t.Fatalf("want *ApiError %d, got %T: %v", want, err, err)
	}
	if apiErr.Status != want {
		t.Fatalf("status = %d (%s), want %d", apiErr.Status, apiErr.Message, want)
	}
}

func (p *inviteProbe) pending(ownerID int32, role string) []map[string]any {
	p.t.Helper()
	res, err := p.call(ownerID, role, "", p.app.listTeamInvites)
	if err != nil {
		p.t.Fatalf("listTeamInvites: %v", err)
	}
	out, ok := res.([]map[string]any)
	if !ok {
		p.t.Fatalf("pending list returned %T, want []map[string]any", res)
	}
	return out
}

func (p *inviteProbe) history(ownerID int32, role string) []map[string]any {
	p.t.Helper()
	res, err := p.call(ownerID, role, "", p.app.listTeamInviteHistory)
	if err != nil {
		p.t.Fatalf("listTeamInviteHistory: %v", err)
	}
	out, ok := res.([]map[string]any)
	if !ok {
		p.t.Fatalf("history returned %T, want []map[string]any", res)
	}
	return out
}

func historyEntry(t *testing.T, rows []map[string]any, inviteID int64) map[string]any {
	t.Helper()
	for _, row := range rows {
		if id, _ := row["invite_id"].(int64); id == inviteID {
			return row
		}
	}
	t.Fatalf("invite %d missing from history (%d rows)", inviteID, len(rows))
	return nil
}

func invitedUsernames(t *testing.T, entry map[string]any) []string {
	t.Helper()
	raw, ok := entry["invited"].([]map[string]any)
	if !ok {
		t.Fatalf("invited = %T, want []map[string]any", entry["invited"])
	}
	names := make([]string, 0, len(raw))
	for _, u := range raw {
		name, _ := u["username"].(string)
		names = append(names, name)
	}
	return names
}

func TestInviteStatusOrdering(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	revoked := now
	cases := []struct {
		name      string
		revokedAt *time.Time
		expiresAt time.Time
		used      int64
		max       int64
		want      string
	}{
		{"fresh", nil, future, 0, 1, "pending"},
		{"partly used", nil, future, 1, 3, "pending"},
		{"used up before expiry", nil, future, 1, 1, "exhausted"},
		{"expired unused", nil, past, 0, 1, "expired"},
		{"exhausted wins over expired", nil, past, 2, 2, "exhausted"},
		{"revoked wins over everything", &revoked, past, 2, 2, "revoked"},
	}
	for _, tc := range cases {
		if got := inviteStatus(tc.revokedAt, tc.expiresAt, tc.used, tc.max, now); got != tc.want {
			t.Errorf("%s: status = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTeamInviteAcceptBindsCallerAndIsSingleUse(t *testing.T) {
	p := newInviteProbe(t)
	owner := p.user("inv_owner", "user")
	invitee := p.user("inv_mate", "user")

	inviteID, code := p.createInvite(owner, "user", "Bopha", []string{"sales"})

	// The body carries only the code: acceptance can only ever bind the caller.
	p.status(t, p.accept(invitee, "user", code), 0)

	var boundOwner int32
	var displayName string
	var skills []string
	if err := p.pool.QueryRow(p.ctx,
		"SELECT owner_user_id, display_name, skills FROM agent_teams WHERE agent_user_id = $1",
		invitee).Scan(&boundOwner, &displayName, &skills); err != nil {
		t.Fatalf("agent_teams row for the invitee: %v", err)
	}
	if boundOwner != owner {
		t.Errorf("bound owner = %d, want the inviting owner %d", boundOwner, owner)
	}
	if displayName != "Bopha" || len(skills) != 1 || skills[0] != "sales" {
		t.Errorf("agent label = (%q, %v), want the invite's (Bopha, [sales])", displayName, skills)
	}

	// The default link seats exactly one person: the second attempt finds no
	// capacity left, and nobody else is seated.
	p.status(t, p.accept(p.user("inv_other", "user"), "user", code), http.StatusConflict)
	entry := historyEntry(t, p.history(owner, "user"), inviteID)
	if got, _ := entry["use_count"].(int64); got != 1 {
		t.Errorf("use_count = %v, want 1", entry["use_count"])
	}
	if got, _ := entry["status"].(string); got != "exhausted" {
		t.Errorf("status = %q, want exhausted", got)
	}
	if names := invitedUsernames(t, entry); len(names) != 1 || names[0] != p.username(invitee) {
		t.Errorf("invited = %v, want just %q", names, p.username(invitee))
	}
}

func TestTeamInviteMultiUseSeatsSeveralAndHistoryRecordsThem(t *testing.T) {
	p := newInviteProbe(t)
	owner := p.user("multi_owner", "user")
	p.setPlan(owner, "pro") // five seats
	alice := p.user("multi_alice", "user")
	bob := p.user("multi_bob", "user")

	before := time.Now()
	inviteID, code := p.createInviteOpts(owner, "user", "Sales team", []string{"sales"}, 3, 24)
	p.status(t, p.accept(alice, "user", code), 0)
	p.status(t, p.accept(bob, "user", code), 0)

	// Pending list reflects the capacity actually used.
	var pendingEntry map[string]any
	for _, row := range p.pending(owner, "user") {
		if id, _ := row["invite_id"].(int64); id == inviteID {
			pendingEntry = row
		}
	}
	if pendingEntry == nil {
		t.Fatal("a link with capacity left is missing from the pending list")
	}
	if got, _ := pendingEntry["use_count"].(int64); got != 2 {
		t.Errorf("pending use_count = %v, want 2", pendingEntry["use_count"])
	}
	if got, _ := pendingEntry["max_uses"].(int64); got != 3 {
		t.Errorf("pending max_uses = %v, want 3", pendingEntry["max_uses"])
	}

	// History: detailed per-link log.
	entry := historyEntry(t, p.history(owner, "user"), inviteID)
	if got, _ := entry["status"].(string); got != "pending" {
		t.Errorf("status = %q, want pending (one use left)", got)
	}
	if names := invitedUsernames(t, entry); len(names) != 2 ||
		names[0] != p.username(alice) || names[1] != p.username(bob) {
		t.Errorf("invited = %v, want [%q %q] in join order", names, p.username(alice), p.username(bob))
	}
	// Per-link expiry: the requested 24h window, not the 7-day default.
	expires, _ := entry["expires_at"].(time.Time)
	if delta := expires.Sub(before); delta < 23*time.Hour || delta > 25*time.Hour {
		t.Errorf("expires_at is %v away from creation, want ~24h", delta)
	}
	if fp, _ := entry["fingerprint"].(string); len(fp) != 8 {
		t.Errorf("fingerprint = %q, want 8 hex chars", fp)
	}

	// Fill the last slot, then the link is exhausted and leaves the pending list.
	carol := p.user("multi_carol", "user")
	p.status(t, p.accept(carol, "user", code), 0)
	p.status(t, p.accept(p.user("multi_dave", "user"), "user", code), http.StatusConflict)
	for _, row := range p.pending(owner, "user") {
		if id, _ := row["invite_id"].(int64); id == inviteID {
			t.Error("an exhausted link is still listed as pending")
		}
	}
	if got, _ := historyEntry(t, p.history(owner, "user"), inviteID)["status"].(string); got != "exhausted" {
		t.Errorf("status after the last seat = %q, want exhausted", got)
	}
}

func TestTeamInviteValidityAndCapacityLimits(t *testing.T) {
	p := newInviteProbe(t)
	owner := p.user("limit_owner", "user") // free plan: exactly one seat
	invitee := p.user("limit_mate", "user")

	// Free plan has one seat, so a link for two people is refused up front
	// instead of failing one acceptance at a time.
	if _, err := p.call(owner, "user", `{"max_uses":2,"expires_in_hours":24}`, p.app.createTeamInvite); err == nil {
		t.Error("a link for more people than seats was created; want a 400")
	} else if apiErr, ok := err.(*ApiError); !ok || apiErr.Status != http.StatusBadRequest {
		t.Errorf("over-capacity create = %v, want 400", err)
	}
	// A validity outside 1h..90d is a client error, not a silently clamped link.
	for _, body := range []string{`{"expires_in_hours":0.5}`, `{"expires_in_hours":2161}`} {
		if _, err := p.call(owner, "user", body, p.app.createTeamInvite); err == nil {
			t.Errorf("create with %s succeeded; want 400", body)
		}
	}

	// Default link, then revoke: the link stops working but the log stays.
	inviteID, code := p.createInvite(owner, "user", "", nil)
	if _, err := p.call(owner, "user", "", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.deleteTeamInvite(w, r, int32(inviteID))
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	p.status(t, p.accept(invitee, "user", code), http.StatusNotFound)
	for _, row := range p.pending(owner, "user") {
		if id, _ := row["invite_id"].(int64); id == inviteID {
			t.Error("a revoked link is still listed as pending")
		}
	}
	entry := historyEntry(t, p.history(owner, "user"), inviteID)
	if got, _ := entry["status"].(string); got != "revoked" {
		t.Errorf("status = %q, want revoked", got)
	}
	if _, ok := entry["revoked_at"].(*time.Time); !ok {
		t.Errorf("revoked_at = %#v, want a timestamp so the history can show when", entry["revoked_at"])
	}

	// An already-expired link is refused and reads as expired in the history.
	expiredID, expiredCode := p.createInvite(owner, "user", "", nil)
	if _, err := p.pool.Exec(p.ctx, "UPDATE team_invites SET expires_at = NOW() - interval '1 hour' WHERE invite_id = $1", expiredID); err != nil {
		t.Fatalf("age the invite: %v", err)
	}
	p.status(t, p.accept(invitee, "user", expiredCode), http.StatusNotFound)
	if got, _ := historyEntry(t, p.history(owner, "user"), expiredID)["status"].(string); got != "expired" {
		t.Errorf("status = %q, want expired", got)
	}
}

func TestTeamInviteSeatFullRollsBackTheLink(t *testing.T) {
	p := newInviteProbe(t)
	owner := p.user("seat_owner", "user") // no billing row → free plan, one seat
	invitee := p.user("seat_mate", "user")
	blocker := p.user("seat_blocker", "user")

	// Created while the seat is free; the seat is taken by someone else before
	// the invitee clicks.
	inviteID, code := p.createInvite(owner, "user", "", nil)
	if _, err := p.pool.Exec(p.ctx,
		"INSERT INTO agent_teams (owner_user_id, agent_user_id, display_name, is_active) VALUES ($1,$2,'blocker',true)",
		owner, blocker); err != nil {
		t.Fatalf("fill the seat: %v", err)
	}

	p.status(t, p.accept(invitee, "user", code), http.StatusPaymentRequired)

	// The rejected accept must not burn the link: the transaction rolled back,
	// including the use row it would have written.
	var useCount int64
	if err := p.pool.QueryRow(p.ctx,
		"SELECT count(*) FROM team_invite_uses WHERE invite_id = $1", inviteID).Scan(&useCount); err != nil {
		t.Fatalf("read uses: %v", err)
	}
	if useCount != 0 {
		t.Fatalf("seat-full accept wrote %d use row(s); it must roll back", useCount)
	}
	if got, _ := historyEntry(t, p.history(owner, "user"), inviteID)["status"].(string); got != "pending" {
		t.Fatalf("status after the rejected accept = %q, want pending", got)
	}

	// Free the seat and the same link works — this is what makes the rollback
	// user-visible rather than theoretical.
	if _, err := p.pool.Exec(p.ctx, "DELETE FROM agent_teams WHERE agent_user_id = $1", blocker); err != nil {
		t.Fatalf("free the seat: %v", err)
	}
	p.status(t, p.accept(invitee, "user", code), 0)
}

func TestTeamInviteRefusalsAndRevoke(t *testing.T) {
	p := newInviteProbe(t)
	owner := p.user("ref_owner", "user")
	invitee := p.user("ref_mate", "user")
	platformAdmin := p.user("ref_admin", "platform_admin")
	secondOwner := p.user("ref_owner2", "user")

	inviteID, code := p.createInvite(owner, "user", "", nil)

	// The owner cannot seat themselves, and the refusal must not consume the link.
	p.status(t, p.accept(owner, "user", code), http.StatusBadRequest)
	// A platform admin is staff, not a tenant's agent.
	p.status(t, p.accept(platformAdmin, "platform_admin", code), http.StatusForbidden)

	// An account with its own tenant state is an independent merchant: consent
	// does not make its own billing/storage someone else's to administer.
	if _, err := p.pool.Exec(p.ctx, "INSERT INTO tenant_billing (user_id, plan) VALUES ($1,'free')", invitee); err != nil {
		t.Fatalf("seed tenant state: %v", err)
	}
	p.status(t, p.accept(invitee, "user", code), http.StatusForbidden)
	if _, err := p.pool.Exec(p.ctx, "DELETE FROM tenant_billing WHERE user_id = $1", invitee); err != nil {
		t.Fatalf("clear tenant state: %v", err)
	}

	// Revoking is owner-scoped: another tenant's owner cannot touch the row.
	if _, err := p.call(secondOwner, "user", "", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.deleteTeamInvite(w, r, int32(inviteID))
	}); err == nil {
		t.Fatal("another tenant's owner revoked the invite; revocation must be owner-scoped")
	}

	// Once seated, a second tenant's link is refused by the one-team invariant,
	// and the rejected use does not eat that link's capacity.
	if _, err := p.pool.Exec(p.ctx, "DELETE FROM agent_teams WHERE agent_user_id = $1", invitee); err != nil {
		t.Fatalf("reset invitee: %v", err)
	}
	_, codeA := p.createInvite(owner, "user", "", nil)
	p.status(t, p.accept(invitee, "user", codeA), 0)
	secondID, codeB := p.createInvite(secondOwner, "user", "", nil)
	p.status(t, p.accept(invitee, "user", codeB), http.StatusConflict)
	if got, _ := historyEntry(t, p.history(secondOwner, "user"), secondID)["use_count"].(int64); got != 0 {
		t.Errorf("rejected accept consumed capacity: use_count = %d, want 0", got)
	}
}
