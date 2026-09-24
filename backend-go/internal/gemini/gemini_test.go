package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDetectLanguageByScript(t *testing.T) {
	if got := DetectLanguage("你好, 请问"); got != "zh" {
		t.Errorf("Chinese text → %q, want zh", got)
	}
	if got := DetectLanguage("សួស្តី"); got != "km" {
		t.Errorf("Khmer text → %q, want km", got)
	}
	if got := DetectLanguage("hello there"); got != "en" {
		t.Errorf("English text → %q, want en", got)
	}
	if got := DetectLanguage("1234 !!"); got != "" {
		t.Errorf("digit-only text → %q, want empty", got)
	}
	if got := DetectLanguage("sok sabay បាទ"); got != "km" {
		t.Errorf("mixed Khmer/latin → %q, want km (Khmer script present)", got)
	}
}

func TestStripSourceMarkers(t *testing.T) {
	cases := map[string]string{
		"price is $15 [Source 1].":        "price is $15.",
		"[Source 2] the answer":           "the answer",
		"no markers here":                 "no markers here",
		"see [source 3] and [Source 12].": "see and.",
		"[Source 1]":                      "",
		// Paren and full-width variants.
		"**eps-16kg**：15 美元 (Source 2)":           "**eps-16kg**：15 美元",
		"基础版：99 美元（Source 1）。":                    "基础版：99 美元。",
		"含高棉语支持 (Source 1, Source 2)。":            "含高棉语支持。",
		"价格见 (Source 1、Source 2)。":                "价格见。",
		"contact us (see brochure)":               "contact us (see brochure)",
		"价格表\n\n产品价格表":                            "价格表\n\n产品价格表",
		"15 美元 (source 3) [Source 4]（Source 5）结束": "15 美元 结束",
	}
	for in, want := range cases {
		if got := StripSourceMarkers(in); got != want {
			t.Errorf("StripSourceMarkers(%q) = %q, want %q", in, got, want)
		}
	}
	// Markdown bold must survive; bracketed non-source labels must survive.
	if got := StripSourceMarkers("**bold** [Note 1]"); !strings.Contains(got, "[Note 1]") {
		t.Errorf("non-source bracket labels must survive: %q", got)
	}
}

func TestFormatVector(t *testing.T) {
	got := FormatVector([]float32{0.1, 1.23456789})
	if !strings.HasPrefix(got, "[0.100000,") || !strings.HasSuffix(got, "]") {
		t.Errorf("FormatVector = %s", got)
	}
}

func TestMockEmbeddingDeterministic768(t *testing.T) {
	a, b := MockEmbedding(), MockEmbedding()
	if len(a) != 768 || len(b) != 768 {
		t.Fatal("mock embedding must be 768-dim")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("mock embedding must be deterministic")
		}
	}
}

func TestParseScoreArray(t *testing.T) {
	reply := "Here you go: [{\"i\":1,\"score\":8},{\"i\":2,\"score\":1}] thanks"
	scores := parseScoreArray(reply, 2)
	if scores == nil {
		t.Fatal("scores must parse")
	}
	if scores[0] != 8 || scores[1] != 1 {
		t.Fatalf("scores = %v", scores)
	}
	if got := parseScoreArray("no json here", 2); got != nil {
		t.Fatal("garbage must return nil")
	}
	if got := parseScoreArray("[{\"i\":9,\"score\":5}]", 2); got != nil {
		t.Fatal("out-of-range-only entries must return nil")
	}
}

func TestNormalizeModelName(t *testing.T) {
	if got := NormalizeModelName("models/gemini-2.5-flash"); got != "gemini-2.5-flash" {
		t.Errorf("normalize = %q", got)
	}
	if got := NormalizeModelName("gemini-2.5-flash"); got != "gemini-2.5-flash" {
		t.Errorf("plain name must pass through: %q", got)
	}
}

func TestBuildRequestBodyMarksAgentTurns(t *testing.T) {
	s := &Service{}
	body := s.buildRequestBody("hello", []HistoryItem{
		{Role: "user", Content: "q1"},
		{Role: "model", Content: "a1"},
		{Role: "agent", Content: "human said this"},
	}, "km", "")
	contents, ok := body["contents"].([]map[string]any)
	if !ok || len(contents) != 4 {
		t.Fatalf("contents = %v, want 4 turns", body["contents"])
	}
	text := func(c map[string]any) string {
		return c["parts"].([]map[string]any)[0]["text"].(string)
	}
	if contents[2]["role"] != "user" || !strings.HasPrefix(text(contents[2]), "[Human agent reply] ") {
		t.Errorf("agent turn not marked as human: role=%v text=%q", contents[2]["role"], text(contents[2]))
	}
	if contents[1]["role"] != "model" || text(contents[1]) != "a1" {
		t.Errorf("model turn altered: %v %q", contents[1]["role"], text(contents[1]))
	}
	if last := contents[3]; last["role"] != "user" || text(last) != "hello" {
		t.Errorf("current message must be appended once verbatim: %v %q", last["role"], text(last))
	}
}

func TestPostWithRetryFailsFastOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"You exceeded your spend-based rate limit"}}`))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_FAST_MODEL", "")

	s := New("test-key", "gemini-test", 128)
	_, err := s.Chat(context.Background(), "hi", nil, "en")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("want a 429 error, got %v", err)
	}
	// A spend stop outlives any backoff worth waiting on: one attempt, no
	// duplicates via the fast-model fallback either.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("429 must fail fast: %d upstream requests, want 1", got)
	}
}

func TestPostWithRetryStillRetries5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"transient"}}`))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_FAST_MODEL", "")

	s := New("test-key", "gemini-test", 128)
	if _, err := s.Chat(context.Background(), "hi", nil, "en"); err == nil {
		t.Fatal("5xx must surface as an error")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("5xx must still retry 3 times: %d requests", got)
	}
}

func TestUsageFromValueFoldsThinkingTokens(t *testing.T) {
	v := map[string]any{"usageMetadata": map[string]any{
		"promptTokenCount":        float64(1000),
		"candidatesTokenCount":    float64(120),
		"thoughtsTokenCount":      float64(800),
		"cachedContentTokenCount": float64(300),
	}}
	prompt, completion, cached := usageFromValue(v)
	if prompt != 1000 || cached != 300 {
		t.Fatalf("prompt/cached = %d/%d, want 1000/300", prompt, cached)
	}
	// Thinking tokens are billed at the output rate, so they belong in the
	// completion count the cost estimate is built from.
	if completion != 920 {
		t.Errorf("completion = %d, want 920 (candidates 120 + thoughts 800)", completion)
	}
}

func TestBuildRequestBodyWithCachedContent(t *testing.T) {
	s := &Service{}
	body := s.buildRequestBody("hi", nil, "en", "cachedContents/abc")
	if body["cachedContent"] != "cachedContents/abc" {
		t.Errorf("cachedContent missing: %v", body["cachedContent"])
	}
	// The instruction lives inside the cache; sending it again is rejected.
	if _, present := body["systemInstruction"]; present {
		t.Error("systemInstruction must be omitted when a cache is referenced")
	}
	plain := s.buildRequestBody("hi", nil, "en", "")
	if _, present := plain["systemInstruction"]; !present {
		t.Error("uncached requests must carry the system instruction")
	}
	if _, present := plain["cachedContent"]; present {
		t.Error("uncached requests must not reference a cache")
	}
}

func TestThinkingBudgetKnob(t *testing.T) {
	s := &Service{}
	t.Setenv("GEMINI_THINKING_BUDGET", "")
	gen := s.buildRequestBody("hi", nil, "en", "")["generationConfig"].(map[string]any)
	if _, present := gen["thinkingConfig"]; present {
		t.Error("unset GEMINI_THINKING_BUDGET must not pin a budget")
	}
	t.Setenv("GEMINI_THINKING_BUDGET", "0")
	gen = s.buildRequestBody("hi", nil, "en", "")["generationConfig"].(map[string]any)
	tc, ok := gen["thinkingConfig"].(map[string]any)
	if !ok || tc["thinkingBudget"] != 0 {
		t.Errorf("thinkingConfig = %v, want budget 0", gen["thinkingConfig"])
	}
}

