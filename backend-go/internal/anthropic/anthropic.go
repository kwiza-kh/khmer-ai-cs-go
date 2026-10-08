// Package anthropic is the Claude client: the second model provider this
// platform can serve from.
//
// It exists because the product is no longer single-provider — Claude Haiku 5.5
// is cheaper per token than the Gemini flash tier and the enterprise console now
// has a provider selector — but the shape of the integration is deliberately the
// same as internal/gemini so callers do not learn a second API:
//
//   - the same Chat / ChatAs / ChatStream / GenerateFast surface, over the same
//     gemini.HistoryItem / gemini.ChatResult types (the DTOs stay in one place,
//     so a reply is a reply regardless of who produced it);
//   - the language directive is appended to the system prompt with
//     gemini.LanguageLabel, exactly as the Gemini client does — the reply
//     language follows what the customer wrote (2026-09-14 fix e520838), and a
//     provider swap must not silently drop that;
//   - auxiliary calls report through gemini.AuxUsageObserver, so the cost
//     dashboard keeps seeing ingest/translation/rerank spend.
//
// Two transports, because the credential story differs:
//
//	api     — https://api.anthropic.com/v1/messages with x-api-key. Works today;
//	          the key is sealed in model_configs.api_key like the studio key.
//	vertex  — Vertex AI's partner-model endpoint (publishers/anthropic/models/
//	          <model>:rawPredict) with the SAME service account Gemini already
//	          uses. Ready to serve, but this project currently has a 0 quota for
//	          the anthropic-claude-haiku base model (429 — see docs §十九).
//
// The Messages API differs from generateContent in ways this file absorbs:
// system is a top-level field (not a message), history roles are user/assistant,
// streaming is `stream: true` on the API but a different URL verb on Vertex, and
// usage arrives as input_tokens/output_tokens/cache_read_input_tokens.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"khmer-ai-cs-go/internal/gemini"
)

const (
	// TransportAPI talks to Anthropic directly; TransportVertex reuses the
	// Google service account through Vertex AI's partner-model endpoint.
	TransportAPI    = "api"
	TransportVertex = "vertex"

	// anthropicVersion is the direct-API header; Vertex wants the version in the
	// body instead (vertexAnthropicVersion).
	anthropicVersion       = "2023-06-01"
	vertexAnthropicVersion = "vertex-2023-10-16"

	defaultBaseURL    = "https://api.anthropic.com"
	defaultMaxTokens  = 2048
	requestTimeout    = 120 * time.Second
	cloudPlatformURL  = "https://www.googleapis.com/auth/cloud-platform"
	defaultOAuthToken = "https://oauth2.googleapis.com/token"
)

// Config is one immutable snapshot of a model config row. HotReload replaces it
// wholesale under the lock: a half-applied config (new model, old key) is the
// kind of thing that only shows up in production.
type Config struct {
	Transport    string
	APIKey       string
	Model        string
	SystemPrompt string
	MaxTokens    int
	Temperature  *float64

	// Vertex transport.
	Project   string
	Region    string
	SAFile    string
	TokenURI  string
	BaseURL   string // test/relay override; empty = the transport's default
	BaseURLVX string // vertex host override (tests)
}

// Service is the Claude client. Safe for concurrent use.
type Service struct {
	mu      sync.RWMutex
	cfg     Config
	http    *http.Client
	creds   *tokenSource
	credErr error
}

// New builds a client. An empty model falls back to the Haiku tier so a
// half-filled row cannot call a model id of "".
func New(cfg Config) *Service {
	s := &Service{http: &http.Client{Timeout: requestTimeout}}
	s.apply(cfg)
	return s
}

func (s *Service) apply(cfg Config) {
	if cfg.Model == "" {
		cfg.Model = "claude-haiku-5-5"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.Transport == "" {
		cfg.Transport = TransportAPI
	}
	if cfg.Transport == TransportVertex && cfg.SAFile != "" {
		// A broken key file is reported on the first call rather than silently
		// degrading to an unauthenticated request.
		ts, err := newTokenSource(cfg.SAFile, cfg.TokenURI)
		if err == nil {
			s.creds = ts
		} else {
			s.creds = nil
			s.credErr = err
		}
	} else {
		s.creds = nil
		s.credErr = nil
	}
	s.cfg = cfg
}

// Region reports the Vertex location (empty on the direct transport). It
// mirrors gemini.Service.Region so the console can render one field.
func (s *Service) Region() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Region
}

