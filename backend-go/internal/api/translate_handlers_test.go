package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"khmer-ai-cs-go/internal/redisstore"
)

// The batch endpoint hands the model a JSON array and expects a JSON array
// back. Models sometimes wrap it in prose or fences, and occasionally merge
// entries — the parser must degrade predictably so callers can fall back.
func TestParseTranslationArray(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  int
		first string
	}{
		{"plain", `["a","b","c"]`, 3, "a"},
		{"fenced", "```json\n[\"你好\",\"再见\"]\n```", 2, "你好"},
		{"with prose", "Here you go:\n[\"x\",\"y\"]\nHope that helps!", 2, "x"},
		{"khmer", `["សួស្តី","អរគុណ"]`, 2, "សួស្តី"},
		{"escaped newline", `["line1\nline2"]`, 1, "line1\nline2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseTranslationArray(c.reply, c.want)
			if len(got) != c.want {
				t.Fatalf("len = %d (%v), want %d", len(got), got, c.want)
			}
			if got[0] != c.first {
				t.Fatalf("first = %q, want %q", got[0], c.first)
			}
		})
	}

	// Non-JSON replies fall back to line splitting only when the count matches.
	if got := parseTranslationArray("one\ntwo\nthree", 3); len(got) != 3 {
		t.Fatalf("line fallback: %v", got)
	}
	if got := parseTranslationArray("just one line", 3); got != nil {
		t.Fatalf("mismatched count must return nil, got %v", got)
	}
	if got := parseTranslationArray("", 1); got != nil {
		t.Fatalf("empty reply must return nil, got %v", got)
	}
}

func TestResolveTranslateTarget(t *testing.T) {
	// Explicit valid target wins.
	if got := resolveTranslateTarget("សួស្តី", "en"); got != "en" {
		t.Errorf("explicit target = %q", got)
	}
	// Unset target: Khmer source → Chinese for the (Chinese-speaking) agent.
	if got := resolveTranslateTarget("តម្លៃប៉ុន្មាន", ""); got != "zh" {
		t.Errorf("khmer source default = %q, want zh", got)
	}
	// Unset target, non-Khmer source → Khmer (the customer's likely language).
	if got := resolveTranslateTarget("how much is it", ""); got != "km" {
		t.Errorf("non-khmer source default = %q, want km", got)
	}
	// Unknown code is treated as unset rather than rejected.
	if got := resolveTranslateTarget("hello", "xx"); got != "km" {
		t.Errorf("unknown target = %q, want km", got)
	}
}

func TestTranslateCacheKeyStable(t *testing.T) {
	a := translateCacheKey(7, "hello", "km")
	b := translateCacheKey(7, "hello", "km")
	if a != b {
		t.Fatal("cache key must be deterministic")
	}
	if a == translateCacheKey(7, "hello", "zh") {
		t.Fatal("different targets must key differently")
	}
	if a == translateCacheKey(7, "hello!", "km") {
		t.Fatal("different text must key differently")
	}
	// A prompt edit must invalidate every entry at once: the version is the one
	// part of the key that changes wholesale with the prompt.
	if !strings.Contains(a, translatePromptVersion) {
		t.Fatalf("key %q must carry the prompt version", a)
	}
	if a == translateCacheKey(8, "hello", "km") {
		t.Fatal("another tenant must not read this tenant's cache")
	}
}

// --- fakes ------------------------------------------------------------------

// scriptedModel answers a fixed queue of replies, and records what it was sent.
type scriptedModel struct {
	mu      sync.Mutex
	replies []string
	ok      []bool
	prompts []string
	budgets []int
}

func (s *scriptedModel) GenerateFastMax(_ context.Context, prompt string, _ time.Duration, maxOutputTokens int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	s.budgets = append(s.budgets, maxOutputTokens)
	reply := ""
	if len(s.replies) > 0 {
		reply = s.replies[0]
		s.replies = s.replies[1:]
	}
	if len(s.ok) > 0 {
		serve := s.ok[0]
		s.ok = s.ok[1:]
		if !serve {
			return "", false
		}
	}
	if reply == "" {
		return "", false
	}
	return reply, true
}

func (s *scriptedModel) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

func (s *scriptedModel) promptAt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.prompts) {
		return ""
	}
	return s.prompts[i]
}

