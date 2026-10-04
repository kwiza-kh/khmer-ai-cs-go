// Command billingreconcile — recount each tenant's monthly usage from the rows
// that actually exist, and (with -apply) write the counters back.
//
// Why this exists: messages_used is advanced by the message quota gate and
// docs_used by the document upload gate, but rows can also arrive by other
// means — a script that seeds a knowledge base, a restore, an import — and every
// one of those leaves the counter behind reality. A counter that disagrees with
// the rows is a plan limit that silently does not apply. Production, 2026-10-04:
// plan=free, monthly_doc_quota=20, 42 knowledge documents, docs_used=0.
//
// Usage:
//
//	go run ./cmd/billingreconcile                  # report only (default)
//	go run ./cmd/billingreconcile -apply           # write the corrected counters
//	go run ./cmd/billingreconcile -dsn "$DATABASE_URL"
//
// The window is the tenant's own billing cycle. An expired cycle is reported as
// due for rollover and reconciled to zero, because that is what the next gate
// call will do anyway (the rollover statement resets both counters).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sqlBillingRows = "SELECT user_id, plan, messages_used, docs_used, monthly_message_quota, monthly_doc_quota, cycle_start, cycle_end FROM tenant_billing ORDER BY user_id"

	sqlCountDocuments = "SELECT count(*) FROM knowledge_documents WHERE uploaded_by = $1 AND created_at >= $2 AND created_at < $3"

	sqlCountMessages = "SELECT count(*) FROM chat_messages m JOIN sessions s ON s.session_id = m.session_id WHERE s.user_id = $1 AND m.role = 'user' AND m.created_at >= $2 AND m.created_at < $3"

	sqlTenantsWithoutBilling = "SELECT u.user_id, u.username FROM users u LEFT JOIN tenant_billing b ON b.user_id = u.user_id WHERE b.user_id IS NULL ORDER BY u.user_id"

	sqlUpdateCounters = "UPDATE tenant_billing SET docs_used = $2, messages_used = $3, updated_at = NOW() WHERE user_id = $1"
)

type tenantRow struct {
	userID     int32
	plan       string
	msgUsed    int64
	docUsed    int64
	msgQuota   int64
	docQuota   int64
	cycleStart time.Time
	cycleEnd   time.Time
}

func main() {
	dsn := flag.String("dsn", "", "PostgreSQL DSN (defaults to $DATABASE_URL)")
	apply := flag.Bool("apply", false, "write the reconciled counters (default: report only)")
	all := flag.Bool("all", false, "show tenants whose counters already agree")
	flag.Parse()

	if err := run(*dsn, *apply, *all); err != nil {
		fmt.Fprintln(os.Stderr, "billingreconcile:", err)
		os.Exit(1)
	}
}

func run(dsn string, apply, all bool) error {
	if strings.TrimSpace(dsn) == "" {
		dsn = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if dsn == "" {
		return errors.New("no DSN: pass -dsn or set DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, sqlBillingRows)
	if err != nil {
		return fmt.Errorf("load billing rows: %w", err)
	}
	defer rows.Close()

	var tenants []tenantRow
	for rows.Next() {
		var t tenantRow
		if err := rows.Scan(&t.userID, &t.plan, &t.msgUsed, &t.docUsed, &t.msgQuota, &t.docQuota, &t.cycleStart, &t.cycleEnd); err != nil {
			return fmt.Errorf("scan billing row: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate billing rows: %w", err)
	}

	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "tenant\tplan\tdocs (used→real / quota)\tmsgs (used→real / quota)\tcycle\tverdict")
	mismatches := 0
	for _, t := range tenants {
		docsReal, msgsReal, expired, err := recount(ctx, pool, t, now)
		if err != nil {
			return err
		}
		verdict := "ok"
		switch {
		case expired:
			verdict = "cycle expired → 0 (rollover pending)"
		case docsReal != t.docUsed || msgsReal != t.msgUsed:
			verdict = "MISMATCH"
		}
		if verdict != "ok" {
			mismatches++
		}
		if verdict == "ok" && !all {
			continue
		}
		cycle := t.cycleStart.Format("01-02") + "→" + t.cycleEnd.Format("01-02")
		fmt.Fprintf(w, "%d\t%s\t%d→%d / %d\t%d→%d / %d\t%s\t%s\n",
			t.userID, t.plan, t.docUsed, docsReal, t.docQuota, t.msgUsed, msgsReal, t.msgQuota, cycle, verdict)
		if apply && verdict != "ok" {
			if _, err := pool.Exec(ctx, sqlUpdateCounters, t.userID, docsReal, msgsReal); err != nil {
				return fmt.Errorf("update tenant %d: %w", t.userID, err)
			}
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// A tenant with no billing row is invisible to every query above, and the
	// gate provisions one lazily with the column defaults — worth naming.
	rowsMissing, err := pool.Query(ctx, sqlTenantsWithoutBilling)
	if err != nil {
		return fmt.Errorf("load tenants without billing: %w", err)
	}
	defer rowsMissing.Close()
	var missing []string
	for rowsMissing.Next() {
		var id int32
		var name string
		if err := rowsMissing.Scan(&id, &name); err != nil {
			return fmt.Errorf("scan tenant without billing: %w", err)
		}
		missing = append(missing, fmt.Sprintf("%d(%s)", id, name))
	}
	if err := rowsMissing.Err(); err != nil {
		return fmt.Errorf("iterate tenants without billing: %w", err)
	}

	fmt.Printf("\ntenants=%d mismatched=%d\n", len(tenants), mismatches)
	if len(missing) > 0 {
		fmt.Printf("no billing row yet (gate provisions defaults on first message): %s\n", strings.Join(missing, ", "))
	}
	if apply && mismatches > 0 {
		fmt.Println("applied: counters rewritten")
	} else if mismatches > 0 {
		fmt.Println("dry run: re-run with -apply to write these counters")
	}
	return nil
}

// recount counts the rows inside the tenant's current cycle. An expired cycle is
// reported as 0/0 with expired=true: the next gate call resets both counters, so
// restoring them to the old cycle's counts would be wrong twice over.
func recount(ctx context.Context, pool *pgxpool.Pool, t tenantRow, now time.Time) (docs, msgs int64, expired bool, err error) {
	if !t.cycleEnd.After(now) {
		return 0, 0, true, nil
	}
	// created_at < cycle_end keeps a row that lands exactly on the boundary in one
	// cycle only; the column defaults make cycle_end start+NOW(), never the row's
	// own insert time, so the boundary is stable.
	if err = pool.QueryRow(ctx, sqlCountDocuments, t.userID, t.cycleStart, t.cycleEnd).Scan(&docs); err != nil {
		return 0, 0, false, fmt.Errorf("count documents for tenant %d: %w", t.userID, err)
	}
	if err = pool.QueryRow(ctx, sqlCountMessages, t.userID, t.cycleStart, t.cycleEnd).Scan(&msgs); err != nil {
		return 0, 0, false, fmt.Errorf("count messages for tenant %d: %w", t.userID, err)
	}
	return docs, msgs, false, nil
}