// SetVertexRegion validates and stores the location. On the direct transport
// this is a no-op rather than an error: the console saves the whole row at once.
func (s *Service) SetVertexRegion(region string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Transport != TransportVertex {
		return nil
	}
	if !gemini.ValidVertexRegion(region) {
		return fmt.Errorf("invalid vertex region %q", region)
	}
	s.cfg.Region = region
	return nil
}

// ModelName is the model id in force.
func (s *Service) ModelName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Model
}

// SetTemperature sets the sampling temperature; nil means "send none, use the
// platform default" — the same nil-is-a-setting contract as the Gemini client.
func (s *Service) SetTemperature(t *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Temperature = t
}

// HotReload swaps the config in place. The key is only meaningful on the direct
// transport; on Vertex the credential is the service account (same split the
// Gemini client documents for studio vs vertex).
func (s *Service) HotReload(apiKey, modelName, systemPrompt string, maxTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if modelName != "" {
		s.cfg.Model = modelName
	}
	s.cfg.SystemPrompt = systemPrompt
	if maxTokens > 0 {
		s.cfg.MaxTokens = maxTokens
	}
	if s.cfg.Transport == TransportAPI && apiKey != "" {
		s.cfg.APIKey = apiKey
	}
}

// IsConfigured reports whether this client can actually make a call.
func (s *Service) IsConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch s.cfg.Transport {
	case TransportVertex:
		return s.creds != nil
	default:
		return strings.TrimSpace(s.cfg.APIKey) != "" || s.cfg.BaseURL != ""
	}
}

// credErr is the service-account parse failure, if any. Kept out of Config so a
// bad key file cannot masquerade as a valid setting.

// ── generation ───────────────────────────────────────────────────────────────

// Chat runs one turn with the configured system prompt.
func (s *Service) Chat(ctx context.Context, message string, history []gemini.HistoryItem, language string) (gemini.ChatResult, error) {
	return s.ChatAs(ctx, message, history, language, "")
}

// ChatAs is Chat under a caller-supplied system prompt (personas). An empty
// override means the configured prompt.
func (s *Service) ChatAs(ctx context.Context, message string, history []gemini.HistoryItem, language, systemPrompt string) (gemini.ChatResult, error) {
	res, err := s.send(ctx, message, history, language, systemPrompt, s.maxTokens(), false, nil)
	return res, err
}

// ChatStream is Chat with incremental text. onToken may be nil.
func (s *Service) ChatStream(ctx context.Context, message string, history []gemini.HistoryItem, language string, onToken func(string)) (gemini.ChatResult, error) {
	return s.send(ctx, message, history, language, "", s.maxTokens(), true, onToken)
}

// GenerateFast is the auxiliary path (translation, rerank, classification,
// compile). It reports usage through gemini.AuxUsageObserver so those calls stay
// visible on the cost dashboard, exactly like the Gemini client's.
func (s *Service) GenerateFast(ctx context.Context, prompt string, timeout time.Duration) (string, bool) {
	return s.GenerateFastMax(ctx, prompt, timeout, defaultMaxTokens)
}

// GenerateFastMax is GenerateFast with an explicit output budget.
func (s *Service) GenerateFastMax(ctx context.Context, prompt string, timeout time.Duration, maxOutputTokens int) (string, bool) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := s.send(ctx, prompt, nil, "", "", maxOutputTokens, false, nil)
	if err != nil || strings.TrimSpace(res.Reply) == "" {
		return "", false
	}
	reportAuxUsage(ctx, s.ModelName(), res.PromptTokens, res.OutputTokens, res.CachedTokens)
	return res.Reply, true
}

func reportAuxUsage(ctx context.Context, model string, prompt, completion, cached int) {
	if gemini.AuxUsageObserver != nil && prompt+completion > 0 {
		gemini.AuxUsageObserver(ctx, model, prompt, completion, cached)
	}
}

func (s *Service) maxTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.MaxTokens
}

// message is one Messages API turn.
type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type requestBody struct {
	Model        string       `json:"model,omitempty"` // direct transport only
	MaxTokens    int          `json:"max_tokens"`
	System       string       `json:"system,omitempty"`
	Messages     []apiMessage `json:"messages"`
	Temperature  *float64     `json:"temperature,omitempty"`
	Stream       bool         `json:"stream,omitempty"` // direct transport only
	AnthropicVer string       `json:"anthropic_version,omitempty"`
}

