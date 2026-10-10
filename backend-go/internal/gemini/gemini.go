// Package gemini wraps the Gemini REST API (generateContent / embedContent)
// with the same endpoints and fallback behaviour as the Rust backend: real
// calls when an API key is configured, deterministic mocks otherwise.
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/textutil"
)

const (
	// EmbeddingModelGE1 and EmbeddingModelGE2 are the two embedding models this
	// codebase routes. They are NOT interchangeable: the vector spaces are
	// incompatible (a corpus indexed with one cannot be queried with the other),
	// they disagree on the serving method on Vertex — 001 answers :predict,
	// 002 answers :embedContent and 404s on :predict (measured 2026-10-09,
	// production project, region global) — and 002 has no batch entry point at
	// all. 001 retires no sooner than 2028-05; 002 is its named successor.
	EmbeddingModelGE1 = "gemini-embedding-001"
	EmbeddingModelGE2 = "gemini-embedding-2"

	// EmbeddingModel is the name used when GEMINI_EMBEDDING_MODEL is unset. It
	// deliberately stays the legacy model: an unconfigured or misconfigured
	// deployment must keep behaving exactly as it did before 002 existed, in the
	// same way GEMINI_PROVIDER defaults to studio. The switch to 002 is an
	// explicit env change (see ConfiguredEmbeddingModel), never a silent default
	// flip.
	EmbeddingModel = EmbeddingModelGE1

	// embeddingModelEnv names the deployment's embedding model.
	embeddingModelEnv = "GEMINI_EMBEDDING_MODEL"

	// embeddingVectorDimension is the width knowledge_chunks.embedding holds.
	// BOTH models default to 3072 when outputDimensionality is omitted, so every
	// call site must send it; a wrong width is not an upstream error (the call
	// succeeds) and only surfaces later as a pgvector mismatch or bad search.
	embeddingVectorDimension = 768

	// embeddingBatchConcurrency bounds the parallel per-text calls that replace
	// batching for models with no batch entry point (002). 6 is deliberately
	// modest: the embedding quota is shared with the reply path and the
	// keep-warm probe, and the 2026-10-05 quota storm showed what saturating it
	// does to retrieval. Raise it only with the quota headroom to match.
	embeddingBatchConcurrency = 6
)

// ConfiguredEmbeddingModel resolves GEMINI_EMBEDDING_MODEL: the model this
// deployment embeds and searches with. It is read per call like every other env
// knob, so tests and the A/B tooling (cmd/embedab) can point one process at one
// model without rebuilding.
func ConfiguredEmbeddingModel() string {
	return NormalizeModelName(config.EnvText(embeddingModelEnv, EmbeddingModel))
}

// embeddingShape describes how ONE embedding model is addressed and conditioned.
// It is a table rather than a switch so a model can be added in one place, and
// so the differences that matter are visible side by side:
//
//	model                  vertex method   batch        conditioning
//	gemini-embedding-001   :predict        yes          taskType (studio only)
//	gemini-embedding-2     :embedContent   NO           prompt prefixes
//
// Measured 2026-10-09 on the production project (global): 002 answers
// :embedContent with any outputDimensionality in {256,768,1536,3072} and returns
// unit-normalised vectors; :batchEmbedContents is an HTML 404 on the platform
// route; an explicit taskType is accepted and IGNORED (vectors bit-identical to
// no taskType), which is why 002 is conditioned with the documented prefixes
// instead — "task: search result | query: …" for queries and
// "title: none | text: …" for documents.
type embeddingShape struct {
	// vertexMethod is the serving method on the platform.
	vertexMethod string
	// nativeBatch reports a one-call batch entry point: studio :batchEmbedContents
	// for 001, the multi-instance :predict for 001 on vertex. 002 has none.
	nativeBatch bool
	// queryPrefix/documentPrefix prepend the model's task idiom. Empty for 001,
	// whose conditioning (where it exists) is the taskType field.
	queryPrefix    string
	documentPrefix string
}

func embeddingShapeFor(model string) embeddingShape {
	if isEmbeddingModelGE2(model) {
		return embeddingShape{
			vertexMethod:   ":embedContent",
			nativeBatch:    false,
			queryPrefix:    "task: search result | query: ",
			documentPrefix: "title: none | text: ",
		}
	}
	return embeddingShape{vertexMethod: ":predict", nativeBatch: true}
}

// isEmbeddingModelGE2 matches the 002 family by prefix, not equality, so a
// spelled suffix (gemini-embedding-2-preview) routes to the same serving shape
// instead of silently falling back to 001's :predict — which 404s — as its
// "failure mode". An unknown future name still falls back to the legacy shape:
// that is the direction that fails loudly (404) rather than mixing vector
// spaces.
func isEmbeddingModelGE2(model string) bool {
	return strings.HasPrefix(NormalizeModelName(model), EmbeddingModelGE2)
}

// EmbeddingVertexMethod names the :method a model is served by on the Vertex
// platform — ":predict" for 001, ":embedContent" for 002 (measured 2026-10-09;
// each 404s on the other's method). Exported for cmd/vertexprobe, whose job is
// to certify that THIS deployment's model still resolves, so it must ask the
// same table the client routes with instead of hard-coding a method that was
// right for the previous model.
func EmbeddingVertexMethod(model string) string {
	return embeddingShapeFor(model).vertexMethod
}

// FastModel routes auxiliary calls (rewrite/rerank/audit) to a cheap model.
// GEMINI_FAST_MODEL overrides it per deploy.
//
// The default is a LOCKED model version, and that is the point of this comment:
// two earlier defaults each failed in a different way.
//
//   - "gemini-3.6-flash" (the previous default) does not exist on the platform:
//     every call 404s. Nothing caught it at startup, so auxiliary calls —
//     rerank / JudgeTurn / document compile — failed one request at a time,
//     each degrading silently to its fallback path.
//   - a floating alias such as "gemini-flash-lite-latest" is worse, because it
//     works until Google moves it: the name is not a version, it is "whatever
//     upstream serves today", and it does not exist on the platform at all.
//     A default that drifts with upstream cannot be verified by a deploy.
//
// So: never leave a retired name, and never default to -latest. When the
// platform retires 3.5-flash the fix is a new pinned default here, chosen from
// a measured `ListModels` answer — not an alias.
var FastModel = config.EnvText("GEMINI_FAST_MODEL", "gemini-3.5-flash")

// JudgeTurnBudget bounds the auxiliary call that audits one customer-service
// turn. It is a real budget, not a nicety: on timeout JudgeTurn reports
// failure and every caller silently degrades to its fallback classifier, so an
// upstream slowdown is indistinguishable from a model that cannot do the job.
// Measured on the production host against one unchanged prompt, the dropout
// moved from 2.4% to 13.5% across runs and reached 18%-32% during an upstream
// degradation window — it tracked the upstream window, not concurrency. Raise
// it to trade a slower turn for coverage; lower it when a late verdict is
// worse than a fallback one.
//
// Distinct from JEV_TURN_BUDGET_MS (the Jev-side budget, pipeline.go turnBudget,
// default 4000): this one bounds only the fast-model fallback that runs when Jev
// returns !ok. Do not merge the two knobs — they default to 4000 vs 10000.
var JudgeTurnBudget = config.EnvMillis("GEMINI_JUDGE_BUDGET_MS", 10*time.Second)

// Transport shape of one generation call. postWithRetry runs up to
// postMaxAttempts attempts, each bounded by the shared client's
// postAttemptTimeout, with a linear backoff between them.
const (
	postMaxAttempts    = 3
	postAttemptTimeout = 60 * time.Second
	postBackoffStep    = 400 * time.Millisecond
)

// callBudget bounds ONE customer-facing generation call *including its retries*.
//
// Before this the only ceiling was per attempt (the shared HTTP client's 60s), so
// a hanging upstream could hold a pipeline worker for the whole worst case —
// attempts x 60s + backoff — while the customer waited and the outbox row stayed
// unclaimed. AstrBot has the mirror-image problem (no wall-clock timeout anywhere
// on the call path, astrbot/core/agent/runners/tool_loop_agent_runner.py:466);
// the lesson taken from it is that the budget has to cover the retries, not just
// one attempt.
//
// The default is ONE attempt's ceiling, not the retry worst case. It used to be
// the retry worst case (3x60s + backoff) — i.e. the budget was set to the exact value it exists to
// bound, which made it a no-op: three attempts could each spend their full 60s
// (181.2s total) while the customer waited. Retries are for FAST transient
// failures (the path to Google drops handshakes; a retry usually gets through),
// and those leave almost all of the budget unspent, so they still happen: what
// the budget now forbids is a second slow attempt after a slow first one.
//
// Measured 2026-10-04 over 98 attempts (31 reply cases + their retrieval and
// judge calls): zero went past attempt 1, so no turn came near the old ceiling.
// This is hardening against the pathological case, not a fix for an observed
// tail — the observed tail is per-hop (see the budgets on embed/rerank) plus the
// provider's own latency, and cutting the retry stack does not touch it.
func callBudget() time.Duration {
	return config.EnvMillis("GEMINI_CALL_BUDGET_MS", postAttemptTimeout)
}

// withCallBudget applies callBudget to ctx. A caller deadline that is already
// sooner is never extended: the tighter of the two wins.
func withCallBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := callBudget()
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= budget {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, budget)
}

// embedBudgetExceeded is budgetExceeded for the query-embedding hop.
//
// The caller logs "vector knowledge search failed; retaining lexical results" with
// whatever error it gets, and without this label a budget expiry is
// indistinguishable from a cancellation — both are "context deadline exceeded" /
// "context canceled" by the time they surface. That distinction IS the question
// when deciding whether GEMINI_EMBED_BUDGET_MS is too tight for this host: a
// deadline means the hop was too slow, a cancellation means the customer left.
// Measured 2026-10-04: in one 31-case run 25 of 34 query embeddings hit the 5s
// budget while a 45-case retrieval sweep on the same host saw 0 of 90 do so, so
// the rate is burst-dependent and has to be counted in production, not inferred
// from a harness run.
func embedBudgetExceeded(err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("query embedding exceeded its %s budget (GEMINI_EMBED_BUDGET_MS): %w", embedBudget(), err)
}

// budgetExceeded labels a timeout so an operator can tell a hung upstream from a
// network fault, and names the knob that controls it.
func budgetExceeded(err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("gemini call exceeded its %s budget (GEMINI_CALL_BUDGET_MS): %w", callBudget(), err)
}

// apiBase — Gemini REST endpoint. Override with GEMINI_API_BASE to route
// through a relay in a Google-supported region when the server's egress IP
// is geo-blocked ("User location is not supported for the API use").
// Value must include the /v1beta version segment, no trailing slash.
// Read per call (not cached at init) so tests can point it at a stub.
func apiBase() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://generativelanguage.googleapis.com/v1beta"
}

// HistoryItem is one chat turn for prompt construction.
type HistoryItem struct {
	Role    string
	Content string
}

// ChatResult is a non-streaming chat turn outcome.
type ChatResult struct {
	Reply        string
	PromptTokens int
	OutputTokens int
	CachedTokens int
	UsedMock     bool
}

// Service is the Gemini client (thread-safe).
type Service struct {
	client       *http.Client
	apiKey       string
	modelName    string
	systemPrompt string
	maxTokens    int
	// temperature is sent as generationConfig.temperature when non-nil, and left
	// out of the request when nil.
	//
	// The distinction is the whole point of the pointer: "not set" and "0.0" are
	// different instructions (the first means "use the platform's default", the
	// second means "greedy"), and this field sat unwired for a year while the
	// console displayed a value nobody sent. Leaving it nil keeps today's bytes on
	// the wire for any deployment that has not switched.
	temperature *float64

	// provider/providerErr are resolved once in New from the process environment
	// and a config file. The provider is NOT immutable: SetVertexRegion re-aims
	// it at another location while requests are in flight, so it is read and
	// written under the mutex like the hot-reloadable fields above (it used to be
	// read without one, on the grounds that only the environment could set it —
	// which the console can now do too). providerErr never changes; a broken
	// service-account file is not repaired by switching regions.
	provider    provider
	providerErr error
	// region is the Vertex location in force: the environment's default resolved
	// in New, then whatever the console last switched to. Empty on the studio
	// transport, which has no locations at all.
	region string

	// fallback is the second transport named by GEMINI_PROVIDER_FALLBACK,
	// resolved once in New. It is resolved here and not per call because minting
	// a vertex token source reads the service-account file, and that must not
	// happen on the reply path. hasFallback is false when the variable is unset,
	// names the primary's own kind, or cannot be built at all.
	fallback    provider
	hasFallback bool
	// failovers counts transport switches since start (observability: an operator
	// can see that the deployment is running on its second transport).
	failovers atomic.Int64

	// embedModel is the embedding model in force: GEMINI_EMBEDDING_MODEL
	// resolved in New, overridable for A/B tooling via SetEmbeddingModel. Empty
	// means "resolve from the environment per call" — the zero-value Service
	// must keep working.
	embedModel string
	// embedPrefixes enables the prefix conditioning GE2 was trained for (GE1's
	// prefixes are empty, so this is a no-op for it). On in New; SetEmbeddingPrefixes
	// exists for the A/B tooling that measures the prefix's effect.
	embedPrefixes bool

	mu         sync.Mutex
	embedCache map[string]embedCacheEntry
}

type embedCacheEntry struct {
	at  time.Time
	vec []float32
}

