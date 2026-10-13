package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Billing statements used by consumeDocQuota. All statements are fully
// parameterized ($n placeholders) and kept as package constants so the call
// sites contain no inline statement text.

const sqlBillingInit = "INSERT INTO tenant_billing (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING"

const sqlBillingRollover = "UPDATE tenant_billing SET messages_used = 0, docs_used = 0, cycle_start = NOW(), cycle_end = NOW() + INTERVAL '30 days' WHERE user_id = $1 AND cycle_end <= NOW()"

const sqlBillingLookup = "SELECT plan, docs_used::bigint, monthly_doc_quota::bigint FROM tenant_billing WHERE user_id = $1"

const sqlBillingIncrement = "UPDATE tenant_billing SET docs_used = docs_used + 1 WHERE user_id = $1"

// sqlBillingIncrementCapped is the atomic quota gate: one conditional UPDATE
// is the whole check-and-increment. The previous SELECT-then-UPDATE pair let
// concurrent uploads all observe remaining quota and over-run the cap.
const sqlBillingIncrementCapped = "UPDATE tenant_billing SET docs_used = docs_used + 1 WHERE user_id = $1 AND monthly_doc_quota > docs_used"

// sqlBillingRefund returns one document slot. GREATEST(..., 0) because a plan
// downgrade can leave docs_used at the floor already, and a refund must never
// push the counter negative (which would hand out a free extra document).
const sqlBillingRefund = "UPDATE tenant_billing SET docs_used = GREATEST(docs_used - 1, 0) WHERE user_id = $1"

// refundDocQuota gives back the slot consumeDocQuota took when the ingest it paid
// for did not happen. Without it, a failed upload consumed a document from the
// tenant's monthly allowance and the retry consumed another: the user paid for
// documents they never received.
//
// Best effort by design — the upload error is what the caller must report, so a
// failed refund is logged rather than replacing it. logger may be nil.
func refundDocQuota(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, userID int32) {
	if _, err := pool.Exec(ctx, sqlBillingRefund, userID); err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("document quota refund failed", "user_id", userID, "error", err.Error())
	}
}

// consumeDocQuota consumes one document from the tenant's monthly quota.
// The gate is atomic for metered plans (RowsAffected of zero on the capped
// increment means the quota was exhausted). Enterprise documents are
// unmetered; the accounting increment stays unconditional there.
// (Moved out of knowledge_handlers.go during the security-audit remediation.)
func consumeDocQuota(ctx context.Context, pool *pgxpool.Pool, userID int32) error {
	if _, err := pool.Exec(ctx, sqlBillingInit, userID); err != nil {
		return ErrInternal("billing init")
	}
	if _, err := pool.Exec(ctx, sqlBillingRollover, userID); err != nil {
		return ErrInternal("billing rollover")
	}
	var plan string
	var used, quota int64
	err := pool.QueryRow(ctx, sqlBillingLookup, userID).Scan(&plan, &used, &quota)
	if err != nil {
		// Match the sentinel, not the message: pgx returns pgx.ErrNoRows, and a
		// string test silently changes branch the day that text is reworded or
		// the error gets wrapped — a tenant without a billing row would then get
		// a 500 instead of the intended "no quota configured, allow".
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return ErrInternal("billing lookup")
	}
	if plan == planEnterprise {
		if _, err := pool.Exec(ctx, sqlBillingIncrement, userID); err != nil {
			return ErrInternal("billing increment")
		}
		return nil
	}
	tag, err := pool.Exec(ctx, sqlBillingIncrementCapped, userID)
	if err != nil {
		return ErrInternal("billing increment")
	}
	if tag.RowsAffected() == 0 {
		return &ApiError{http.StatusPaymentRequired, "月度文档配额已用尽，请升级套餐"}
	}
	return nil
}