// buildBody maps our turn onto the Messages API. Roles: our history uses
// "model" for assistant turns (Gemini's convention); Anthropic rejects that
// outright, so the mapping happens here, once.
func (s *Service) buildBody(message string, history []gemini.HistoryItem, language, systemPromptOverride string, maxTokens int, stream bool) requestBody {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	system := systemPromptOverride
	if system == "" {
		system = cfg.SystemPrompt
	}
	if language != "" {
		system = strings.TrimSpace(system)
		if system != "" {
			system += "\n\n"
		}
		system += "[Language Preference] " + gemini.LanguageLabel(language)
	}

	msgs := make([]apiMessage, 0, len(history)+1)
	for _, h := range history {
		role := h.Role
		switch role {
		case "model", "assistant":
			role = "assistant"
		case "user":
			role = "user"
		default:
			continue // system/tool turns have no place in this mapping
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, apiMessage{Role: role, Content: h.Content})
	}
	if strings.TrimSpace(message) != "" {
		msgs = append(msgs, apiMessage{Role: "user", Content: message})
	}

	body := requestBody{
		MaxTokens:   maxTokens,
		System:      system,
		Messages:    msgs,
		Temperature: cfg.Temperature,
	}
	if cfg.Transport == TransportVertex {
		body.AnthropicVer = vertexAnthropicVersion
	} else {
		body.Model = cfg.Model
		body.Stream = stream
	}
	return body
}

// send performs one request, streaming when onToken is non-nil.
// send performs one request. `stream` is decided by the CALLING METHOD, never by
// whether a callback happens to be nil: ChatStream with no token handler still
// streams (callers that only want timing/usage exist), and conflating the two
// silently turned a streaming call into a blocking one on Vertex, where the verb
// lives in the URL.
func (s *Service) send(ctx context.Context, message string, history []gemini.HistoryItem, language, systemPromptOverride string, maxTokens int, stream bool, onToken func(string)) (gemini.ChatResult, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	if !s.IsConfigured() {
		return gemini.ChatResult{}, fmt.Errorf("anthropic provider is not configured (%s transport)", cfg.Transport)
	}

	body := s.buildBody(message, history, language, systemPromptOverride, maxTokens, stream)
	payload, err := json.Marshal(body)
	if err != nil {
		return gemini.ChatResult{}, err
	}

	endpoint, headers, err := s.endpoint(ctx, cfg, stream)
	if err != nil {
		return gemini.ChatResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return gemini.ChatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	res, err := s.http.Do(req)
	if err != nil {
		return gemini.ChatResult{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
		return gemini.ChatResult{}, fmt.Errorf("anthropic %s: HTTP %d: %s", cfg.Model, res.StatusCode, strings.TrimSpace(string(raw)))
	}

	if !stream {
		return parseMessage(res.Body)
	}
	return parseStream(res.Body, onToken)
}

// endpoint resolves the URL and auth headers for one call.
func (s *Service) endpoint(ctx context.Context, cfg Config, stream bool) (string, map[string]string, error) {
	if cfg.Transport != TransportAPI {
		base := cfg.BaseURLVX
		if base == "" {
			base = gemini.VertexPlatformBase(cfg.Region)
		}
		if base == "" {
			return "", nil, fmt.Errorf("anthropic vertex transport needs a region")
		}
		token, err := s.accessToken(ctx)
		if err != nil {
			return "", nil, err
		}
		verb := "rawPredict"
		if stream {
			verb = "streamRawPredict"
		}
		u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
			base, cfg.Project, cfg.Region, cfg.Model, verb)
		return u, map[string]string{"Authorization": "Bearer " + token}, nil
	}

	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	return strings.TrimSuffix(base, "/") + "/v1/messages", map[string]string{
		"x-api-key":         cfg.APIKey,
		"anthropic-version": anthropicVersion,
	}, nil
}

// ── response parsing ─────────────────────────────────────────────────────────

type apiResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens          int `json:"input_tokens"`
		OutputTokens         int `json:"output_tokens"`
		CacheReadInputTokens int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r apiResponse) toResult() gemini.ChatResult {
	var sb strings.Builder
	for _, block := range r.Content {
		if block.Type == "text" || block.Type == "" {
			sb.WriteString(block.Text)
		}
	}
	return gemini.ChatResult{
		Reply:        sb.String(),
		PromptTokens: r.Usage.InputTokens,
		OutputTokens: r.Usage.OutputTokens,
		CachedTokens: r.Usage.CacheReadInputTokens,
	}
}