// New builds a service from env-style config values. Empty key → mock mode.
func New(apiKey, model string, maxTokens int) *Service {
	prov, provErr := providerFromEnv()
	s := &Service{
		apiKey:        apiKey,
		modelName:     NormalizeModelName(model),
		systemPrompt:  DefaultSystemPrompt,
		maxTokens:     maxTokens,
		provider:      prov,
		providerErr:   provErr,
		region:        NormalizeRegion(prov.vertex.region),
		embedModel:    ConfiguredEmbeddingModel(),
		embedPrefixes: true,
		embedCache:    make(map[string]embedCacheEntry),
	}
	// Resolve the failover transport once, here, for the reason the field
	// documents: building a vertex provider reads the service-account file.
	if kind := providerFallbackKind(); kind != "" && kind != prov.kind {
		switch kind {
		case providerStudio:
			// Usability depends on the API key, which the caller may swap later
			// via HotReload, so that check happens per call in providerCandidates.
			s.fallback, s.hasFallback = provider{kind: providerStudio}, true
		case providerVertex:
			if fb, ferr := providerForKind(providerVertex); ferr == nil {
				s.fallback, s.hasFallback = fb, true
			}
		}
	}
	// An empty key means mock mode on the studio path — unchanged. Vertex
	// authenticates with a service account, so an empty key is a valid
	// configuration there and the service is live; and a vertex deployment whose
	// configuration is BROKEN still counts as configured, so every turn fails
	// with that error instead of quietly serving template replies to customers.
	if apiKey != "" || prov.ready() || provErr != nil {
		s.client = &http.Client{Timeout: postAttemptTimeout}
	}
	return s
}

// activeProvider returns the transport for this deployment, or the
// configuration error that must fail the request. Every network entry point
// goes through this instead of reading s.provider directly: a vertex
// deployment that is missing its key file must fail loudly, not fall back to
// whatever URL shape a zero provider happens to produce.
func (s *Service) activeProvider() (provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providerErr != nil {
		return provider{}, s.providerErr
	}
	return s.provider, nil
}

// providerCandidates returns the ordered transports to try for one call: the
// deployment's own transport first, then GEMINI_PROVIDER_FALLBACK when it was
// configured AND is usable in this process.
//
// A deployment that sets nothing gets exactly one candidate, so this cannot
// change behaviour by itself. A broken primary configuration still returns nil
// (the caller re-reads the error and fails loudly) — a half-declared vertex
// migration must never be papered over by a fallback.
//
// The ordered list is the failover half of AstrBot's provider handling
// (tool_loop_agent_runner.py:541-613). The WHEN half is failoverEligible below.
func (s *Service) providerCandidates() []provider {
	s.mu.Lock()
	prov, broken := s.provider, s.providerErr
	fb, hasFB, apiKey := s.fallback, s.hasFallback, s.apiKey
	s.mu.Unlock()

	if broken != nil {
		return nil
	}
	out := []provider{prov}
	if !hasFB || fb.kind == prov.kind {
		return out
	}
	if fb.kind == providerStudio && strings.TrimSpace(apiKey) == "" {
		// Studio authenticates with the API key; without one every attempt on
		// this candidate would be a 401.
		return out
	}
	return append(out, fb)
}

// FailoverCount reports how many times a call moved to the second transport.
func (s *Service) FailoverCount() int64 { return s.failovers.Load() }

// failoverEligible decides whether trying another transport is worthwhile.
//
// Eligible: a transport-level error (nothing answered at all), 5xx, and the
// statuses that are a property of THIS transport's credential or region —
// 401/403 (credential refused) and 404 (the model does not exist in this
// transport's region; measured: gemini-3.8-flash answers 404 in
// asia-southeast1 and 200 in global, docs/GEMINI-RATE-LIMIT.md).
//
// Not eligible: 429. It is a spend/rate refusal that outlives any switch — the
// same reasoning postWithRetry already uses to fail fast on it — and a second
// transport would only double the doomed requests. Nor are ordinary 4xx: they
// describe the request, which the next transport would send unchanged.
func failoverEligible(status int, err error) bool {
	if err != nil && status == 0 {
		return true
	}
	switch {
	case status >= http.StatusInternalServerError:
		return true
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusNotFound:
		return true
	default:
		return false
	}
}

// FromPartsFull builds a service from explicit settings (startup DB config /
// admin hot-reload). Empty system prompt → default prompt.
func FromPartsFull(apiKey, modelName, systemPrompt string, maxTokens int) *Service {
	s := New(apiKey, modelName, maxTokens)
	if strings.TrimSpace(systemPrompt) != "" {
		s.systemPrompt = systemPrompt
	}
	return s
}

// The mutable fields below are swapped by HotReload while requests are in
// flight, so every access must take s.mu (read or write) — see the accessors.
func (s *Service) IsConfigured() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return true
	}
	// Under vertex the credential is the service-account file, not an API key,
	// so an empty model_configs.api_key is a VALID configuration there. Returning
	// false would send main.go down its "falling back to env/mock" branch and
	// answer customers with mock templates while a working service account sat
	// unused — and the startup check cannot catch it, because that check
	// validates the key file rather than this flag.
	//
	// No file is read here: the provider was validated at boot, and re-reading
	// per call would put filesystem I/O on the reply path.
	return CredentialSourceOf() == CredentialServiceAccount
}

func (s *Service) ModelName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelName
}

// EmbeddingModel reports the embedding model this service embeds and searches
// with. The zero-value Service resolves it from the environment, so a Service
// assembled without New behaves exactly like one built by it.
func (s *Service) EmbeddingModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.embedModel == "" {
		return ConfiguredEmbeddingModel()
	}
	return s.embedModel
}

// SetEmbeddingModel pins the embedding model for this service, overriding
// GEMINI_EMBEDDING_MODEL. Intended for measurement tooling that needs two
// models in one process (cmd/embedab); production switches the env var instead
// — one writer, one source of truth. The query cache is keyed by model, so a
// swap cannot serve one model's vectors for another's query.
func (s *Service) SetEmbeddingModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedModel = NormalizeModelName(model)
}

// SetEmbeddingPrefixes toggles the GE2 prompt-prefix conditioning. It exists so
// a measurement can isolate the prefix's effect; a deployment that never calls
// it gets the documented prefixes.
func (s *Service) SetEmbeddingPrefixes(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedPrefixes = enabled
}

// conditionText applies this service's embedding conditioning to raw text.
//
// It is the ONE place the text that goes on the wire is assembled, shared by
// the embed calls and by the query cache key — two copies of this rule would
// let a cached vector from one conditioning answer for the other.
//
// GE1 sends the text raw here because its conditioning is the taskType FIELD
// (studio) or nothing at all (vertex, which ignores task fields — measured);
// GE2 ignores taskType and is conditioned by these prefixes instead.
func (s *Service) conditionText(text, taskType string) string {
	s.mu.Lock()
	model := s.embedModel
	prefixes := s.embedPrefixes
	s.mu.Unlock()
	if model == "" {
		model = ConfiguredEmbeddingModel()
	}
	shape := embeddingShapeFor(model)
	if !prefixes {
		return text
	}
	switch taskType {
	case "RETRIEVAL_QUERY":
		return shape.queryPrefix + text
	case "RETRIEVAL_DOCUMENT":
		return shape.documentPrefix + text
	default:
		return text
	}
}

// conditionDocument applies the DOCUMENT-side conditioning, using the
// document's real title when the indexing path knows it.
//
// This is where the measured result lives: GE2's documented idiom is
// "title: <title> | text: <text>", and filling the title field with a real
// title beat both the placeholder and the bare text on the production Khmer
// corpus (recall@5 0.563 vs 0.492 vs 0.548 respectively, 2026-10-09). A title
// that is present but blank falls back to the placeholder, so a document whose
// title is whitespace cannot produce a malformed prefix.
//
// Models with no prefix conditioning (GE1) get the text unchanged: their
// vectors must stay byte-identical to the ones already in the column.
func (s *Service) conditionDocument(title, text string) string {
	s.mu.Lock()
	model := s.embedModel
	prefixes := s.embedPrefixes
	s.mu.Unlock()
	if model == "" {
		model = ConfiguredEmbeddingModel()
	}
	if !prefixes {
		return text
	}
	shape := embeddingShapeFor(model)
	if shape.documentPrefix == "" {
		// GE1: no document-side conditioning exists, not even a placeholder.
		return text
	}
	if t := strings.TrimSpace(title); t != "" {
		return "title: " + t + " | text: " + text
	}
	return shape.documentPrefix + text
}

// snapshot returns an immutable copy of the serving config for one request.
type servingConfig struct {
	apiKey       string
	modelName    string
	systemPrompt string
	maxTokens    int
	temperature  *float64
	client       *http.Client
}

func (s *Service) snapshot() servingConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return servingConfig{apiKey: s.apiKey, modelName: s.modelName, systemPrompt: s.systemPrompt,
		maxTokens: s.maxTokens, temperature: s.temperature, client: s.client}
}

// SetTemperature sets the sampling temperature sent with chat requests. nil means
// "do not send one", which is not the same as 0: the platform's own default
// applies then (documented as 1.0, and the value Google recommends for Gemini 3).
func (s *Service) SetTemperature(t *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.temperature = t
}

// Temperature reports what chat requests currently send, or nil for "nothing".
func (s *Service) Temperature() *float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.temperature
}

func (s *Service) SetModelName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != "" {
		s.modelName = NormalizeModelName(name)
	}
}

func (s *Service) SetSystemPrompt(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systemPrompt = prompt
}

// HotReload swaps the serving client's credentials/model/prompt in place so
// admin edits apply without a restart. Empty key leaves the client in place.
//
// The key is the STUDIO credential only. It cannot override a vertex
// deployment's authentication, because provider.authorize never reads it on
// that path — so passing "" under vertex reloads the model/prompt/max_tokens it
// was called with, and that is the whole intent: an empty model_configs.api_key
// is normal there and must not be read as "nothing to reload".
func (s *Service) HotReload(apiKey, modelName, systemPrompt string, maxTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if apiKey != "" {
		s.apiKey = apiKey
		if s.client == nil {
			s.client = &http.Client{Timeout: postAttemptTimeout}
		}
	}
	if modelName != "" {
		s.modelName = NormalizeModelName(modelName)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		s.systemPrompt = systemPrompt
	}
	if maxTokens > 0 {
		s.maxTokens = maxTokens
	}
}

// CredentialSource names the secret the ACTIVE transport authenticates with.
//
// It exists because the two credentials live in different places and have
// different lifecycles, and every caller that owns one of them (the admin
// console owns model_configs.api_key) was left guessing about the other. The
// guess is only visible as a wrong answer: on vertex the api_key column is
// empty, and a caller that read "empty key" as "not configured" refused to
// list models or to hot-reload an edit that had nothing to do with the key.
type CredentialSource string

const (
	// CredentialAPIKey — AI Studio: the key in model_configs.api_key, sealed by
	// security.Sealer, sent as x-goog-api-key. This is today's production.
	CredentialAPIKey CredentialSource = "api_key"
	// CredentialServiceAccount — Vertex: the service-account JSON named by
	// GEMINI_VERTEX_SA_FILE, exchanged for a Bearer token by vertex.go.
	//
	// The private key deliberately never enters the database. It is not a
	// bearer string that can be rotated and revoked in seconds — it is the
	// long-lived private half of an identity — and model_configs.api_key is a
	// column that travels through the admin UI, request logs and DB backups,
	// all of which a 0600 file owned by the service account does not. So in
	// vertex mode that column is not a chat credential at all: nothing in this
	// package reads it on the vertex path (provider.authorize ignores it).
	CredentialServiceAccount CredentialSource = "service_account"
)

// CredentialSourceOf reports where the active transport's credential comes
// from. It resolves the SAME environment the transport itself resolves
// (provider.go), so a caller branching on it branches on the transport's own
// opinion rather than on a second copy of the configuration that could drift.
func CredentialSourceOf() CredentialSource {
	if providerKindFromEnv() == providerVertex {
		return CredentialServiceAccount
	}
	return CredentialAPIKey
}

// IsAPIKey reports whether serving requires a caller-supplied API key. False
// means the transport authenticates itself, so an empty model_configs.api_key
// is a VALID configuration and not a missing credential — the same distinction
// New and HotReload already draw internally, exposed to the admin handlers.
func (c CredentialSource) IsAPIKey() bool { return c == CredentialAPIKey }

// Model capability vocabulary. The admin console switches on these EXACT
// strings (AvailableModel.capability), so they are named constants instead of
// literals scattered through the classifier.
const (
	CapabilityChat      = "chat"
	CapabilityEmbedding = "embedding"
	CapabilityImage     = "image"
	CapabilityTTS       = "tts"
	CapabilityLive      = "live"
	CapabilityOther     = "other"
)

// CatalogModel is one entry of the admin model picker.
type CatalogModel struct {
	Name        string
	DisplayName string
	// LaunchStage is the platform's own label ("GA", "PREVIEW", …). It is ""
	// whenever the entry carries none — the normal case on the studio path, and
	// also measured on some regional entries — so a caller must render "" as
	// "unknown" rather than as a value.
	LaunchStage string
	Capability  string
	// Available is true unless this deployment has POSITIVE evidence that the
	// model cannot be used in the region that was asked about. See ModelCatalog:
	// list membership is NOT such evidence (it under-reports in production's own
	// region), so nothing in this package sets it false today and the operator's
	// Test button remains the authority. It stays in the wire contract so a
	// future probe can add positive evidence without a shape change; today it is
	// constant-true, and a console must not read "true" as a guarantee.
	Available bool
}

// VertexRegion is one selectable Vertex location.
type VertexRegion struct {
	ID    string
	Label string
}

