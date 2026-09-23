package replycache

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB-gated tests for the semantic reply cache — same discipline as the RLS
// backstop tests: skip without DATABASE_URL, all probe rows committed under
// dedicated throwaway users and precisely cleaned up in t.Cleanup.
//
// The embedder is a deterministic stub (bag of character 4-grams → hashed
// buckets), so the tests exercise the cache mechanics — hit, miss, threshold,
// tenant invalidation, TTL — with no network and no Gemini key. Two texts
// sharing no 4-grams embed orthogonally; a text embedding to the same bucket
// vector hits.

const cacheSkip = "DATABASE_URL not set — reply cache tests need a real migrated database"

type stubEmbed map[string][]float32

func (s stubEmbed) embed(_ context.Context, text string) ([]float32, error) {
	if v, ok := s[text]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("no stub embedding for %q", text)
}

func nearlyOrthogonal(dim int, unit int) []float32 {
	v := make([]float32, dim)
	v[unit] = 1
	return v
}

func cachePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip(cacheSkip)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func cacheUser(t *testing.T, pool *pgxpool.Pool, tag string) int32 {
	t.Helper()
	var id int32
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO users (username, email, password_hash) VALUES ($1, $2, 'cache-probe') RETURNING user_id",
		"__rcache_"+tag+"_"+suffix, "__rcache_"+tag+"_"+suffix+"@cache-probe.invalid").Scan(&id); err != nil {
		t.Fatalf("seed cache user: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE user_id = $1", id)
	})
	return id
}

// TestReplyCacheHitMissInvalidation covers the core contract on a real schema:
// store→hit on the same ask, miss on an unrelated ask, and the tenant-wide
// invalidation that a knowledge-base change triggers.
func TestReplyCacheHitMissInvalidation(t *testing.T) {
	pool := cachePool(t)
	userID := cacheUser(t, pool, "a")
	const dim = 768

	embeds := stubEmbed{
		"រាងកាយធ្វើពីអ្វី តម្លៃប៉ុន្មាន?": nearlyOrthogonal(dim, 1), // "what is it made of, how much?"
		"shipping to siem reap how long": nearlyOrthogonal(dim, 2),
		"completely unrelated spam text": nearlyOrthogonal(dim, 3),
	}
	s := &Service{DB: pool, Logger: nil, Embed: embeds.embed}
	if !s.Enabled() {
		t.Fatal("stub-embedded service must be enabled")
	}
	ctx := context.Background()

	answer := "ក្រោមជើង​ប្រាក់ 250$ មួយគ្រឿង។" // "250 USD each"
	s.Store(ctx, userID, "រាងកាយធ្វើពីអ្វី តម្លៃប៉ុន្មាន?", "km", answer, "test-model")

	got, hit := s.Lookup(ctx, userID, "រាងកាយធ្វើពីអ្វី តម្លៃប៉ុន្មាន?", "km")
	if !hit || got != answer {
		t.Fatalf("same-ask lookup = %q hit=%v, want the stored answer", got, hit)
	}

	if _, hit := s.Lookup(ctx, userID, "shipping to siem reap how long", "km"); hit {
		t.Fatal("an orthogonal question must not hit another question's answer")
	}
	if _, hit := s.Lookup(ctx, userID, "completely unrelated spam text", "en"); hit {
		t.Fatal("a different language must not hit a km answer (language guard)")
	}

	// Short queries never look up: a follow-up must not resolve to anything.
	if _, hit := s.Lookup(ctx, userID, "តម្លៃ?", "km"); hit {
		t.Fatal("sub-minRunes query hit the cache — follow-ups would steal answers")
	}

	s.InvalidateTenant(ctx, userID)
	if _, hit := s.Lookup(ctx, userID, "រាងកាយធ្វើពីអ្វី តម្លៃប៉ុន្មាន?", "km"); hit {
		t.Fatal("answer survived InvalidateTenant — KB changes would serve stale grounding")
	}
}

