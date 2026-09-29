package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// End-to-end vertex tests: a Service built from the environment talks to two
// stub servers (the token endpoint from the key's own token_uri, and the
// platform endpoint from GEMINI_VERTEX_API_BASE) and every assertion is about
// what actually left the process — path, credential header and body shape.

// platformStub records what the service sent and answers with reply(body).
type platformStub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []stubRequest
}

type stubRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string
	APIKey string
	Body   []byte
}

func (s *platformStub) last(t *testing.T) stubRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request reached the platform stub")
	}
	return s.requests[len(s.requests)-1]
}

func (s *platformStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func newPlatformStub(t *testing.T, reply func(r *http.Request, body []byte) (int, string)) *platformStub {
	t.Helper()
	stub := &platformStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// io.ReadAll, not a single Read: a 4KB+ prompt arrives in several TCP
		// segments and a short read would look like a truncated body.
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.requests = append(stub.requests, stubRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
			APIKey: r.Header.Get("x-goog-api-key"),
			Body:   body,
		})
		stub.mu.Unlock()
		status, out := reply(r, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(stub.Close)
	return stub
}

// vertexService wires the environment for a vertex deployment and returns the
// service plus the platform stub.
func vertexService(t *testing.T, reply func(r *http.Request, body []byte) (int, string)) (*Service, *platformStub, *int32) {
	t.Helper()
	var tokenCalls int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		_, _ = w.Write([]byte(`{"access_token":"tok-vertex","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)

	saPath, _ := writeTestServiceAccount(t, tokenSrv.URL, "")
	platform := newPlatformStub(t, reply)

	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
	t.Setenv("GEMINI_VERTEX_PROJECT", "proj-1")
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")
	t.Setenv("GEMINI_VERTEX_API_BASE", platform.URL)
	t.Setenv("GEMINI_CACHE_TTL", "")
	t.Setenv("GEMINI_FAST_MODEL", "")

	return New("", "gemini-3.5-flash", 128), platform, &tokenCalls
}

// TestSetVertexRegionAimsTheTransport — the console's region switch has to move
// the URL every model call is built from, and has to refuse anything that could
// aim a token-bearing request at another host. No stub base here on purpose:
// the HOST is exactly what this test is about, so GEMINI_VERTEX_API_BASE stays
// unset (it is the one knob that hides the region from the URL).
func TestSetVertexRegionAimsTheTransport(t *testing.T) {
	saPath, _ := writeTestServiceAccount(t, "https://oauth2.googleapis.com/token", "proj-1")
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", saPath)
	t.Setenv("GEMINI_VERTEX_PROJECT", "proj-1")
	t.Setenv("GEMINI_VERTEX_REGION", "asia-southeast1")
	t.Setenv("GEMINI_VERTEX_API_BASE", "")

	s := New("", "gemini-3.5-flash", 128)
	if got := s.Region(); got != "asia-southeast1" {
		t.Fatalf("boot region = %q, want the environment's asia-southeast1", got)
	}
	if got := s.generateURLFor("m"); !strings.Contains(got, "asia-southeast1-aiplatform.googleapis.com") {
		t.Fatalf("boot URL = %q, want the configured region's host", got)
	}

	// A switch, with the console's own casing and padding: the value is
	// normalized, and `global` is the one location whose host carries no prefix.
	if err := s.SetVertexRegion(" GLOBAL "); err != nil {
		t.Fatalf("switch to global: %v", err)
	}
	if got := s.Region(); got != "global" {
		t.Errorf("region after switch = %q, want global", got)
	}
	want := "https://aiplatform.googleapis.com/v1/projects/proj-1/locations/global/publishers/google/models/m:generateContent"
	if got := s.generateURLFor("m"); got != want {
		t.Errorf("URL after switch = %q, want %q", got, want)
	}

	// Values that could steer the host of a credentialed request are refused, and
	// a refusal changes nothing — the deployment keeps serving where it was.
	for _, bad := range []string{"us.example.com", "us/east", "-us", "us-", "a--b", "us central", strings.Repeat("x", 41)} {
		if err := s.SetVertexRegion(bad); err == nil {
			t.Errorf("SetVertexRegion(%q) was accepted", bad)
		}
	}
	if got := s.Region(); got != "global" {
		t.Errorf("region after refused values = %q, want it unchanged (global)", got)
	}

	// Empty is "no change", not "reset to the environment": a caller clearing
	// the column must not silently relocate serving to a value it cannot see.
	if err := s.SetVertexRegion(""); err != nil {
		t.Fatalf("empty region: %v", err)
	}
	if got := s.Region(); got != "global" {
		t.Errorf("region after an empty switch = %q, want it unchanged (global)", got)
	}
}

// TestSetVertexRegionIsANoOpOnStudio — studio is one global endpoint with no
// locations, so a stored region is inert there rather than an error: the column
// can outlive a switch back to AI Studio, exactly as api_key outlives the switch
// the other way.
func TestSetVertexRegionIsANoOpOnStudio(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "studio")
	t.Setenv("GEMINI_API_BASE", "")

	s := New("AIza-key", "gemini-3.5-flash", 0)
	if err := s.SetVertexRegion("us"); err != nil {
		t.Fatalf("studio switch: %v", err)
	}
	if got := s.Region(); got != "" {
		t.Errorf("studio region = %q, want empty", got)
	}
	if got := s.generateURLFor("m"); !strings.Contains(got, "generativelanguage.googleapis.com") {
		t.Errorf("studio URL = %q, want the studio endpoint", got)
	}
}

// vertexModelResource is the BARE resource name (no host, no /v1) — what the
// cachedContents body's `model` field takes; vertexModelPrefix is the same
// thing as a URL path.
const vertexModelResource = "projects/proj-1/locations/asia-southeast1/publishers/google/models"
const vertexModelPrefix = "/" + vertexModelResource

func generateOK(model string) (int, string) {
	return http.StatusOK, fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"hello from %s"}]}}],`+
		`"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":2}}`, model)
}

// TestVertexEmptyAPIKeyIsLiveNotMock — the single most dangerous integration
// point: vertex has no api key, so `New("", …)` must not read as "not
// configured" and answer customers with template replies.
func TestVertexEmptyAPIKeyIsLiveNotMock(t *testing.T) {
	s, _, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return generateOK("gemini-3.5-flash")
	})
	if !s.IsConfigured() {
		t.Fatal("a vertex service authenticates with the service account: an empty api key must still be live")
	}
	res, err := s.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.UsedMock {
		t.Fatal("vertex service answered with a mock reply")
	}
	if res.Reply != "hello from gemini-3.5-flash" {
		t.Errorf("reply = %q", res.Reply)
	}
}

