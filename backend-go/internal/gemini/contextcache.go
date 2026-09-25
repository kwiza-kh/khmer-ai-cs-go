package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Explicit context caching (the cachedContents API).
//
// The system instruction is re-sent with every request and is the only stable
// block of the prompt (history, retrieved knowledge and the message itself
// change every turn), so it is what gets registered as a cached content and
// billed at the cached-input rate — $0.03/1M against $0.30/1M on
// gemini-2.5-flash, a 10x discount.
//
// It is OFF by default because storage is not free: a cache costs $1.00/1M
// tokens per hour, i.e. 3.7x the full input price of the same tokens. Both the
// saving and the storage cost scale with the prefix length, so they cancel out
// and the break-even is a pure rate:
//
//	break-even ≈ $1.00 / ($0.30 - $0.03) ≈ 3.7 requests per hour
//
// per cached prefix (see docs/GEMINI-RATE-LIMIT.md §5.1). Below ~4 requests an
// hour a live cache is a net loss — and a deployment answering "a handful of
// messages a day" is far below it. Set GEMINI_CACHE_TTL (seconds) to switch it
// on: at that point every request carries cachedContent instead of the
// instruction, and traffic bursts (a load test, a campaign) get the discount.
//
// Correctness notes:
//   - the cache key is the exact system instruction (language included), so a
//     prompt edit or a different reply language gets its own entry;
//   - an entry is dropped when the API reports it stale, and the turn is
//     retried without it (see chatWithModel);
//   - a registration that fails is remembered for a backoff window, so a prefix
//     the API will not accept costs one rejected call per window instead of one
//     per request (measured 2026-09-25: the built-in prompt is 984 tokens,
//     min_total_token_count is 1024, so this is the expected state here);
//   - entries live in-process, so each instance registers its own cache for
//     the same prefix: correct, just redundant.

type contextCacheEntry struct {
	name    string
	expires time.Time
}

var (
	contextCacheMu sync.Mutex
	contextCache   = map[string]contextCacheEntry{}
	// contextCacheRejected records registration failures per key, so a prefix
	// the API refuses is not re-attempted on every turn.
	contextCacheRejected = map[string]time.Time{}
)