// vertexRegionCandidates is the region picker's static candidate list.
//
// There is no "list every region" API, so the candidates have to be static. The
// labels are the locations' human names, NOT a claim that each one serves
// Gemini — the listing itself is what answers that, and it answers per region.
var vertexRegionCandidates = []VertexRegion{
	{ID: "global", Label: "global (multi-region)"},
	{ID: "us-central1", Label: "us-central1 (Iowa)"},
	{ID: "us-east1", Label: "us-east1 (South Carolina)"},
	{ID: "us-east4", Label: "us-east4 (North Virginia)"},
	{ID: "us-west1", Label: "us-west1 (Oregon)"},
	{ID: "us-west4", Label: "us-west4 (Las Vegas)"},
	{ID: "northamerica-northeast1", Label: "northamerica-northeast1 (Montréal)"},
	{ID: "southamerica-east1", Label: "southamerica-east1 (São Paulo)"},
	{ID: "europe-west1", Label: "europe-west1 (Belgium)"},
	{ID: "europe-west2", Label: "europe-west2 (London)"},
	{ID: "europe-west3", Label: "europe-west3 (Frankfurt)"},
	{ID: "europe-west4", Label: "europe-west4 (Netherlands)"},
	{ID: "europe-central2", Label: "europe-central2 (Warsaw)"},
	{ID: "asia-east1", Label: "asia-east1 (Taiwan)"},
	{ID: "asia-east2", Label: "asia-east2 (Hong Kong)"},
	{ID: "asia-northeast1", Label: "asia-northeast1 (Tokyo)"},
	{ID: "asia-northeast3", Label: "asia-northeast3 (Seoul)"},
	{ID: "asia-south1", Label: "asia-south1 (Mumbai)"},
	{ID: "asia-southeast1", Label: "asia-southeast1 (Singapore)"},
	{ID: "asia-southeast2", Label: "asia-southeast2 (Jakarta)"},
	{ID: "australia-southeast1", Label: "australia-southeast1 (Sydney)"},
	{ID: "me-central1", Label: "me-central1 (Doha)"},
}

// VertexRegions returns the picker's candidates with the deployment's own region
// FIRST, plus that region as `current`.
//
// `current` is passed in by the caller because it is NOT the environment's value
// any more: it is the region the serving service is on RIGHT NOW (Service.Region,
// env default overridden by whatever the console last switched to). Reading the
// environment here would paint the selector with a region the deployment no
// longer uses — and it is the same value the admin save guard reasons about, so
// two sources would disagree about where traffic goes.
//
// It is never taken from the static list, and a configured region that is NOT in
// that list is still returned first: an operator running an unusual location
// must see the region their service actually uses, not a selector that cannot
// express it.
func VertexRegions(current string) ([]VertexRegion, string) {
	current = NormalizeRegion(current)
	out := make([]VertexRegion, 0, len(vertexRegionCandidates)+1)
	out = append(out, VertexRegion{ID: current, Label: regionLabel(current)})
	for _, r := range vertexRegionCandidates {
		if r.ID == current {
			continue
		}
		out = append(out, r)
	}
	return out, current
}

// regionLabel is the operator-facing label for one location id.
func regionLabel(region string) string {
	for _, r := range vertexRegionCandidates {
		if r.ID == region {
			return r.Label
		}
	}
	return region + " (configured)"
}

// vertexFallbackNames is the LAST-RESORT picker list, used only when a region's
// catalog cannot be fetched at all.
//
// It is not a gate and never was meant to be one: every entry it contributes is
// marked Available=false, because a name typed from memory is precisely what it
// is. Its one job is to keep a broken listing from rendering as "Gemini is
// broken" — the failure the curated list was originally introduced to paper
// over, back when the publisher route was believed to be unreachable at any
// version.
var vertexFallbackNames = []string{
	"gemini-3.5-flash",
	"gemini-2.5-flash",
	"gemini-embedding-001",
}

// publisherModelList is the platform's model-list envelope.
//
// The entries arrive under `publisherModels`; `models` is read too because the
// studio root uses that key and a relay in front of the platform may normalise
// one to the other. Which key the platform picks is the platform's business,
// not this package's.
type publisherModelList struct {
	PublisherModels []publisherModelEntry `json:"publisherModels"`
	Models          []publisherModelEntry `json:"models"`
	NextPageToken   string                `json:"nextPageToken"`
}

// publisherModelEntry is one model as the platform describes it.
//
// supportedActions is NOT a []string, and typing it as one took the model list
// down in production. The field's shape varies per entry (measured with the
// production service account on 2026-09-25):
//
//	absent            most entries
//	{}                gemma4, gemma3
//	{openNotebook:{…}} / {openGenerationAiStudio:{…}}   imagetext, image-segmentation-001
//
// A single entry of the wrong Go type fails the WHOLE page — `cannot unmarshal
// object into Go struct field …publisherModels.2.supportedActions of type
// []string` — and the listing then silently degrades to the three-name fallback
// for every region, which is indistinguishable from the platform being down.
// RawMessage accepts all three shapes, and nothing reads it (capabilityFor
// classifies by name), so there is no reason to model its contents at all.
//
// launchStage is decoded but must also tolerate absence: `gemma3` answered
// without one. Only the console's GA/preview badge consumes it, and an empty
// value renders as no badge rather than as a wrong one.
type publisherModelEntry struct {
	Name             string          `json:"name"`
	DisplayName      string          `json:"displayName"`
	LaunchStage      string          `json:"launchStage"`
	SupportedActions json.RawMessage `json:"supportedActions"`
}

// ModelCatalog returns the models an operator may pick from in `region`.
//
// The region selects the LISTING only. Nothing here changes which model or
// which region serves traffic — the serving path resolves its own region from
// GEMINI_VERTEX_REGION in providerFromEnv, and an empty region means exactly
// that configured region, so the picker's default request keeps the behaviour
// it had before region selection existed.
//
// Two rules make the result honest, and both exist because the platform's model
// list is NOT a callability oracle. Measured 2026-09-25, listing vs a real
// generateContent in the same region:
//
//	us-central1     gemini-2.5-flash  listed, 200   | gemini-3.5-flash  unlisted, 404
//	europe-west4    gemini-2.5-flash  listed, 200   | gemini-3.5-flash  unlisted, 404
//	asia-southeast1 gemini-2.5-flash  listed, 200   | gemini-3.5-flash  UNLISTED, 200  <-- the anomaly
//
// So the list can AGREE with callability (us-central1, europe-west4) and can
// also UNDER-report it (asia-southeast1, which serves the model it omits). It is
// never over-reporting in the measurements taken, but nothing guarantees that,
// and the under-report is the one that would do damage: reading "not listed" as
// "unusable" paints this deployment's own serving model as unavailable.
//
//  1. The deployment's configured model is ALWAYS unioned in, first, and is
//     never dropped for being absent from the list. A model that is answering
//     customers must not become unpickable because the catalog forgot it.
//  2. Available defaults to TRUE, and list membership is never turned into an
//     availability verdict in EITHER direction. The only honest encoding of "we
//     did not measure it" is true; the console's Test button is what measures.
//     Deliberately not encoded here: a per-model callability probe. It would
//     cost one billed request per listed model per region (133 in us-central1)
//     to sharpen a hint, and a probe that fails for quota or network reasons
//     would report a healthy model as unusable.
//
// A listing that 404s, fails or comes back empty is NOT an error: the caller
// gets the union plus the last-resort names and a non-empty warning naming the
// region and the cause. Only a broken CONFIGURATION (a missing or unreadable
// service-account key) is returned as an error, because no rendering makes that
// list correct.
func ModelCatalog(ctx context.Context, apiKey, region, configuredModel string) ([]CatalogModel, string, error) {
	prov, err := providerFromEnv()
	if err != nil {
		return nil, "", err
	}
	region = strings.ToLower(strings.TrimSpace(region))
	if prov.kind != providerVertex {
		// Studio is one global endpoint with no region at all: the parameter is
		// accepted (the console sends it unconditionally once a selector exists)
		// and ignored, rather than rejected.
		listed, err := studioCatalog(ctx, prov, apiKey)
		if err != nil {
			return nil, "", err
		}
		return withConfiguredModel(listed, configuredModel), "", nil
	}
	// One spelling for both the empty case and the URL: GEMINI_VERTEX_REGION is a
	// location id (lowercase by definition), but an operator's .env is not
	// obliged to be, and this value is interpolated into the request host.
	configuredRegion := strings.ToLower(prov.vertex.region)
	if region == "" {
		region = configuredRegion
	}
	listed, warning := publisherCatalog(ctx, prov, region)
	if len(listed) == 0 {
		listed = vertexFallbackCatalog()
		if warning == "" {
			warning = fmt.Sprintf("region %s listed no publisher models", region)
		}
	}
	return withConfiguredModel(listed, configuredModel), warning, nil
}

// ListModels returns just the model names — the pre-catalog shape, kept for the
// callers and tests that only need names. The admin picker uses ModelCatalog,
// which also carries the label, the launch stage and the capability.
//

// publisherCatalog pages one region's publisher-model list.
//
// A failure on the first page leaves the result empty and names the region and
// the cause, which is what tells the caller to fall back; a failure on a later
// page keeps the pages already fetched and still reports — a truncated catalog
// with a stated reason beats either a silent partial list or a hard error.
func publisherCatalog(ctx context.Context, prov provider, region string) ([]CatalogModel, string) {
	client := &http.Client{Timeout: 20 * time.Second}
	listed := make([]CatalogModel, 0, vertexListPageSize)
	seen := make(map[string]bool, vertexListPageSize)
	pageToken := ""
	for page := 1; page <= vertexListMaxPages; page++ {
		entries, next, err := publisherModelPage(ctx, client, prov, region, pageToken)
		if err != nil {
			return listed, fmt.Sprintf("region %s: publisher model list failed on page %d: %v", region, page, err)
		}
		for _, e := range entries {
			name := modelNameFromResource(e.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			listed = append(listed, CatalogModel{
				Name:        name,
				DisplayName: displayNameForEntry(name, e.DisplayName),
				LaunchStage: strings.TrimSpace(e.LaunchStage),
				Capability:  capabilityFor(name),
				Available:   true,
			})
		}
		if next == "" {
			return listed, ""
		}
		pageToken = next
	}
	return listed, fmt.Sprintf("region %s: model list truncated after %d pages", region, vertexListMaxPages)
}

// publisherModelPage fetches one page of one region's publisher-model list.
func publisherModelPage(ctx context.Context, client *http.Client, prov provider, region, pageToken string) ([]publisherModelEntry, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publisherModelsURL(region, pageToken), nil)
	if err != nil {
		return nil, "", err
	}
	// The studio key is deliberately NOT passed through: provider.authorize
	// picks the header per transport, and an AI Studio key reaching a platform
	// endpoint is the one thing the provider split exists to prevent.
	if err := prov.authorize(ctx, req, ""); err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// The platform explains itself in the body ("... is not found" for a
		// region that does not serve this route), and that sentence is the
		// difference between "the region is wrong" and "we are not allowed".
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode,
			textutil.Ellipsize(strings.TrimSpace(string(body)), 200))
	}
	var list publisherModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, "", fmt.Errorf("unparsable model list: %w", err)
	}
	entries := list.PublisherModels
	if len(entries) == 0 {
		entries = list.Models
	}
	return entries, strings.TrimSpace(list.NextPageToken), nil
}

// studioCatalog lists the AI Studio models — the pre-vertex request, unchanged:
// no region, the key in x-goog-api-key, pageSize=200.
//
// Unlike the vertex catalog this has no fallback: a failure here is a credential
// or connectivity problem the operator must see, and the console already
// reports it as one.
func studioCatalog(ctx context.Context, prov provider, apiKey string) ([]CatalogModel, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prov.listModelsURL(), nil)
	if err != nil {
		return nil, err
	}
	if err := prov.authorize(ctx, req, apiKey); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("models list HTTP %d", resp.StatusCode)
	}
	var list publisherModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	entries := list.Models
	if len(entries) == 0 {
		entries = list.PublisherModels
	}
	out := make([]CatalogModel, 0, len(entries))
	for _, e := range entries {
		name := modelNameFromResource(e.Name)
		if name == "" {
			continue
		}
		out = append(out, CatalogModel{
			Name:        name,
			DisplayName: displayNameForEntry(name, e.DisplayName),
			LaunchStage: strings.TrimSpace(e.LaunchStage),
			Capability:  capabilityFor(name),
			Available:   true,
		})
	}
	return out, nil
}

// vertexFallbackCatalog is vertexFallbackNames as catalog entries.
//
// They are offered as available, like everything else here, because a failed
// listing is evidence about the LISTING and not about any model: the same
// measurement that showed the list is not a callability oracle also showed a
// model answering 200 from a region whose list never mentions it. What the
// operator needs to know is that these names are unverified, and that is what
// the warning beside them says.
func vertexFallbackCatalog() []CatalogModel {
	out := make([]CatalogModel, 0, len(vertexFallbackNames))
	for _, name := range vertexFallbackNames {
		out = append(out, CatalogModel{
			Name:        name,
			DisplayName: displayNameFor(name),
			Capability:  capabilityFor(name),
			Available:   true,
		})
	}
	return out
}

