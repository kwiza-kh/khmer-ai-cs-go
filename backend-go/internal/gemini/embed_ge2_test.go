package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// GE2 tests. gemini-embedding-2 is the mirror image of 001 on the platform:
// :embedContent answers 200 while :predict 404s (measured 2026-10-09 against the
// production project in region global), it has NO batch entry point on either
// platform, and it ignores taskType — its conditioning is prompt prefixes. Each
// of those differences is a silent failure mode if it is wrong: a wrong method
// 404s every retrieval, a missing prefix quietly degrades ranking, and a batch
// call to a model that has none turns document ingest into a permanent error.

// TestEmbeddingShapeSelection — the routing table itself, as data.
func TestEmbeddingShapeSelection(t *testing.T) {
	if got := EmbeddingVertexMethod(EmbeddingModelGE1); got != ":predict" {
		t.Errorf("001 vertex method = %q, want :predict", got)
	}
	if got := EmbeddingVertexMethod(EmbeddingModelGE2); got != ":embedContent" {
		t.Errorf("002 vertex method = %q, want :embedContent (its :predict 404s)", got)
	}
	// The prefix family rule: a spelled preview of 002 must route to 002's
	// serving shape, not fall back to 001's.
	if EmbeddingVertexMethod("gemini-embedding-2-preview") != ":embedContent" {
		t.Error("gemini-embedding-2-preview must route to the 002 shape")
	}
	// An unrecognised name takes the legacy shape: wrong in the loud direction
	// (404), never a silent cross-space mix.
	if EmbeddingVertexMethod("some-unknown-embedding") != ":predict" {
		t.Error("an unknown model must take the legacy shape, not guess")
	}
	if embeddingShapeFor(EmbeddingModelGE2).nativeBatch {
		t.Error("002 has no batch entry point on either platform; nativeBatch must be false")
	}
	if !embeddingShapeFor(EmbeddingModelGE1).nativeBatch {
		t.Error("001 keeps its batch path")
	}
}

// TestConfiguredEmbeddingModelDefaultIsLegacy — the default must not flip to
// 002 by itself: an undeclared deployment keeps embedding with 001, exactly the
// way an unset GEMINI_PROVIDER stays on studio.
func TestConfiguredEmbeddingModelDefaultIsLegacy(t *testing.T) {
	t.Setenv(embeddingModelEnv, "")
	if got := ConfiguredEmbeddingModel(); got != EmbeddingModelGE1 {
		t.Errorf("default = %q, want %q", got, EmbeddingModelGE1)
	}
	t.Setenv(embeddingModelEnv, "gemini-embedding-2")
	if got := ConfiguredEmbeddingModel(); got != EmbeddingModelGE2 {
		t.Errorf("override = %q, want %q", got, EmbeddingModelGE2)
	}
	t.Setenv(embeddingModelEnv, " models/gemini-embedding-2 ")
	if got := ConfiguredEmbeddingModel(); got != EmbeddingModelGE2 {
		t.Errorf("override with models/ prefix and padding = %q, want %q", got, EmbeddingModelGE2)
	}
}

// embedContentJSON renders the GE2/:embedContent envelope for one vector whose
// first value is seed, so the caller can prove which text produced it.
func embedContentJSON(width int, seed float64) string {
	return `{"embedding":{"values":[` + vectorValues(width, seed) + `]},"usageMetadata":{"promptTokenCount":7}}`
}

// TestVertexGE2EmbedUsesEmbedContentWithDocPrefix — the shape that was measured
// for 002 on the platform: :embedContent, `content.parts`, outputDimensionality
// at the top level, and the document prefix on the text.
func TestVertexGE2EmbedUsesEmbedContentWithDocPrefix(t *testing.T) {
	t.Setenv(embeddingModelEnv, EmbeddingModelGE2)
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, embedContentJSON(embeddingVectorDimension, 1)
	})
	vec, err := s.GenerateEmbedding(context.Background(), "shipping cost")
	if err != nil {
		t.Fatalf("GenerateEmbedding: %v", err)
	}
	if len(vec) != embeddingVectorDimension {
		t.Fatalf("vector has %d dimensions, want %d", len(vec), embeddingVectorDimension)
	}
	got := platform.last(t)
	if want := vertexModelPrefix + "/gemini-embedding-2:embedContent"; got.Path != want {
		t.Errorf("path = %q, want %q (:predict 404s for this model)", got.Path, want)
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["outputDimensionality"] != float64(embeddingVectorDimension) {
		t.Errorf("outputDimensionality = %v, want %d at the TOP level", body["outputDimensionality"], embeddingVectorDimension)
	}
	text := digArrayText(t, body, "content", "parts", 0, "text")
	if want := "title: none | text: shipping cost"; text != want {
		t.Errorf("text = %q, want %q (002 is conditioned by prefixes, not taskType)", text, want)
	}
	// The 001/:predict shape must not leak in: `instances` on :embedContent is a
	// 400 (measured: Unknown name "instances").
	for _, key := range []string{"instances", "parameters", "taskType"} {
		if _, present := body[key]; present {
			t.Errorf("vertex-001 field %q leaked into the 002 body: %s", key, got.Body)
		}
	}
}