// TestVertexChatSendsBearerToTheResourcePath — the endpoint, the credential and
// the token cache, all in one turn pair: the second turn must reuse the token
// rather than mint another one.
func TestVertexChatSendsBearerToTheResourcePath(t *testing.T) {
	s, platform, tokenCalls := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return generateOK("gemini-3.5-flash")
	})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := s.Chat(ctx, "hi", nil, "en"); err != nil {
			t.Fatalf("Chat %d: %v", i, err)
		}
	}
	got := platform.last(t)
	if want := vertexModelPrefix + "/gemini-3.5-flash:generateContent"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if got.Auth != "Bearer tok-vertex" {
		t.Errorf("Authorization = %q, want the minted bearer token", got.Auth)
	}
	if got.APIKey != "" {
		t.Errorf("vertex must never send x-goog-api-key, got %q", got.APIKey)
	}
	if n := atomic.LoadInt32(tokenCalls); n != 1 {
		t.Errorf("token endpoint called %d times over two turns, want 1 (the token must be cached)", n)
	}
	// The body shape is the studio one — the platform accepts contents +
	// generationConfig as-is, and re-deriving it here would be the regression.
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if _, ok := body["contents"]; !ok {
		t.Errorf("body lost its contents: %s", got.Body)
	}
	if _, ok := body["systemInstruction"]; !ok {
		t.Errorf("body lost its systemInstruction: %s", got.Body)
	}
}

// TestVertexChatStreamUsesSSE — alt=sse and the bearer token on the streaming
// path, which builds its own request instead of going through postWithRetry.
func TestVertexChatStreamUsesSSE(t *testing.T) {
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"chunk one\"}]}}]}\n\n" +
			"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" chunk two\"}]}}]," +
			"\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":4}}\n\n"
	})
	var streamed strings.Builder
	res, err := s.ChatStream(context.Background(), "hi", nil, "en", func(tok string) { streamed.WriteString(tok) })
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if res.Reply != "chunk one chunk two" || streamed.String() != res.Reply {
		t.Errorf("streamed %q / reply %q", streamed.String(), res.Reply)
	}
	got := platform.last(t)
	if want := vertexModelPrefix + "/gemini-3.5-flash:streamGenerateContent"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if got.Query != "alt=sse" {
		t.Errorf("query = %q, want alt=sse (without it the answer arrives as one JSON array)", got.Query)
	}
	if got.Auth != "Bearer tok-vertex" {
		t.Errorf("Authorization = %q", got.Auth)
	}
}