// withConfiguredModel puts the deployment's configured model at the FRONT of the
// catalog, adding it when the list did not mention it.
//
// Front, not back: this is the model the operator is looking at, and burying it
// in a 133-entry dropdown is how the current selection becomes invisible. Its
// availability is not derived from the list either — see ModelCatalog's rule 2,
// and note that this is the exact model (gemini-3.5-flash in asia-southeast1)
// that a list-membership reading would wrongly hide.
func withConfiguredModel(listed []CatalogModel, configuredModel string) []CatalogModel {
	configured := NormalizeModelName(configuredModel)
	if configured == "" {
		return listed
	}
	var current *CatalogModel
	rest := make([]CatalogModel, 0, len(listed)+1)
	for _, m := range listed {
		if m.Name == configured {
			current = &m
			continue
		}
		rest = append(rest, m)
	}
	if current == nil {
		current = &CatalogModel{
			Name:        configured,
			DisplayName: displayNameFor(configured),
			Capability:  capabilityFor(configured),
			Available:   true,
		}
	}
	return append([]CatalogModel{*current}, rest...)
}

// displayNameForEntry prefers the platform's own label and derives one when the
// entry has none. The entries measured on the platform carry no displayName at
// all, so the derived path is the one that actually runs in production.
func displayNameForEntry(name, platformLabel string) string {
	if label := strings.TrimSpace(platformLabel); label != "" {
		return label
	}
	return displayNameFor(name)
}

// displayNameFor derives the operator-facing label from a model id:
// "gemini-3.8-flash" -> "Gemini 3.8 Flash".
//
// Derived rather than stored because the picker has to label models this
// deployment has never seen: operators type new ids into other screens, and a
// model that the platform lists but this file has never heard of still needs a
// label that is not its raw id in ALL CAPS or an empty string.
func displayNameFor(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		if upper, ok := modelNameAcronyms[strings.ToLower(p)]; ok {
			parts[i] = upper
			continue
		}
		// Version and size segments stay verbatim: "3.8" must not become "3.8"
		// with a capital, and "2.5" must never be re-cased into a different
		// model number.
		if p[0] >= '0' && p[0] <= '9' {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// modelNameAcronyms are the segments a plain title-case would mangle.
var modelNameAcronyms = map[string]string{
	"tts": "TTS", // gemini-2.5-flash-preview-tts -> "… Preview TTS"
	"asr": "ASR",
	"ocr": "OCR",
	"ai":  "AI",
	"api": "API",
}

// ProbeOutcome is what a callability probe learned about one model in one
// region. The third value is the point: it separates "the platform told us this
// model is not here" from "we could not ask", and only the former is allowed to
// stop an operator from saving. Quota exhaustion (429), a permission problem and
// a network failure are all Indeterminate — treating any of them as "unusable"
// would refuse a healthy model because of a transient condition.
type ProbeOutcome string

const (
	// ProbeServed — the region accepted the request.
	ProbeServed ProbeOutcome = "served"
	// ProbeNotServed — the region answered NOT_FOUND for this model. This is the
	// only evidence strong enough to block a save.
	ProbeNotServed ProbeOutcome = "not_served"
	// ProbeIndeterminate — no answer, or an answer that is not about the model.
	ProbeIndeterminate ProbeOutcome = "indeterminate"
)

// Region is the Vertex location every chat/generate call this deployment makes
// is sent to right now: GEMINI_VERTEX_REGION (or the measured default) as the
// boot value, then whatever the console last switched to.
//
// It is NOT the region an operator browses in the console — browsing picks a
// catalog to LIST and stays free — but it IS the region the model-save guard
// must reason about, because "will this model name work where it will actually
// be called" cannot be answered without it. One accessor for that question, so
// the picker's `current`, the guard and the request URL cannot disagree.
func (s *Service) Region() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.region
}

// SetVertexRegion moves the deployment to another Vertex location in place.
//
// Empty means "no change" rather than "reset to the environment": the console
// never sends an empty region, and a caller that cleared the column would
// otherwise silently relocate serving back to a value from a `.env` file it
// cannot see.
//
// The region is interpolated into the HOST of a request that carries a bearer
// token, so it is validated here with the same rule the request path uses
// (ValidVertexRegion) — an unvalidated one would be a way to aim this
// deployment's credential at an arbitrary host.
//
// On the studio transport there is nothing to aim (studio is one global endpoint
// with no location concept), so this is a no-op there rather than an error: the
// console is where a region request is refused, because only the console knows
// which transport the operator is looking at.
func (s *Service) SetVertexRegion(region string) error {
	region = NormalizeRegion(region)
	if region == "" {
		return nil
	}
	if !ValidVertexRegion(region) {
		return fmt.Errorf("invalid Vertex region %q", region)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.providerErr != nil {
		// A broken service-account configuration is not repaired by aiming it at
		// another location, and reporting "invalid region" for it would hide the
		// cause the operator has to fix.
		return s.providerErr
	}
	if s.provider.kind != providerVertex {
		return nil
	}
	s.provider = s.provider.withRegion(region)
	s.region = region
	return nil
}

// ProbeModel answers one question that neither the publisher list nor a region's
// documentation can answer reliably: does THIS region serve THIS model?
//
// It is a real (tiny) generateContent call, because that is the only API that
// reflects routing. Measured 2026-09-25 against the production service account:
// gemini-3.8-flash answers 200 on global/us/eu and 404 on asia-southeast1,
// us-central1, europe-west4 and nine other single regions. The publisher list
// cannot be used instead — it UNDER-reports (asia-southeast1 serves
// gemini-3.5-flash while omitting it from its list), so absence from a list is
// not evidence, and turning it into a verdict would mark this deployment's own
// serving model unusable.
//
// Cost is one request of a handful of tokens, and callers are expected to use it
// only where a wrong answer has consequences (see the save guard in the admin
// handler), never across a whole catalog — us-central1 lists 133 models.
//
// maxOutputTokens is 1 because only the status matters; the body is discarded.
// `?region=` on the console never reaches this function: the region passed here
// is always the deployment's configured SERVING region, which is the one a
// saved model name will actually be called against.
func ProbeModel(ctx context.Context, region, model string) ProbeOutcome {
	prov, err := providerFromEnv()
	if err != nil || prov.kind != providerVertex {
		// Nothing to probe on the studio transport: there is one endpoint and one
		// catalog, so a region-shaped question has no answer there.
		return ProbeIndeterminate
	}
	region = strings.ToLower(strings.TrimSpace(region))
	model = NormalizeModelName(strings.TrimSpace(model))
	if model == "" || !ValidVertexRegion(region) {
		return ProbeIndeterminate
	}
	body, err := json.Marshal(map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": "ok"}}}},
		"generationConfig": map[string]any{"maxOutputTokens": 1},
	})
	if err != nil {
		return ProbeIndeterminate
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		prov.vertex.model(model)+":generateContent", bytes.NewReader(body))
	if err != nil {
		return ProbeIndeterminate
	}
	req.Header.Set("Content-Type", "application/json")
	if err := prov.authorize(ctx, req, ""); err != nil {
		return ProbeIndeterminate
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeIndeterminate
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// The platform names the model and the location in the body; NOT_FOUND is
		// its answer to "this location does not serve this model".
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var envelope struct {
			Error struct {
				Status string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Error.Status == "NOT_FOUND" {
			return ProbeNotServed
		}
		// A 404 that is not NOT_FOUND (a wrong path, for instance) says nothing
		// about the model.
		return ProbeIndeterminate
	case resp.StatusCode == http.StatusTooManyRequests:
		// Quota, not availability: the model is served and merely busy.
		return ProbeIndeterminate
	case resp.StatusCode >= 400:
		// 400/401/403/5xx — a request or permission problem, not a routing answer.
		return ProbeIndeterminate
	default:
		return ProbeServed
	}
}

// capabilityFor classifies a model id into the console's vocabulary.
//
// Name-based, and ONLY name-based. The platform's own action list cannot carry
// this: it was absent on every entry a measurement against the production
// service account checked (gemini-3.8-flash, gemini-3.5-flash,
// gemini-2.5-flash-tts, gemini-embedding-001 all answered `actions=None`), while
// the naming convention is stable across families — including the families that
// would never answer a customer message, which is the mistake this classification
// exists to prevent.
//
// Pure function, no I/O: the console groups and filters on these strings, so it
// is unit-tested as a table.
func capabilityFor(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "embedding"):
		return CapabilityEmbedding
	// "native-audio" is the one live family whose id does not contain "live"
	// (gemini-2.5-flash-preview-native-audio).
	case strings.Contains(n, "live"), strings.Contains(n, "native-audio"):
		return CapabilityLive
	// Before the chat test: gemini-…-tts and gemini-…-image are Gemini ids, and
	// classifying them as chat is exactly the mistake that would put a speech
	// model in a customer service deployment's reply path.
	case strings.Contains(n, "tts") || strings.Contains(n, "text-to-speech"):
		return CapabilityTTS
	case strings.Contains(n, "imagen"), strings.Contains(n, "image"):
		return CapabilityImage
	case strings.Contains(n, "gemini"), strings.Contains(n, "gemma"), strings.Contains(n, "bison"),
		strings.Contains(n, "palm"):
		return CapabilityChat
	default:
		// Anything unrecognised is "other": guessing "chat" here is what would
		// offer a video or speech-to-text model as a reply model.
		return CapabilityOther
	}
}

// fastModelName — the fast model for THIS service instance. The package
// constant ages out (retired model names 404), so it defaults to the
// currently configured serving model, which tracks what actually works.
// GEMINI_FAST_MODEL still overrides for cost tuning.
func (s *Service) fastModelName() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_FAST_MODEL")); v != "" {
		return v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelName != "" {
		return s.modelName
	}
	return FastModel
}

// generateURLFor delegates to the transport (see provider.go) so that the
// endpoint every model call uses is built in one place.
//
// streamURLFor and embedURL used to sit next to it and were removed with this
// change: their only callers (ChatStream and embed) now hold the provider and
// ask it directly, so keeping the wrappers would have left two dead ways to
// build an endpoint — exactly the fragmentation that let the studio/vertex
// split hide in the first place.
func (s *Service) generateURLFor(model string) string {
	prov, err := s.activeProvider()
	if err != nil {
		// The caller resolved the transport first and already returned this
		// error (postWithRetry/cmd paths), so an unusable provider here means the
		// deployment was switched to a region with no credential between those
		// two reads — an empty URL fails the request the same way.
		return ""
	}
	return prov.generateURL(model)
}

// postWithRetry posts JSON, retrying 5xx and transport errors up to 3 times
// (the path to Google drops some handshakes; one retry usually gets through).
//
// 429 is deliberately NOT retried. A refusal is either the rolling spend limit
// (Tier 1 allows $10 per 10 minutes) or an exhausted balance, and both outlive
// any backoff worth waiting on inside a reply path: retrying only multiplies
// the number of doomed requests against the wall — measured 2026-09-24 at 6-9
// per customer turn once the fast-model fallback is counted — and delays the
// caller's own graceful degradation. The status and body are returned
// unchanged so the caller can tell a spend stop from a network fault.
func (s *Service) postWithRetry(ctx context.Context, target string, body any) (int, string, error) {
	prov, err := s.activeProvider()
	if err != nil {
		// A broken transport is not a transport blip: retrying inside this turn
		// would only multiply requests that cannot succeed, and token minting is
		// serialised, so a burst of turns would just queue behind it.
		return 0, "", err
	}
	return s.postWithProviderRetry(ctx, prov, target, body)
}

