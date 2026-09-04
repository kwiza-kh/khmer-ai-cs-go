// Package gemini wraps the Gemini REST API (generateContent / embedContent)
// with the same endpoints and fallback behaviour as the Rust backend: real
// calls when an API key is configured, deterministic mocks otherwise.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	// FastModel routes auxiliary calls (rewrite/rerank) to a cheap model.
	FastModel                = "gemini-2.5-flash"
	embeddingVectorDimension = 768
)

// apiBase — Gemini REST endpoint. Override with GEMINI_API_BASE to route
// through a relay in a Google-supported region when the server's egress IP
// is geo-blocked ("User location is not supported for the API use").
// Value must include the /v1beta version segment, no trailing slash.
var apiBase = func() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://generativelanguage.googleapis.com/v1beta"
}()

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
	UsedMock     bool
}

// Service is the Gemini client (thread-safe).
type Service struct {
	client       *http.Client
	apiKey       string
	modelName    string
	systemPrompt string
	maxTokens    int

	mu         sync.Mutex
	embedCache map[string]embedCacheEntry
}

type embedCacheEntry struct {
	at  time.Time
	vec []float32
}

// New builds a service from env-style config values. Empty key → mock mode.
func New(apiKey, model string, maxTokens int) *Service {
	s := &Service{
		apiKey:       apiKey,
		modelName:    NormalizeModelName(model),
		systemPrompt: DefaultSystemPrompt,
		maxTokens:    maxTokens,
		embedCache:   make(map[string]embedCacheEntry),
	}
	if apiKey != "" {
		s.client = &http.Client{Timeout: 60 * time.Second}
	}
	return s
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

func (s *Service) IsConfigured() bool { return s.client != nil }

func (s *Service) ModelName() string { return s.modelName }

func (s *Service) SetModelName(name string) {
	if name != "" {
		s.modelName = NormalizeModelName(name)
	}
}

func (s *Service) SetSystemPrompt(prompt string) { s.systemPrompt = prompt }

// HotReload swaps the serving client's credentials/model/prompt in place so
// admin edits apply without a restart. Empty key leaves the client in place.
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

// ListModels returns the generative model names available for the key.
func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	u := fmt.Sprintf("%s/models?pageSize=200&key=%s", apiBase, url.QueryEscape(apiKey))
	resp, err := client.Get(u)
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, m := range v.Models {
		n := strings.TrimPrefix(m.Name, "models/")
		if strings.Contains(strings.ToLower(n), "gemini") {
			names = append(names, n)
		}
	}
	return names, nil
}

func fastModel() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_FAST_MODEL")); v != "" {
		return v
	}
	return FastModel
}

func (s *Service) generateURLFor(model string) string {
	return fmt.Sprintf("%s/models/%s:generateContent?key=%s", apiBase, NormalizeModelName(model), url.QueryEscape(s.apiKey))
}

func (s *Service) embedURL() string {
	return fmt.Sprintf("%s/models/%s:embedContent?key=%s", apiBase, EmbeddingModel, url.QueryEscape(s.apiKey))
}

// postWithRetry posts JSON, retrying 5xx/429/transport errors up to 3 times
// (the path to Google drops some handshakes; one retry usually gets through).
func (s *Service) postWithRetry(ctx context.Context, target string, body any) (int, string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, "", fmt.Errorf("marshal request: %w", err)
	}
	lastErr := ""
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
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Sprintf("request: %v", err)
			continue
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		text := buf.String()
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
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

func (s *Service) chatWithModel(ctx context.Context, message string, history []HistoryItem, language, model string) (ChatResult, error) {
	if !s.IsConfigured() {
		return s.chatMock(message, language), nil
	}
	if model == "" {
		model = s.modelName
	}
	body := s.buildRequestBody(message, history, language)
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(model), body)
	if err == nil && (status >= 500 || status == http.StatusTooManyRequests) && model == s.modelName {
		// Degrade to the fast model once on overload.
		status, text, err = s.postWithRetry(ctx, s.generateURLFor(fastModel()), body)
	}
	if err != nil {
		return s.chatMock(message, language), nil
	}
	if status != http.StatusOK {
		return s.chatMock(message, language), nil
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return s.chatMock(message, language), nil
	}
	return s.resultFromValue(v), nil
}