// contextCacheFailBackoff — how long a failed registration is remembered. Long
// enough that a too-small prefix costs a rounding error in quota, short enough
// that a transient error (or a prompt grown past the minimum) recovers without
// an operator touching anything.
func contextCacheFailBackoff() time.Duration {
	if v := strings.TrimSpace(os.Getenv("GEMINI_CACHE_FAIL_BACKOFF_SEC")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 10 * time.Minute
}

// cacheTTLSeconds — how long a registered cache stays valid, and the on/off
// switch: 0 (the default) disables explicit caching entirely. Turn it on only
// where the traffic justifies the storage bill (see the break-even above).
func cacheTTLSeconds() int {
	if v := strings.TrimSpace(os.Getenv("GEMINI_CACHE_TTL")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// minCacheableTokens — the prefix size the API requires before it accepts a
// cached content (1024 for flash-class models, 2048 for pro). Tunable so the
// guard can be exercised in tests.
func minCacheableTokens() int {
	if v := strings.TrimSpace(os.Getenv("GEMINI_CACHE_MIN_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 1024
}

// estimateTokens is deliberately rough and script-aware: Khmer and CJK
// tokenize at roughly one token per 1-2 runes, Latin at one per 4-5.
//
// The Latin divisor is 5 rather than 4 because the estimate is a GUARD, not a
// measurement: it decides whether to spend a registration call on the reply
// path, and the failure it prevents is the expensive one. Measured against the
// API's own count for the built-in prompt: 4387 runes, estimated 910, actual
// 984 (min 1024). Estimating low skips a borderline prefix; estimating high
// spends a call that the API answers "Cached content is too small". Both are
// recoverable (see contextCacheFailBackoff), but only one costs latency.
func estimateTokens(s string) int {
	runes := []rune(s)
	dense := 0
	for _, r := range runes {
		if r >= 0x1780 && r <= 0x17FF { // Khmer
			dense++
			continue
		}
		if (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x4E00 && r <= 0x9FFF) { // CJK
			dense++
		}
	}
	return dense/2 + (len(runes)-dense)/5
}

// contextCacheFor returns the cached-content name to attach to this request, or
// "" when caching is off, the prefix is too short to register, or registration
// failed. It never fails a turn: every error path returns "" and the caller
// sends a plain uncached request.
func (s *Service) contextCacheFor(ctx context.Context, language string) string {
	if !s.IsConfigured() || cacheTTLSeconds() <= 0 {
		return ""
	}
	prefix := s.systemInstruction(language)
	if estimateTokens(prefix) < minCacheableTokens() {
		return ""
	}
	key := contextCacheKey(prefix)
	contextCacheMu.Lock()
	if e, ok := contextCache[key]; ok && time.Now().Before(e.expires) {
		contextCacheMu.Unlock()
		return e.name
	}
	if at, ok := contextCacheRejected[key]; ok && time.Since(at) < contextCacheFailBackoff() {
		contextCacheMu.Unlock()
		return ""
	}
	contextCacheMu.Unlock()

	name, err := s.registerContextCache(ctx, prefix)
	if err != nil || name == "" {
		// Remember the failure: without this, a prefix the API will never
		// accept (too small, or a relay that does not proxy cachedContents)
		// costs a rejected round trip on EVERY turn.
		contextCacheMu.Lock()
		contextCacheRejected[key] = time.Now()
		contextCacheMu.Unlock()
		return ""
	}
	// Expire a minute early: the request must not travel with a name that dies
	// mid-flight, and a stale name costs a failed round trip before the retry.
	ttl := time.Duration(cacheTTLSeconds())*time.Second - time.Minute
	if ttl <= 0 {
		ttl = time.Duration(cacheTTLSeconds()) * time.Second / 2
	}
	contextCacheMu.Lock()
	contextCache[key] = contextCacheEntry{name: name, expires: time.Now().Add(ttl)}
	delete(contextCacheRejected, key)
	contextCacheMu.Unlock()
	return name
}

// forgetContextCache drops the entry for one language after the API reported it
// stale, so the next turn registers a fresh one instead of retrying a dead name.
func (s *Service) forgetContextCache(language string) {
	key := contextCacheKey(s.systemInstruction(language))
	contextCacheMu.Lock()
	delete(contextCache, key)
	contextCacheMu.Unlock()
}

func contextCacheKey(prefix string) string {
	sum := sha256.Sum256([]byte(prefix))
	return hex.EncodeToString(sum[:])
}

// registerContextCache posts the prefix as a cached content and returns the
// name to reference it by.
func (s *Service) registerContextCache(ctx context.Context, prefix string) (string, error) {
	prov, err := s.activeProvider()
	if err != nil {
		return "", err
	}
	body := map[string]any{
		// The body's `model` is a RESOURCE NAME, and the two platforms spell it
		// differently — see provider.cachedContentModel. Vertex rejects the
		// endpoint URL here with 400 "The Model name 'https://…' is malformed".
		"model": prov.cachedContentModel(s.snapshot().modelName),
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": prefix}},
		},
		"ttl": fmt.Sprintf("%ds", cacheTTLSeconds()),
	}
	status, text, err := s.postWithRetry(ctx, prov.cachedContentsURL(), body)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("cachedContents returned HTTP %d: %s", status, truncateRunes(text, 200))
	}
	var v struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(text), &v); err != nil || v.Name == "" {
		return "", fmt.Errorf("cachedContents: unusable response")
	}
	return v.Name, nil
}

// contextCacheStale reports whether an error response means the referenced
// cache is gone (expired TTL, deleted, or never existed). The API answers
// 400/403/404 with "cached content not found" in the body. A 429 is a quota
// refusal about the request itself, not evidence about the cache, and must not
// trigger the uncached retry.
func contextCacheStale(status int, body string) bool {
	if status < 400 || status >= 500 || status == http.StatusTooManyRequests {
		return false
	}
	lowered := strings.ToLower(body)
	return strings.Contains(lowered, "cached content") || strings.Contains(lowered, "cachedcontent")
}
