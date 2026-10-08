// Package replycache — tenant-scoped semantic answer cache. A grounded reply
// that was already generated, guarded and stored is served verbatim to the
// next semantically identical ask, skipping both the retrieval wait and
// generation. Lookups cost one embedding + one HNSW search, which runs
// concurrently with the speculative retrieval the pipeline starts anyway, so
// a miss adds no wall time and a hit answers in a fraction of a second.
//
// Correctness rules (each backed by a call-site comment):
//   - only post-guard, non-mock answers are stored;
//   - the whole tenant's cache is dropped whenever its knowledge base changes
//     (rag.Service.KBChanged → InvalidateTenant) — grounding sources changed,
//     so cached answers may be stale;
//   - small talk never touches the cache (conversational replies depend on
//     history), and very short queries are never looked up or stored (a
//     follow-up like "那第二种呢" must not resolve to an unrelated answer).
//
// A nil *Service is a valid, disabled cache: every caller checks Enabled and
// falls through to the full path.
package replycache

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
)

// Service is the reply cache. A nil *Service is a valid, disabled cache.
type Service struct {
	DB     *pgxpool.Pool
	Gemini *gemini.Service
	Logger *slog.Logger
	// Serving names the model that answers customers right now. A cached answer is served
	// only when the model that wrote it is the one in force: an answer from the previous
	// provider is not what this deployment says now. nil = no model filter (tests). An
	// empty name means no model is in force, and nothing is served.
	Serving func() string
	// Embed overrides the embedding source (tests inject a deterministic
	// stub). nil = use the configured Gemini embedding model.
	Embed func(ctx context.Context, text string) ([]float32, error)
}

// Enabled reports whether the cache may be used: wired, embeddable, and not
// switched off by env.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	if v := os.Getenv("REPLY_CACHE_ENABLED"); v != "" {
		if on, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil && !on {
			return false
		}
	}
	return s.DB != nil && (s.Embed != nil || (s.Gemini != nil && s.Gemini.IsConfigured()))
}

func minSimilarity() float64 { return config.EnvFloat("REPLY_CACHE_MIN_SIM", 0.92) }
func ttlHours() int          { return config.EnvInt("REPLY_CACHE_TTL_HOURS", 72) }
func maxPerUser() int        { return config.EnvInt("REPLY_CACHE_MAX_PER_USER", 500) }
func minQueryRunes() int     { return config.EnvInt("REPLY_CACHE_MIN_RUNES", 12) }

func (s *Service) embed(ctx context.Context, text string) ([]float32, error) {
	if s.Embed != nil {
		return s.Embed(ctx, text)
	}
	return s.Gemini.GenerateQueryEmbedding(ctx, text)
}

// Lookup returns the cached answer for a semantically identical ask, or false.
// Any failure (embedding error, DB error) is a miss, never an error: the cache
// must not be able to fail a customer turn.
func (s *Service) Lookup(ctx context.Context, userID int32, query, language string) (string, bool) {
	if !s.Enabled() || len([]rune(query)) < minQueryRunes() {
		return "", false
	}
	model := ""
	if s.Serving != nil {
		if model = s.Serving(); model == "" {
			return "", false
		}
	}
	// Same normalization the retrieval side applies (rag.NormalizeText): NFC,
	// Khmer digits → ASCII, ZWSP/ZWNJ/NBSP → space. Without it, two renders
	// of the same Khmer question embed as two entries and hits drift.
	query = rag.NormalizeText(query)
	vec, err := s.embed(ctx, query)
	if err != nil || len(vec) == 0 {
		return "", false
	}
	var cacheID int64
	var answer string
	err = s.DB.QueryRow(ctx,
		"SELECT cache_id, answer FROM reply_cache "+
			"WHERE user_id = $1 AND language = $2 "+
			"AND created_at > NOW() - make_interval(hours => $3) "+
			"AND query_embedding <=> $4::vector < $5 "+
			"AND ($6 = '' OR model_name = $6) "+
			"ORDER BY query_embedding <=> $4::vector LIMIT 1",
		userID, language, ttlHours(), gemini.FormatVector(vec), 1-minSimilarity(), model).
		Scan(&cacheID, &answer)
	if err != nil {
		return "", false
	}
	// By primary key: identical answer texts are common across different
	// questions ("250$") — counting by answer would bump every sibling row.
	_, _ = s.DB.Exec(ctx,
		"UPDATE reply_cache SET hit_count = hit_count + 1, last_hit_at = NOW() WHERE cache_id = $1",
		cacheID)
	return answer, true
}

// Store caches a final (post-guard) answer. Best-effort: failures are logged,
// never propagated — a lost store only costs the next identical ask a miss.
func (s *Service) Store(ctx context.Context, userID int32, query, language, answer, model string) {
	if !s.Enabled() || strings.TrimSpace(answer) == "" || len([]rune(query)) < minQueryRunes() {
		return
	}
	// Mirror the lookup-side normalization (see Lookup) so the stored key and
	// embedding are canonical Khmer/Latin text.
	query = rag.NormalizeText(query)
	vec, err := s.embed(ctx, query)
	if err != nil || len(vec) == 0 {
		if s.Logger != nil {
			s.Logger.Warn("reply cache store skipped: embedding failed", "error", err.Error())
		}
		return
	}
	if _, err := s.DB.Exec(ctx,
		"INSERT INTO reply_cache (user_id, language, query_text, query_embedding, answer, model_name) "+
			"VALUES ($1, $2, $3, $4::vector, $5, $6)",
		userID, language, query, gemini.FormatVector(vec), answer, model); err != nil {
		if s.Logger != nil {
			s.Logger.Warn("reply cache store failed", "error", err.Error())
		}
		return
	}
	// Housekeeping in the same breath: expire past-TTL rows globally and keep
	// the tenant under its cap (most-recently-hit win). Both tables are tiny.
	_, _ = s.DB.Exec(ctx,
		"DELETE FROM reply_cache WHERE created_at < NOW() - make_interval(hours => $1)", ttlHours())
	_, _ = s.DB.Exec(ctx,
		"DELETE FROM reply_cache WHERE user_id = $1 AND cache_id NOT IN ("+
			"SELECT cache_id FROM reply_cache WHERE user_id = $1 "+
			"ORDER BY COALESCE(last_hit_at, created_at) DESC LIMIT $2)",
		userID, maxPerUser())
}

// InvalidateTenant drops every cached answer for a tenant. Called whenever
// that tenant's knowledge base changes (document indexed or deleted): cached
// answers were grounded in the old content and may be stale. Synchronous and
// cheap — correctness over latency.
func (s *Service) InvalidateTenant(ctx context.Context, userID int32) {
	if s == nil || s.DB == nil {
		return
	}
	if _, err := s.DB.Exec(ctx, "DELETE FROM reply_cache WHERE user_id = $1", userID); err != nil && s.Logger != nil {
		s.Logger.Warn("reply cache invalidation failed", "user_id", userID, "error", err.Error())
	}
}