// postWithProviderRetry is the INNER layer of a two-layer retry: up to
// postMaxAttempts attempts against one transport, with the backoff between them.
// The outer layer (chatWithModel) moves to the next candidate once this layer has
// given up and the failure is failoverEligible.
func (s *Service) postWithProviderRetry(ctx context.Context, prov provider, target string, body any) (int, string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, "", fmt.Errorf("marshal request: %w", err)
	}
	lastErr := ""
	// Snapshot once: Reload swaps client under the mutex when an admin saves a
	// new model config, so reading s.client per attempt would race.
	client := s.snapshot().client
	for attempt := 0; attempt < postMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, "", ctx.Err()
			case <-time.After(postBackoffStep * time.Duration(attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Content-Type", "application/json")
		// Credential selection (studio api key vs vertex bearer token) happens in
		// exactly one place — see provider.authorize. It used to be an inline
		// x-goog-api-key write here; the comment that travelled with it (the key
		// belongs in the header, never the URL query, because a transport error
		// would otherwise render it into the error string) lives there now.
		if err := prov.authorize(ctx, req, s.snapshot().apiKey); err != nil {
			return 0, "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Sprintf("request: %v", err)
			continue
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		text := buf.String()
		if resp.StatusCode < http.StatusInternalServerError {
			// Everything below 5xx comes back as-is, including 429 — see the
			// doc comment: a refusal is not worth retrying.
			return resp.StatusCode, text, nil
		}
		lastErr = fmt.Sprintf("failed (%d): %s", resp.StatusCode, textutil.Ellipsize(text, 300))
	}
	return 0, "", fmt.Errorf("gemini: %s", lastErr)
}

// Chat — non-streaming turn against the main model (mock fallback parity).
//
// The turn runs under callBudget, so a hung upstream cannot hold the caller for
// the whole retry worst case.
func (s *Service) Chat(ctx context.Context, message string, history []HistoryItem, language string) (ChatResult, error) {
	return s.ChatAs(ctx, message, history, language, "")
}

// ChatAs is Chat under a caller-supplied system prompt; an empty systemPrompt is
// exactly Chat.
//
// A persona (migration 067) is bound per session or per conversation, so it
// cannot live on the Service: one Service serves every tenant, and moving the
// configured prompt around the call would race two concurrent turns and leak one
// tenant's persona into the other's reply. The override travels with the turn.
//
// The explicit context cache is skipped for an override. A cachedContents
// resource is registered against the BASE prompt (contextcache.go:
// prefix := s.systemInstruction(language)) and is sent in place of
// systemInstruction, so carrying it into a persona turn would answer as the
// tenant prompt with nothing anywhere reporting an error.
func (s *Service) ChatAs(ctx context.Context, message string, history []HistoryItem, language, systemPrompt string) (ChatResult, error) {
	ctx, cancel := withCallBudget(ctx)
	defer cancel()
	res, err := s.chatWithModel(ctx, message, history, language, "", systemPrompt)
	return res, budgetExceeded(err)
}

// chatWithModel runs one turn. Two distinct outcomes produce a mock reply, and
// they must not be conflated:
//
//   - No API key configured → deliberate mock mode (demos, CI). Returns the
//     template reply with a nil error; this is the documented behaviour.
//   - Configured but the call failed → returns the template reply *and* an
//     error. Callers must surface or retry it.
//
// The second case previously returned a nil error too, which made a live
// Gemini outage indistinguishable from mock mode: the platform pipeline
// persisted the template reply and delivered it to real customers, with no
// alert anywhere. The result is still populated so a caller may choose to
// degrade deliberately, but the error makes that an explicit decision.
func (s *Service) chatWithModel(ctx context.Context, message string, history []HistoryItem, language, model, systemPrompt string) (ChatResult, error) {
	if !s.IsConfigured() {
		return s.chatMock(message, language), nil
	}
	candidates := s.providerCandidates()
	if len(candidates) == 0 {
		// Only reachable when the configured transport is broken: re-read the
		// error so the caller keeps seeing the configuration fault itself rather
		// than a generic failure.
		_, err := s.activeProvider()
		return s.chatMock(message, language), err
	}
	var lastErr error
	for i, prov := range candidates {
		res, status, err := s.chatWithProvider(ctx, prov, message, history, language, model, i == 0, systemPrompt)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if i == len(candidates)-1 || !failoverEligible(status, err) {
			break
		}
		// Moving to the next transport. The customer has seen nothing yet: the
		// mock reply is only returned after every candidate has failed.
		s.failovers.Add(1)
	}
	return s.chatMock(message, language), lastErr
}

// chatWithProvider runs one turn against one transport.
//
// useCache is false for a fallback transport: a cachedContents resource name is a
// property of the transport that registered it (a vertex resource path means
// nothing to studio), so sending it to the other one is a guaranteed 400.
//
// systemPrompt, when non-empty, is a per-turn persona prompt; see ChatAs for why
// it also disables the context cache.
func (s *Service) chatWithProvider(ctx context.Context, prov provider, message string, history []HistoryItem, language, model string, useCache bool, systemPrompt string) (ChatResult, int, error) {
	cfg := s.snapshot()
	if model == "" {
		model = cfg.modelName
	}
	cacheName := ""
	if useCache && systemPrompt == "" {
		cacheName = s.contextCacheFor(ctx, language)
	}
	body := s.buildRequestBody(message, history, language, cacheName)
	if systemPrompt != "" {
		// buildRequestBody keeps its signature because eight tests call it
		// directly; the override lands on the shape it just built.
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": s.systemInstructionFor(language, systemPrompt)}},
		}
	}
	status, text, err := s.postWithProviderRetry(ctx, prov, prov.generateURL(model), body)
	if err == nil && cacheName != "" && contextCacheStale(status, text) {
		// The registered cache expired or was deleted server-side (its own TTL,
		// a cleanup, or another instance replacing it). Forget it and answer
		// uncached rather than failing the turn over an optimisation.
		s.forgetContextCache(language)
		body = s.buildRequestBody(message, history, language, "")
		status, text, err = s.postWithProviderRetry(ctx, prov, prov.generateURL(model), body)
	}
	fast := s.fastModelName()
	if err == nil && (status >= http.StatusInternalServerError || status == http.StatusTooManyRequests) &&
		model == cfg.modelName && fast != model {
		// Degrade to the fast model once on overload — only when it really is
		// another model. fastModelName() falls back to the serving model when
		// GEMINI_FAST_MODEL is unset, so the previous guard re-sent the
		// identical request to the model that had just refused it.
		status, text, err = s.postWithProviderRetry(ctx, prov, prov.generateURL(fast), body)
	}
	if err != nil {
		return ChatResult{}, status, fmt.Errorf("gemini request failed: %w", err)
	}
	if status != http.StatusOK {
		return ChatResult{}, status, fmt.Errorf("gemini returned HTTP %d: %s", status, textutil.Ellipsize(text, 300))
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return ChatResult{}, status, fmt.Errorf("gemini returned unparsable JSON: %w", err)
	}
	return s.resultFromValue(v), status, nil
}

// historyRuneBudget bounds the prompt side of a turn at roughly 8k tokens
// (~24k runes). Long agent replies or RAG-heavy turns could otherwise grow the
// request without limit; the window query caps message COUNT, not size.
const historyRuneBudget = 24000

// TrimHistoryBudget drops the oldest turns until the history fits the budget,
// always keeping the four newest items so the current exchange has context.
func TrimHistoryBudget(items []HistoryItem) []HistoryItem {
	total := 0
	for _, h := range items {
		total += len([]rune(h.Content))
	}
	for total > historyRuneBudget && len(items) > 4 {
		total -= len([]rune(items[0].Content))
		items = items[1:]
	}
	return items
}

// RepairHistoryShape enforces the invariants a history must satisfy before it
// becomes a Gemini `contents` array.
//
// TrimHistoryBudget drops from the front, so it can leave the window starting on
// a model turn — a multi-turn request is expected to open with the user's turn —
// and it can leave turns whose text is blank, which become an empty part. Both
// come back as a 400 that fails the customer's turn, so the shape is restored
// here rather than discovered in production.
//
// This is the part of AstrBot's message-shape repair that applies here
// (astrbot/core/agent/context/truncator.py: never truncate system messages, never
// leave an orphaned tool_call/tool_result). There is no function calling in this
// codebase — grep for functionDeclarations/toolCall returns nothing — so the
// orphan case does not exist; "the shape the provider accepts must survive a
// trim" is what does.
//
// Consecutive same-role turns are deliberately NOT merged. An "agent" turn is
// sent as user-role with a marker (see buildRequestBody) so the model can tell a
// staff reply from its own; merging a customer turn with the agent turn that
// answered it would erase exactly that distinction.
func RepairHistoryShape(items []HistoryItem) []HistoryItem {
	out := make([]HistoryItem, 0, len(items))
	for _, h := range items {
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		out = append(out, h)
	}
	for len(out) > 0 && !isUserRole(out[0].Role) {
		out = out[1:]
	}
	return out
}

// isUserRole reports whether a stored role is sent to Gemini as the user role.
func isUserRole(role string) bool { return role == "user" || role == "agent" }

// systemInstruction — the stable prompt prefix for one language. The uncached
// request body and the explicit context cache are both built from this single
// function, so a cache can never be registered for a prefix the request does
// not actually send.
func (s *Service) systemInstruction(language string) string {
	system := s.snapshot().systemPrompt
	if system == "" {
		system = DefaultSystemPrompt
	}
	return system + "\n\n[Language Preference] " + LanguageLabel(language)
}

// systemInstructionFor is systemInstruction with a per-turn persona override. An
// empty override is the configured prompt, so a turn with no persona is
// byte-identical to what this Service sent before personas existed.
func (s *Service) systemInstructionFor(language, systemPrompt string) string {
	if systemPrompt == "" {
		return s.systemInstruction(language)
	}
	return systemPrompt + "\n\n[Language Preference] " + LanguageLabel(language)
}

// generationConfig builds the config block for one chat request.
//
// thinkingBudget (GEMINI_THINKING_BUDGET) is sent only when set: it caps the
// model's internal reasoning tokens, which are billed at the OUTPUT rate
// (usageFromValue folds them into the completion count) and dominate the tail
// of reply latency. Unset keeps the model's own default.
//
// temperature is sent only when non-nil, for the same reason plus one more: the
// platform default (1.0) is what a Gemini 3 deployment SHOULD use — Google
// recommends keeping it, warning that lower values can cause looping or degraded
// performance — so a deployment that never touched the knob must not suddenly
// start sending a value because this code learned how to.
func generationConfig(maxTokens int, temperature *float64) map[string]any {
	cfg := map[string]any{"maxOutputTokens": maxTokens}
	if temperature != nil {
		cfg["temperature"] = *temperature
	}
	if b := thinkingBudget(); b >= 0 {
		cfg["thinkingConfig"] = map[string]any{"thinkingBudget": b}
	}
	return cfg
}

// thinkingBudget returns GEMINI_THINKING_BUDGET, or -1 to send nothing (the
// model's default). 0 disables thinking outright: cheaper and faster, at the
// cost of accuracy on questions that need reasoning.
func thinkingBudget() int {
	if v := strings.TrimSpace(os.Getenv("GEMINI_THINKING_BUDGET")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

// embedBudget is the ceiling for the query-embedding hop
// (GEMINI_EMBED_BUDGET_MS, default 5000). Read per call, like the other knobs
// in this file, so a deploy can re-tune without a rebuild.
//
// WHY THIS ONE NEEDS A BUDGET OF ITS OWN
// Every other auxiliary call (RerankChunks 8s, RewriteSearchQuery 6s,
// GenerateFastMax explicit) passes a deadline into GenerateFast before it
// spends anything. The query embedding is the exception: it goes straight to
// postWithRetry, which may make THREE attempts against the client's 60s
// timeout. A wobbling embed endpoint therefore stalled the reply path for up
// to ~3 minutes — past every caller's own patience — and the caller then
// answered ungrounded anyway. Failing at 5s costs one retrieval leg and lands
// in the "no ground, answer anyway" degradation the callers already implement.
func embedBudget() time.Duration {
	if v := strings.TrimSpace(os.Getenv("GEMINI_EMBED_BUDGET_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 5 * time.Second
}

// buildRequestBody assembles one generateContent request. cachedContent, when
// non-empty, names a registered context cache that already holds the system
// instruction — the API rejects a request that carries both, so the
// instruction is omitted in that case.
func (s *Service) buildRequestBody(message string, history []HistoryItem, language, cachedContent string) map[string]any {
	// Single choke point for the request shape: whatever a caller assembled (and
	// whatever TrimHistoryBudget left behind) is repaired here, so no path can
	// send a leading model turn or an empty part.
	history = RepairHistoryShape(history)
	contents := make([]map[string]any, 0, len(history)+1)
	for _, h := range history {
		role := h.Role
		content := h.Content
		switch role {
		case "user":
			// customer turn, pass through
		case "agent":
			// Human-agent turns stay user-role but carry a marker: mapped to
			// plain "model" the AI could not tell staff answers from its own
			// and would re-promise or contradict what a human already said.
			role = "user"
			content = "[Human agent reply] " + content
		default:
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]any{{"text": content}},
		})
	}
	contents = append(contents, map[string]any{
		"role":  "user",
		"parts": []map[string]any{{"text": message}},
	})
	snap := s.snapshot()
	body := map[string]any{
		"contents":         contents,
		"generationConfig": generationConfig(snap.maxTokens, snap.temperature),
	}
	if cachedContent != "" {
		body["cachedContent"] = cachedContent
	} else {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": s.systemInstruction(language)}},
		}
	}
	return body
}

func (s *Service) resultFromValue(v map[string]any) ChatResult {
	// SanitizeReply, not the raw extraction: this is the single point where a
	// provider response becomes the text a customer reads (chatWithModel and every
	// ChatStream fallback land here), and a reply that reaches a chat bubble with
	// invisible characters still inside it is a reply nobody can copy, search or
	// wrap. See khmer.go for what is canonicalised and why.
	res := ChatResult{Reply: SanitizeReply(ExtractTextFromValue(v))}
	res.PromptTokens, res.OutputTokens, res.CachedTokens = usageFromValue(v)
	return res
}

// usageFromValue extracts (prompt, completion, cached) token counts from one
// generateContent / stream chunk response body.
//
// completion folds in thoughtsTokenCount: thinking tokens are billed at the
// output rate but ride in their own field, so a thinking model's real output
// spend was invisible in token_usage — and the spend ceiling documented in
// docs/GEMINI-RATE-LIMIT.md was reverse-engineered from those understated
// numbers. Folding them in makes cost_estimate track the invoice.
func usageFromValue(v map[string]any) (int, int, int) {
	var prompt, completion, cached int
	usage, _ := v["usageMetadata"].(map[string]any)
	if usage != nil {
		if n, ok := usage["promptTokenCount"].(float64); ok {
			prompt = int(n)
		}
		if n, ok := usage["candidatesTokenCount"].(float64); ok {
			completion = int(n)
		}
		if n, ok := usage["thoughtsTokenCount"].(float64); ok {
			completion += int(n)
		}
		if n, ok := usage["cachedContentTokenCount"].(float64); ok {
			cached = int(n)
		}
	}
	return prompt, completion, cached
}

// ChatStream runs a turn with token-level streaming: onToken is invoked for
// every text delta as it arrives. On any transport/API failure it degrades to
// the non-streaming path (the reply is still delivered, just not incrementally).
//
// It shares Chat's per-call budget: an upstream that accepts the connection and
// then goes silent is exactly the case a streaming client never notices on its
// own.
func (s *Service) ChatStream(ctx context.Context, message string, history []HistoryItem, language string, onToken func(string)) (ChatResult, error) {
	ctx, cancel := withCallBudget(ctx)
	defer cancel()
	res, err := s.chatStream(ctx, message, history, language, onToken)
	return res, budgetExceeded(err)
}

// chatStream is the budget-free body. ChatStream owns the budget so there is
// exactly one place that applies it.
func (s *Service) chatStream(ctx context.Context, message string, history []HistoryItem, language string, onToken func(string)) (ChatResult, error) {
	if !s.IsConfigured() {
		res := s.chatMock(message, language)
		onToken(res.Reply)
		return res, nil
	}
	// Resolve the transport before building anything: this path does not travel
	// through postWithRetry, so it is the one place a configuration error has to
	// be checked explicitly. It degrades exactly like any other streaming
	// failure — the non-streaming path returns the error.
	prov, perr := s.activeProvider()
	if perr != nil {
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	body := s.buildRequestBody(message, history, language, s.contextCacheFor(ctx, language))
	payload, err := json.Marshal(body)
	if err != nil {
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	// Read the serving config through the mutex: an admin saving a new model
	// config calls Reload, which rewrites modelName/client under the lock.
	cfg := s.snapshot()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.streamURL(cfg.modelName), bytes.NewReader(payload))
	if err != nil {
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	req.Header.Set("Content-Type", "application/json")
	// Credential selection is shared with postWithRetry (see provider.authorize).
	if err := prov.authorize(ctx, req, cfg.apiKey); err != nil {
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	resp, err := cfg.client.Do(req)
	if err != nil {
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return s.chatWithModel(ctx, message, history, language, "", "")
	}

	var full strings.Builder
	res := ChatResult{}
	emitted := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if text := ExtractTextFromValue(chunk); text != "" {
			full.WriteString(text)
			emitted = true
			// The streamed tokens are what the customer watches arrive, so they get
			// the character-level rules immediately; the layout rules wait for the
			// assembled reply below, because a chunk boundary can fall in the middle
			// of the whitespace between two words.
			onToken(SanitizeReplyChunk(text))
		}
		if p, c, cached := usageFromValue(chunk); p+c+cached > 0 {
			res.PromptTokens, res.OutputTokens, res.CachedTokens = p, c, cached
		}
	}
	// A stream that dies mid-answer used to be indistinguishable from a complete
	// one — scanner.Err() was never consulted, so a truncated reply was delivered
	// and every caller believed the turn had succeeded.
	if serr := scanner.Err(); serr != nil {
		if emitted {
			// Some of the answer already reached the customer. Restarting the turn
			// on another transport or model would put a second, contradictory
			// answer on top of the first, which is exactly what AstrBot's failover
			// avoids by switching only while no streaming output exists
			// (tool_loop_agent_runner.py:599-609). Return the partial reply with
			// the error so the caller decides.
			res.Reply = SanitizeReply(full.String())
			return res, fmt.Errorf("gemini stream interrupted after %d chars: %w", full.Len(), serr)
		}
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	res.Reply = full.String()
	if res.Reply == "" {
		return s.chatWithModel(ctx, message, history, language, "", "")
	}
	res.Reply = SanitizeReply(res.Reply)
	return res, nil
}

// DescribeImage — Gemini multimodal image understanding for customer photos.
// Returns a concise description of what the image contains (same-script text
// where legible). Mock mode returns a fixed Khmer notice so the flow is testable.
func (s *Service) DescribeImage(ctx context.Context, data []byte, mimeType string) (string, error) {
	if !s.IsConfigured() {
		return "[图片内容转述] (mock) 客户发送了一张图片，未配置 GEMINI_API_KEY 无法识别。", nil
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	body := map[string]any{
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": "Describe this customer-service photo in 1-2 sentences for an AI assistant that cannot see it: state what is shown, transcribe any clearly visible text (prices, order numbers, error messages) in its original language, and note the likely customer intent. Reply with the description only — no preamble."},
				{"inlineData": map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)}},
			},
		}},
		"generationConfig": map[string]any{"maxOutputTokens": 512},
	}
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(s.fastModelName()), body)
	if err != nil {
		return "", fmt.Errorf("describe image request: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("describe image failed (%d): %s", status, textutil.Ellipsize(text, 300))
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", fmt.Errorf("describe image: invalid response")
	}
	auxPrompt, auxCompletion, auxCached := usageFromValue(v)
	reportAuxUsage(ctx, s.fastModelName(), auxPrompt, auxCompletion, auxCached)
	return strings.TrimSpace(ExtractTextFromValue(v)), nil
}

