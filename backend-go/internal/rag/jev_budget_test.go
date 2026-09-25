package rag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/typesafe"
)

// hangingJevClient answers only once the caller gives up: the handler blocks on
// the request context, or on test cleanup so a regression cannot wedge the
// suite. The client is deliberately built without an HTTP timeout — that
// mirrors "nothing bounds this call below the context", so the only thing that
// can end it is the per-call budget under test.
func hangingJevClient(t *testing.T) *typesafe.Client {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return &typesafe.Client{
		Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest",
		HTTP: srv.Client(), Logger: quietLogger(),
	}
}

// runWithin fails the test if fn has not returned in time, so a missing budget
// surfaces as a fast failure instead of a wedged suite.
func runWithin(t *testing.T, limit time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not return — its Jev budget is not applied", what)
	}
}

// TestRewriteQueryJevRespectsBudget pins the retrieval-path bound. The rewrite
// runs inside Search, ahead of retrieval and generation, so an unbounded call
// held the whole turn for the HTTP client's 10s timeout.
func TestRewriteQueryJevRespectsBudget(t *testing.T) {
	old := jevRewriteBudget
	jevRewriteBudget = 100 * time.Millisecond
	t.Cleanup(func() { jevRewriteBudget = old })

	s := &Service{Jev: hangingJevClient(t), Logger: quietLogger()}
	history := []gemini.HistoryItem{{Role: "user", Content: "KWF-RO-75 有货吗"}}

	var q *string
	var decided bool
	var took time.Duration
	runWithin(t, 5*time.Second, "rewriteQueryJev", func() {
		start := time.Now()
		q, decided = s.rewriteQueryJev(context.Background(), "多少钱", history)
		took = time.Since(start)
	})
	// (nil, true) means "original query is best" — a real decision, not a
	// fallback. A timed-out call must report undecided so the caller uses the
	// legacy Gemini rewrite.
	if decided || q != nil {
		t.Fatalf("a Jev that outlives its budget must report undecided, got q=%v decided=%v", q, decided)
	}
	if took >= time.Second {
		t.Fatalf("budget must cut the call short, took %v", took)
	}
}

// TestRerankScoresJevRespectsBudget pins the same bound on the Jev rerank: on
// expiry the caller keeps the fused order rather than waiting.
func TestRerankScoresJevRespectsBudget(t *testing.T) {
	old := jevRerankBudget
	jevRerankBudget = 100 * time.Millisecond
	t.Cleanup(func() { jevRerankBudget = old })

	s := &Service{Jev: hangingJevClient(t), Logger: quietLogger()}

	var scores []float32
	var ok bool
	var took time.Duration
	runWithin(t, 5*time.Second, "rerankScoresJev", func() {
		start := time.Now()
		scores, ok = s.rerankScoresJev(context.Background(), "price of 5cm boards?", []string{"a", "b", "c"})
		took = time.Since(start)
	})
	if ok || scores != nil {
		t.Fatalf("a Jev that outlives its budget must report failure, got ok=%v scores=%v", ok, scores)
	}
	if took >= time.Second {
		t.Fatalf("budget must cut the call short, took %v", took)
	}
}
