package api

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/usage"
)

// The billing card is where a merchant sees the plan they bought, so its seat
// numbers have to agree with the gate that refuses the next agent
// (checkSeatLimit): same plan, same count. This pins the ways they could
// disagree — a plan read from the wrong row, an inactive agent counted (the gate
// only counts is_active), and enterprise's unlimited sentinel leaking into the
// UI — plus the free default for a tenant that never touched billing.
//
// Gated on DATABASE_URL like the other tests that need real tables.
func TestPaidStateReportsSeatUsageAndQuota(t *testing.T) {
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
	seed := func(name string) int32 {
		var id int32
		if err := pool.QueryRow(ctx,
			"INSERT INTO users (username, email, password_hash) VALUES ($1,$2,'probe') RETURNING user_id",
			name, name+"@seat-probe.invalid").Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return id
	}
	ownerID := seed("__seat_owner_" + suffix)
	agentID := seed("__seat_agent_" + suffix)
	inactiveID := seed("__seat_inactive_" + suffix)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, "DELETE FROM agent_teams WHERE owner_user_id = $1", ownerID)
		_, _ = pool.Exec(c, "DELETE FROM tenant_billing WHERE user_id = $1", ownerID)
		_, _ = pool.Exec(c, "DELETE FROM users WHERE user_id = ANY($1::int[])", []int32{ownerID, agentID, inactiveID})
	})

	app := &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// No billing row yet: free, nothing in use, and a finite quota — not 0/0.
	state, err := app.paidState(ctx, ownerID)
	if err != nil {
		t.Fatalf("paidState (no billing row): %v", err)
	}
	free, _ := usage.PlanByName(usage.PlanFree)
	if got := state["seats_used"]; got != int64(0) {
		t.Errorf("seats_used on a fresh tenant = %#v, want 0", got)
	}
	if quota, ok := state["seats_quota"].(*int64); !ok || quota == nil || *quota != free.Seats {
		t.Errorf("seats_quota on a fresh tenant = %#v, want %d", state["seats_quota"], free.Seats)
	}

	// One active agent plus one inactive row on a pro tenant: the count matches
	// checkSeatLimit's SQL, and the quota follows the plan actually on the row.
	if _, err := pool.Exec(ctx,
		"INSERT INTO agent_teams (owner_user_id, agent_user_id, display_name, is_active) VALUES ($1,$2,'active',true), ($1,$3,'inactive',false)",
		ownerID, agentID, inactiveID); err != nil {
		t.Fatalf("seed agent_teams: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO tenant_billing (user_id, plan) VALUES ($1,'pro')", ownerID); err != nil {
		t.Fatalf("seed tenant_billing: %v", err)
	}
	state, err = app.paidState(ctx, ownerID)
	if err != nil {
		t.Fatalf("paidState (pro): %v", err)
	}
	pro, _ := usage.PlanByName(usage.PlanPro)
	if got := state["seats_used"]; got != int64(1) {
		t.Errorf("seats_used = %#v, want 1 (an inactive row does not hold a seat)", got)
	}
	if quota, ok := state["seats_quota"].(*int64); !ok || quota == nil || *quota != pro.Seats {
		t.Errorf("seats_quota = %#v, want %d", state["seats_quota"], pro.Seats)
	}

	// Enterprise is unlimited: the console renders null as "不限"; the sentinel
	// number must not reach the browser.
	if _, err := pool.Exec(ctx, "UPDATE tenant_billing SET plan = 'enterprise' WHERE user_id = $1", ownerID); err != nil {
		t.Fatalf("upgrade to enterprise: %v", err)
	}
	state, err = app.paidState(ctx, ownerID)
	if err != nil {
		t.Fatalf("paidState (enterprise): %v", err)
	}
	if v, ok := state["seats_quota"].(*int64); !ok || v != nil {
		t.Errorf("seats_quota on enterprise = %#v, want null", state["seats_quota"])
	}
}
