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
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// EmbeddingModel — shared with the Rust backend (identical vectors).
	EmbeddingModel = "gemini-embedding-001"
	// embeddingVectorDimension matches EmbeddingModel's output width.
	embeddingVectorDimension = 768
)

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
var FastModel = envOr("GEMINI_FAST_MODEL", "gemini-3.5-flash")

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
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

	// provider/providerErr come from the process environment and a config file,
	// neither of which changes while the process runs, so they are resolved once
	// in New and read without the mutex (unlike the hot-reloadable fields above).
	provider    provider
	providerErr error

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
		apiKey:       apiKey,
		modelName:    NormalizeModelName(model),
		systemPrompt: DefaultSystemPrompt,
		maxTokens:    maxTokens,
		provider:     prov,
		providerErr:  provErr,
		embedCache:   make(map[string]embedCacheEntry),
	}
	// An empty key means mock mode on the studio path — unchanged. Vertex
	// authenticates with a service account, so an empty key is a valid
	// configuration there and the service is live; and a vertex deployment whose
	// configuration is BROKEN still counts as configured, so every turn fails
	// with that error instead of quietly serving template replies to customers.
	if apiKey != "" || prov.ready() || provErr != nil {
		s.client = &http.Client{Timeout: 60 * time.Second}
	}
	return s
}

// activeProvider returns the transport for this deployment, or the
// configuration error that must fail the request. Every network entry point
// goes through this instead of reading s.provider directly: a vertex
// deployment that is missing its key file must fail loudly, not fall back to
// whatever URL shape a zero provider happens to produce.
func (s *Service) activeProvider() (provider, error) {
	if s.providerErr != nil {
		return provider{}, s.providerErr
	}
	return s.provider, nil
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

// snapshot returns an immutable copy of the serving config for one request.
type servingConfig struct {
	apiKey       string
	modelName    string
	systemPrompt string
	maxTokens    int
	client       *http.Client
}

func (s *Service) snapshot() servingConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return servingConfig{apiKey: s.apiKey, modelName: s.modelName, systemPrompt: s.systemPrompt, maxTokens: s.maxTokens, client: s.client}
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
			s.client = &http.Client{Timeout: 60 * time.Second}
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

// ListModels returns the generative model names available for the key.
//
// The apiKey argument is the STUDIO credential; on the vertex path it is
// ignored and the request is authorised with a service-account token instead
// (provider.authorize picks the header per transport), which is why a vertex
// caller may — and the admin console does — pass "" without losing access.
// The signature is kept because callers (the admin model picker) hold the key
// from model_configs and have no notion of the transport.
// vertexPublisherModels is the curated picker list for vertex. Kept next to
// ListModels because it exists only to compensate for the missing platform
// route; see the comment at its use site.
var vertexPublisherModels = []string{
	"gemini-3.5-flash",
	"gemini-2.5-flash",
	"gemini-embedding-001",
}

func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	prov, err := providerFromEnv()
	if err != nil {
		return nil, err
	}
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
	var v struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
		// Vertex answers under a different key; both are read because that
		// envelope is the platform's choice, not this package's.
		PublisherModels []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"publisherModels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	entries := v.Models
	if len(entries) == 0 {
		entries = v.PublisherModels
	}
	names := make([]string, 0)
	for _, m := range entries {
		n := modelNameFromResource(m.Name)
		if strings.Contains(strings.ToLower(n), "gemini") {
			names = append(names, n)
		}
	}
	// On vertex this route lists the project's OWN models (tuned/uploaded), not
	// the Gemini publisher models — the platform exposes no publisher-model list
	// at all (…/publishers/google/models answers 404 before authentication).
	// Without this the admin picker would present an empty dropdown on a
	// deployment that is serving fine, which reads as "Gemini is broken".
	// The names below are the ones measured working in this project's region;
	// they are a convenience list for the picker, never a gate — a model missing
	// from here can still be typed in and will work if the region serves it.
	if prov.kind == providerVertex {
		seen := make(map[string]bool, len(names))
		for _, n := range names {
			seen[n] = true
		}
		for _, n := range vertexPublisherModels {
			if !seen[n] {
				names = append(names, n)
				seen[n] = true
			}
		}
	}

	return names, nil
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
	return s.provider.generateURL(model)
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
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, "", fmt.Errorf("marshal request: %w", err)
	}
	prov, err := s.activeProvider()
	if err != nil {
		// A broken transport is not a transport blip: retrying inside this turn
		// would only multiply requests that cannot succeed, and token minting is
		// serialised, so a burst of turns would just queue behind it.
		return 0, "", err
	}
	lastErr := ""
	// Snapshot once: Reload swaps client under the mutex when an admin saves a
	// new model config, so reading s.client per attempt would race.
	client := s.snapshot().client
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, "", ctx.Err()
			case <-time.After(time.Duration(400*attempt) * time.Millisecond):
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
		lastErr = fmt.Sprintf("failed (%d): %s", resp.StatusCode, truncateRunes(text, 300))
	}
	return 0, "", fmt.Errorf("gemini: %s", lastErr)
}

