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
)

// TestScanSLABreachesEndToEnd is the test whose absence let the SLA engine sit
// broken in production: it EXECUTES the scanner against a real database, so a
// parameter-encoding failure surfaces. The old query used
// `($2 || ' seconds')::interval`, which Postgres resolves as a text parameter
// — pgx cannot encode a Go int into text, so every scan failed before sending
// and the `if err == nil` guard swallowed it. psql PREPARE and internal/sqlcheck
// both passed, because neither binds parameters the way the server does.
//
// Gated on DATABASE_URL like internal/sqlcheck: skipped without a database.
func TestScanSLABreachesEndToEnd(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — SLA scan test needs a real migrated database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var userID, slaID int32
	var sessionID string
	if err := pool.QueryRow(ctx,
		"INSERT INTO users (username, email, password_hash) VALUES ($1, $2, 'sla-probe') RETURNING user_id",
		"__sla_"+suffix, "__sla_"+suffix+"@sla-probe.invalid").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, "DELETE FROM notifications WHERE user_id = $1", userID)
		_, _ = pool.Exec(c, "DELETE FROM users WHERE user_id = $1", userID) // cascades policy/session/breaches
	})

	if err := pool.QueryRow(ctx,
		"INSERT INTO sla_policies (user_id, name, first_response_secs, resolution_secs) VALUES ($1, 'probe SLA', 60, NULL) RETURNING sla_id",
		userID).Scan(&slaID); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	// Two hours old, still unanswered → past the 60s first-response target.
	if err := pool.QueryRow(ctx,
		"INSERT INTO sessions (user_id, platform, platform_user_id, status, language, created_at) "+
			"VALUES ($1, 'telegram', $2, 'active', 'km', NOW() - INTERVAL '2 hours') RETURNING session_id::text",
		userID, "SLA_PROBE_"+suffix).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	app := &App{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	app.scanSLABreaches(ctx)

	var breachType string
	err = pool.QueryRow(ctx,
		"SELECT breach_type FROM sla_breaches WHERE session_id = $1 AND breach_type = 'first_response'",
		sessionID).Scan(&breachType)
	if err != nil {
		t.Fatalf("no first_response breach recorded for an overdue session: %v "+
			"(the scanner is not executing — check for a pgx parameter-encoding failure)", err)
	}

	var notifications int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sla'", userID).Scan(&notifications); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if notifications == 0 {
		t.Error("breach recorded but the tenant was never notified")
	}

	// Re-running must not duplicate (UNIQUE (session_id, breach_type)).
	app.scanSLABreaches(ctx)
	var breaches int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM sla_breaches WHERE session_id = $1", sessionID).Scan(&breaches); err != nil {
		t.Fatalf("recount breaches: %v", err)
	}
	if breaches != 1 {
		t.Errorf("breach rows = %d after two scans, want 1 (idempotency lost)", breaches)
	}
}