// embeddingValuesJSON renders the platform's :predict answer for n vectors.
func predictJSON(width int, n int) string {
	values := make([]string, width)
	for i := range values {
		values[i] = fmt.Sprintf("%d.5", i%7)
	}
	vector := `{"embeddings":{"values":[` + strings.Join(values, ",") + `]}}`
	predictions := make([]string, n)
	for i := range predictions {
		predictions[i] = vector
	}
	return `{"predictions":[` + strings.Join(predictions, ",") + `]}`
}

// TestVertexEmbedUsesPredictWithExplicitWidth — the shape that was measured:
// :predict, `instances`, and outputDimensionality INSIDE `parameters`. Sending
// the studio body (content/model/taskType at the top level) is a 400, and
// omitting the width silently returns 3072-dim vectors against a vector(768)
// column.
func TestVertexEmbedUsesPredictWithExplicitWidth(t *testing.T) {
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, predictJSON(embeddingVectorDimension, 1)
	})
	vec, err := s.GenerateEmbedding(context.Background(), "shipping cost")
	if err != nil {
		t.Fatalf("GenerateEmbedding: %v", err)
	}
	if len(vec) != embeddingVectorDimension {
		t.Fatalf("vector has %d dimensions, want %d", len(vec), embeddingVectorDimension)
	}
	got := platform.last(t)
	if want := vertexModelPrefix + "/gemini-embedding-001:predict"; got.Path != want {
		t.Errorf("path = %q, want %q (:embedContent 400s on the platform)", got.Path, want)
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	instances, _ := body["instances"].([]any)
	if len(instances) != 1 {
		t.Fatalf("instances = %v, want one entry", body["instances"])
	}
	first, _ := instances[0].(map[string]any)
	if first["content"] != "shipping cost" {
		t.Errorf("instances[0].content = %v", first["content"])
	}
	params, _ := body["parameters"].(map[string]any)
	if params["outputDimensionality"] != float64(embeddingVectorDimension) {
		t.Errorf("parameters.outputDimensionality = %v, want %d", params["outputDimensionality"], embeddingVectorDimension)
	}
	// The studio-only fields must not leak into the platform body.
	for _, key := range []string{"content", "model", "taskType", "outputDimensionality"} {
		if _, present := body[key]; present {
			t.Errorf("studio field %q leaked into the vertex body: %s", key, got.Body)
		}
	}
}

// TestVertexEmbedRejectsTheModelsDefaultWidth — 3072 is what the model returns
// when outputDimensionality is not honoured. Accepting it would store vectors
// pgvector cannot put in a vector(768) column.
func TestVertexEmbedRejectsTheModelsDefaultWidth(t *testing.T) {
	s, _, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, predictJSON(3072, 1)
	})
	vec, err := s.GenerateEmbedding(context.Background(), "doc")
	if err == nil {
		t.Fatalf("a 3072-dim answer must be rejected, got a %d-dim vector", len(vec))
	}
	if !strings.Contains(err.Error(), "3072") || !strings.Contains(err.Error(), "768") {
		t.Errorf("err = %v, want both widths named", err)
	}
}

// TestVertexBatchEmbedSendsInstances — Vertex has no :batchEmbedContents; the
// batch survives as ONE :predict carrying every instance.
func TestVertexBatchEmbedSendsInstances(t *testing.T) {
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, predictJSON(embeddingVectorDimension, 3)
	})
	vecs, err := s.GenerateEmbeddings(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("GenerateEmbeddings: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("got %d vectors, want 3", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != embeddingVectorDimension {
			t.Errorf("vector %d has %d dimensions, want %d", i, len(v), embeddingVectorDimension)
		}
	}
	got := platform.last(t)
	if want := vertexModelPrefix + "/gemini-embedding-001:predict"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatal(err)
	}
	instances, _ := body["instances"].([]any)
	if len(instances) != 3 {
		t.Fatalf("instances = %d, want 3 (one :predict for the whole batch)", len(instances))
	}
	for i, want := range []string{"a", "b", "c"} {
		m, _ := instances[i].(map[string]any)
		if m["content"] != want {
			t.Errorf("instances[%d].content = %v, want %q", i, m["content"], want)
		}
	}
	if _, present := body["requests"]; present {
		t.Errorf("the studio batch envelope leaked into the vertex body: %s", got.Body)
	}
}

