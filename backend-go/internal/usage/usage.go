// Package usage records every Gemini turn into token_usage so the admin
// analytics surface (tokens / cost / cache hit) has real data. Best-effort:
// recording must never fail the chat path.
package usage

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Gemini 2.5 Flash list pricing (USD per 1M tokens). Cached input is billed at
// a 4x discount; overrides via env are intentionally not provided — the number
// is an estimate for dashboards, not an invoice.
const (
	inputPer1M    = 0.30
	outputPer1M   = 2.50
	cachedInPer1M = 0.03
	// embeddingPer1M is the embedding model's list price. It is a separate rate
	// because an embedding billed at the chat input rate would overstate a
	// document ingest by an order of magnitude and shed turns at the spend gate
	// for no reason.
	embeddingPer1M = 0.15
)

// EstimateCostFor prices one call by the model that served it: the chat formula
// is the default, embedding models bill input only.
func EstimateCostFor(model string, prompt, completion, cached int) float64 {
	if isEmbeddingModel(model) {
		if prompt < 0 {
			prompt = 0
		}
		return float64(prompt) / 1e6 * embeddingPer1M
	}
	if isClaude(model) {
		return estimateClaude(prompt, completion, cached)
	}
	return EstimateCost(prompt, completion, cached)
}

// Claude list prices (USD per 1M tokens) for claude-haiku-5-5, the one model the
// anthropic catalog offers: a prompt of up to claudeLongPromptTokens bills at the
// short rate, and a longer prompt bills every token at the long rate. Cache reads
// bill at a tenth of the input rate. A Claude model added to the catalog needs its
// own rate card here before the console offers it.
const (
	claudeLongPromptTokens = 100_000
	claudeCacheReadFactor  = 0.10
)

type rateCard struct{ in, out float64 }

var (
	claudeShortRates = rateCard{in: 0.10, out: 0.50}
	claudeLongRates  = rateCard{in: 0.50, out: 2.50}
)

// isClaude matches the Claude family by name, as isEmbeddingModel does for the
// embedding family.
func isClaude(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "claude-")
}

// estimateClaude prices one Claude call. The prompt count includes the cached part,
// as the Gemini accounting does, so the uncached part is what is left after it.
func estimateClaude(prompt, completion, cached int) float64 {
	rates := claudeShortRates
	if prompt > claudeLongPromptTokens {
		rates = claudeLongRates
	}
	uncached := prompt - cached
	if uncached < 0 {
		uncached = 0
	}
	return float64(uncached)/1e6*rates.in +
		float64(cached)/1e6*rates.in*claudeCacheReadFactor +
		float64(completion)/1e6*rates.out
}

// isEmbeddingModel matches the embedding family by name. Both the chat path and
// the auxiliary observer pass whatever model string the client used, and an
// embedding call has no completion tokens to feed the chat formula.
func isEmbeddingModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "embedding")
}

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
	cost := EstimateCostFor(model, prompt, completion, cached)
	if _, err := db.Exec(ctx,
		"INSERT INTO token_usage (user_id, session_id, model, prompt_tokens, completion_tokens, total_tokens, cached_tokens, cost_estimate, created_at) "+
			"VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		userID, sessionID, model, prompt, completion, prompt+completion, cached, cost, time.Now()); err != nil {
		// Still best-effort — the chat path must never fail on accounting — but
		// no longer silent. This row is not only a dashboard input: the rolling
		// spend gate (spend.go) sums exactly cost_estimate over a 10-minute
		// window to decide whether to shed the next turn, so a dropped insert
		// under-counts spend and weakens the guardrail that protects the account
		// from Google's 429 wall. Losing it must therefore be audible.
		slog.Default().Error("token usage not recorded",
			"error", err.Error(), "user_id", userID, "model", model,
			"prompt_tokens", prompt, "completion_tokens", completion, "cached_tokens", cached)
	}
}
