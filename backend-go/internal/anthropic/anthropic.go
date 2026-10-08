// Package anthropic is the Claude client: the second model provider this
// platform can serve generation from.
//
// Its generation surface is the one internal/gemini already exposes (Chat,
// ChatAs, ChatStream, GenerateFast, GenerateFastMax, ModelName, IsConfigured),
// over the same gemini.HistoryItem and gemini.ChatResult types, so internal/llm
// can route a call to either provider without the callers learning a second API.
// What the Gemini client does for a turn is mirrored here:
//
//   - the system prompt falls back to gemini.DefaultSystemPrompt when the
//     configured one is empty, and the reply language follows the customer
//     (gemini.LanguageLabel; the fix is e520838 of 2026-09-14);
//   - a staff reply keeps its marker, and history is repaired with
//     gemini.RepairHistoryShape; trimming stays with the callers, as for Gemini;
//   - auxiliary calls (translation, classification, compile, gap drafts) send no
//     system prompt and report through gemini.AuxUsageObserver, so the cost
//     dashboard keeps seeing them.
//
// Where the Messages API differs from generateContent, this file absorbs it:
//
//   - system is a top-level field, not a message;
//   - the model is in the body on the API and in the URL on Vertex;
//   - some models answer 400 to temperature, top_p or top_k, so sampling is sent
//     only where the catalog says the model takes it;
//   - adaptive thinking is on by default and its tokens count toward max_tokens,
//     so the catalog can turn it off explicitly;
//   - usage reports input_tokens WITHOUT the cached parts. The client folds the
//     cached part back into the prompt count, so the cost arithmetic (which
//     subtracts the cached part from the prompt) sees the same numbers it sees
//     for Gemini.
//
// Two transports, because the credential story differs:
//
//	api     — https://api.anthropic.com/v1/messages with x-api-key. The key is
//	          sealed in model_configs.api_key, like the Gemini studio key.
//	vertex  — Vertex AI's partner-model endpoint
//	          (publishers/anthropic/models/<model>:rawPredict, or
//	          :streamRawPredict for a stream) with the SAME service account the
//	          Gemini vertex transport uses. Served from global, us and eu only.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
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
	// TransportAPI talks to Anthropic directly; TransportVertex reaches Claude
	// through the Google service account, as Vertex partner models do.
	TransportAPI    = "api"
	TransportVertex = "vertex"

	// anthropicVersion is the direct-API header. Vertex wants its version in the
	// request body instead (vertexAnthropicVersion).
	anthropicVersion       = "2023-06-01"
	vertexAnthropicVersion = "vertex-2023-10-16"

	defaultBaseURL    = "https://api.anthropic.com"
	defaultModel      = "claude-haiku-5-5"
	defaultMaxTokens  = 2048
	defaultRegion     = "global"
	requestTimeout    = 120 * time.Second
	auxTimeout        = 20 * time.Second
	cloudPlatformURL  = "https://www.googleapis.com/auth/cloud-platform"
	defaultOAuthToken = "https://oauth2.googleapis.com/token"

	// humanAgentMarker must match the Gemini client's. A staff reply reaches the
	// model as a user turn carrying this prefix, so the model can tell it from its
	// own earlier output.
	humanAgentMarker = "[Human agent reply] "
)

// tokenHTTP performs the OAuth exchange. http.DefaultClient has no timeout, and a
// hung token endpoint would hold a reply turn for as long as its caller waits.
var tokenHTTP = &http.Client{Timeout: 30 * time.Second}

// Config is one immutable snapshot of a model config row. Reconfigure swaps it
// wholesale under the lock: a half-applied config (a new model with an old key)
// is the kind of fault that only shows up in production.
type Config struct {
	Transport    string
	APIKey       string
	Model        string
	SystemPrompt string
	MaxTokens    int
	Temperature  *float64

	// Vertex transport. Project may be empty: the service-account file names one.
	Project  string
	Region   string
	SAFile   string
	TokenURI string

	// BaseURL replaces the direct transport's root. BaseURLVX replaces the Vertex
	// host root; the /v1 segment is added here, not in the value. Both exist for
	// tests and relays. Empty means the transport's own host.
	BaseURL   string
	BaseURLVX string
}