func parseMessage(r io.Reader) (gemini.ChatResult, error) {
	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&parsed); err != nil {
		return gemini.ChatResult{}, fmt.Errorf("anthropic: unparseable response: %w", err)
	}
	if parsed.Error != nil {
		return gemini.ChatResult{}, fmt.Errorf("anthropic: %s: %s", parsed.Error.Type, parsed.Error.Message)
	}
	if strings.TrimSpace(parsed.toResult().Reply) == "" {
		return gemini.ChatResult{}, fmt.Errorf("anthropic: response contained no text")
	}
	return parsed.toResult(), nil
}

// parseStream consumes the SSE stream. The event shapes absorbed here:
//
//	message_start      → usage.input_tokens (and cache_read_input_tokens)
//	content_block_delta→ delta.text, emitted to onToken as it arrives
//	message_delta      → usage.output_tokens (final counts)
//	error              → a mid-stream failure, surfaced as an error
//
// A stream that ends without message_stop is treated as a failure rather than a
// short answer: the widget would otherwise show a truncated reply as complete.
func parseStream(r io.Reader, onToken func(string)) (gemini.ChatResult, error) {
	var out gemini.ChatResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	sawMessage := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" {
			continue
		}
		var evt struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					InputTokens          int `json:"input_tokens"`
					CacheReadInputTokens int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &evt); err != nil {
			continue // keepalives and unknown frames must not kill the turn
		}
		switch evt.Type {
		case "message_start":
			sawMessage = true
			out.PromptTokens = evt.Message.Usage.InputTokens
			out.CachedTokens = evt.Message.Usage.CacheReadInputTokens
		case "content_block_delta":
			if evt.Delta.Text != "" {
				out.Reply += evt.Delta.Text
				if onToken != nil {
					onToken(evt.Delta.Text)
				}
			}
		case "message_delta":
			if evt.Usage.OutputTokens > 0 {
				out.OutputTokens = evt.Usage.OutputTokens
			}
		case "message_stop":
			if strings.TrimSpace(out.Reply) == "" {
				return out, fmt.Errorf("anthropic: stream produced no text")
			}
			return out, nil
		case "error":
			if evt.Error != nil {
				return out, fmt.Errorf("anthropic: %s: %s", evt.Error.Type, evt.Error.Message)
			}
			return out, fmt.Errorf("anthropic: stream error")
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("anthropic: stream read: %w", err)
	}
	if !sawMessage {
		return out, fmt.Errorf("anthropic: stream ended before any message")
	}
	return out, fmt.Errorf("anthropic: stream ended before completion")
}

// ── Vertex service-account token ─────────────────────────────────────────────

// tokenSource mints OAuth tokens from a Vertex service account. It is the same
// shape as internal/gemini's (which is unexported); keeping one here means the
// Claude transport does not have to reach into the Gemini client for a
// credential, and the host rule still comes from gemini.VertexPlatformBase.
type tokenSource struct {
	email    string
	key      *rsa.PrivateKey
	tokenURI string

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func newTokenSource(path, tokenURI string) (*tokenSource, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse service account: %w", err)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, fmt.Errorf("service account private_key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(block.Bytes); rsaErr == nil {
			parsed = rsaKey
		} else {
			return nil, fmt.Errorf("parse service account key: %w", err)
		}
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("service account key is %T, want RSA", parsed)
	}
	uri := tokenURI
	if uri == "" {
		uri = sa.TokenURI
	}
	if uri == "" {
		uri = defaultOAuthToken
	}
	return &tokenSource{email: sa.ClientEmail, key: key, tokenURI: uri}, nil
}

func (t *tokenSource) accessToken(ctx context.Context) (string, error) {
	if t == nil {
		return "", fmt.Errorf("anthropic vertex transport has no readable service account key")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expiry) > 2*time.Minute {
		return t.token, nil
	}
	now := time.Now()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   t.email,
		"scope": cloudPlatformURL,
		"aud":   t.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}).SignedString(t.key)
	if err != nil {
		return "", fmt.Errorf("sign service account assertion: %w", err)
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {signed},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth token exchange: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.AccessToken == "" {
		return "", fmt.Errorf("oauth token exchange: unparseable response")
	}
	ttl := parsed.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	t.token = parsed.AccessToken
	t.expiry = now.Add(time.Duration(ttl) * time.Second)
	return t.token, nil
}

// accessToken is the Service-level entry point so callers do not juggle the
// token source's nil-ness.
func (s *Service) accessToken(ctx context.Context) (string, error) {
	s.mu.RLock()
	creds := s.creds
	s.mu.RUnlock()
	if creds == nil {
		return "", fmt.Errorf("anthropic vertex transport is not configured (service account unreadable)")
	}
	return creds.accessToken(ctx)
}