// TestVertexBatchEmbedRejectsWrongWidthInAnyVector — a padded or truncated
// prediction in one slot must fail the batch rather than be stored as a valid
// chunk embedding. The public path then recovers the documented way, by
// dropping to per-text calls — which is exactly why the batch must not paper
// over the bad slot: once a wrong-width vector is returned, GenerateEmbeddings
// cannot tell it from a right one.
func TestVertexBatchEmbedRejectsWrongWidthInAnyVector(t *testing.T) {
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		// One good vector (768), one truncated.
		return http.StatusOK, `{"predictions":[{"embeddings":{"values":[` +
			strings.Trim(strings.Repeat("0.5,", embeddingVectorDimension), ",") +
			`]}},{"embeddings":{"values":[0.1,0.2]}}]}`
	})
	if _, err := s.embedBatch(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("a short vector in the batch must fail the whole batch")
	}

	// The public API degrades to one :predict per text, and those succeed (a
	// single-instance call reads predictions[0]).
	vecs, err := s.GenerateEmbeddings(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("the per-text fallback must recover the batch: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != embeddingVectorDimension || len(vecs[1]) != embeddingVectorDimension {
		t.Fatalf("fallback returned %d vectors of %d/%d dimensions", len(vecs), len(vecs[0]), len(vecs[1]))
	}
	if got := platform.count(); got < 3 {
		t.Errorf("expected the batch plus two fallback calls, got %d requests", got)
	}
}

// TestVertexContextCacheUsesTheResourceName — the cachedContents body's `model`
// must be a resource name; the endpoint URL there is a 400 "malformed". The
// returned name is a resource path too, and must travel back verbatim.
func TestVertexContextCacheUsesTheResourceName(t *testing.T) {
	const cacheName = "projects/proj-1/locations/asia-southeast1/cachedContents/abc123"
	s, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/cachedContents") {
			return http.StatusOK, `{"name":"` + cacheName + `"}`
		}
		return generateOK("gemini-3.5-flash")
	})
	t.Setenv("GEMINI_CACHE_TTL", "3600")
	t.Setenv("GEMINI_CACHE_MIN_TOKENS", "0")

	s = New("", "gemini-3.5-flash", 128)
	// A prompt unique to this test: the cache registry is process-global.
	s.SetSystemPrompt("vertex-cache-test " + strings.Repeat("x", 64))

	got := s.contextCacheFor(context.Background(), "km")
	if got != cacheName {
		t.Fatalf("contextCacheFor = %q, want the resource name returned by the platform", got)
	}
	createReq := platform.last(t)
	if want := "/projects/proj-1/locations/asia-southeast1/cachedContents"; createReq.Path != want {
		t.Errorf("cachedContents path = %q, want %q", createReq.Path, want)
	}
	var createBody map[string]any
	if err := json.Unmarshal(createReq.Body, &createBody); err != nil {
		t.Fatal(err)
	}
	wantModel := vertexModelResource + "/gemini-3.5-flash"
	if createBody["model"] != wantModel {
		t.Errorf("cachedContents model = %v, want the resource name %q", createBody["model"], wantModel)
	}
	if strings.Contains(fmt.Sprint(createBody["model"]), "https://") {
		t.Error("the model field must never be an endpoint URL")
	}

	// A turn then references the cache by the returned name.
	if _, err := s.Chat(context.Background(), "hi", nil, "km"); err != nil {
		t.Fatal(err)
	}
	var chatBody map[string]any
	if err := json.Unmarshal(platform.last(t).Body, &chatBody); err != nil {
		t.Fatal(err)
	}
	if chatBody["cachedContent"] != cacheName {
		t.Errorf("cachedContent = %v, want %q", chatBody["cachedContent"], cacheName)
	}
	if _, present := chatBody["systemInstruction"]; present {
		t.Error("a cached turn must not resend the instruction it lives in")
	}
}