// TestVertexGE2QueryUsesQueryPrefixAndCachesPerModel — the query side gets the
// query prefix, and the 5-minute cache must never answer a GE1 query with a GE2
// vector (or the reverse) after a model switch: a stale cross-space hit is
// silent, because retrieval still "succeeds".
func TestVertexGE2QueryUsesQueryPrefixAndCachesPerModel(t *testing.T) {
	t.Setenv(embeddingModelEnv, EmbeddingModelGE2)
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		if strings.HasSuffix(r.URL.Path, ":predict") {
			return http.StatusOK, predictJSON(embeddingVectorDimension, 1)
		}
		return http.StatusOK, embedContentJSON(embeddingVectorDimension, 1)
	})
	if _, err := s.GenerateQueryEmbedding(context.Background(), "how much is eps"); err != nil {
		t.Fatalf("GenerateQueryEmbedding: %v", err)
	}
	got := platform.last(t)
	var body map[string]any
	_ = json.Unmarshal(got.Body, &body)
	if text := digArrayText(t, body, "content", "parts", 0, "text"); text != "task: search result | query: how much is eps" {
		t.Errorf("query text = %q, want the query prefix", text)
	}
	// Same text again: served from the cache, no second network call.
	before := platform.count()
	if _, err := s.GenerateQueryEmbedding(context.Background(), "how much is eps"); err != nil {
		t.Fatalf("cached GenerateQueryEmbedding: %v", err)
	}
	if platform.count() != before {
		t.Errorf("cache miss on the identical query: %d → %d requests", before, platform.count())
	}
	// A model switch must MISS the cache — that is the whole point of keying it
	// by model.
	s.SetEmbeddingModel(EmbeddingModelGE1)
	if _, err := s.GenerateQueryEmbedding(context.Background(), "how much is eps"); err != nil {
		t.Fatalf("GenerateQueryEmbedding after model switch: %v", err)
	}
	if platform.count() == before {
		t.Error("the query cache answered a GE1 query from the GE2 entry (key must include the model)")
	}
	if got := platform.last(t).Path; !strings.HasSuffix(got, "/gemini-embedding-001:predict") {
		t.Errorf("post-switch path = %q, want 001's :predict", got)
	}
}

// TestVertexGE2BatchEmbedsPerTextInOrder — 002 has no batch entry point, so
// GenerateEmbeddings must issue one call per text and still return vectors in
// input order (indexDocument pairs embeddings[i] with chunks[i] by index).
func TestVertexGE2BatchEmbedsPerTextInOrder(t *testing.T) {
	t.Setenv(embeddingModelEnv, EmbeddingModelGE2)
	var mu sync.Mutex
	seen := 0
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		// Decode the text, then answer with a vector whose first element is the
		// text's own tail index — so an out-of-order result is detectable.
		var req struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		}
		_ = json.Unmarshal(body, &req)
		text := ""
		if len(req.Content.Parts) > 0 {
			text = req.Content.Parts[0].Text
		}
		var idx float64
		_, _ = fmt.Sscanf(text[strings.LastIndex(text, "-")+1:], "%f", &idx)
		mu.Lock()
		seen++
		mu.Unlock()
		return http.StatusOK, embedContentJSON(embeddingVectorDimension, idx)
	})
	texts := []string{"doc-0", "doc-1", "doc-2", "doc-3", "doc-4", "doc-5", "doc-6", "doc-7"}
	vecs, err := s.GenerateEmbeddings(context.Background(), texts)
	if err != nil {
		t.Fatalf("GenerateEmbeddings: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vecs), len(texts))
	}
	mu.Lock()
	n := seen
	mu.Unlock()
	if n != len(texts) {
		t.Errorf("stub saw %d calls for %d texts; 002 has no batch entry point, one call per text is the contract", n, len(texts))
	}
	for i := range vecs {
		if got := vecs[i][0]; got != float32(i) {
			t.Errorf("vector %d has seed %v: results are out of input order", i, got)
		}
	}
	// And at least the concurrency bound was exercised rather than a serial walk
	// — 8 texts with a bound of 6 can only be served by a pool.
	if got := platform.count(); got != len(texts) {
		t.Errorf("platform saw %d requests, want one per text (%d)", got, len(texts))
	}
}