// ExtractDocumentText transcribes a document verbatim (image or PDF). It is
// the OCR fallback for knowledge ingestion when native text extraction fails:
// Khmer PDFs whose fonts carry no ToUnicode map, or photos of printed
// material. Returns the transcribed text or an error; the caller decides
// whether OCR text is better than what it already has.
func (s *Service) ExtractDocumentText(ctx context.Context, data []byte, mimeType string) (string, error) {
	if !s.IsConfigured() {
		return "", fmt.Errorf("Gemini not configured; document OCR unavailable")
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	body := map[string]any{
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": "Transcribe ALL text in this document verbatim, preserving the original language and reading order. Khmer text must stay Khmer script - never transliterate and never translate. Do not describe the image, do not add commentary. Output only the transcribed text, with blank lines between blocks."},
				{"inlineData": map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)}},
			},
		}},
		"generationConfig": map[string]any{"temperature": 0.0, "maxOutputTokens": 8192},
	}
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(s.fastModelName()), body)
	if err != nil {
		return "", fmt.Errorf("document OCR request: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("document OCR failed (%d): %s", status, textutil.Ellipsize(text, 300))
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", fmt.Errorf("document OCR: invalid response")
	}
	auxPrompt, auxCompletion, auxCached := usageFromValue(v)
	reportAuxUsage(ctx, s.fastModelName(), auxPrompt, auxCompletion, auxCached)
	out := strings.TrimSpace(ExtractTextFromValue(v))
	if out == "" {
		return "", fmt.Errorf("document OCR returned no text")
	}
	return out, nil
}

// ttsModel / ttsVoice — speech-synthesis settings (env-overridable).
//
// The default below is a STUDIO-only name: no preview TTS model exists on the
// platform (probed across the Asian regions), so a vertex deployment must leave
// TTS_ENABLED=false — which is the shipped default. There is deliberately no
// pinned replacement here: inventing one would turn "TTS is off" into "TTS is
// on and 404s on every reply", and the measured answer is that this platform
// has nothing to point at yet.
func TTSModel() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_TTS_MODEL")); v != "" {
		return v
	}
	return "gemini-2.5-flash-preview-tts"
}

func TTSVoice() string {
	if v := strings.TrimSpace(os.Getenv("TTS_VOICE")); v != "" {
		return v
	}
	return "Kore"
}

// SynthesizeSpeech renders text to WAV audio via the Gemini TTS model. The
// model returns raw PCM (24kHz 16-bit mono); we wrap it in a WAV header so
// Telegram/Messenger can play it from a URL. Returns ("", nil) when TTS is
// not usable (mock mode or any failure) — callers treat that as "no audio".
func (s *Service) SynthesizeSpeech(ctx context.Context, text string) ([]byte, error) {
	if !s.IsConfigured() || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	body := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": text}},
		}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{
					"prebuiltVoiceConfig": map[string]any{"voiceName": TTSVoice()},
				},
			},
		},
	}
	// TTS stays on the provider's generateContent endpoint like every other
	// model call; it is switched off at the call sites (TTS_ENABLED), not here,
	// so the studio request below is byte-for-byte the one that shipped.
	target := s.generateURLFor(TTSModel())
	status, respText, err := s.postWithRetry(ctx, target, body)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("tts failed (%d): %v", status, err)
	}
	var v map[string]any
	if json.Unmarshal([]byte(respText), &v) != nil {
		return nil, fmt.Errorf("tts: invalid response")
	}
	candidates, _ := v["candidates"].([]any)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("tts: no candidates")
	}
	first, _ := candidates[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	for _, p := range parts {
		pm, _ := p.(map[string]any)
		inline, _ := pm["inlineData"].(map[string]any)
		if inline == nil {
			continue
		}
		b64, _ := inline["data"].(string)
		pcm, derr := base64.StdEncoding.DecodeString(b64)
		if derr != nil || len(pcm) == 0 {
			continue
		}
		return wavFromPCM(pcm, 24000), nil
	}
	return nil, fmt.Errorf("tts: no audio part")
}

// wavFromPCM wraps 16-bit mono PCM in a canonical WAV header.
func wavFromPCM(pcm []byte, sampleRate int) []byte {
	dataLen := uint32(len(pcm))
	buf := make([]byte, 44+len(pcm))
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], 36+dataLen)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1) // mono
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:34], 2) // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataLen)
	copy(buf[44:], pcm)
	return buf
}

func (s *Service) chatMock(message, language string) ChatResult {
	preview := textutil.Ellipsize(message, 50)
	reply := "(mock) អរគុណសម្រាប់សំណួររបស់អ្នកអំពី \"" + preview + "\" — set GEMINI_API_KEY to enable real AI."
	switch language {
	case "en":
		reply = "(mock) Thank you for your question about \"" + preview + "\" — set GEMINI_API_KEY to enable real AI."
	case "zh":
		reply = "(mock) 感谢你的提问「" + preview + "」—— 配置 GEMINI_API_KEY 后启用真实 AI。"
	}
	return ChatResult{Reply: SanitizeReply(reply), PromptTokens: len(message) / 4, UsedMock: true}
}

// GenerateFast runs an auxiliary prompt on the fast model with a timeout.
// Returns ("", false) on any failure/mock — callers degrade gracefully.
func (s *Service) GenerateFast(ctx context.Context, prompt string, timeout time.Duration) (string, bool) {
	return s.GenerateFastMax(ctx, prompt, timeout, 2048)
}

// AuxUsageObserver, when set, receives token counts for every auxiliary model
// call. The main chat path records through usage.Record directly — it has the
// session in hand — but the auxiliary calls (ingest-time compile, translation,
// rerank, query rewrite, turn classification, transcription, image
// description, TTS) happen deep inside packages with no notion of a tenant.
// Those were entirely unmetered, which hid the single largest LLM expense
// (the compile's 4096-token call) from every cost dashboard.
//
// Callers attribute the spend by tagging ctx with usage.WithUser; an observer
// that finds no user simply skips the row.
var AuxUsageObserver func(ctx context.Context, model string, prompt, completion, cached int)

func reportAuxUsage(ctx context.Context, model string, prompt, completion, cached int) {
	if AuxUsageObserver != nil && prompt+completion > 0 {
		AuxUsageObserver(ctx, model, prompt, completion, cached)
	}
}

// GenerateFastMax is GenerateFast with an explicit output-token budget for
// prompts that emit longer structured payloads (e.g. the ingest-time compile,
// whose JSON is truncated into invalid syntax at the default 2048).
func (s *Service) GenerateFastMax(ctx context.Context, prompt string, timeout time.Duration, maxOutputTokens int) (string, bool) {
	if !s.IsConfigured() {
		return "", false
	}
	if maxOutputTokens <= 0 {
		maxOutputTokens = 2048
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": prompt}},
		}},
		"generationConfig": map[string]any{"temperature": 0.0, "maxOutputTokens": maxOutputTokens},
	}
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(s.fastModelName()), body)
	if err != nil || status != http.StatusOK {
		return "", false
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", false
	}
	// Bill the call even when the reply turns out unusable: the tokens were
	// spent either way.
	auxPrompt, auxCompletion, auxCached := usageFromValue(v)
	reportAuxUsage(ctx, s.fastModelName(), auxPrompt, auxCompletion, auxCached)
	reply := strings.TrimSpace(StripSourceMarkers(ExtractTextFromValue(v)))
	if reply == "" {
		return "", false
	}
	return reply, true
}

// RewriteSearchQuery resolves coreference into one standalone query.
func (s *Service) RewriteSearchQuery(ctx context.Context, message string, history []HistoryItem) *string {
	if len(history) == 0 {
		return nil
	}
	var convo strings.Builder
	start := len(history) - 6
	if start < 0 {
		start = 0
	}
	for _, h := range history[start:] {
		fmt.Fprintf(&convo, "%s: %s\n", h.Role, textutil.Ellipsize(h.Content, 160))
	}
	prompt := "Rewrite the user's latest message as ONE standalone web-search query. " +
		"Resolve pronouns and vague references (它/那个/this/it/that...) using the conversation below. " +
		"Keep the user's language. Reply with ONLY the query text.\n\n" +
		"Conversation:\n" + convo.String() + "Latest message: " + message
	reply, ok := s.GenerateFast(ctx, prompt, 6*time.Second)
	if !ok {
		return nil
	}
	reply = strings.TrimSpace(strings.Trim(reply, "\""))
	if reply == "" || len(reply) > 300 || strings.Contains(reply, "\n") {
		return nil
	}
	return &reply
}

// TurnVerdict is the classifier's decision for one customer-service turn.
type TurnVerdict struct {
	Sentiment  string  `json:"sentiment"`
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
	Escalate   bool    `json:"escalate"`
}