// TestReplyCacheHitCountsByRow pins the hit-counter contract: identical
// answers cached under different questions are common ("250$"), and a hit on
// one must not bump its siblings' hit_count — the counter is per row (by
// cache_id), not per answer text.
func TestReplyCacheHitCountsByRow(t *testing.T) {
	pool := cachePool(t)
	ctx := context.Background()
	const dim = 768

	embeds := stubEmbed{
		"how much is the water filter":          nearlyOrthogonal(dim, 1),
		"how much is the replacement cartridge": nearlyOrthogonal(dim, 2),
	}
	s := &Service{DB: pool, Embed: embeds.embed}
	userID := cacheUser(t, pool, "c")
	shared := "It costs 250 USD."
	s.Store(ctx, userID, "how much is the water filter", "en", shared, "test-model")
	s.Store(ctx, userID, "how much is the replacement cartridge", "en", shared, "test-model")

	if _, hit := s.Lookup(ctx, userID, "how much is the water filter", "en"); !hit {
		t.Fatal("lookup on a stored question must hit")
	}
	var aHits, bHits int
	if err := pool.QueryRow(ctx,
		"SELECT hit_count FROM reply_cache WHERE user_id = $1 AND query_text = 'how much is the water filter'",
		userID).Scan(&aHits); err != nil {
		t.Fatalf("read counter A: %v", err)
	}
	if err := pool.QueryRow(ctx,
		"SELECT hit_count FROM reply_cache WHERE user_id = $1 AND query_text = 'how much is the replacement cartridge'",
		userID).Scan(&bHits); err != nil {
		t.Fatalf("read counter B: %v", err)
	}
	if aHits != 1 || bHits != 0 {
		t.Fatalf("hit counters after one lookup: A=%d B=%d, want A=1 B=0 (sibling must not be bumped)", aHits, bHits)
	}
}

// TestReplyCacheThresholdAndTTL are knob contracts: a lowered bar must let an
// inexact match hit, and TTL=0 must expire everything.
func TestReplyCacheThresholdAndTTL(t *testing.T) {
	pool := cachePool(t)
	ctx := context.Background()
	const dim = 768

	// Two vectors 0.95-similar: identical except dim 1 carries 0.05 against
	// dim 2 — cosine sim between unit vectors e1 and 0.05*e2+~1.0*e1 ≈ 0.9987…
	// (exactly computable but the assertion only needs the two sides of 0.92).
	base := nearlyOrthogonal(dim, 1)
	other := nearlyOrthogonal(dim, 1)
	other[2] = 0.05 // tiny perturbation → similarity just under 1.0

	embeds := stubEmbed{
		"how long does delivery to battambang take":  base,
		"how long does delivery to battambang take?": other,
	}
	s := &Service{DB: pool, Embed: embeds.embed}
	userID := cacheUser(t, pool, "b")
	q := "how long does delivery to battambang take"
	s.Store(ctx, userID, q, "en", "3-5 days by car.", "test-model")

	t.Setenv("REPLY_CACHE_MIN_SIM", "0.99")
	if got, hit := s.Lookup(ctx, userID, "how long does delivery to battambang take?", "en"); !hit {
		t.Fatalf("inexact match below the tightened bar missed unexpectedly (got %q)", got)
	}

	t.Setenv("REPLY_CACHE_MIN_SIM", "1.0")
	if _, hit := s.Lookup(ctx, userID, "how long does delivery to battambang take?", "en"); hit {
		t.Fatal("similarity 1.0 bar matched a perturbed vector — threshold not applied")
	}

	t.Setenv("REPLY_CACHE_MIN_SIM", "0.92")
	t.Setenv("REPLY_CACHE_TTL_HOURS", "0")
	if _, hit := s.Lookup(ctx, userID, q, "en"); hit {
		t.Fatal("TTL=0 must expire every row")
	}
}

// TestReplyCacheNilServiceIsDisabled pins the nil-safety contract every caller
// relies on.
func TestReplyCacheNilServiceIsDisabled(t *testing.T) {
	var s *Service
	if s.Enabled() {
		t.Fatal("nil service must be disabled")
	}
	if _, hit := s.Lookup(context.Background(), 1, "a question long enough", "km"); hit {
		t.Fatal("nil service must never hit")
	}
	s.InvalidateTenant(context.Background(), 1)                                  // must not panic
	s.Store(context.Background(), 1, "a question long enough", "km", "ans", "m") // must not panic
}
