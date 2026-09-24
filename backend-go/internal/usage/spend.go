package usage

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/redisstore"
)

// Rolling spend accounting for the Gemini spend-based rate limit.
//
// Google caps what a billing account may spend per rolling 10-minute window
// ($10 on Tier 1, $50 on Tier 2, $200 on Tier 3) and answers 429 once the
// window is full. token_usage already records every turn's cost_estimate, so
// the same figure Google enforces against is computable here — that is what
// Budget exposes: the alert threshold and the local gate that sheds a turn
// before the wall is hit, instead of after it.

const spendWindow = 10 * time.Minute

// spendCacheKey caches the window sum for a few seconds: it is one indexed
// query over a small table, but it runs on the reply path.
const (
	spendCacheKey = "gemini:spend:10m"
	spendCacheTTL = 20 * time.Second
)

// SpendLimitUSD — the account's rolling 10-minute ceiling, from
// GEMINI_SPEND_LIMIT_USD, defaulting to 10. Tier 1 was the measured tier when
// the limit was hit (docs/GEMINI-RATE-LIMIT.md §3), so 10 is the honest default
// until someone upgrades the account: Tier 2 is 50, Tier 3 is 200, and the
// value must be raised with the tier or the gate starts shedding turns that
// Google would have accepted. 0 or negative disables both gate and alert.
func SpendLimitUSD() float64 {
	if v := strings.TrimSpace(os.Getenv("GEMINI_SPEND_LIMIT_USD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 10
}

// GateRatio — the fraction of the ceiling at which the gate starts shedding.
// Below 1.0 on purpose: turns already in flight have not been recorded yet
// (cost is written after generation), so the last few percent of the window is
// always slightly understated.
func GateRatio() float64 {
	if v := strings.TrimSpace(os.Getenv("GEMINI_SPEND_GATE_RATIO")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			return f
		}
	}
	return 0.85
}

// RecentSpend returns the USD spent on model calls in the rolling window.
// Redis-cached for a few seconds; nil Redis falls through to the database.
func RecentSpend(ctx context.Context, db *pgxpool.Pool, rdb *redisstore.Client) (float64, error) {
	if db == nil {
		return 0, errors.New("no database")
	}
	if rdb != nil {
		if v, err := rdb.GetString(ctx, spendCacheKey); err == nil {
			if f, perr := strconv.ParseFloat(strings.TrimSpace(v), 64); perr == nil {
				return f, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var spent float64
	if err := db.QueryRow(ctx,
		"SELECT COALESCE(SUM(cost_estimate), 0) FROM token_usage WHERE created_at > $1",
		time.Now().Add(-spendWindow)).Scan(&spent); err != nil {
		return 0, err
	}
	if rdb != nil {
		_ = rdb.SetString(ctx, spendCacheKey, strconv.FormatFloat(spent, 'f', 6, 64), spendCacheTTL)
	}
	return spent, nil
}

// Budget returns (spent, limit, over) for the current window. It fails OPEN:
// when the figure cannot be computed the caller takes its normal path, because
// a metering outage must never stop customer replies.
func Budget(ctx context.Context, db *pgxpool.Pool, rdb *redisstore.Client) (float64, float64, bool) {
	limit := SpendLimitUSD()
	if limit <= 0 {
		return 0, limit, false
	}
	spent, err := RecentSpend(ctx, db, rdb)
	if err != nil {
		return 0, limit, false
	}
	return spent, limit, spent >= limit*GateRatio()
}
