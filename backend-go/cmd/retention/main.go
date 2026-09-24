// Command retention purges data that has outlived its purpose.
//
// The database has never had a retention mechanism: every table only ever
// grows. Two of the tables below hold content the privacy policy does not
// mention holding indefinitely, and one holds OAuth flow state that includes a
// plaintext page access token.
//
// DRY RUN IS THE DEFAULT. Nothing is deleted unless --apply is passed, and the
// dry run prints exactly what would go. Run it that way first.
//
//	retention                  show what each rule would delete (deletes nothing)
//	retention --apply          actually delete
//	retention --dsn URL        override $DATABASE_URL
//
// Each rule runs in its own transaction, so one failing table does not block
// the rest and a re-run picks up where it stopped. Rows are capped by --limit
// per rule per run so a first run on a long-neglected database cannot hold a
// long lock; run it again to continue.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"khmer-ai-cs-go/internal/db"
)

// rule is one purge. where must be a self-contained predicate on table.
type rule struct {
	table string
	why   string
	where string
}

func main() {
	apply := flag.Bool("apply", false, "actually delete (default: dry run, deletes nothing)")
	dsn := flag.String("dsn", "", "override $DATABASE_URL")
	limit := flag.Int("limit", 5000, "max rows to delete per rule per run")
	ragDays := flag.Int("rag-logs-days", 90, "keep rag_query_logs newer than this")
	notifyDays := flag.Int("notifications-days", 90, "keep notifications newer than this")
	inboundDays := flag.Int("inbound-events-days", 30, "keep completed platform_inbound_events newer than this")
	auditDays := flag.Int("audit-days", 0, "purge audit_logs older than this; 0 = never (opt-in)")
	outboxDays := flag.Int("outbox-days", 0, "purge sent platform_outbox older than this; 0 = never (opt-in)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_ = godotenv.Load()
	databaseURL := *dsn
	if databaseURL == "" {
		databaseURL = os.Getenv("DATABASE_URL")
	}
	if databaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		logger.Error("postgres unavailable", "error", err.Error())
		os.Exit(1)
	}
	defer pool.Close()

	rules := []rule{
		{
			// Transient by construction: the landing spot for a browser OAuth
			// round-trip, useless once expired. It also stores the page access
			// token in payload_json WITHOUT the encryption the
			// platform_configs copy gets, so leaving expired rows behind
			// leaves plaintext credentials behind.
			table: "platform_oauth_sessions",
			why:   "expired OAuth flow state (payload_json holds a plaintext page token)",
			where: "expires_at < now()",
		},
		{
			table: "rag_query_logs",
			why:   "retrieval tuning logs — a second copy of customer question text",
			where: fmt.Sprintf("created_at < now() - interval '%d days'", *ragDays),
		},
		{
			table: "notifications",
			why:   "delivered in-app notifications",
			where: fmt.Sprintf("created_at < now() - interval '%d days'", *notifyDays),
		},
		{
			table: "platform_inbound_events",
			why:   "processed inbound events — content holds the raw customer message",
			where: fmt.Sprintf("status = 'completed' AND created_at < now() - interval '%d days'", *inboundDays),
		},
	}

	// Opt-in rules. Audit trails and delivery history have legitimate long-term
	// uses, so they are never purged unless an operator asks by name.
	if *auditDays > 0 {
		rules = append(rules, rule{
			table: "audit_logs",
			why:   "admin action audit trail (includes ip_address)",
			where: fmt.Sprintf("created_at < now() - interval '%d days'", *auditDays),
		})
	}
	if *outboxDays > 0 {
		rules = append(rules, rule{
			table: "platform_outbox",
			why:   "delivered outbound messages",
			where: fmt.Sprintf("sent_at IS NOT NULL AND sent_at < now() - interval '%d days'", *outboxDays),
		})
	}

	mode := "DRY RUN — nothing will be deleted"
	if *apply {
		mode = "APPLY — rows will be deleted"
	}
	fmt.Printf("retention: %s (limit %d rows/rule/run)\n\n", mode, *limit)

	var total int64
	for _, r := range rules {
		n, err := purge(ctx, pool, r, *limit, *apply)
		if err != nil {
			// One bad rule must not stop the others; a re-run retries it.
			fmt.Printf("  %-28s ERROR: %v\n", r.table, err)
			logger.Error("retention rule failed", "table", r.table, "error", err.Error())
			continue
		}
		total += n
		verb := "would delete"
		if *apply {
			verb = "deleted"
		}
		fmt.Printf("  %-28s %s %6d  (%s)\n", r.table, verb, n, r.why)
	}

	fmt.Println()
	if *apply {
		fmt.Printf("total deleted: %d\n", total)
	} else {
		fmt.Printf("total that WOULD be deleted: %d — re-run with --apply to delete\n", total)
	}
}

// purge counts (dry run) or deletes up to limit matching rows, in one
// transaction. The dry run applies the same LIMIT through the same predicate,
// so its number is exactly what --apply would remove in that run.
func purge(ctx context.Context, pool *pgxpool.Pool, r rule, limit int, apply bool) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("limit must be positive")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if !apply {
		var n int64
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM (SELECT 1 FROM "+r.table+" WHERE "+r.where+" LIMIT $1) s",
			limit).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}

	// ctid rather than a key column: the rules span tables with different
	// primary keys, and ctid is the one identifier they all share.
	tag, err := tx.Exec(ctx,
		"DELETE FROM "+r.table+" WHERE ctid IN (SELECT ctid FROM "+r.table+" WHERE "+r.where+" LIMIT $1)",
		limit)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