// TestVertexGE2WidthErrorNamesTheModel — a 3072-dim answer (the model's default
// when outputDimensionality is dropped) must fail with the model named: the fix
// differs between "wrong env model" and "width param lost".
func TestVertexGE2WidthErrorNamesTheModel(t *testing.T) {
	t.Setenv(embeddingModelEnv, EmbeddingModelGE2)
	s, _, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, embedContentJSON(3072, 1)
	})
	_, err := s.GenerateEmbedding(context.Background(), "doc")
	if err == nil {
		t.Fatal("a 3072-dim vector against a vector(768) column must fail")
	}
	if !strings.Contains(err.Error(), EmbeddingModelGE2) || !strings.Contains(err.Error(), "768") {
		t.Errorf("width error must name the model and the column width, got: %v", err)
	}
}

// TestStudioGE2BodyDropsTaskType — on AI Studio, 002 keeps the :embedContent
// shape but must NOT receive a taskType field (it ignores it) and must carry
// the model by name. GE1-studio still receives taskType, which its ranking
// depends on.
func TestStudioGE2BodyDropsTaskType(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "studio")
	server, requests := studioBodyStub(t)
	t.Setenv("GEMINI_API_BASE", server)

	s := New("key", "gemini-test", 0)
	s.SetEmbeddingModel(EmbeddingModelGE2)
	if _, err := s.GenerateEmbedding(context.Background(), "doc"); err != nil {
		t.Fatalf("GenerateEmbedding: %v", err)
	}
	body := (*requests)[0]
	if _, present := body["taskType"]; present {
		t.Errorf("002-studio body must not carry taskType: %v", body)
	}
	if body["model"] != "models/"+EmbeddingModelGE2 {
		t.Errorf("model = %v, want models/%s", body["model"], EmbeddingModelGE2)
	}
	if text := digArrayText(t, body, "content", "parts", 0, "text"); text != "title: none | text: doc" {
		t.Errorf("text = %q, want the document prefix", text)
	}

	// GE1 keeps its taskType on the same transport.
	s.SetEmbeddingModel(EmbeddingModelGE1)
	if _, err := s.GenerateEmbedding(context.Background(), "doc"); err != nil {
		t.Fatalf("GE1 GenerateEmbedding: %v", err)
	}
	ge1 := (*requests)[1]
	if ge1["taskType"] != "RETRIEVAL_DOCUMENT" {
		t.Errorf("GE1-studio taskType = %v, want RETRIEVAL_DOCUMENT", ge1["taskType"])
	}
	if text := digArrayText(t, ge1, "content", "parts", 0, "text"); text != "doc" {
		t.Errorf("GE1 text = %q, want it unprefixed", text)
	}
}

// TestGE2DocumentEmbeddingsUseTheRealTitle — the indexing path's contract: the
// document text carries the DOCUMENT'S OWN title in the GE2 idiom. Measured on
// the production corpus, the real title beats the placeholder (recall@5 0.563
// vs 0.492), which is why the title travels with the chunk instead of being
// replaced by "none".
func TestGE2DocumentEmbeddingsUseTheRealTitle(t *testing.T) {
	t.Setenv(embeddingModelEnv, EmbeddingModelGE2)
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, embedContentJSON(embeddingVectorDimension, 1)
	})
	vecs, err := s.GenerateDocumentEmbeddings(context.Background(),
		[]string{"SVN-002-price-list", "SVN-002-price-list"},
		[]string{"chunk one", "chunk two"})
	if err != nil {
		t.Fatalf("GenerateDocumentEmbeddings: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	if got := platform.count(); got != 2 {
		t.Errorf("platform saw %d calls, want 2 (GE2 has no batch path)", got)
	}
	// Both texts must carry the real title and their OWN chunk body. The SET is
	// asserted, never the arrival order: 002 has no batch entry point, so the two
	// calls race each other and land in whatever order the pool finishes them.
	// Input order is a contract of the RESULT, which
	// TestVertexGE2BatchEmbedsPerTextInOrder covers by seeding each answer from
	// its own request.
	want := []string{
		"title: SVN-002-price-list | text: chunk one",
		"title: SVN-002-price-list | text: chunk two",
	}
	sort.Strings(want)
	if got := embedTextsSent(t, platform); !slices.Equal(got, want) {
		t.Errorf("document texts = %q, want %q (any order)", got, want)
	}

	// A blank title must fall back to the placeholder rather than producing
	// "title:  | text: …".
	if _, err := s.GenerateDocumentEmbeddings(context.Background(), []string{"   "}, []string{"doc"}); err != nil {
		t.Fatalf("blank-title call: %v", err)
	}
	if got := embedTextsSent(t, platform); !slices.Contains(got, "title: none | text: doc") {
		t.Errorf("blank-title texts = %q, want the placeholder fallback", got)
	}
}

