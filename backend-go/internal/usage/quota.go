package usage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Plan names live here because two layers have to agree on them: the api layer
// enforces the document quota, the pipeline enforces the message quota.
const (
	PlanFree       = "free"
	PlanPro        = "pro"
	PlanEnterprise = "enterprise"
)

// ErrMessageQuotaExhausted is returned by ConsumeMessageQuota when the tenant's
// monthly message quota is used up. Each caller decides what its surface shows:
// the pipeline answers the customer with the handoff acknowledgement, the
// console returns 402, the widget escalates to a human.
var ErrMessageQuotaExhausted = errors.New("usage: monthly message quota exhausted")

// Message-quota statements. All fully parameterized and kept as package
// constants so call sites contain no inline statement text (and so
// internal/sqlcheck finds them).
const (
	sqlMessageQuotaInit = "INSERT INTO tenant_billing (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING"

	// Rollover is a no-op on an unexpired cycle. Both counters move together —
	// whichever surface is billed first in a new cycle resets the pair.
	sqlMessageQuotaRollover = "UPDATE tenant_billing SET messages_used = 0, docs_used = 0, cycle_start = NOW(), cycle_end = NOW() + INTERVAL '30 days' WHERE user_id = $1 AND cycle_end <= NOW()"

	sqlMessageQuotaLookup = "SELECT plan, messages_used::bigint, monthly_message_quota::bigint FROM tenant_billing WHERE user_id = $1"

	sqlMessageQuotaIncrement = "UPDATE tenant_billing SET messages_used = messages_used + 1, updated_at = NOW() WHERE user_id = $1"

	// sqlMessageQuotaIncrementCapped is the atomic gate: one conditional UPDATE
	// is the whole check-and-increment. A SELECT-then-UPDATE pair lets concurrent
	// turns all observe remaining quota and over-run the cap.
	sqlMessageQuotaIncrementCapped = "UPDATE tenant_billing SET messages_used = messages_used + 1, updated_at = NOW() WHERE user_id = $1 AND monthly_message_quota > messages_used"
)

// messageQuotaIsMetered reports whether a plan's messages are capped. Enterprise
// is unmetered: its counter still advances so the dashboards stay truthful, but
// nothing is ever refused.
func messageQuotaIsMetered(plan string) bool { return plan != PlanEnterprise }

// ConsumeMessageQuota consumes one received customer message from the tenant's
// monthly quota, returning ErrMessageQuotaExhausted when there is none left.
//
// The counter has existed from the start, but was deliberately not enforced —
// the note on the old bumpMessagesUsed read "enforcement is a deliberate product
// decision, kept off", and this is that decision turned on. Enforcing it needed
// exactly the atomic check-and-increment the document quota already had.
//
// A tenant with no billing row is unlimited rather than blocked (fail open, the
// same rule consumeDocQuota uses): the row is provisioned lazily, so a missing
// one means nobody ever configured this tenant, and locking them out of their own
// bot would be the wrong failure. A row whose quota is zero is a deliberate
// setting ("no messages"), so it blocks.
func ConsumeMessageQuota(ctx context.Context, db *pgxpool.Pool, userID int32) error {
	if _, err := db.Exec(ctx, sqlMessageQuotaInit, userID); err != nil {
		return fmt.Errorf("message quota init: %w", err)
	}
	if _, err := db.Exec(ctx, sqlMessageQuotaRollover, userID); err != nil {
		return fmt.Errorf("message quota rollover: %w", err)
	}
	var plan string
	var used, quota int64
	if err := db.QueryRow(ctx, sqlMessageQuotaLookup, userID).Scan(&plan, &used, &quota); err != nil {
		// Match the sentinel, not the message: pgx returns pgx.ErrNoRows, and a
		// string test silently changes branch the day that text is reworded.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("message quota lookup: %w", err)
	}
	if !messageQuotaIsMetered(plan) {
		if _, err := db.Exec(ctx, sqlMessageQuotaIncrement, userID); err != nil {
			return fmt.Errorf("message quota increment: %w", err)
		}
		return nil
	}
	tag, err := db.Exec(ctx, sqlMessageQuotaIncrementCapped, userID)
	if err != nil {
		return fmt.Errorf("message quota increment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMessageQuotaExhausted
	}
	return nil
}