func (s *scriptedModel) budgetAt(i int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.budgets) {
		return 0
	}
	return s.budgets[i]
}

// echoModel reads the JSON array out of the prompt and answers one translated
// string per entry, so a test can assert on chunking and ordering without
// hard-coding the chunk boundaries.
type echoModel struct {
	mu      sync.Mutex
	prompts []string
	budgets []int
	failAll bool
}

func (e *echoModel) GenerateFastMax(_ context.Context, prompt string, _ time.Duration, maxOutputTokens int) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prompts = append(e.prompts, prompt)
	e.budgets = append(e.budgets, maxOutputTokens)
	if e.failAll {
		return "", false
	}
	cut := strings.LastIndex(prompt, "\n[")
	if cut < 0 {
		return "", false
	}
	var texts []string
	if json.Unmarshal([]byte(strings.TrimSpace(prompt[cut+1:])), &texts) != nil {
		return "", false
	}
	out := make([]string, len(texts))
	for i, text := range texts {
		out[i] = "T(" + text + ")"
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func (e *echoModel) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.prompts)
}

func (e *echoModel) budgetAt(i int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.budgets) {
		return 0
	}
	return e.budgets[i]
}

func newTranslateApp(t *testing.T) *App {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := redisstore.Connect(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis connect: %v", err)
	}
	return &App{Redis: c}
}

// --- pipeline tests ---------------------------------------------------------

// TestTranslateAllDedupesAndKeepsOrder is the base contract: one call, one
// rendering per distinct text, and every duplicate position filled from it.
func TestTranslateAllDedupesAndKeepsOrder(t *testing.T) {
	app := newTranslateApp(t)
	m := &scriptedModel{replies: []string{`["Hello","How much?"]`}}

	got, verified, skipped := app.translateAll(context.Background(), m,
		[]string{"សួស្តី", "តម្លៃប៉ុន្មាន", "សួស្តី"}, "en")

	want := []string{"Hello", "How much?", "Hello"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
		if !verified[i] {
			t.Fatalf("index %d must be verified", i)
		}
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v", skipped)
	}
	if m.calls() != 1 {
		t.Fatalf("model calls = %d, want 1 (duplicates must not be re-sent)", m.calls())
	}
}

// TestTranslateChunkRetriesOnlyTheGap — a model that drops an entry must cost
// one small retry, not one call per item: the old fallback issued a request per
// missing message.
func TestTranslateChunkRetriesOnlyTheGap(t *testing.T) {
	app := newTranslateApp(t)
	m := &scriptedModel{replies: []string{`["one","two"]`, `["three"]`}}

	got, _, _ := app.translateAll(context.Background(), m, []string{"a", "b", "c"}, "km")

	if got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Fatalf("got %v", got)
	}
	if m.calls() != 2 {
		t.Fatalf("model calls = %d, want 2 (batch + one gap retry)", m.calls())
	}
	retry := m.promptAt(1)
	if !strings.Contains(retry, `["c"]`) {
		t.Fatalf("retry must ask only for the dropped entry, got prompt:\n%s", retry)
	}
	if strings.Contains(retry, `"a"`) || strings.Contains(retry, `"b"`) {
		t.Fatalf("retry must not re-send entries that already resolved, got prompt:\n%s", retry)
	}
}

// TestTranslateChunkSplitsLargeBatches — 45 texts cost three calls (20/20/5),
// and each call budgets well over the 2048-token default that used to truncate
// a long batch into invalid JSON.
func TestTranslateChunkSplitsLargeBatches(t *testing.T) {
	app := newTranslateApp(t)
	texts := make([]string, 45)
	for i := range texts {
		texts[i] = "សារអតិថិជន " + strconv.Itoa(i) + " " + strings.Repeat("x", 40)
	}
	m := &echoModel{}

	got, verified, _ := app.translateAll(context.Background(), m, texts, "en")

	if m.calls() != 3 {
		t.Fatalf("model calls = %d, want 3 chunks", m.calls())
	}
	for i := 0; i < m.calls(); i++ {
		if b := m.budgetAt(i); b <= 2048 {
			t.Fatalf("call %d budget = %d; the old fixed 2048 is what truncated long batches", i, b)
		}
	}
	for i, g := range got {
		if g != "T("+texts[i]+")" {
			t.Fatalf("index %d = %q", i, g)
		}
		if !verified[i] {
			t.Fatalf("index %d must be verified", i)
		}
	}
}