// JudgeTurn — intent-aware audit of one customer-service turn. The escalation
// bar is deliberately broad: anything a human must own (complaints, refunds,
// delivery problems, customization, bulk/negotiation, payment/legal terms,
// anger, or an answer that clearly missed) sets Escalate=true.
func (s *Service) JudgeTurn(ctx context.Context, customerMsg, reply string, hasMatch bool) (TurnVerdict, bool) {
	prompt := "You audit one customer-service turn for a building-materials store. Reply with ONLY a JSON object:\n" +
		`{"sentiment":"positive|neutral|negative","intent":"one of: question, complaint, refund, order_status, price, booking, customization, bulk_order, delivery, payment, legal, small_talk, other","confidence":0.0-1.0,"escalate":true|false}` + "\n" +
		"Set escalate=true when ANY of these apply:\n" +
		"- The customer is angry, rude, or frustrated, or repeats the same complaint.\n" +
		"- Refund / return / money-back demands, even if the assistant quoted a policy.\n" +
		"- Delivery problems: late, damaged, wrong items, changing the address after ordering.\n" +
		"- Custom sizing or special specifications that are not in the catalog (sales must confirm).\n" +
		"- Bulk / wholesale orders, price negotiation, or asking for a better price.\n" +
		"- Contract, invoice, payment terms, or legal threats.\n" +
		"- The answer clearly did not resolve the question, or the customer says it is wrong.\n" +
		"- The customer explicitly asks for a human.\n" +
		"escalate=false only for well-answered product questions, greetings, and small talk.\n\n" +
		"[KB grounded]=" + fmt.Sprintf("%t", hasMatch) + "\n" +
		"Customer: " + textutil.Ellipsize(customerMsg, 600) + "\n" +
		"Assistant: " + textutil.Ellipsize(reply, 600)

	out, ok := s.GenerateFast(ctx, prompt, JudgeTurnBudget)
	if !ok {
		return TurnVerdict{}, false
	}
	trimmed := strings.TrimSpace(out)
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var v TurnVerdict
	if json.Unmarshal([]byte(trimmed), &v) != nil {
		return TurnVerdict{}, false
	}
	switch v.Sentiment {
	case "positive", "negative":
	default:
		v.Sentiment = "neutral"
	}
	// Intent is enum-enforced (mirroring sentiment above), not merely
	// length-capped: the value persists into sessions.intent and is exported
	// to CSV reports, so a steered model emitting a formula-leading string
	// ("=...", "+...") must never survive validation.
	switch v.Intent {
	case "question", "complaint", "refund", "order_status", "price", "booking",
		"customization", "bulk_order", "delivery", "payment", "legal", "small_talk", "other":
	default:
		v.Intent = "other"
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		v.Confidence = 0.5
	}
	return v, true
}

// RerankChunks scores fused candidates 0-10 for question relevance. Nil on
// any failure — callers keep the RRF ordering.
func (s *Service) RerankChunks(ctx context.Context, query string, chunks []string) []float32 {
	if len(chunks) == 0 || len(chunks) > 20 {
		return nil
	}
	var body strings.Builder
	for i, chunk := range chunks {
		fmt.Fprintf(&body, "[%d] %s\n\n", i+1, textutil.Ellipsize(chunk, 400))
	}
	prompt := "Rate how relevant each numbered passage is to the query on a 0-10 integer " +
		"scale (10 = answers it directly). Reply with ONLY a JSON array like " +
		"[{\"i\":1,\"score\":7},{\"i\":2,\"score\":0}] — one entry per passage.\n\n" +
		"Query: " + query + "\n\n" + body.String()
	reply, ok := s.GenerateFast(ctx, prompt, 8*time.Second)
	if !ok {
		return nil
	}
	return parseScoreArray(reply, len(chunks))
}

// GenerateEmbedding — 768-dim vector for document indexing.
func (s *Service) GenerateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return s.embed(ctx, text, "RETRIEVAL_DOCUMENT")
}

// GenerateEmbeddings — batch document embeddings for callers that hold only
// texts (no titles). See GenerateDocumentEmbeddings for the indexing path.
func (s *Service) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	return s.generateEmbeddings(ctx, s.conditionedTexts(texts, "RETRIEVAL_DOCUMENT"))
}

// GenerateDocumentEmbeddings — the indexing path: embeds each chunk with its
// DOCUMENT's title where the model supports it.
//
// WHY TITLES: GE2's documented document idiom is "title: <title> | text: <text>",
// and it is not decorative. Measured 2026-10-09 on the production Khmer corpus
// (602 chunks, 126 generated questions): with the field filled by a real title
// recall@5 = 0.563 / MRR 0.384; with the documented placeholder ("title: none")
// the SAME model scored 0.492 / 0.361 — worse than the model it is replacing.
// The placeholder is not neutral, it is actively harmful on this corpus, so the
// title is passed through rather than fabricated.
//
// GE1 is unaffected: it has no such conditioning, so titles are ignored and its
// vectors stay byte-identical to the ones already in the column (which is what
// makes the switch reversible without a re-embed in the other direction).
//
// The return contract matches GenerateEmbeddings: n vectors in input order.
func (s *Service) GenerateDocumentEmbeddings(ctx context.Context, titles, texts []string) ([][]float32, error) {
	conditioned := make([]string, len(texts))
	for i, text := range texts {
		title := ""
		if i < len(titles) {
			title = titles[i]
		}
		conditioned[i] = s.conditionDocument(title, text)
	}
	return s.generateEmbeddings(ctx, conditioned)
}

// generateEmbeddings dispatches ALREADY-CONDITIONED texts to the batch path its
// model supports; see GenerateEmbeddings for the contract.
//
// GE1 uses its native batch entry point (up to 100 texts per call; falls back
// to per-text calls on batch failure). GE2 has NO batch entry point on either
// platform (measured 2026-10-09: :batchEmbedContents is an HTML 404 on the
// platform route, and multi-instance :predict 404s for the model itself), so
// its documents go one text per call, bounded by embeddingBatchConcurrency.
func (s *Service) generateEmbeddings(ctx context.Context, conditioned []string) ([][]float32, error) {
	if !s.IsConfigured() {
		mock := MockEmbedding()
		out := make([][]float32, len(conditioned))
		for i := range out {
			out[i] = mock
		}
		return out, nil
	}
	if !embeddingShapeFor(s.EmbeddingModel()).nativeBatch {
		return s.embedAllConcurrent(ctx, conditioned)
	}
	out := make([][]float32, 0, len(conditioned))
	for start := 0; start < len(conditioned); start += 100 {
		end := start + 100
		if end > len(conditioned) {
			end = len(conditioned)
		}
		batch := conditioned[start:end]
		vecs, err := s.embedBatch(ctx, batch)
		if err != nil {
			// Fall back to per-text embedding so one bad batch never blocks
			// indexing. The fallback costs one round trip per text, which is why
			// it stays a fallback; GE2 is that path by design. The texts are
			// already conditioned, so the fallback embeds them as they are.
			for i, text := range batch {
				vec, err := s.embedConditioned(ctx, text, "RETRIEVAL_DOCUMENT")
				if err != nil {
					return nil, fmt.Errorf("text %d: %w", start+i, err)
				}
				vecs = append(vecs, vec)
			}
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// embedAllConcurrent embeds one text per call with at most
// embeddingBatchConcurrency in flight, preserving input order.
//
// This is the batching story for models with no batch API. Order preservation
// is part of the contract: indexDocument pairs embeddings[i] with chunks[i] by
// index, so a reordered or dropped result would silently mismatch every chunk
// of the document with its vector. The first error cancels the batch — a
// partially embedded document must fail, not be stored half-vectorised.
//
// Texts arrive ALREADY conditioned; this function adds no prefix of its own.
func (s *Service) embedAllConcurrent(ctx context.Context, conditioned []string) ([][]float32, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([][]float32, len(conditioned))
	sem := make(chan struct{}, embeddingBatchConcurrency)
	var wg sync.WaitGroup
	var firstErrOnce sync.Once
	var firstErr error
	for i := range conditioned {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			vec, err := s.embedConditioned(ctx, conditioned[i], "RETRIEVAL_DOCUMENT")
			if err != nil {
				firstErrOnce.Do(func() {
					firstErr = fmt.Errorf("text %d: %w", i, err)
					cancel()
				})
				return
			}
			out[i] = vec
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		// A cancellation with no reported per-text error came from the caller's
		// own context dying mid-batch; surface it rather than the misleading
		// "vector was not produced" the nil scan below would invent.
		return nil, err
	}
	for i, vec := range out {
		if vec == nil {
			return nil, fmt.Errorf("embedding %d of %d was not produced", i, len(conditioned))
		}
	}
	return out, nil
}

func (s *Service) embedBatch(ctx context.Context, conditioned []string) ([][]float32, error) {
	// The body differs per provider (studio: :batchEmbedContents with a
	// `requests` array; vertex: one :predict carrying `instances`) — see
	// provider.embedBatchBody. The return contract does not: n vectors of
	// embeddingVectorDimension, in input order. Texts arrive already
	// conditioned, exactly as they will be sent.
	model := s.EmbeddingModel()
	// GE2 has no batch entry point on either platform; a direct caller that
	// missed generateEmbeddings' dispatch gets the concurrent path rather than a
	// 404-shaped failure.
	if !embeddingShapeFor(model).nativeBatch {
		return s.embedAllConcurrent(ctx, conditioned)
	}
	prov, err := s.activeProvider()
	if err != nil {
		return nil, err
	}
	status, respText, err := s.postWithRetry(ctx, prov.batchEmbedURL(model), prov.embedBatchBody(model, conditioned))
	if err != nil {
		return nil, fmt.Errorf("batch embed request: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("batch embed failed (%d): %s", status, textutil.Ellipsize(respText, 300))
	}
	// Embedded spend used to be invisible: the auxiliary observer is fed by the
	// generative calls only. Reported here, before the provider split, because the
	// provider billed this call the moment it answered 200 — a later parse failure
	// does not un-bill it. Token count is estimated from the text (the embedding API
	// returns a vector, not usageMetadata), the same 4-chars-per-token heuristic the
	// mock path uses; the rate is usage.embeddingPer1M, not the chat input rate.
	total := 0
	for _, t := range conditioned {
		total += len(t)
	}
	reportAuxUsage(ctx, model, total/4, 0, 0)
	if prov.kind == providerVertex {
		return parseVertexPredictBatch(respText, len(conditioned), model)
	}
	var v struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if json.Unmarshal([]byte(respText), &v) != nil || len(v.Embeddings) != len(conditioned) {
		return nil, fmt.Errorf("batch embed: invalid response")
	}
	out := make([][]float32, len(v.Embeddings))
	for i, e := range v.Embeddings {
		if len(e.Values) == 0 {
			return nil, fmt.Errorf("batch embed: empty vector at %d", i)
		}
		out[i] = e.Values
	}
	return out, nil
}

// conditionedTexts applies conditionText to each text in order.
func (s *Service) conditionedTexts(texts []string, taskType string) []string {
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = s.conditionText(t, taskType)
	}
	return out
}

// GenerateQueryEmbedding — query-time vector, cached 5 minutes.
func (s *Service) GenerateQueryEmbedding(ctx context.Context, text string) ([]float32, error) {
	// The cache key carries BOTH the model and the conditioned text: a cache
	// keyed by raw text alone would answer a GE2 query with a GE1 vector (or a
	// prefixed query with the plain text's vector) for five minutes after any
	// model or prefix switch — the worst kind of stale, because every retrieval
	// still "succeeds".
	key := s.EmbeddingModel() + "\x00" + strings.ToLower(s.conditionText(text, "RETRIEVAL_QUERY"))
	s.mu.Lock()
	if entry, ok := s.embedCache[key]; ok && time.Since(entry.at) < 5*time.Minute {
		s.mu.Unlock()
		return entry.vec, nil
	}
	s.mu.Unlock()
	// The budget covers this hop only — see embedBudget. It is placed after the
	// cache lookup on purpose: a cache hit must not be charged the deadline
	// (or, worse, be failed by a context that expired while it waited).
	embedCtx, cancel := context.WithTimeout(ctx, embedBudget())
	defer cancel()
	vec, err := s.embed(embedCtx, text, "RETRIEVAL_QUERY")
	if err != nil {
		return nil, embedBudgetExceeded(err)
	}
	s.mu.Lock()
	if len(s.embedCache) > 200 {
		s.embedCache = make(map[string]embedCacheEntry)
	}
	s.embedCache[key] = embedCacheEntry{at: time.Now(), vec: vec}
	s.mu.Unlock()
	return vec, nil
}

// embed conditions raw text and embeds it — the single-text entry point for
// callers that hold only text (queries, probes, similarity samples).
func (s *Service) embed(ctx context.Context, text, taskType string) ([]float32, error) {
	return s.embedConditioned(ctx, s.conditionText(text, taskType), taskType)
}

// embedConditioned embeds text that is already in its final wire form: the ONE
// place a single embedding is requested, its billed usage is reported, and its
// width is validated.
func (s *Service) embedConditioned(ctx context.Context, text, taskType string) ([]float32, error) {
	if !s.IsConfigured() {
		return MockEmbedding(), nil
	}
	model := s.EmbeddingModel()
	prov, err := s.activeProvider()
	if err != nil {
		return nil, err
	}
	// The body shape (instances/:predict vs content/:embedContent) lives behind
	// the provider — see provider.embedBody. taskType reaches the studio-GE1
	// body, the one transport that conditions on the field.
	status, respText, err := s.postWithRetry(ctx, prov.embedURL(model), prov.embedBody(model, text, taskType))
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("embed failed (%d): %s", status, textutil.Ellipsize(respText, 300))
	}
	// Single-document/query embedding: same reasoning as embedBatch above —
	// reported before validation so a billed call is always recorded.
	reportAuxUsage(ctx, model, len(text)/4, 0, 0)
	var v map[string]any
	if json.Unmarshal([]byte(respText), &v) != nil {
		return nil, fmt.Errorf("embed: invalid response")
	}
	values := prov.embeddingValues(v, 0, model)
	if len(values) != embeddingVectorDimension {
		// Checked here rather than at the pgvector INSERT because a wrong-width
		// vector is not an error upstream: the call succeeds, and the mismatch
		// only surfaces later as a failed insert or as silently bad search
		// results. See embeddingWidthError.
		return nil, embeddingWidthError(prov, model, len(values))
	}
	out := make([]float32, len(values))
	for i, n := range values {
		out[i] = float32(n)
	}
	return out, nil
}

// DetectLanguage reads the message script: CJK → zh, Khmer → km, Latin → en.
// "" when no clear script (digits/emoji only).
func DetectLanguage(message string) string {
	for _, c := range message {
		if (c >= 0x4e00 && c <= 0x9fff) || (c >= 0x3400 && c <= 0x4dbf) ||
			(c >= 0x3000 && c <= 0x303f) || (c >= 0xff00 && c <= 0xffef) {
			return "zh"
		}
	}
	for _, c := range message {
		if c >= 0x1780 && c <= 0x17ff {
			return "km"
		}
	}
	for _, c := range message {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return "en"
		}
	}
	return ""
}

// TranscribeAudio — verbatim speech-to-text via Gemini multimodal. Returns the
// transcript (mock returns a fixed Khmer phrase so the flow is testable).
//
// langHint pins the spoken language when the caller genuinely knows it; empty
// means "detect it". Callers must NOT pass the merchant's UI language here —
// the merchant reads Chinese while their customers speak Khmer, so pinning it
// would force Khmer speech into the wrong script (observed: Amharic).
func (s *Service) TranscribeAudio(ctx context.Context, audio []byte, mimeType, langHint string) (string, error) {
	transcript, err := s.transcribeOnce(ctx, audio, mimeType, transcribePrompt(langHint))
	if err != nil {
		return "", err
	}
	// Khmer voice notes occasionally decode into an unrelated script. When we
	// were not pinned, retry once with Khmer explicit rather than storing
	// gibberish the AI cannot ground on.
	if langHint == "" && hasUnexpectedScript(transcript) {
		if retry, rerr := s.transcribeOnce(ctx, audio, mimeType, transcribePrompt("km")); rerr == nil && strings.TrimSpace(retry) != "" {
			return retry, nil
		}
	}
	return transcript, nil
}

func (s *Service) transcribeOnce(ctx context.Context, audio []byte, mimeType, prompt string) (string, error) {
	if !s.IsConfigured() {
		return "ផលិតផលនេះតម្លៃប៉ុន្មាន និងមានសេវាកម្មអ្វីខ្លះ?", nil
	}
	if mimeType == "" {
		mimeType = "audio/ogg"
	}
	// The endpoint rejects an unrecognised container name with a generic 400, and
	// the caller only ever shows "语音转写失败" — so the label is canonicalised
	// here, at the one place that talks to the vendor. See audio_mime.go for the
	// measured aliases (application/ogg, the default type for .m4a) and the real
	// customer voice note that was lost to this.
	mimeType = CanonicalAudioMime(mimeType, audio)
	body := map[string]any{
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": prompt},
				{"inlineData": map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(audio)}},
			},
		}},
	}
	cfg := s.snapshot()
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(cfg.modelName), body)
	if err != nil {
		return "", fmt.Errorf("transcribe request: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("transcribe failed (%d): %s", status, textutil.Ellipsize(text, 300))
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", fmt.Errorf("transcribe: invalid response")
	}
	auxPrompt, auxCompletion, auxCached := usageFromValue(v)
	reportAuxUsage(ctx, s.fastModelName(), auxPrompt, auxCompletion, auxCached)
	transcript := strings.TrimSpace(ExtractTextFromValue(v))
	if isTranscriptionRefusal(transcript) {
		return "", fmt.Errorf("transcribe: model could not decode audio (mime=%s): %s", mimeType, textutil.Ellipsize(transcript, 160))
	}
	return transcript, nil
}

