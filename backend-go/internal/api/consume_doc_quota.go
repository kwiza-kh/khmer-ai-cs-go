package api

import (
	"context"
	"net/http"
	"strings"

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
		if strings.Contains(err.Error(), "no rows") {
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