// TestTranslateAllSkipsSameLanguage — a rendering nobody needs must not cost a
// model call: the console defaults the read language to the agent's own, so
// Khmer→Khmer is the normal case for a Khmer-speaking agent, not an edge case.
func TestTranslateAllSkipsSameLanguage(t *testing.T) {
	app := newTranslateApp(t)
	m := &scriptedModel{replies: []string{`["តម្លៃប៉ុន្មាន?"]`}}

	// The English messages are already in the target language: they come back
	// unchanged and never reach the model. Only the Khmer one is worth a call.
	got, verified, skipped := app.translateAll(context.Background(), m,
		[]string{"hello there", "how much is it?"}, "en")

	for i, want := range []string{"hello there", "how much is it?"} {
		if got[i] != want {
			t.Fatalf("same-language text %d must come back unchanged, got %q", i, got[i])
		}
		if !verified[i] {
			t.Fatalf("an unchanged rendering carries no digit risk (index %d)", i)
		}
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v", skipped)
	}
	if m.calls() != 0 {
		t.Fatalf("model calls = %d, want 0", m.calls())
	}

	// Mixed batch: the Khmer message is the only one that costs a call.
	mixed := &scriptedModel{replies: []string{`["How much is it?"]`}}
	got2, _, _ := app.translateAll(context.Background(), mixed,
		[]string{"hello there", "តម្លៃប៉ុន្មាន?"}, "en")
	if got2[0] != "hello there" || got2[1] != "How much is it?" {
		t.Fatalf("mixed batch = %v", got2)
	}
	if mixed.calls() != 1 {
		t.Fatalf("mixed batch model calls = %d, want 1", mixed.calls())
	}
}

// TestTranslateAllFlagsChangedDigits — a rendering that alters a price is
// retried once; when the retry is still wrong it is returned flagged and left
// out of the cache, so the next pass tries again instead of serving a wrong
// price for a week.
func TestTranslateAllFlagsChangedDigits(t *testing.T) {
	app := newTranslateApp(t)
	m := &scriptedModel{replies: []string{`["price 2000"]`, `["price 1000"]`}}

	got, verified, _ := app.translateAll(context.Background(), m, []string{"price 1000"}, "km")

	if got[0] != "price 1000" || !verified[0] {
		t.Fatalf("retry should have fixed the digits: %v %v", got, verified)
	}
	if m.calls() != 2 {
		t.Fatalf("model calls = %d, want 2 (re-render once)", m.calls())
	}

	// Still wrong after the retry → flagged, returned, and never cached.
	stubborn := newTranslateApp(t)
	m2 := &scriptedModel{replies: []string{`["price 2000"]`, `["price 3000"]`, `["price 4000"]`, `["price 5000"]`}}
	got2, verified2, _ := stubborn.translateAll(context.Background(), m2, []string{"price 1000"}, "km")
	if got2[0] != "price 3000" {
		t.Fatalf("the strict retry's rendering must be kept, got %q", got2[0])
	}
	if verified2[0] {
		t.Fatal("a changed digit must be reported as unverified")
	}
	before := m2.calls()
	if _, verified3, _ := stubborn.translateAll(context.Background(), m2, []string{"price 1000"}, "km"); verified3[0] {
		t.Fatal("the second pass must flag the digits too")
	}
	if m2.calls() == before {
		t.Fatal("an unverified rendering must never be cached — the next pass must ask the model again")
	}
}

// TestTranslateAllSkipsOverLongMessages — one essay in a conversation must not
// fail the whole batch, and must not be silently retried forever.
func TestTranslateAllSkipsOverLongMessages(t *testing.T) {
	app := newTranslateApp(t)
	m := &scriptedModel{replies: []string{`["Hello"]`}}

	got, _, skipped := app.translateAll(context.Background(), m,
		[]string{strings.Repeat("字", translateMaxRunes+1), "សួស្តី"}, "en")

	if len(skipped) != 1 || skipped[0] != 0 {
		t.Fatalf("skipped = %v, want [0]", skipped)
	}
	if got[0] != "" || got[1] != "Hello" {
		t.Fatalf("got %v", got)
	}
	if m.calls() != 1 {
		t.Fatalf("model calls = %d, want 1 (the essay is never sent)", m.calls())
	}
}