// transcribePrompt builds the speech-to-text instruction. The platform serves
// Khmer, English and Chinese speakers, so the default is an explicit candidate
// list plus "use the script you actually hear" — pinning a single language is
// what produced wrong-script transcripts.
func transcribePrompt(lang string) string {
	if label := speechLanguageLabel(lang); label != "" {
		return "The audio is spoken in " + label + ". Transcribe it verbatim in " + label + " script ONLY — no translation, no labels, no commentary, no quotes."
	}
	return "Transcribe this audio verbatim. The speaker is most likely speaking Khmer, English, or Chinese — " +
		"use whichever of those languages you actually hear and write it in that language's OWN script. " +
		"Never translate, never transliterate into another script, and never substitute a different language " +
		"(for example do not render Khmer speech in Amharic or any other script). " +
		"Output ONLY the transcribed text — no labels, no commentary, no quotes."
}

// speechLanguageLabel maps an ISO-ish language code to its display name
// ("" = unknown → auto-detect prompt).
func speechLanguageLabel(lang string) string {
	switch lang {
	case "km":
		return "Khmer (ភាសាខ្មែរ)"
	case "zh":
		return "Chinese (中文)"
	case "en":
		return "English"
	default:
		return ""
	}
}

// hasUnexpectedScript reports whether a transcript contains letters from a
// script the platform never serves (Ethiopic, Arabic, Thai, Devanagari, …).
// Khmer, CJK and Latin — plus digits/punctuation — are expected.
func hasUnexpectedScript(text string) bool {
	for _, r := range text {
		if r < 0x80 { // ASCII letters, digits, punctuation, space
			continue
		}
		switch {
		case r >= 0x00C0 && r <= 0x024F: // Latin-1 / Extended (é, ü, ǎ…)
			continue
		case r >= 0x1780 && r <= 0x17FF, r >= 0x19E0 && r <= 0x19FF: // Khmer + symbols
			continue
		case r >= 0x4E00 && r <= 0x9FFF, r >= 0x3400 && r <= 0x4DBF: // CJK ideographs
			continue
		case r >= 0x3000 && r <= 0x303F, r >= 0xFF00 && r <= 0xFFEF: // CJK punctuation / fullwidth
			continue
		case r >= 0x2000 && r <= 0x206F, r >= 0x20A0 && r <= 0x20CF: // general punctuation, currency
			continue
		case r >= 0x0E00 && r <= 0x0E7F: // Thai — a Khmer speaker's neighbours, but never our market
			return true
		case r >= 0x1200 && r <= 0x137F: // Ethiopic (Amharic) — the observed mis-decode
			return true
		case r >= 0x0600 && r <= 0x06FF, r >= 0x0750 && r <= 0x077F: // Arabic
			return true
		case r >= 0x0900 && r <= 0x097F: // Devanagari
			return true
		case r >= 0x0E80 && r <= 0x0EFF: // Lao
			return true
		case r >= 0x1000 && r <= 0x109F: // Myanmar
			return true
		case r >= 0x0530 && r <= 0x058F: // Armenian
			return true
		case r >= 0x10A0 && r <= 0x10FF: // Georgian
			return true
		case r >= 0x0080 && r <= 0x00BF: // C1 controls / Latin-1 punctuation
			continue
		default:
			return true
		}
	}
	return false
}

// isTranscriptionRefusal catches "I am unable to decode or process raw binary
// audio..." style replies so they are never stored as a real transcript.
func isTranscriptionRefusal(transcript string) bool {
	t := strings.ToLower(transcript)
	if t == "" {
		return false
	}
	for _, marker := range []string{
		"unable to decode",
		"cannot decode",
		"can't decode",
		"unable to process raw binary",
		"cannot process raw binary",
		"can't process raw binary",
		"i can't process audio",
		"i cannot process audio",
		"not able to process audio",
	} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}

// LanguageLabel appends the per-request language preference line.
func LanguageLabel(lang string) string {
	switch lang {
	case "en":
		return "English - Secondary"
	case "zh":
		return "中文 (Chinese) - Secondary"
	default:
		return "ភាសាខ្មែរ (Khmer) - Primary"
	}
}

// NormalizeModelName strips a leading "models/" prefix.
func NormalizeModelName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "models/")
}

// FormatVector renders a pgvector literal "[v1,v2,...]".
func FormatVector(values []float32) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.FormatFloat(float64(v), 'f', 6, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// MockEmbedding — deterministic 768-dim placeholder (mock mode).
func MockEmbedding() []float32 {
	out := make([]float32, embeddingVectorDimension)
	for i := range out {
		out[i] = float32(i) * 0.001
	}
	return out
}

// StripSourceMarkers removes citation leftovers like "[Source 1]",
// "(Source 2)" or "（Source 1、Source 2）" from customer-visible replies.
// A stripped group leaves no space before punctuation or line end, and
// exactly one space when text continues after it.
func StripSourceMarkers(text string) string {
	var out []rune
	runes := []rune(text)
	closers := map[rune]rune{'[': ']', '(': ')', '（': '）', '【': '】'}
	for i := 0; i < len(runes); {
		closer, isBracket := closers[runes[i]]
		if !isBracket {
			out = append(out, runes[i])
			i++
			continue
		}
		if end, ok := scanSourceGroup(runes, i, closer); ok {
			// Trim the spaces the model emitted before the marker.
			n := 0
			for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\t') {
				out = out[:len(out)-1]
				n++
			}
			// Keep one space when ordinary text continues after the group
			// (unless the original text already provides that space).
			if n > 0 && end < len(runes) && runes[end] != ' ' && runes[end] != '\t' && !isCitationBoundary(runes[end]) {
				out = append(out, ' ')
			}
			i = end
			continue
		}
		out = append(out, runes[i])
		i++
	}
	return strings.TrimSpace(string(out))
}

// isCitationBoundary reports whether the character after a stripped citation
// group makes a trailing space unnecessary (punctuation or line end).
func isCitationBoundary(r rune) bool {
	switch r {
	case '.', ',', ';', ':', '!', '?', ')', ']', '》', '。', '，', '、', '；', '：', '！', '？', '\n', '\r':
		return true
	}
	return false
}

// scanSourceGroup parses a citation group starting at the opening bracket —
// "source N" entries separated by , 、 ， ; — returning the index just past
// the matching closing bracket.
func scanSourceGroup(runes []rune, i int, closer rune) (int, bool) {
	j := i + 1
	for {
		for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
			j++
		}
		if !strings.HasPrefix(strings.ToLower(string(runes[j:])), "source") {
			return 0, false
		}
		k := j + len("source")
		for k < len(runes) && runes[k] == ' ' {
			k++
		}
		digits := 0
		for k < len(runes) && runes[k] >= '0' && runes[k] <= '9' {
			k++
			digits++
		}
		if digits == 0 {
			return 0, false
		}
		for k < len(runes) && (runes[k] == ' ' || runes[k] == '\t') {
			k++
		}
		if k < len(runes) && runes[k] == closer {
			return k + 1, true
		}
		if k < len(runes) && (runes[k] == ',' || runes[k] == '、' || runes[k] == '，' || runes[k] == '；' || runes[k] == ';') {
			j = k + 1
			continue
		}
		return 0, false
	}
}

// ExtractTextFromValue concatenates candidate text parts.
func ExtractTextFromValue(v map[string]any) string {
	candidates, ok := v["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		return ""
	}
	first, ok := candidates[0].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := first["content"].(map[string]any)
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok {
			if t, ok := pm["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

// LoadDefaultConfig reads the Gemini model config row from the DB: the default row when it is a Gemini row, else the oldest Gemini row (see llm.LoadGemini).
// (api_key, model_name, system_prompt, max_tokens, vertex_region, temperature).
//
// The first return value is the STUDIO credential (see CredentialSourceOf), and
// callers must treat it that way: under vertex it is irrelevant to serving and
// may be empty, so it must never be required, validated or used to decide
// whether the service is configured — New already answers that (IsConfigured),
// and on the vertex path the transport authenticates from the service-account
// file no matter what this column holds.
//
// vertex_region is the location the console last switched to, empty when the
// deployment never switched. Callers that SERVE must apply it (SetVertexRegion):
// without that, a restart quietly sends traffic back to the region in `.env-go`
// while the console still displays the switched one.
//
// temperature is nil when the column is NULL, which means "send no temperature"
// (the platform default applies). Callers that serve should apply it
// (SetTemperature) — until 2026-09-29 this column was displayed by the admin page
// and read by nothing, so the console's value had no effect on any reply.
func LoadDefaultConfig(ctx context.Context, pool *pgxpool.Pool) (string, string, string, int, string, *float64, bool) {
	var apiKey, modelName, systemPrompt, region string
	var maxTokens int
	var temperature *float64
	err := pool.QueryRow(ctx,
		"SELECT api_key, model_name, COALESCE(system_prompt, ''), COALESCE(max_tokens, 2048), "+
			"COALESCE(vertex_region, ''), temperature "+
			"FROM model_configs WHERE provider NOT IN ('anthropic', 'deepseek') ORDER BY is_default DESC, config_id LIMIT 1",
	).Scan(&apiKey, &modelName, &systemPrompt, &maxTokens, &region, &temperature)
	if err != nil {
		return "", "", "", 0, "", nil, false
	}
	return apiKey, modelName, systemPrompt, maxTokens, NormalizeRegion(region), temperature, true
}

func digArray(v map[string]any, keys ...string) []float64 {
	var cur any = v
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	arr, ok := cur.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, item := range arr {
		if n, ok := item.(float64); ok {
			out = append(out, n)
		}
	}
	return out
}

// parseScoreArray tolerantly extracts [{"i":1,"score":7},...] from the reply.
func parseScoreArray(text string, n int) []float32 {
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start < 0 || end <= start {
		return nil
	}
	var entries []map[string]any
	if json.Unmarshal([]byte(text[start:end+1]), &entries) != nil {
		return nil
	}
	scores := make([]float32, n)
	seen := 0
	for _, e := range entries {
		i, ok1 := e["i"].(float64)
		score, ok2 := e["score"].(float64)
		if !ok1 || !ok2 {
			continue
		}
		idx := int(i) - 1
		if idx < 0 || idx >= n {
			continue
		}
		scores[idx] = float32(score)
		seen++
	}
	if seen == 0 {
		return nil
	}
	return scores
}