func (s *Service) buildRequestBody(message string, history []HistoryItem, language string) map[string]any {
	system := s.systemPrompt
	if system == "" {
		system = DefaultSystemPrompt
	}
	system = system + "\n\n[Language Preference] " + LanguageLabel(language)
	contents := make([]map[string]any, 0, len(history)+1)
	for _, h := range history {
		role := h.Role
		if role != "user" {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]any{{"text": h.Content}},
		})
	}
	contents = append(contents, map[string]any{
		"role":  "user",
		"parts": []map[string]any{{"text": message}},
	})
	return map[string]any{
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": system}},
		},
		"contents": contents,
		"generationConfig": map[string]any{
			"maxOutputTokens": s.maxTokens,
		},
	}
}

func (s *Service) resultFromValue(v map[string]any) ChatResult {
	res := ChatResult{Reply: ExtractTextFromValue(v)}
	usage, _ := v["usageMetadata"].(map[string]any)
	if usage != nil {
		if n, ok := usage["promptTokenCount"].(float64); ok {
			res.PromptTokens = int(n)
		}
		if n, ok := usage["candidatesTokenCount"].(float64); ok {
			res.OutputTokens = int(n)
		}
	}
	return res
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
	if !s.IsConfigured() {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]any{{"text": prompt}},
		}},
		"generationConfig": map[string]any{"temperature": 0.0, "maxOutputTokens": 2048},
	}
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(fastModel()), body)
	if err != nil || status != http.StatusOK {
		return "", false
	}
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil {
		return "", false
	}
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

// GenerateQueryEmbedding — query-time vector, cached 5 minutes.
func (s *Service) GenerateQueryEmbedding(ctx context.Context, text string) ([]float32, error) {
	key := strings.ToLower(strings.TrimSpace(text))
	s.mu.Lock()
	if entry, ok := s.embedCache[key]; ok && time.Since(entry.at) < 5*time.Minute {
		s.mu.Unlock()
		return entry.vec, nil
	}
	s.mu.Unlock()
	vec, err := s.embed(ctx, text, "RETRIEVAL_QUERY")
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
	body := map[string]any{
		"model":              "models/" + EmbeddingModel,
		"content":            map[string]any{"parts": []map[string]any{{"text": text}}},
		"taskType":           taskType,
		"outputDimensionality": embeddingVectorDimension,
	}
	status, respText, err := s.postWithRetry(ctx, s.embedURL(), body)
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
	values := digArray(v, "embedding", "values")
	if len(values) != embeddingVectorDimension {
		return nil, fmt.Errorf("Gemini returned an invalid embedding")
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
func (s *Service) TranscribeAudio(ctx context.Context, audio []byte, mimeType string) (string, error) {
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
				{"text": "Transcribe this audio verbatim. Output ONLY the transcribed text in its original script — no labels, no translation, no commentary, no quotes."},
				{"inlineData": map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(audio)}},
			},
		}},
	}
	status, text, err := s.postWithRetry(ctx, s.generateURLFor(s.modelName), body)
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
	return strings.TrimSpace(ExtractTextFromValue(v)), nil
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

// StripSourceMarkers removes "[Source N]" citation leftovers from replies.
func StripSourceMarkers(text string) string {
	var out []rune
	runes := []rune(text)
	for i := 0; i < len(runes); {
		if runes[i] == '[' {
			j := i + 1
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
				j++
			}
			rest := string(runes[j:])
			if strings.HasPrefix(strings.ToLower(rest), "source") {
				k := j + len("source")
				for k < len(runes) && runes[k] == ' ' {
					k++
				}
				digits := 0
				for k < len(runes) && runes[k] >= '0' && runes[k] <= '9' {
					k++
					digits++
				}
				for k < len(runes) && (runes[k] == ' ' || runes[k] == '\t') {
					k++
				}
				if digits > 0 && k < len(runes) && runes[k] == ']' {
					// Confirmed marker — trim spaces emitted before it.
					for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == '\t') {
						out = out[:len(out)-1]
					}
					i = k + 1
					continue
				}
			}
		}
		out = append(out, runes[i])
		i++
	}
	return strings.TrimSpace(string(out))
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