// withDefaults fills the fields a half-filled row would leave empty, so a row
// cannot call a model id of "" or send a zero max_tokens.
func withDefaults(cfg Config) Config {
	if cfg.Transport == "" {
		cfg.Transport = TransportAPI
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.Transport == TransportVertex && cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	return cfg
}

// Service is the Claude client. Safe for concurrent use.
type Service struct {
	mu  sync.RWMutex
	cfg Config
	// creds is the Vertex token source. It is nil on the direct transport, and on
	// Vertex when the service-account file could not be read; credErr says why.
	creds   *tokenSource
	credErr error
	http    *http.Client
}

// snapshot is one consistent view of the configuration, taken once per call.
type snapshot struct {
	cfg     Config
	creds   *tokenSource
	credErr error
}

// New builds a client. A Vertex config whose key file is broken is kept and fails
// every call with the reason, rather than degrading to an unauthenticated request.
func New(cfg Config) *Service {
	s := &Service{http: &http.Client{Timeout: requestTimeout}}
	s.Reconfigure(cfg)
	return s
}

// Reconfigure replaces the configuration. The serving path calls it on every hot
// reload, so a new model, key, region or service-account file takes effect on the
// next call without a restart.
func (s *Service) Reconfigure(cfg Config) {
	cfg = withDefaults(cfg)
	var creds *tokenSource
	var credErr error
	if cfg.Transport == TransportVertex && cfg.SAFile != "" {
		creds, credErr = newTokenSource(cfg.SAFile, cfg.TokenURI)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.creds = creds
	s.credErr = credErr
}

// HotReload swaps the key, model, prompt and output budget in place, with the
// same shape as the Gemini client's. The key only applies on the direct transport;
// on Vertex the credential is the service-account file.
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

// SetTemperature sets the sampling temperature for chat calls. It is sent only for
// models whose catalog entry says they take sampling parameters.
func (s *Service) SetTemperature(t *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Temperature = t
}

func (s *Service) snapshot() snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshot{cfg: s.cfg, creds: s.creds, credErr: s.credErr}
}

// ModelName is the model id in force.
func (s *Service) ModelName() string {
	return s.snapshot().cfg.Model
}

// Region is the Vertex location in force; empty on the direct transport.
func (s *Service) Region() string {
	return s.snapshot().cfg.Region
}

// IsConfigured reports whether this client can make a call.
func (s *Service) IsConfigured() bool {
	return configured(s.snapshot())
}

func configured(sn snapshot) bool {
	if sn.cfg.Transport == TransportVertex {
		return sn.creds != nil
	}
	return strings.TrimSpace(sn.cfg.APIKey) != "" || sn.cfg.BaseURL != ""
}

// unconfiguredError names what is missing, so the operator reads the cause and not
// a bare "not configured".
func unconfiguredError(sn snapshot) error {
	if sn.cfg.Transport == TransportVertex {
		if sn.credErr != nil {
			return fmt.Errorf("anthropic vertex transport: service account unreadable: %w", sn.credErr)
		}
		return errors.New("anthropic vertex transport has no service account (GEMINI_VERTEX_SA_FILE)")
	}
	return errors.New("anthropic provider has no API key configured")
}

// ── generation ───────────────────────────────────────────────────────────────

// Chat runs one turn under the configured system prompt.
func (s *Service) Chat(ctx context.Context, message string, history []gemini.HistoryItem, language string) (gemini.ChatResult, error) {
	return s.ChatAs(ctx, message, history, language, "")
}

// ChatAs is Chat under a caller-supplied system prompt (a persona). An empty
// override means the configured prompt, as it does for the Gemini client.
func (s *Service) ChatAs(ctx context.Context, message string, history []gemini.HistoryItem, language, systemPrompt string) (gemini.ChatResult, error) {
	sn := s.snapshot()
	return s.send(ctx, sn, chatCall(sn.cfg, message, history, language, systemPrompt, false, nil))
}

// ChatStream is Chat with incremental text. onToken may be nil. The call streams
// either way: the shape of the request is decided by the method, not by whether a
// callback was passed.
func (s *Service) ChatStream(ctx context.Context, message string, history []gemini.HistoryItem, language string, onToken func(string)) (gemini.ChatResult, error) {
	sn := s.snapshot()
	return s.send(ctx, sn, chatCall(sn.cfg, message, history, language, "", true, onToken))
}

// GenerateFast is the auxiliary path with the default output budget.
func (s *Service) GenerateFast(ctx context.Context, prompt string, timeout time.Duration) (string, bool) {
	return s.GenerateFastMax(ctx, prompt, timeout, defaultMaxTokens)
}

// GenerateFastMax is the auxiliary path with an explicit output budget. Usage is
// reported for every call the API answered, even when the reply is unusable: the
// tokens were spent either way.
func (s *Service) GenerateFastMax(ctx context.Context, prompt string, timeout time.Duration, maxOutputTokens int) (string, bool) {
	if timeout <= 0 {
		timeout = auxTimeout
	}
	if maxOutputTokens <= 0 {
		maxOutputTokens = defaultMaxTokens
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sn := s.snapshot()
	res, err := s.send(ctx, sn, auxCall(prompt, maxOutputTokens))
	reportAuxUsage(ctx, sn.cfg.Model, res.PromptTokens, res.OutputTokens, res.CachedTokens)
	if err != nil {
		return "", false
	}
	reply := strings.TrimSpace(gemini.StripSourceMarkers(res.Reply))
	return reply, reply != ""
}

// reportAuxUsage forwards one auxiliary call's usage to the observer the platform
// installed, which attributes it to the tenant carried on the context. Nothing is
// reported for a call that never reached the API.
func reportAuxUsage(ctx context.Context, model string, prompt, completion, cached int) {
	if gemini.AuxUsageObserver != nil && prompt+completion > 0 {
		gemini.AuxUsageObserver(ctx, model, prompt, completion, cached)
	}
}

// call is one request as the caller means it, before it is shaped for a transport.
type call struct {
	message     string
	history     []gemini.HistoryItem
	system      string // the resolved system text; "" sends none
	maxTokens   int
	temperature *float64 // sent only where the model takes sampling parameters
	stream      bool
	onToken     func(string)
}

// chatCall resolves the system text the way the Gemini client does: a persona
// override wins, then the configured prompt, then the built-in default. The
// language directive is appended to whichever one won.
func chatCall(cfg Config, message string, history []gemini.HistoryItem, language, systemPrompt string, stream bool, onToken func(string)) call {
	system := systemPrompt
	if system == "" {
		system = cfg.SystemPrompt
	}
	if system == "" {
		system = gemini.DefaultSystemPrompt
	}
	return call{
		message:     message,
		history:     history,
		system:      system + "\n\n[Language Preference] " + gemini.LanguageLabel(language),
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
		stream:      stream,
		onToken:     onToken,
	}
}

// auxCall is one auxiliary prompt: a single user turn, no system text, and greedy
// sampling where the model takes it. The Gemini aux path sends temperature 0 for
// the same reason: classification and translation must not wander.
func auxCall(prompt string, maxOutputTokens int) call {
	zero := 0.0
	return call{message: prompt, maxTokens: maxOutputTokens, temperature: &zero}
}

// requestBody is one Messages API request, for either transport.
type requestBody struct {
	Model        string          `json:"model,omitempty"`             // direct transport only
	AnthropicVer string          `json:"anthropic_version,omitempty"` // Vertex only: the version travels in the body there
	MaxTokens    int             `json:"max_tokens"`
	System       string          `json:"system,omitempty"`
	Messages     []apiMessage    `json:"messages"`
	Temperature  *float64        `json:"temperature,omitempty"`
	Thinking     *thinkingConfig `json:"thinking,omitempty"`
	Stream       bool            `json:"stream,omitempty"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type thinkingConfig struct {
	Type string `json:"type"`
}

// buildBody shapes one call for the configured transport and model.
func buildBody(cfg Config, c call) requestBody {
	body := requestBody{
		MaxTokens: c.maxTokens,
		System:    c.system,
		Messages:  buildMessages(c.message, c.history),
		Stream:    c.stream,
	}
	if cfg.Transport == TransportVertex {
		body.AnthropicVer = vertexAnthropicVersion
	} else {
		body.Model = cfg.Model
	}
	if m, ok := Lookup(cfg.Model); ok {
		if m.Sampling {
			body.Temperature = c.temperature
		}
		if m.ThinkingOff {
			body.Thinking = &thinkingConfig{Type: "disabled"}
		}
	}
	return body
}

// buildMessages maps our turns onto the Messages API:
//
//   - "user" stays "user";
//   - "agent" (a staff reply) is a user turn carrying humanAgentMarker;
//   - "model" and "assistant" become "assistant", Gemini's name for our own turns;
//   - any other role has no place in this mapping and is dropped.
//
// Consecutive turns of one role are joined, so the sequence alternates on every
// transport: some platforms combine such turns themselves and others reject them.
func buildMessages(message string, history []gemini.HistoryItem) []apiMessage {
	out := make([]apiMessage, 0, len(history)+1)
	for _, h := range gemini.RepairHistoryShape(history) {
		switch h.Role {
		case "user":
			out = appendTurn(out, "user", h.Content)
		case "agent":
			out = appendTurn(out, "user", humanAgentMarker+h.Content)
		case "model", "assistant":
			out = appendTurn(out, "assistant", h.Content)
		}
	}
	if strings.TrimSpace(message) != "" {
		out = appendTurn(out, "user", message)
	}
	return out
}

func appendTurn(out []apiMessage, role, content string) []apiMessage {
	if n := len(out); n > 0 && out[n-1].Role == role {
		out[n-1].Content += "\n\n" + content
		return out
	}
	return append(out, apiMessage{Role: role, Content: content})
}

// send performs one request. Whether it streams is decided by the calling method,
// never by whether a callback is nil. Usage is returned whenever a body was parsed,
// so a caller can bill a call whose reply turned out empty.
func (s *Service) send(ctx context.Context, sn snapshot, c call) (gemini.ChatResult, error) {
	if !configured(sn) {
		return gemini.ChatResult{}, unconfiguredError(sn)
	}
	body := buildBody(sn.cfg, c)
	if len(body.Messages) == 0 {
		return gemini.ChatResult{}, errors.New("anthropic: nothing to send")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return gemini.ChatResult{}, err
	}
	endpoint, headers, err := endpointFor(ctx, sn, c.stream)
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
	resp, err := s.http.Do(req)
	if err != nil {
		return gemini.ChatResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return gemini.ChatResult{}, fmt.Errorf("anthropic %s: HTTP %d: %s", sn.cfg.Model, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var res gemini.ChatResult
	if c.stream {
		res, err = parseStream(resp.Body, c.onToken)
	} else {
		res, err = parseMessage(resp.Body)
	}
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(res.Reply) == "" {
		return res, errors.New("anthropic: response contained no text")
	}
	return res, nil
}

// endpointFor resolves the URL and auth headers for one call.
func endpointFor(ctx context.Context, sn snapshot, stream bool) (string, map[string]string, error) {
	cfg := sn.cfg
	if cfg.Transport != TransportVertex {
		base := strings.TrimRight(cfg.BaseURL, "/")
		if base == "" {
			base = defaultBaseURL
		}
		return base + "/v1/messages", map[string]string{
			"x-api-key":         cfg.APIKey,
			"anthropic-version": anthropicVersion,
		}, nil
	}
	if !SupportsRegion(cfg.Region) {
		return "", nil, fmt.Errorf("claude on vertex is served from global, us or eu, not %q", cfg.Region)
	}
	project := cfg.Project
	if project == "" && sn.creds != nil {
		project = sn.creds.project
	}
	if project == "" {
		return "", nil, errors.New("anthropic vertex transport needs a project: set GEMINI_VERTEX_PROJECT or use a service-account file that names one")
	}
	token, err := sn.creds.accessToken(ctx)
	if err != nil {
		return "", nil, err
	}
	verb := "rawPredict"
	if stream {
		verb = "streamRawPredict"
	}
	root := strings.TrimRight(cfg.BaseURLVX, "/")
	if root == "" {
		root = vertexRoot(cfg.Region)
	}
	u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
		root, project, cfg.Region, cfg.Model, verb)
	return u, map[string]string{"Authorization": "Bearer " + token}, nil
}

// vertexRoot is the host root for one location, without the /v1 segment.
//
// The host rule is the one cmd/claudeprobe measured and pins in its tests: global
// has no region prefix, and the multi-region locations use the rep host. The Gemini
// client's VertexHost does not cover us and eu, which is why Claude does not reuse
// it. GEMINI_VERTEX_API_BASE still wins, as it does for Gemini, so a relay sees the
// Claude traffic too.
func vertexRoot(region string) string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_VERTEX_API_BASE")); v != "" {
		return strings.TrimSuffix(strings.TrimRight(v, "/"), "/v1")
	}
	switch region {
	case "us", "eu":
		return "https://aiplatform." + region + ".rep.googleapis.com"
	default:
		return "https://aiplatform.googleapis.com"
	}
}

// ── response parsing ─────────────────────────────────────────────────────────

// usageCounts is the usage block of a Messages API response.
type usageCounts struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

// prompt is the whole input side of the call: the uncached input plus everything
// read from or written to the prompt cache. The platform's accounting subtracts the
// cached part from the prompt, so the cached part has to be inside this number.
func (u usageCounts) prompt() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

type apiResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage usageCounts `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseMessage(r io.Reader) (gemini.ChatResult, error) {
	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&parsed); err != nil {
		return gemini.ChatResult{}, fmt.Errorf("anthropic: unparseable response: %w", err)
	}
	if parsed.Error != nil {
		return gemini.ChatResult{}, fmt.Errorf("anthropic: %s: %s", parsed.Error.Type, parsed.Error.Message)
	}
	var sb strings.Builder
	for _, block := range parsed.Content {
		// Thinking blocks carry no "text" field, so they add nothing here; the
		// usage they cost is still in the counts below.
		if block.Type == "text" || block.Type == "" {
			sb.WriteString(block.Text)
		}
	}
	return gemini.ChatResult{
		Reply:        sb.String(),
		PromptTokens: parsed.Usage.prompt(),
		OutputTokens: parsed.Usage.OutputTokens,
		CachedTokens: parsed.Usage.CacheReadTokens,
	}, nil
}

// parseStream consumes the SSE stream. The event shapes absorbed here:
//
//	message_start       → usage before the first token
//	content_block_delta → delta.text, handed to onToken as it arrives
//	message_delta       → usage.output_tokens, the running total
//	message_stop        → the only event that ends a stream successfully
//	error               → a mid-stream failure, surfaced as an error
//
// A stream that ends without message_stop is a failure, not a short answer: the
// widget would otherwise show a truncated reply as complete.
func parseStream(r io.Reader, onToken func(string)) (gemini.ChatResult, error) {
	var out gemini.ChatResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	sawMessage := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // event: names, keep-alive blanks
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
				Usage usageCounts `json:"usage"`
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
			continue // keep-alives and unknown frames must not kill the turn
		}
		switch evt.Type {
		case "message_start":
			sawMessage = true
			out.PromptTokens = evt.Message.Usage.prompt()
			out.CachedTokens = evt.Message.Usage.CacheReadTokens
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
			return out, nil
		case "error":
			if evt.Error != nil {
				return out, fmt.Errorf("anthropic: %s: %s", evt.Error.Type, evt.Error.Message)
			}
			return out, errors.New("anthropic: stream error")
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("anthropic: stream read: %w", err)
	}
	if !sawMessage {
		return out, errors.New("anthropic: stream ended before any message")
	}
	return out, errors.New("anthropic: stream ended before completion")
}

// ── Vertex service-account token ─────────────────────────────────────────────

// tokenSource mints OAuth tokens from a Vertex service account. Its shape matches
// the Gemini client's (which is unexported); a copy here means the Claude transport
// does not reach into the Gemini client for a credential.
type tokenSource struct {
	email    string
	project  string
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
		ProjectID   string `json:"project_id"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse service account: %w", err)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("service account private_key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(block.Bytes)
		if rsaErr != nil {
			return nil, fmt.Errorf("parse service account key: %w", err)
		}
		parsed = rsaKey
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
	return &tokenSource{email: sa.ClientEmail, project: sa.ProjectID, key: key, tokenURI: uri}, nil
}

func (t *tokenSource) accessToken(ctx context.Context) (string, error) {
	if t == nil {
		return "", errors.New("anthropic vertex transport has no readable service account key")
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
	res, err := tokenHTTP.Do(req)
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
		return "", errors.New("oauth token exchange: unparseable response")
	}
	ttl := parsed.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	t.token = parsed.AccessToken
	t.expiry = now.Add(time.Duration(ttl) * time.Second)
	return t.token, nil
}
