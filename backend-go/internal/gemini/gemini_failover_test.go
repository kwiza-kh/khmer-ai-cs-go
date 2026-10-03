package gemini

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Provider failover: an ordered candidate list (primary, then
// GEMINI_PROVIDER_FALLBACK) with a per-transport retry ladder inside and a
// switch between them outside. The switch only happens for failures that another
// transport could actually fix, and — on the streaming path — only while the
// customer has seen nothing yet.

func TestFailoverEligible(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{"transport error", 0, errors.New("dial tcp: connection refused"), true},
		{"500", http.StatusInternalServerError, nil, true},
		{"503", http.StatusServiceUnavailable, nil, true},
		{"401 credential refused", http.StatusUnauthorized, nil, true},
		{"403 credential refused", http.StatusForbidden, nil, true},
		{"404 model absent from this region", http.StatusNotFound, nil, true},
		{"429 spend stop", http.StatusTooManyRequests, nil, false},
		{"400 malformed request", http.StatusBadRequest, nil, false},
		{"200", http.StatusOK, nil, false},
	}
	for _, tc := range cases {
		if got := failoverEligible(tc.status, tc.err); got != tc.want {
			t.Errorf("%s: failoverEligible(%d) = %v, want %v", tc.name, tc.status, got, tc.want)
		}
	}
}

func TestProviderCandidatesOrderPrimaryThenFallback(t *testing.T) {
	primary := provider{kind: providerStudio}

	// No fallback configured: exactly today's single transport.
	s := &Service{apiKey: "k", provider: primary}
	if got := s.providerCandidates(); len(got) != 1 {
		t.Fatalf("candidates = %d, want 1", len(got))
	}

	// Fallback naming the primary's own kind is not a fallback.
	s.fallback, s.hasFallback = provider{kind: providerStudio}, true
	if got := s.providerCandidates(); len(got) != 1 {
		t.Fatalf("same-kind fallback must be ignored, got %d candidates", len(got))
	}

	// A vertex fallback is usable and comes second.
	s.fallback = provider{kind: providerVertex, tokens: &tokenSource{}}
	if got := s.providerCandidates(); len(got) != 2 || got[1].kind != providerVertex {
		t.Fatalf("candidates = %v, want studio then vertex", got)
	}

	// A studio fallback without an API key would 401 on every attempt.
	s = &Service{apiKey: "", provider: provider{kind: providerVertex, tokens: &tokenSource{}}}
	s.fallback, s.hasFallback = provider{kind: providerStudio}, true
	if got := s.providerCandidates(); len(got) != 1 {
		t.Fatalf("keyless studio fallback must be skipped, got %d candidates", len(got))
	}
	s.apiKey = "k"
	if got := s.providerCandidates(); len(got) != 2 {
		t.Fatalf("with a key the studio fallback must be offered, got %d candidates", len(got))
	}

	// A broken primary configuration stays broken: no candidates, so the caller
	// re-reads the error and fails loudly instead of quietly using a fallback.
	s = &Service{apiKey: "k", providerErr: errors.New("broken vertex config")}
	if got := s.providerCandidates(); got != nil {
		t.Fatalf("broken primary must yield no candidates, got %v", got)
	}
}

