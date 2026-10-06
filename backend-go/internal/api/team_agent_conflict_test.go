package api

import (
	"context"
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
)

// TestAddTeamAgentDoubleAddIsConflict pins the status code for the
// "unique key already satisfied" path of addTeamAgent.
//
// Adding the same agent twice is the deterministic form of the concurrent
// claim the pre-check cannot catch: the second call's SELECT finds no OTHER
// owner (claimedElsewhere is false, so it passes) and only the INSERT hits
// 024's UNIQUE (owner_user_id, agent_user_id) or 059's
// uq_agent_teams_agent_user_id. That used to be reported as 500 "添加失败",
// which told the client to retry a request that can never succeed.
//
// Gated on DATABASE_URL like the SLA scan test: it needs real unique indexes,
// which only exist in a migrated database.
func TestAddTeamAgentDoubleAddIsConflict(t *testing.T) {
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var ownerID, agentID, tenantOwnerID int32
	if err := pool.QueryRow(ctx,
		"INSERT INTO users (username, email, password_hash, role) VALUES ($1,$2,'probe','platform_admin') RETURNING user_id",
		"__owner_"+suffix, "__owner_"+suffix+"@agent-probe.invalid").Scan(&ownerID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"INSERT INTO users (username, email, password_hash) VALUES ($1,$2,'probe') RETURNING user_id",
		"__agent_"+suffix, "__agent_"+suffix+"@agent-probe.invalid").Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"INSERT INTO users (username, email, password_hash) VALUES ($1,$2,'probe') RETURNING user_id",
		"__tenant_"+suffix, "__tenant_"+suffix+"@agent-probe.invalid").Scan(&tenantOwnerID); err != nil {
		t.Fatalf("seed tenant owner: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, "DELETE FROM agent_teams WHERE owner_user_id = $1", ownerID)
		_, _ = pool.Exec(c, "DELETE FROM users WHERE user_id = ANY($1::int[])", []int32{ownerID, agentID, tenantOwnerID})
	})

	app := &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	addAs := func(caller *CurrentUser) (any, error) {
		body := `{"agent_user_id":` + strconv.Itoa(int(agentID)) + `,"display_name":"probe"}`
		r := httptest.NewRequest(http.MethodPost, "/api/v1/team/agents", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), userKey, caller))
		return app.addTeamAgent(httptest.NewRecorder(), r)
	}
	// The break-glass path is platform-only now: a tenant owner reaching for a
	// raw user_id is told to invite instead (the invitee binds their own id).
	_, err = addAs(&CurrentUser{UserID: tenantOwnerID, Username: "probe-tenant", Role: "user"})
	apiErr, ok := err.(*ApiError)
	if !ok || apiErr.Status != http.StatusForbidden {
		t.Fatalf("tenant owner add = %v, want 403 forbidden", err)
	}

	if _, err := addAs(&CurrentUser{UserID: ownerID, Username: "probe-owner", Role: "platform_admin"}); err != nil {
		t.Fatalf("first add must succeed: %v", err)
	}
	_, err = addAs(&CurrentUser{UserID: ownerID, Username: "probe-owner", Role: "platform_admin"})
	if err == nil {
		t.Fatal("adding the same agent twice must fail")
	}
	apiErr, ok = err.(*ApiError)
	if !ok {
		t.Fatalf("want *ApiError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (a unique-key conflict is not a server fault): %s",
			apiErr.Status, apiErr.Message)
	}
}
