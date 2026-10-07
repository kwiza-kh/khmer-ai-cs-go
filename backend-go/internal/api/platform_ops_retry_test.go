package api

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// retryFailedDeliveries is the operator's only way to un-stick a send on a tenant
// they do not own (the tenant-scoped retry endpoint answers 404 for a channel
// that is not theirs). It has to flip exactly the failed rows of that one
// channel, and hand them a clean attempt budget — a row that keeps attempts=5
// goes straight back to 'failed' on the next tick.
//
// Drives the real statement inside a transaction that is rolled back, so this
// exercises the production schema without leaving a row behind.
func TestRetryFailedDeliveriesRequeuesFailedRowsOnly(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	defer pool.Close()

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Two active channels when the deployment has them, so one can act as the
	// control: another tenant's failure must not be requeued by this call.
	var ids []int32
	rows, err := tx.Query(ctx, "SELECT config_id FROM platform_configs WHERE is_active ORDER BY config_id LIMIT 2")
	if err != nil {
		t.Fatalf("select configs: %v", err)
	}
	for rows.Next() {
		var id int32
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("select configs: %v", err)
	}
	if len(ids) == 0 {
		t.Skip("no active channel in this database; nothing to exercise")
	}

	// Probe rows must satisfy three foreign keys (config, session, message) and
	// chat_message_id is UNIQUE per outbox row, so each probe borrows its own real
	// session+message pair. Nothing is created; the transaction rolls back.
	type probe struct {
		sessionID string
		messageID int64
		platform  string
	}
	var probes []probe
	prows, err := tx.Query(ctx, `SELECT s.session_id::text, cm.message_id, c.platform::text
		FROM platform_configs c
		JOIN sessions s ON s.user_id = c.user_id
		JOIN chat_messages cm ON cm.session_id = s.session_id
		WHERE c.config_id = $1
		  AND NOT EXISTS (SELECT 1 FROM platform_outbox o WHERE o.chat_message_id = cm.message_id)
		ORDER BY cm.message_id DESC LIMIT 4`, ids[0])
	if err != nil {
		t.Fatalf("select probe anchors: %v", err)
	}
	for prows.Next() {
		var p probe
		if prows.Scan(&p.sessionID, &p.messageID, &p.platform) == nil {
			probes = append(probes, p)
		}
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		t.Fatalf("select probe anchors: %v", err)
	}
	if len(probes) < 3 {
		t.Skipf("need 3 messages to attach probe rows to channel %d, found %d", ids[0], len(probes))
	}

	insert := func(configID int32, p probe, status string, attempts int, lastErr string) int64 {
		t.Helper()
		var id int64
		if err := tx.QueryRow(ctx,
			"INSERT INTO platform_outbox (config_id, session_id, chat_message_id, platform, recipient_id, content, status, attempts, last_error) "+
				"VALUES ($1,$2::uuid,$3,$4::platform_type,'1','retry-probe',$5,$6,$7) RETURNING delivery_id",
			configID, p.sessionID, p.messageID, p.platform, status, attempts, lastErr).Scan(&id); err != nil {
			t.Fatalf("insert %s row: %v", status, err)
		}
		return id
	}
	read := func(id int64) (status string, attempts int, lastErr string) {
		t.Helper()
		if err := tx.QueryRow(ctx,
			"SELECT status, attempts, COALESCE(last_error,'') FROM platform_outbox WHERE delivery_id = $1", id).
			Scan(&status, &attempts, &lastErr); err != nil {
			t.Fatalf("read row %d: %v", id, err)
		}
		return status, attempts, lastErr
	}

	// Production may already have failed rows on this channel (it does: two Meta
	// sends stuck on the HUMAN_AGENT tag) — the function requeues all of them, so
	// the expectation is "whatever was there before, plus my probe". Count first.
	var preExisting int64
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM platform_outbox WHERE config_id = $1 AND status = 'failed'", ids[0]).
		Scan(&preExisting); err != nil {
		t.Fatalf("count failed rows: %v", err)
	}

	failedID := insert(ids[0], probes[0], "failed", 5, "boom")
	sentID := insert(ids[0], probes[1], "sent", 1, "")
	var otherFailedID int64
	if len(ids) > 1 {
		otherFailedID = insert(ids[1], probes[2], "failed", 5, "other-tenant")
	}

	n, err := retryFailedDeliveries(ctx, tx, ids[0])
	if err != nil {
		t.Fatalf("retryFailedDeliveries: %v", err)
	}
	if want := preExisting + 1; n != want {
		t.Errorf("requeued %d rows, want %d (probe + %d pre-existing)", n, want, preExisting)
	}
	if status, attempts, lastErr := read(failedID); status != "pending" || attempts != 0 || lastErr != "" {
		t.Errorf("requeued row = %s/attempts=%d/last_error=%q, want pending/0/\"\"", status, attempts, lastErr)
	}
	if status, attempts, _ := read(sentID); status != "sent" || attempts != 1 {
		t.Errorf("a sent row changed: %s/attempts=%d", status, attempts)
	}
	if otherFailedID != 0 {
		if status, attempts, _ := read(otherFailedID); status != "failed" || attempts != 5 {
			t.Errorf("another channel's failed row was touched: %s/attempts=%d", status, attempts)
		}
	}
}