// TestVertexListModelsStripsTheResourcePath — the platform answers with
// publishers/google/models/… names; pasting one into a URL would double the
// path.
func TestVertexListModelsStripsTheResourcePath(t *testing.T) {
	// ListModels is a package function with no Service: it must resolve the
	// transport on its own.
	_, platform, _ := vertexService(t, func(r *http.Request, body []byte) (int, string) {
		return http.StatusOK, `{"publisherModels":[` +
			`{"name":"publishers/google/models/gemini-2.5-flash","displayName":"2.5 Flash"},` +
			`{"name":"publishers/google/models/text-embedding-005","displayName":"Embeddings"}]}`
	})
	names, err := ListModels(context.Background(), "ignored-in-vertex-mode")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(names) != 2 || names[0] != "gemini-2.5-flash" || names[1] != "text-embedding-005" {
		t.Fatalf("names = %v, want both parsed entries, prefix stripped", names)
	}
	got := platform.last(t)
	// See provider_test: the catalog route is /v1beta1/publishers/google/models.
	// The old path here was .../locations/{l}/models, which lists the project's
	// OWN models — names every URL this client builds would then mis-address.
	if want := "/v1beta1/publishers/google/models"; got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if got.Auth != "Bearer tok-vertex" {
		t.Errorf("Authorization = %q", got.Auth)
	}
	// A model name from this list must build a valid URL, not a doubled path.
	if err := ValidateProviderConfig(); err != nil {
		t.Fatal(err)
	}
	prov, err := providerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := prov.generateURL(names[0]); strings.Contains(got, "publishers/google/models/publishers/google/models") {
		t.Errorf("generated URL doubles the resource path: %q", got)
	}
}

// TestStudioIgnoresVertexVariables — the red line from the other side: studio
// must keep working when the vertex variables are absent, empty or garbage.
func TestStudioIgnoresVertexVariables(t *testing.T) {
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "studio-key" {
			t.Errorf("studio request lost its api key header")
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("studio request carried an Authorization header")
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"studio ok"}]}}]}`))
	}))
	defer studio.Close()

	t.Setenv("GEMINI_PROVIDER", "studio")
	t.Setenv("GEMINI_API_BASE", studio.URL)
	t.Setenv("GEMINI_VERTEX_SA_FILE", "/does/not/exist.json")
	t.Setenv("GEMINI_VERTEX_PROJECT", "")
	t.Setenv("GEMINI_VERTEX_REGION", "")
	t.Setenv("GEMINI_VERTEX_API_BASE", "https://should-not-be-used.example/v1")

	s := New("studio-key", "gemini-flash-lite-latest", 128)
	res, err := s.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("studio Chat: %v", err)
	}
	if res.Reply != "studio ok" {
		t.Errorf("reply = %q, want the stub's answer", res.Reply)
	}
}

// TestBrokenVertexConfigFailsLoudlyWithoutSending — a half-configured vertex
// deployment must fail every turn with the configuration error, and must not
// leak a request to either platform.
func TestBrokenVertexConfigFailsLoudlyWithoutSending(t *testing.T) {
	var hits int32
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"should not be reached"}]}}]}`))
	}))
	defer studio.Close()

	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", "/does/not/exist.json")
	t.Setenv("GEMINI_API_BASE", studio.URL)
	t.Setenv("GEMINI_FAST_MODEL", "")

	// A database key is present (model_configs still holds one during the
	// migration), so the service is "configured" and every turn must fail on
	// the broken transport instead of quietly using the studio endpoint.
	s := New("studio-key-still-in-db", "gemini-3.5-flash", 128)
	if !s.IsConfigured() {
		t.Fatal("a misconfigured vertex service must not degrade to silent mock mode")
	}
	_, err := s.Chat(context.Background(), "hi", nil, "en")
	if err == nil || !strings.Contains(err.Error(), "service-account") {
		t.Fatalf("err = %v, want the service-account configuration error", err)
	}
	if _, err := s.GenerateEmbedding(context.Background(), "doc"); err == nil {
		t.Error("embeddings must fail on the same configuration error")
	}
	if _, err := s.ChatStream(context.Background(), "hi", nil, "en", func(string) {}); err == nil {
		t.Error("the streaming path must surface the configuration error too")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("%d request(s) left the process under a broken vertex configuration", got)
	}
}

// TestVertexMissingAPIKeyEnvStillRediscoveredByListModels — ListModels is a
// package function with no Service, so it resolves the transport itself; it
// must not silently fall back to the studio URL in vertex mode.
func TestListModelsInVertexModeWithoutConfiguration(t *testing.T) {
	t.Setenv("GEMINI_PROVIDER", "vertex")
	t.Setenv("GEMINI_VERTEX_SA_FILE", "")
	if _, err := ListModels(context.Background(), "k"); err == nil || !strings.Contains(err.Error(), "GEMINI_VERTEX_SA_FILE") {
		t.Fatalf("err = %v, want the configuration error rather than a studio request", err)
	}
}
