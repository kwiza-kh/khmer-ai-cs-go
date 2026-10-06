package api

import (
	"context"
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
//   - a link is single-use;
//   - a rejected accept (seat full) rolls the invite back, so the link survives
//     a transient condition instead of being burned;
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

func (p *inviteProbe) cleanup() {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = p.pool.Exec(c, "DELETE FROM agent_teams WHERE owner_user_id = ANY($1::int[]) OR agent_user_id = ANY($1::int[])", p.ids)
	_, _ = p.pool.Exec(c, "DELETE FROM team_invites WHERE owner_user_id = ANY($1::int[]) OR used_by_user_id = ANY($1::int[])", p.ids)
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

// createInvite mints a link as the owner and returns its id plus the code the
// invitee would receive (parsed out of the URL the handler returns, so the URL
// shape itself is part of what is tested).
func (p *inviteProbe) createInvite(ownerID int32, role, displayName string, skills []string) (int64, string) {
	p.t.Helper()
	body := `{"display_name":"` + displayName + `","skills":["` + strings.Join(skills, `","`) + `"]}`
	res, err := p.call(ownerID, role, body, p.app.createTeamInvite)
	if err != nil {
		p.t.Fatalf("createInvite: %v", err)
	}
	entry, ok := res.(map[string]any)
	if !ok {
		p.t.Fatalf("createInvite returned %T, want a map", res)
	}
	raw, _ := entry["url"].(string)
	parsed, err := url.Parse(raw)
	if err != nil {
		p.t.Fatalf("invite url %q does not parse: %v", raw, err)
	}
	code := parsed.Query().Get("code")
	if !strings.HasPrefix(code, "ti_") {
		p.t.Fatalf("invite code = %q, want a ti_ token", code)
	}
	id, _ := entry["invite_id"].(int64)
	return id, code
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

	// Single use: the same link cannot seat a second account, and the row keeps
	// the record of who used it.
	p.status(t, p.accept(p.user("inv_other", "user"), "user", code), http.StatusNotFound)
	var usedBy *int32
	if err := p.pool.QueryRow(p.ctx,
		"SELECT used_by_user_id FROM team_invites WHERE invite_id = $1", inviteID).Scan(&usedBy); err != nil {
		t.Fatalf("read invite: %v", err)
	}
	if usedBy == nil || *usedBy != invitee {
		t.Errorf("used_by_user_id = %v, want the accepting invitee %d", usedBy, invitee)
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

	// The rejected accept must not burn the link: the transaction rolled back.
	var usedBy *int32
	if err := p.pool.QueryRow(p.ctx,
		"SELECT used_by_user_id FROM team_invites WHERE invite_id = $1", inviteID).Scan(&usedBy); err != nil {
		t.Fatalf("read invite: %v", err)
	}
	if usedBy != nil {
		t.Fatalf("seat-full accept consumed the invite (used_by=%d); it must roll back", *usedBy)
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

	// Revoked links are dead, and revocation is owner-scoped.
	if _, err := p.call(owner, "user", "", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.deleteTeamInvite(w, r, int32(inviteID))
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	p.status(t, p.accept(invitee, "user", code), http.StatusNotFound)
	if _, err := p.call(secondOwner, "user", "", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return p.app.deleteTeamInvite(w, r, int32(inviteID))
	}); err == nil {
		t.Fatal("another tenant's owner revoked the invite; deletion must be owner-scoped")
	}

	// Once seated, a second tenant's link is refused by the one-team invariant.
	if _, err := p.pool.Exec(p.ctx, "DELETE FROM agent_teams WHERE agent_user_id = $1", invitee); err != nil {
		t.Fatalf("reset invitee: %v", err)
	}
	_, codeA := p.createInvite(owner, "user", "", nil)
	p.status(t, p.accept(invitee, "user", codeA), 0)
	_, codeB := p.createInvite(secondOwner, "user", "", nil)
	p.status(t, p.accept(invitee, "user", codeB), http.StatusConflict)
}
