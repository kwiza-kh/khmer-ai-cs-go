// Package usage records every Gemini turn into token_usage so the admin
// analytics surface (tokens / cost / cache hit) has real data. Best-effort:
// recording must never fail the chat path.
package usage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Gemini 2.5 Flash list pricing (USD per 1M tokens). Cached input is billed at
// a 4x discount; overrides via env are intentionally not provided — the number
// is an estimate for dashboards, not an invoice.
const (
	inputPer1M      = 0.30
	outputPer1M     = 2.50
	cachedInPer1M   = 0.03
)

// EstimateCost returns the USD cost of one turn given non-cached prompt,
// completion and cached-input token counts.
func EstimateCost(prompt, completion, cached int) float64 {
	uncached := prompt - cached
	if uncached < 0 {
		uncached = 0
	}
	return float64(uncached)/1e6*inputPer1M +
		float64(cached)/1e6*cachedInPer1M +
		float64(completion)/1e6*outputPer1M
}

// ctxKey tags a context with the user a model call should be billed to.
type ctxKey int

const userKey ctxKey = 1

// WithUser attributes any model spend incurred under ctx to userID. The
// auxiliary Gemini calls (compile, translate, rerank, rewrite, classify,
// transcribe, describe, TTS) happen deep inside packages that have no notion
// of a tenant, so the owner travels on the context instead of through every
// signature.
func WithUser(ctx context.Context, userID int32) context.Context {
	return context.WithValue(ctx, userKey, userID)
}

// UserFrom returns the user tagged by WithUser.
func UserFrom(ctx context.Context) (int32, bool) {
	id, ok := ctx.Value(userKey).(int32)
	return id, ok
}

// Record inserts one token_usage row (user, optional session, model, counts).
// prompt includes the cached portion (Gemini semantics); cached is the subset
// served from the context cache.
func Record(ctx context.Context, db *pgxpool.Pool, userID int32, sessionID *string, model string, prompt, completion, cached int) {
	if db == nil || prompt+completion == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	cost := EstimateCost(prompt, completion, cached)
	_, _ = db.Exec(ctx,
		"INSERT INTO token_usage (user_id, session_id, model, prompt_tokens, completion_tokens, total_tokens, cached_tokens, cost_estimate, created_at) "+
			"VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		userID, sessionID, model, prompt, completion, prompt+completion, cached, cost, time.Now())
}