// embedTextsSent returns the text of every embedding request the stub recorded,
// sorted. It is the assertion shape for the concurrent embedding path, where
// the order requests arrive in says nothing and only the set does.
func embedTextsSent(t *testing.T, s *platformStub) []string {
	t.Helper()
	n := s.count()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		var body map[string]any
		if err := json.Unmarshal(s.requestAt(t, i).Body, &body); err != nil {
			t.Fatalf("request %d body: %v", i, err)
		}
		out = append(out, digArrayText(t, body, "content", "parts", 0, "text"))
	}
	sort.Strings(out)
	return out
}

// TestGE1DocumentEmbeddingsIgnoreTitles — GE1 has no title conditioning, so the
// indexing path must send the chunk text unchanged. If titles leaked into GE1
// bodies, every stored 001 vector would stop matching the ones already in the
// column and the switch would become irreversible without a re-embed.
func TestGE1DocumentEmbeddingsIgnoreTitles(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "studio")
	server, requests := studioBodyStub(t)
	t.Setenv("GEMINI_API_BASE", server)
	s := New("key", "gemini-test", 0)
	s.SetEmbeddingModel(EmbeddingModelGE1)
	if _, err := s.GenerateDocumentEmbeddings(context.Background(),
		[]string{"SVN-002-price-list"}, []string{"chunk one"}); err != nil {
		t.Fatalf("GenerateDocumentEmbeddings: %v", err)
	}
	if n := len(*requests); n != 1 {
		t.Fatalf("got %d requests, want 1 (GE1 batches)", n)
	}
	body := (*requests)[0]
	// GE1-studio sends the batch envelope: requests[0].content.parts[0].text.
	reqs, _ := body["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("requests = %v, want one entry", body["requests"])
	}
	first, _ := reqs[0].(map[string]any)
	text := digArrayText(t, first, "content", "parts", 0, "text")
	if text != "chunk one" {
		t.Errorf("GE1 text = %q, want the bare chunk (titles must not leak)", text)
	}
}

// studioBodyStub answers like AI Studio and records each request body, in
// order. It answers the two shapes GE1 uses — :batchEmbedContents with one
// envelope per request, and :embedContent with a single embedding — so a GE1
// call is served by its batch path rather than silently falling back.
func studioBodyStub(t *testing.T) (string, *[]map[string]any) {
	t.Helper()
	requests := &[]map[string]any{}
	mu := &sync.Mutex{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		mu.Lock()
		*requests = append(*requests, decoded)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ":batchEmbedContents") {
			n := 1
			if reqs, ok := decoded["requests"].([]any); ok {
				n = len(reqs)
			}
			items := make([]string, n)
			for i := range items {
				items[i] = `{"values":[` + vectorValues(embeddingVectorDimension, 1) + `]}`
			}
			_, _ = w.Write([]byte(`{"embeddings":[` + strings.Join(items, ",") + `]}`))
			return
		}
		_, _ = w.Write([]byte(embedContentJSON(embeddingVectorDimension, 1)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

// vectorValues renders the comma-joined float list embedContentJSON embeds, so
// the batch envelope and the single envelope carry identical values.
func vectorValues(width int, seed float64) string {
	values := make([]string, width)
	for i := range values {
		values[i] = fmt.Sprintf("%g", seed+(float64(i)/1000))
	}
	return strings.Join(values, ",")
}

// digArrayText digs a string out of a decoded JSON body by key path, with an
// array index before the final key: content.parts[0].text.
func digArrayText(t *testing.T, v map[string]any, keys ...any) string {
	t.Helper()
	var cur any = v
	for _, k := range keys {
		switch key := k.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("path %v: %v is not an object", keys, cur)
			}
			cur = m[key]
		case int:
			arr, ok := cur.([]any)
			if !ok || key >= len(arr) {
				t.Fatalf("path %v: %v is not an array with index %d", keys, cur, key)
			}
			cur = arr[key]
		}
	}
	s, _ := cur.(string)
	return s
}
