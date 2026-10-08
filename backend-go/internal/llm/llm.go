// Package llm chooses the model provider that serves generation: the customer
// reply path, the inbox summary, translation, knowledge-gap drafts and the other
// text-generation calls.
//
// The choice is data. model_configs.provider on the default row (is_default) names
// the provider, the console writes it, and the router applies it on every hot
// reload. Nothing here reads an environment variable to pick a provider: a
// deployment switches with a row edit, and the decision sits in the same table as
// the model, the key and the prompt it governs.
//
// What stays on Gemini whichever provider serves generation: embeddings and the
// retrieval path built on them (Claude has no embedding model), reranking and query
// rewriting (Gemini-specific prompts), image and audio understanding, speech
// synthesis and transcription, and the turn judge. Those callers keep the Gemini
// client they always used.
package llm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/anthropic"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/usage"
)

// The provider values stored in model_configs.provider. They are the console's
// vocabulary as well, so a new provider is one constant here, one client, and one
// option in the Models page.
const (
	// ProviderGemini serves through the Gemini client. Its transport (AI Studio or
	// Vertex) is set by GEMINI_PROVIDER, as it always was.
	ProviderGemini = "gemini"
	// ProviderAnthropic serves Claude through the Anthropic API. The key is sealed in
	// model_configs.api_key, like the Gemini studio key. (A second Claude provider,
	// "anthropic-vertex", used to reach Claude through Vertex AI with the platform
	// service account; it was removed — see DEVELOPMENT.md §十九.)
	ProviderAnthropic = "anthropic"
)

// Providers lists every value the console may store.
func Providers() []string {
	return []string{ProviderGemini, ProviderAnthropic}
}

// Valid reports whether the console may store this provider.
func Valid(provider string) bool {
	for _, p := range Providers() {
		if p == provider {
			return true
		}
	}
	return false
}

// IsClaude reports whether a provider serves through the Claude client. Every other
// value, including one nothing recognises, serves through Gemini: that is the
// reading every existing row has always had, and it fails in the known direction.
func IsClaude(provider string) bool {
	return provider == ProviderAnthropic
}

// CredentialSource names the secret a provider authenticates with, in the
// vocabulary the console already uses for Gemini: "api_key" is a key stored in
// model_configs.api_key, and "service_account" is the file GEMINI_VERTEX_SA_FILE
// names (which only the Gemini transport reads now).
func CredentialSource(provider string) string {
	switch provider {
	case ProviderAnthropic:
		return "api_key"
	default:
		return string(gemini.CredentialSourceOf())
	}
}

// Model is the generation surface both providers implement. It is the method set
// callers already used on *gemini.Service, so a call site changes its receiver and
// nothing else.
type Model interface {
	Chat(ctx context.Context, message string, history []gemini.HistoryItem, language string) (gemini.ChatResult, error)
	ChatAs(ctx context.Context, message string, history []gemini.HistoryItem, language, systemPrompt string) (gemini.ChatResult, error)
	ChatStream(ctx context.Context, message string, history []gemini.HistoryItem, language string, onToken func(string)) (gemini.ChatResult, error)
	GenerateFast(ctx context.Context, prompt string, timeout time.Duration) (string, bool)
	GenerateFastMax(ctx context.Context, prompt string, timeout time.Duration, maxOutputTokens int) (string, bool)
	ModelName() string
	IsConfigured() bool
}

var (
	_ Model = (*gemini.Service)(nil)
	_ Model = (*anthropic.Service)(nil)
)

// Router holds the serving clients and the provider in force. Safe for concurrent
// use: every generation call reads the provider once, under the lock.
type Router struct {
	mu       sync.RWMutex
	gemini   *gemini.Service
	claude   *anthropic.Service
	provider string
}

// NewRouter starts on Gemini, the provider every existing row means.
func NewRouter(g *gemini.Service) *Router {
	return &Router{gemini: g, provider: ProviderGemini}
}

// Gemini returns the Gemini client. Embeddings, retrieval and the Google-only
// capabilities use it directly, whichever provider serves generation.
func (r *Router) Gemini() *gemini.Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.gemini
}

// Claude returns the Claude client, or nil until one is installed.
func (r *Router) Claude() *anthropic.Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.claude
}

// SetClaude installs a Claude client built elsewhere. InstallClaude is the path the
// serving code takes; this one is for a caller that already holds a configured client.
func (r *Router) SetClaude(c *anthropic.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claude = c
}

// Provider names the provider in force.
func (r *Router) Provider() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.provider
}

// SetProvider puts a provider in force. An unrecognised value serves as Gemini, the
// same reading IsClaude gives every row. The spend guardrail is told the same
// thing, because the AI Studio tier ceiling is a wall on Gemini calls only.
func (r *Router) SetProvider(provider string) {
	if !IsClaude(provider) {
		provider = ProviderGemini
	}
	r.mu.Lock()
	r.provider = provider
	r.mu.Unlock()
	usage.SetServingProvider(provider)
}