// Chat — non-streaming turn against the main model (mock fallback parity).
func (s *Service) Chat(ctx context.Context, message string, history []HistoryItem, language string) (ChatResult, error) {
	return s.chatWithModel(ctx, message, history, language, "")
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
func (s *Service) chatWithModel(ctx context.Context, message string, history []HistoryItem, language, model string) (ChatResult, error) {
	if !s.IsConfigured() {
		return s.chatMock(message, language), nil
	}
	cfg := s.snapshot()
	if model == "" {
		model = cfg.modelName
	}
	cacheName := s.contextCacheFor(ctx, language)
	body := s.buildRequestBody(message, history, language, cacheName)
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(model), body)
	if err == nil && cacheName != "" && contextCacheStale(status, text) {
		// The registered cache expired or was deleted server-side (its own TTL,
		// a cleanup, or another instance replacing it). Forget it and answer
		// uncached rather than failing the turn over an optimisation.
		s.forgetContextCache(language)
		body = s.buildRequestBody(message, history, language, "")
		status, text, err = s.postWithRetry(ctx, s.generateURLFor(model), body)
	}
	fast := s.fastModelName()
	if err == nil && (status >= http.StatusInternalServerError || status == http.StatusTooManyRequests) &&
		model == cfg.modelName && fast != model {
		// Degrade to the fast model once on overload — only when it really is
		// another model. fastModelName() falls back to the serving model when
		// GEMINI_FAST_MODEL is unset, so the previous guard re-sent the
		// identical request to the model that had just refused it.
		status, text, err = s.postWithRetry(ctx, s.generateURLFor(fast), body)
	}
	if err != nil {
		return s.chatMock(message, language), fmt.Errorf("gemini request failed: %w", err)
	}
	if status != http.StatusOK {
		return s.chatMock(message, language), fmt.Errorf("gemini returned HTTP %d: %s", status, truncateRunes(text, 300))
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return s.chatMock(message, language), fmt.Errorf("gemini returned unparsable JSON: %w", err)
	}
	return s.resultFromValue(v), nil
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

// generationConfig builds the config block for one chat request.
//
// thinkingBudget (GEMINI_THINKING_BUDGET) is sent only when set: it caps the
// model's internal reasoning tokens, which are billed at the OUTPUT rate
// (usageFromValue folds them into the completion count) and dominate the tail
// of reply latency. Unset keeps the model's own default.
func generationConfig(maxTokens int) map[string]any {
	cfg := map[string]any{"maxOutputTokens": maxTokens}
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
	body := map[string]any{
		"contents":         contents,
		"generationConfig": generationConfig(s.snapshot().maxTokens),
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
	res := ChatResult{Reply: ExtractTextFromValue(v)}
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
func (s *Service) ChatStream(ctx context.Context, message string, history []HistoryItem, language string, onToken func(string)) (ChatResult, error) {
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
		return s.chatWithModel(ctx, message, history, language, "")
	}
	body := s.buildRequestBody(message, history, language, s.contextCacheFor(ctx, language))
	payload, err := json.Marshal(body)
	if err != nil {
		return s.chatWithModel(ctx, message, history, language, "")
	}
	// Read the serving config through the mutex: an admin saving a new model
	// config calls Reload, which rewrites modelName/client under the lock.
	cfg := s.snapshot()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prov.streamURL(cfg.modelName), bytes.NewReader(payload))
	if err != nil {
		return s.chatWithModel(ctx, message, history, language, "")
	}
	req.Header.Set("Content-Type", "application/json")
	// Credential selection is shared with postWithRetry (see provider.authorize).
	if err := prov.authorize(ctx, req, cfg.apiKey); err != nil {
		return s.chatWithModel(ctx, message, history, language, "")
	}
	resp, err := cfg.client.Do(req)
	if err != nil {
		return s.chatWithModel(ctx, message, history, language, "")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return s.chatWithModel(ctx, message, history, language, "")
	}

	var full strings.Builder
	res := ChatResult{}
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
			onToken(text)
		}
		if p, c, cached := usageFromValue(chunk); p+c+cached > 0 {
			res.PromptTokens, res.OutputTokens, res.CachedTokens = p, c, cached
		}
	}
	res.Reply = full.String()
	if res.Reply == "" {
		return s.chatWithModel(ctx, message, history, language, "")
	}
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
		return "", fmt.Errorf("describe image failed (%d): %s", status, truncateRunes(text, 300))
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
		return "", fmt.Errorf("document OCR failed (%d): %s", status, truncateRunes(text, 300))
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
	target := s.provider.generateURL(TTSModel())
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
	preview := truncateRunes(message, 50)
	reply := "(mock) អរគុណសម្រាប់សំណួររបស់អ្នកអំពី \"" + preview + "\" — set GEMINI_API_KEY to enable real AI."
	switch language {
	case "en":
		reply = "(mock) Thank you for your question about \"" + preview + "\" — set GEMINI_API_KEY to enable real AI."
	case "zh":
		reply = "(mock) 感谢你的提问「" + preview + "」—— 配置 GEMINI_API_KEY 后启用真实 AI。"
	}
	return ChatResult{Reply: reply, PromptTokens: len(message) / 4, UsedMock: true}
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
		fmt.Fprintf(&convo, "%s: %s\n", h.Role, truncateRunes(h.Content, 160))
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
		"Customer: " + truncateRunes(customerMsg, 600) + "\n" +
		"Assistant: " + truncateRunes(reply, 600)

	out, ok := s.GenerateFast(ctx, prompt, 10*time.Second)
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
		fmt.Fprintf(&body, "[%d] %s\n\n", i+1, truncateRunes(chunk, 400))
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