func TestContextCacheOffAndGuarded(t *testing.T) {
	// The plugin is off unless GEMINI_CACHE_TTL says otherwise: a live cache
	// costs $1/1M tokens/hour, which only pays above ~3.7 requests/hour.
	t.Setenv("GEMINI_CACHE_TTL", "")
	s := New("test-key", "gemini-test", 128)
	if name := s.contextCacheFor(context.Background(), "km"); name != "" {
		t.Errorf("caching must be off by default: %q", name)
	}

	// Switched on, but with a prefix below the API minimum: still nothing —
	// and no request may leave the process (the base points at a server that
	// fails the test if it is reached).
	hit := int32(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"cachedContents/x"}`))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_CACHE_TTL", "3600")
	t.Setenv("GEMINI_API_BASE", srv.URL)
	short := &Service{apiKey: "k", modelName: "gemini-test", systemPrompt: "be nice"}
	if name := short.contextCacheFor(context.Background(), "km"); name != "" {
		t.Errorf("short prefix must not be registered: %q", name)
	}
	if got := atomic.LoadInt32(&hit); got != 0 {
		t.Errorf("a guarded-off cache must not call the API (%d requests)", got)
	}
}

func TestEstimateTokensScriptAware(t *testing.T) {
	// Khmer/CJK ≈ one token per 2 runes; Latin ≈ one per 5.
	khmer := strings.Repeat("ក", 400)
	latin := strings.Repeat("x", 400)
	if got := estimateTokens(khmer); got != 200 {
		t.Errorf("Khmer estimate = %d, want 200", got)
	}
	if got := estimateTokens(latin); got != 80 {
		t.Errorf("Latin estimate = %d, want 80", got)
	}
	// The estimator must sit at or below the API's own count, or the guard
	// waves through registrations the API rejects. Measured production prompt:
	// 4387 runes (4278 latin / 109 khmer+cjk) → API says 984.
	if got := estimateTokens(strings.Repeat("a", 4278) + strings.Repeat("ក", 109)); got > 984 {
		t.Errorf("estimate %d exceeds the measured API count 984 — the guard would attempt a rejected registration", got)
	}
}

func TestContextCacheRemembersRegistrationFailure(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Cached content is too small. total_token_count=984, min_total_token_count=1024"}}`))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_CACHE_TTL", "3600")
	t.Setenv("GEMINI_CACHE_MIN_TOKENS", "0")

	s := New("test-key", "gemini-test", 128)
	for i := 0; i < 5; i++ {
		if name := s.contextCacheFor(context.Background(), "km"); name != "" {
			t.Fatalf("registration must fail here, got %q", name)
		}
	}
	// Five turns, one attempt: a prefix the API refuses must not cost a
	// rejected round trip on every request.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("registration attempted %d times, want 1 (failures must be remembered)", got)
	}

	// A zero backoff models recovery (a grown prompt, a transient error):
	// the next turn tries again rather than staying off forever.
	t.Setenv("GEMINI_CACHE_FAIL_BACKOFF_SEC", "0")
	if name := s.contextCacheFor(context.Background(), "km"); name != "" {
		t.Errorf("unexpected success: %q", name)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("after the backoff window the registration must be retried: %d attempts", got)
	}
}

func TestContextCacheStaleDetection(t *testing.T) {
	if !contextCacheStale(400, `{"error":{"message":"Cached content not found"}}`) {
		t.Error("a 400 mentioning cached content is stale")
	}
	if contextCacheStale(500, "cached content") {
		t.Error("5xx is a transport problem, not a stale cache")
	}
	if contextCacheStale(429, "cachedContent quota") {
		t.Error("429 must not be mistaken for a stale cache")
	}
}

func TestTrimHistoryBudgetDropsOldestKeepsRecent(t *testing.T) {
	big := strings.Repeat("x", 10000)
	items := []HistoryItem{
		{Role: "user", Content: big},
		{Role: "model", Content: big},
		{Role: "user", Content: big},
		{Role: "model", Content: big},
		{Role: "user", Content: "recent1"},
		{Role: "model", Content: "recent2"},
	}
	got := TrimHistoryBudget(items)
	if len(got) >= len(items) {
		t.Fatalf("over-budget history was not trimmed: %d items", len(got))
	}
	for i, h := range got[len(got)-2:] {
		want := []string{"recent1", "recent2"}[i]
		if h.Content != want {
			t.Errorf("newest turns must survive trimming: got %q want %q", h.Content, want)
		}
	}
	// Even an all-huge history keeps the four newest items.
	allBig := []HistoryItem{}
	for i := 0; i < 6; i++ {
		allBig = append(allBig, HistoryItem{Role: "user", Content: big})
	}
	if got := TrimHistoryBudget(allBig); len(got) != 4 {
		t.Errorf("floor of 4 newest items violated: %d items", len(got))
	}
	// Under-budget history passes through untouched.
	small := []HistoryItem{{Role: "user", Content: "a"}, {Role: "model", Content: "b"}}
	if got := TrimHistoryBudget(small); len(got) != 2 {
		t.Errorf("small history must not be trimmed: %d items", len(got))
	}
}
