// Package deepseek is the DeepSeek client: the third model provider this
// platform can serve generation from.
//
// DeepSeek speaks the OpenAI chat-completions format natively
// (https://api.deepseek.com/chat/completions, Authorization: Bearer). The
// generation surface here is the one internal/gemini already exposes (Chat,
// ChatAs, ChatStream, GenerateFast, GenerateFastMax, ModelName, IsConfigured),
// over the same gemini.HistoryItem and gemini.ChatResult types, so internal/llm
// routes a call to any provider without the callers learning a second API. What
// the Gemini client does for a turn is mirrored:
//
//   - the system prompt falls back to gemini.DefaultSystemPrompt when the
//     configured one is empty, and the reply language follows the customer
//     (gemini.LanguageLabel);
//   - a staff reply keeps its marker, and history is repaired with
//     gemini.RepairHistoryShape; trimming stays with the callers, as for Gemini;
//   - auxiliary calls (translation, classification, compile, gap drafts) send no
//     system prompt and report through gemini.AuxUsageObserver, so the cost
//     dashboard keeps seeing them.
//
// Where the chat-completions format differs from generateContent, this file
// absorbs it:
//
//   - system is the first message, not a top-level field;
//   - thinking is ON by default (thinking.type "enabled") and its tokens count
//     as output, so every catalogued model sets ThinkingOff and the request says
//     thinking.type "disabled";
//   - usage reports prompt_tokens INCLUDING the cached part
//     (prompt_tokens = prompt_cache_hit_tokens + prompt_cache_miss_tokens), which
//     is the shape the platform's cost arithmetic expects, so the cache fields
//     map straight onto PromptTokens/CachedTokens;
//   - the stream terminates with "data: [DONE]", and the token usage rides on
//     the last chunk before it — there is no separate usage-only chunk, and
//     stream_options is not needed to receive it.
package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"khmer-ai-cs-go/internal/gemini"
)

const (
	defaultBaseURL   = "https://api.deepseek.com"
	defaultModel     = "deepseek-flash"
	defaultMaxTokens = 2048
	requestTimeout   = 120 * time.Second
	auxTimeout       = 20 * time.Second

	// humanAgentMarker must match the Gemini client's. A staff reply reaches the
	// model as a user turn carrying this prefix, so the model can tell it from its
	// own earlier output.
	humanAgentMarker = "[Human agent reply] "
)

// Config is one immutable snapshot of a model config row. Reconfigure swaps it
// wholesale under the lock: a half-applied config (a new model with an old key)
// is the kind of fault that only shows up in production.
type Config struct {
	APIKey       string
	Model        string
	SystemPrompt string
	MaxTokens    int
	Temperature  *float64

	// BaseURL replaces the API root; /chat/completions is added here, not in the
	// value. It exists for tests and relays. Empty means DeepSeek's own host.
	BaseURL string
}

// withDefaults fills the fields a half-filled row would leave empty, so a row
// cannot call a model id of "" or send a zero max_tokens.
func withDefaults(cfg Config) Config {
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	return cfg
}

// Service is the DeepSeek client. Safe for concurrent use.
type Service struct {
	mu   sync.RWMutex
	cfg  Config
	http *http.Client
}

// snapshot is one consistent view of the configuration, taken once per call.
type snapshot struct {
	cfg Config
}

// New builds a client.
func New(cfg Config) *Service {
	s := &Service{http: &http.Client{Timeout: requestTimeout}}
	s.Reconfigure(cfg)
	return s
}