// GenerateEmbeddings — batch document embeddings via batchEmbedContents
// (up to 100 texts per call; falls back to per-text calls on batch failure).
func (s *Service) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	if !s.IsConfigured() {
		mock := MockEmbedding()
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = mock
		}
		return out, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 100 {
		end := start + 100
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := s.embedBatch(ctx, batch)
		if err != nil {
			// Fall back to per-text embedding so one bad batch never blocks
			// indexing.
			for _, text := range batch {
				vec, err := s.embed(ctx, text, "RETRIEVAL_DOCUMENT")
				if err != nil {
					return nil, err
				}
				vecs = append(vecs, vec)
			}
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (s *Service) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	// The body differs per provider (studio: :batchEmbedContents with a
	// `requests` array; vertex: one :predict carrying `instances`) — see
	// provider.embedBatchBody. The return contract does not: n vectors of
	// embeddingVectorDimension, in input order.
	prov, err := s.activeProvider()
	if err != nil {
		return nil, err
	}
	status, respText, err := s.postWithRetry(ctx, prov.batchEmbedURL(), prov.embedBatchBody(texts))
	if err != nil {
		return nil, fmt.Errorf("batchEmbedContents request: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("batchEmbedContents failed (%d): %s", status, truncateRunes(respText, 300))
	}
	if prov.kind == providerVertex {
		return parseVertexPredictBatch(respText, len(texts))
	}
	var v struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	if json.Unmarshal([]byte(respText), &v) != nil || len(v.Embeddings) != len(texts) {
		return nil, fmt.Errorf("batchEmbedContents: invalid response")
	}
	out := make([][]float32, len(v.Embeddings))
	for i, e := range v.Embeddings {
		if len(e.Values) == 0 {
			return nil, fmt.Errorf("batchEmbedContents: empty vector at %d", i)
		}
		out[i] = e.Values
	}
	return out, nil
}

// GenerateQueryEmbedding — query-time vector, cached 5 minutes.
func (s *Service) GenerateQueryEmbedding(ctx context.Context, text string) ([]float32, error) {
	key := strings.ToLower(strings.TrimSpace(text))
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
		return nil, err
	}
	s.mu.Lock()
	if len(s.embedCache) > 200 {
		s.embedCache = make(map[string]embedCacheEntry)
	}
	s.embedCache[key] = embedCacheEntry{at: time.Now(), vec: vec}
	s.mu.Unlock()
	return vec, nil
}

func (s *Service) embed(ctx context.Context, text, taskType string) ([]float32, error) {
	if !s.IsConfigured() {
		return MockEmbedding(), nil
	}
	prov, err := s.activeProvider()
	if err != nil {
		return nil, err
	}
	// Same query/document split, two different bodies — see provider.embedBody.
	status, respText, err := s.postWithRetry(ctx, prov.embedURL(), prov.embedBody(text, taskType))
	if err != nil {
		return nil, fmt.Errorf("embedContent request: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("embedContent failed (%d): %s", status, truncateRunes(respText, 300))
	}
	var v map[string]any
	if json.Unmarshal([]byte(respText), &v) != nil {
		return nil, fmt.Errorf("embedContent: invalid response")
	}
	values := prov.embeddingValues(v, 0)
	if len(values) != embeddingVectorDimension {
		// Checked here rather than at the pgvector INSERT because a wrong-width
		// vector is not an error upstream: the call succeeds, and the mismatch
		// only surfaces later as a failed insert or as silently bad search
		// results. See embeddingWidthError.
		return nil, embeddingWidthError(prov.kind, len(values))
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
		return "", fmt.Errorf("transcribe failed (%d): %s", status, truncateRunes(text, 300))
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", fmt.Errorf("transcribe: invalid response")
	}
	auxPrompt, auxCompletion, auxCached := usageFromValue(v)
	reportAuxUsage(ctx, s.fastModelName(), auxPrompt, auxCompletion, auxCached)
	transcript := strings.TrimSpace(ExtractTextFromValue(v))
	if isTranscriptionRefusal(transcript) {
		return "", fmt.Errorf("transcribe: model could not decode audio (mime=%s): %s", mimeType, truncateRunes(transcript, 160))
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

// LoadDefaultConfig reads the default model config row from the DB:
// (api_key, model_name, system_prompt, max_tokens).
//
// The first return value is the STUDIO credential (see CredentialSourceOf), and
// callers must treat it that way: under vertex it is irrelevant to serving and
// may be empty, so it must never be required, validated or used to decide
// whether the service is configured — New already answers that (IsConfigured),
// and on the vertex path the transport authenticates from the service-account
// file no matter what this column holds.
func LoadDefaultConfig(ctx context.Context, pool *pgxpool.Pool) (string, string, string, int, bool) {
	var apiKey, modelName, systemPrompt string
	var maxTokens int
	err := pool.QueryRow(ctx,
		"SELECT api_key, model_name, COALESCE(system_prompt, ''), COALESCE(max_tokens, 2048) "+
			"FROM model_configs WHERE is_default = true ORDER BY config_id LIMIT 1",
	).Scan(&apiKey, &modelName, &systemPrompt, &maxTokens)
	if err != nil {
		return "", "", "", 0, false
	}
	return apiKey, modelName, systemPrompt, maxTokens, true
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

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