// vertexTransportStub answers both halves of the vertex transport: the OAuth2
// token exchange and :generateContent.
func vertexTransportStub(t *testing.T, answer string, genCalls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") {
			atomic.AddInt32(genCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":%q}]}}]}`, answer)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"vertex-token","expires_in":3600,"token_type":"Bearer"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// vertexFallbackFor builds a real vertex transport aimed at the given stub: a
// generated service-account file (reusing the helper from vertex_test.go) whose
// token URI is the stub itself.
func vertexFallbackFor(t *testing.T, srv *httptest.Server) provider {
	t.Helper()
	saPath, _ := writeTestServiceAccount(t, srv.URL, "proj-fallback")
	ts, err := newTokenSource(saPath)
	if err != nil {
		t.Fatalf("newTokenSource: %v", err)
	}
	return provider{
		kind:   providerVertex,
		vertex: vertexResource{base: srv.URL, project: "proj-fallback", region: vertexGlobalRegion},
		tokens: ts,
	}
}

// studioStub points the studio transport at a stub and prepares the environment
// so New() resolves to studio with no env-derived fallback.
func studioStub(t *testing.T, url string) {
	t.Helper()
	t.Setenv("GEMINI_API_BASE", url)
	t.Setenv("GEMINI_PROVIDER", "studio")
	t.Setenv("GEMINI_PROVIDER_FALLBACK", "")
	t.Setenv("GEMINI_FAST_MODEL", "")
}

func TestChatFailsOverToTheSecondTransport(t *testing.T) {
	var primaryCalls, fallbackCalls int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	t.Cleanup(srvA.Close)
	srvB := vertexTransportStub(t, "answered by vertex", &fallbackCalls)
	studioStub(t, srvA.URL)

	s := New("studio-key", "gemini-test", 128)
	s.fallback = vertexFallbackFor(t, srvB)
	s.hasFallback = true

	res, err := s.Chat(context.Background(), "hi", nil, "en")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Reply != "answered by vertex" {
		t.Fatalf("reply = %q, want the fallback's answer", res.Reply)
	}
	// The inner layer exhausted its own ladder before the outer one switched.
	if got := atomic.LoadInt32(&primaryCalls); got != postMaxAttempts {
		t.Errorf("primary attempts = %d, want %d", got, postMaxAttempts)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 1 {
		t.Errorf("fallback attempts = %d, want 1", got)
	}
	if got := s.FailoverCount(); got != 1 {
		t.Errorf("FailoverCount = %d, want 1", got)
	}
}

// A spend stop is not a transport failure: switching would double the doomed
// requests against a wall that outlives the switch.
func TestChatDoesNotFailOverOn429(t *testing.T) {
	var primaryCalls, fallbackCalls int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&primaryCalls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"status":"RESOURCE_EXHAUSTED"}}`))
	}))
	t.Cleanup(srvA.Close)
	srvB := vertexTransportStub(t, "must not be used", &fallbackCalls)
	studioStub(t, srvA.URL)

	s := New("studio-key", "gemini-test", 128)
	s.fallback = vertexFallbackFor(t, srvB)
	s.hasFallback = true

	_, err := s.Chat(context.Background(), "hi", nil, "en")
	if err == nil {
		t.Fatal("a 429 must fail the turn")
	}
	if got := atomic.LoadInt32(&primaryCalls); got != 1 {
		t.Errorf("primary attempts = %d, want 1 (fail fast on 429)", got)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 0 {
		t.Errorf("fallback attempts = %d, want 0", got)
	}
	if got := s.FailoverCount(); got != 0 {
		t.Errorf("FailoverCount = %d, want 0", got)
	}
}

// A stream that dies after the customer has already seen text must NOT restart on
// another transport: that would put a second, contradictory answer on top of the
// first. The truncation is reported instead — which it never used to be, because
// scanner.Err() was not consulted at all.
func TestChatStreamDoesNotRestartAfterContent(t *testing.T) {
	var streamCalls, fallbackCalls int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&streamCalls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial answer\"}]}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Drop the connection mid-answer.
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srvA.Close)
	srvB := vertexTransportStub(t, "second answer", &fallbackCalls)
	studioStub(t, srvA.URL)

	s := New("studio-key", "gemini-test", 128)
	s.fallback = vertexFallbackFor(t, srvB)
	s.hasFallback = true

	res, err := s.ChatStream(context.Background(), "hi", nil, "en", func(string) {})
	if err == nil {
		t.Fatal("an interrupted stream must report an error, not a silent truncation")
	}
	if !strings.Contains(res.Reply, "partial answer") {
		t.Fatalf("partial reply = %q, want the text the customer already saw", res.Reply)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 0 {
		t.Errorf("fallback attempts = %d, want 0 (never restart after content)", got)
	}
	if got := s.FailoverCount(); got != 0 {
		t.Errorf("FailoverCount = %d, want 0", got)
	}
}

// …but a stream that fails BEFORE any content is exactly the case where the
// non-streaming path (which owns the candidate list) should take over.
func TestChatStreamFailsOverBeforeAnyContent(t *testing.T) {
	var streamCalls, primaryGenCalls, fallbackCalls int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			atomic.AddInt32(&streamCalls, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		atomic.AddInt32(&primaryGenCalls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srvA.Close)
	srvB := vertexTransportStub(t, "answered by vertex", &fallbackCalls)
	studioStub(t, srvA.URL)

	s := New("studio-key", "gemini-test", 128)
	s.fallback = vertexFallbackFor(t, srvB)
	s.hasFallback = true

	var streamed []string
	res, err := s.ChatStream(context.Background(), "hi", nil, "en", func(tok string) {
		streamed = append(streamed, tok)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if res.Reply != "answered by vertex" {
		t.Fatalf("reply = %q, want the fallback's answer", res.Reply)
	}
	if len(streamed) != 0 {
		t.Fatalf("nothing should have been streamed, got %v", streamed)
	}
	if got := atomic.LoadInt32(&streamCalls); got != 1 {
		t.Errorf("stream attempts = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&primaryGenCalls); got != postMaxAttempts {
		t.Errorf("primary non-streaming attempts = %d, want %d", got, postMaxAttempts)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 1 {
		t.Errorf("fallback attempts = %d, want 1", got)
	}
}