// Reconfigure replaces the configuration. The serving path calls it on every hot
// reload, so a new model, key or prompt takes effect on the next call without a
// restart.
func (s *Service) Reconfigure(cfg Config) {
	cfg = withDefaults(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// HotReload swaps the key, model, prompt and output budget in place, with the
// same shape as the Gemini client's.
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
	if apiKey != "" {
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
	return snapshot{cfg: s.cfg}
}

// ModelName is the model id in force.
func (s *Service) ModelName() string {
	return s.snapshot().cfg.Model
}

// IsConfigured reports whether this client can make a call.
func (s *Service) IsConfigured() bool {
	return configured(s.snapshot())
}

func configured(sn snapshot) bool {
	return strings.TrimSpace(sn.cfg.APIKey) != "" || sn.cfg.BaseURL != ""
}

// unconfiguredError is the one reason a turn cannot start: no key on the row.
func unconfiguredError() error {
	return errors.New("deepseek provider has no API key configured")
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

// call is one request as the caller means it, before it is shaped for the wire.
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
// sampling, as the Gemini aux path sends for the same reason: classification and
// translation must not wander.
func auxCall(prompt string, maxOutputTokens int) call {
	zero := 0.0
	return call{message: prompt, maxTokens: maxOutputTokens, temperature: &zero}
}

// requestBody is one chat-completions request. stream_options is deliberately
// absent: DeepSeek carries the usage numbers on the last chunk whether or not it
// is set, and asking for them on every chunk only adds null fields.
type requestBody struct {
	Model       string          `json:"model"`
	Messages    []apiMessage    `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	Thinking    *thinkingConfig `json:"thinking,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type thinkingConfig struct {
	Type string `json:"type"`
}

// buildBody shapes one call for the configured model.
func buildBody(cfg Config, c call) requestBody {
	body := requestBody{
		Model:     cfg.Model,
		Messages:  buildMessages(c.system, c.message, c.history),
		MaxTokens: c.maxTokens,
		Stream:    c.stream,
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

// buildMessages maps our turns onto the chat-completions message list:
//
//   - the system text is a leading "system" message (omitted when empty, which is
//     how auxiliary calls send none);
//   - "user" stays "user";
//   - "agent" (a staff reply) is a user turn carrying humanAgentMarker;
//   - "model" and "assistant" become "assistant", Gemini's name for our own turns;
//   - any other role has no place in this mapping and is dropped.
//
// Consecutive turns of one role are joined, so the sequence alternates after the
// system message: some platforms combine such turns themselves and others reject
// them.
func buildMessages(system, message string, history []gemini.HistoryItem) []apiMessage {
	out := make([]apiMessage, 0, len(history)+2)
	if strings.TrimSpace(system) != "" {
		out = append(out, apiMessage{Role: "system", Content: system})
	}
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

// hasTurn reports whether anything but the system message would be sent. A
// system-only body is a caller fault (an empty turn), not a request.
func hasTurn(messages []apiMessage) bool {
	for _, m := range messages {
		if m.Role != "system" {
			return true
		}
	}
	return false
}

// send performs one request. Whether it streams is decided by the calling method,
// never by whether a callback is nil. Usage is returned whenever a body was parsed,
// so a caller can bill a call whose reply turned out empty.
func (s *Service) send(ctx context.Context, sn snapshot, c call) (gemini.ChatResult, error) {
	if !configured(sn) {
		return gemini.ChatResult{}, unconfiguredError()
	}
	body := buildBody(sn.cfg, c)
	if !hasTurn(body.Messages) {
		return gemini.ChatResult{}, errors.New("deepseek: nothing to send")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return gemini.ChatResult{}, err
	}
	endpoint, headers := endpointFor(sn)
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
		return gemini.ChatResult{}, fmt.Errorf("deepseek %s: HTTP %d: %s", sn.cfg.Model, resp.StatusCode, strings.TrimSpace(string(raw)))
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
		return res, errors.New("deepseek: response contained no text")
	}
	return res, nil
}

// endpointFor resolves the URL and auth header for one call.
func endpointFor(sn snapshot) (string, map[string]string) {
	base := strings.TrimRight(sn.cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return base + "/chat/completions", map[string]string{
		"Authorization": "Bearer " + sn.cfg.APIKey,
	}
}

// ── response parsing ─────────────────────────────────────────────────────────

// usageCounts is the usage block of a chat-completions response. prompt_tokens is
// the whole input side and already includes the cache hits, which is exactly the
// convention the platform's accounting uses (the cost arithmetic subtracts the
// cached part from the prompt).
type usageCounts struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

// prompt is the whole input side of the call. prompt_tokens is documented to
// equal hit + miss, so the fallback only matters for a response that omitted it.
func (u usageCounts) prompt() int {
	if u.PromptTokens > 0 {
		return u.PromptTokens
	}
	return u.PromptCacheHitTokens + u.PromptCacheMissTokens
}

func (u usageCounts) tokens() int { return u.prompt() + u.CompletionTokens }

// apiError is the error object of either a failed response or a mid-stream frame.
type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (e *apiError) Error() string {
	if e.Type != "" {
		return e.Type + ": " + e.Message
	}
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

type apiResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage usageCounts `json:"usage"`
	Error *apiError   `json:"error"`
}

func parseMessage(r io.Reader) (gemini.ChatResult, error) {
	var parsed apiResponse
	if err := json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&parsed); err != nil {
		return gemini.ChatResult{}, fmt.Errorf("deepseek: unparseable response: %w", err)
	}
	if parsed.Error != nil {
		return gemini.ChatResult{}, fmt.Errorf("deepseek: %w", parsed.Error)
	}
	var sb strings.Builder
	for _, choice := range parsed.Choices {
		// reasoning_content carries the model's private thinking, not the answer:
		// it is billed as output and must never reach a customer.
		sb.WriteString(choice.Message.Content)
	}
	return gemini.ChatResult{
		Reply:        sb.String(),
		PromptTokens: parsed.Usage.prompt(),
		OutputTokens: parsed.Usage.CompletionTokens,
		CachedTokens: parsed.Usage.PromptCacheHitTokens,
	}, nil
}

// parseStream consumes the SSE stream. The frames absorbed here:
//
//	choices[].delta.content → handed to onToken as it arrives
//	usage                   → the last chunk before [DONE] carries the totals
//	data: [DONE]            → the only frame that ends a stream successfully
//	error                   → a mid-stream failure, surfaced as an error
//
// A stream that ends without [DONE] is a failure, not a short answer: the widget
// would otherwise show a truncated reply as complete.
func parseStream(r io.Reader, onToken func(string)) (gemini.ChatResult, error) {
	var out gemini.ChatResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	sawChunk := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // keep-alive comments and blank separators
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" {
			continue
		}
		if raw == "[DONE]" {
			return out, nil
		}
		var evt struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *usageCounts `json:"usage"`
			Error *apiError    `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &evt); err != nil {
			continue // keep-alives and unknown frames must not kill the turn
		}
		sawChunk = true
		if evt.Error != nil {
			return out, fmt.Errorf("deepseek: %w", evt.Error)
		}
		for _, choice := range evt.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			out.Reply += choice.Delta.Content
			if onToken != nil {
				onToken(choice.Delta.Content)
			}
		}
		// Usage rides on the last content chunk (finish_reason set), and on every
		// chunk when the caller asked for include_usage. Reading it wherever it is
		// non-zero covers both shapes.
		if evt.Usage != nil && evt.Usage.tokens() > 0 {
			out.PromptTokens = evt.Usage.prompt()
			out.CachedTokens = evt.Usage.PromptCacheHitTokens
			out.OutputTokens = evt.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("deepseek: stream read: %w", err)
	}
	if !sawChunk {
		return out, errors.New("deepseek: stream ended before any chunk")
	}
	return out, errors.New("deepseek: stream ended before completion")
}

var _ interface {
	Chat(context.Context, string, []gemini.HistoryItem, string) (gemini.ChatResult, error)
	ChatAs(context.Context, string, []gemini.HistoryItem, string, string) (gemini.ChatResult, error)
	ChatStream(context.Context, string, []gemini.HistoryItem, string, func(string)) (gemini.ChatResult, error)
	GenerateFast(context.Context, string, time.Duration) (string, bool)
	GenerateFastMax(context.Context, string, time.Duration, int) (string, bool)
	ModelName() string
	IsConfigured() bool
} = (*Service)(nil)