// TestTranslateAllBoundsSingleFallbacks — when the batch call fails outright,
// the per-item fallback is capped. The old code had no cap: 50 missing entries
// meant 50 sequential 15s calls inside one request.
func TestTranslateAllBoundsSingleFallbacks(t *testing.T) {
	app := newTranslateApp(t)
	texts := make([]string, 12)
	replies := make([]string, 12)
	for i := range texts {
		texts[i] = "message " + string(rune('a'+i))
		replies[i] = "ok"
	}
	m := &scriptedModel{ok: append([]bool{false}, make([]bool, 12)...), replies: replies}
	for i := 1; i < len(m.ok); i++ {
		m.ok[i] = true
	}

	got, _, _ := app.translateAll(context.Background(), m, texts, "km")

	if m.calls() > 1+translateSingleFallbacks {
		t.Fatalf("model calls = %d, want ≤ %d", m.calls(), 1+translateSingleFallbacks)
	}
	filled := 0
	for _, g := range got {
		if g != "" {
			filled++
		}
	}
	if filled == 0 {
		t.Fatal("the bounded fallback must still translate some messages")
	}
}

// TestTranslateBudgetScalesWithInput — the fixed 2048 is the bug this replaced.
func TestTranslateBudgetScalesWithInput(t *testing.T) {
	small := translateBudget([]string{"hi"})
	if small < 1024 {
		t.Fatalf("small budget = %d, want at least the 1024 floor", small)
	}
	long := translateBudget([]string{strings.Repeat("ខ", 2000)})
	if long <= small {
		t.Fatalf("budget must grow with the input: %d vs %d", long, small)
	}
	if huge := translateBudget([]string{strings.Repeat("ខ", 200000)}); huge > 16384 {
		t.Fatalf("budget must stay capped, got %d", huge)
	}
}

func TestNumbersPreserved(t *testing.T) {
	cases := []struct {
		src, dst string
		want     bool
	}{
		{"price 1000", "តម្លៃ 1000", true},
		{"price 1000", "តម្លៃ 1,000", true}, // separator formatting is not a change
		{"price 1000", "តម្លៃ 2000", false},
		{"call 012345678", "โทร 012345678", true},
		{"order #12", "订单 #12", true},
		{"no digits", "គ្មានលេខ", true},
		{"order #12", "订单", false},
	}
	for _, c := range cases {
		if got := numbersPreserved(c.src, c.dst); got != c.want {
			t.Errorf("numbersPreserved(%q, %q) = %v, want %v", c.src, c.dst, got, c.want)
		}
	}
}

// TestTranslateBatchValidation covers the request bounds the handler owns. All
// of these must fail before any model call happens.
func TestTranslateBatchValidation(t *testing.T) {
	app := newTranslateApp(t)

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/translate/batch", strings.NewReader(body))
		w := httptest.NewRecorder()
		if _, err := app.translateBatch(w, req); err != nil {
			return err.(*ApiError).Status
		}
		return w.Code
	}

	if got := post(`{"texts":[]}`); got != http.StatusBadRequest {
		t.Fatalf("empty batch status = %d, want 400", got)
	}
	if got := post(`{"texts":["","   "]}`); got != http.StatusOK {
		t.Fatalf("all-blank batch status = %d, want 200 (nothing to translate is not a failure)", got)
	}
	if got := post(`{"texts":["a","b","c"]}`); got != http.StatusBadGateway {
		// Not a bound test: with no model wired the call fails, and a batch
		// that produced nothing at all must be a retryable 502, not a 200 with
		// an array of empty strings.
		t.Fatalf("unserved batch status = %d, want 502", got)
	}

	// 41 items of 500 runes each = 20500 runes, past the total bound.
	items := make([]string, 0, 41)
	for i := 0; i < 41; i++ {
		items = append(items, `"`+strings.Repeat("x", 500)+`"`)
	}
	if got := post(`{"texts":[` + strings.Join(items, ",") + `]}`); got != http.StatusBadRequest {
		t.Fatalf("oversize total status = %d, want 400", got)
	}
}