// InstallClaude builds the Claude client from a row, or reconfigures the one already
// built. apiKey is the unsealed key for the direct transport; the caller holds the
// Sealer, and the row keeps the key sealed.
func (r *Router) InstallClaude(row Row, apiKey string) {
	cfg := ClaudeConfig(row, apiKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claude == nil {
		r.claude = anthropic.New(cfg)
		return
	}
	r.claude.Reconfigure(cfg)
}

// Model returns the client that answers generation calls right now.
//
// A Claude provider with no client answers every call with an error. It never falls
// back to Gemini: a silent change of model is exactly the kind of change that must
// be visible, and the turn should fail loudly on the money path instead.
func (r *Router) Model() Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if IsClaude(r.provider) {
		if r.claude == nil {
			return unavailable{provider: r.provider}
		}
		return r.claude
	}
	if r.gemini == nil {
		return unavailable{provider: r.provider}
	}
	return r.gemini
}

// unavailable answers a call with an error. A serving provider that was never built
// is a configuration fault, and it must surface as one.
type unavailable struct{ provider string }

func (u unavailable) err() error {
	return fmt.Errorf("model provider %q is not built: its configuration did not load", u.provider)
}

func (u unavailable) Chat(context.Context, string, []gemini.HistoryItem, string) (gemini.ChatResult, error) {
	return gemini.ChatResult{}, u.err()
}

func (u unavailable) ChatAs(context.Context, string, []gemini.HistoryItem, string, string) (gemini.ChatResult, error) {
	return gemini.ChatResult{}, u.err()
}

func (u unavailable) ChatStream(context.Context, string, []gemini.HistoryItem, string, func(string)) (gemini.ChatResult, error) {
	return gemini.ChatResult{}, u.err()
}

func (u unavailable) GenerateFast(context.Context, string, time.Duration) (string, bool) {
	return "", false
}

func (u unavailable) GenerateFastMax(context.Context, string, time.Duration, int) (string, bool) {
	return "", false
}

func (u unavailable) ModelName() string { return "" }

func (u unavailable) IsConfigured() bool { return false }

// Row is the part of one model_configs row the serving path reads.
type Row struct {
	ConfigID     int32
	Provider     string
	APIKey       string // as stored: sealed by security.Sealer
	ModelName    string
	SystemPrompt string
	MaxTokens    int
	Region       string // normalised vertex_region; "" when unset
	Temperature  *float64
}

// querier is the one method the loaders need. A pool and a transaction both have
// it, which is what lets the row-selection rules be tested inside a rolled-back
// transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LoadDefault reads the default row: the one the console marks as serving.
func LoadDefault(ctx context.Context, db querier) (Row, bool) {
	return scanRow(db.QueryRow(ctx,
		"SELECT config_id, provider, api_key, model_name, COALESCE(system_prompt,''), COALESCE(max_tokens,2048), COALESCE(vertex_region,''), temperature "+
			"FROM model_configs WHERE is_default = true ORDER BY config_id LIMIT 1"))
}

// LoadGemini reads the row the Gemini client is configured from. That is the default
// row when it is a Gemini row, otherwise the oldest Gemini row. Gemini keeps its
// credentials when Claude serves, because embeddings and retrieval run on it. ok is
// false when no Gemini row exists, and the Gemini client then runs on its environment.
func LoadGemini(ctx context.Context, db querier) (Row, bool) {
	return scanRow(db.QueryRow(ctx,
		"SELECT config_id, provider, api_key, model_name, COALESCE(system_prompt,''), COALESCE(max_tokens,2048), COALESCE(vertex_region,''), temperature "+
			"FROM model_configs WHERE provider NOT IN ('anthropic') "+
			"ORDER BY is_default DESC, config_id LIMIT 1"))
}

// LoadByID reads one row by its id. ok is false when no such row exists.
func LoadByID(ctx context.Context, db querier, configID int32) (Row, bool) {
	return scanRow(db.QueryRow(ctx,
		"SELECT config_id, provider, api_key, model_name, COALESCE(system_prompt,''), COALESCE(max_tokens,2048), COALESCE(vertex_region,''), temperature "+
			"FROM model_configs WHERE config_id = $1", configID))
}

func scanRow(row pgx.Row) (Row, bool) {
	var out Row
	var region string
	if err := row.Scan(&out.ConfigID, &out.Provider, &out.APIKey, &out.ModelName, &out.SystemPrompt,
		&out.MaxTokens, &region, &out.Temperature); err != nil {
		return Row{}, false
	}
	out.Region = gemini.NormalizeRegion(region)
	return out, true
}

// ClaudeConfig builds the Claude client's configuration from a row. The key arrives
// unsealed.
func ClaudeConfig(row Row, apiKey string) anthropic.Config {
	return anthropic.Config{
		APIKey:       apiKey,
		Model:        row.ModelName,
		SystemPrompt: row.SystemPrompt,
		MaxTokens:    row.MaxTokens,
		Temperature:  row.Temperature,
	}
}
