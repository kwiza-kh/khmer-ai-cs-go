package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestJudgeTurnJevRespectsBudget pins the reply-path bound on the turn
// judgment. The call reached the HTTP client's 10s timeout before this knob
// existed, which held a website-widget turn — and pinned a classifier slot on
// the background path — for the full ten seconds instead of falling back to the
// fast-model audit.
func TestJudgeTurnJevRespectsBudget(t *testing.T) {
	old := turnBudget
	turnBudget = 100 * time.Millisecond
	t.Cleanup(func() { turnBudget = old })

	p := &Pipeline{Jev: hangingJevClient(t), Logger: quietLogger()}

	var ok bool
	var took time.Duration
	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		_, _, _, ok = p.judgeTurnJev(context.Background(), "I want my money back", "Our policy says…", true)
		took = time.Since(start)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("judgeTurnJev did not return — turnBudget is not applied to the Jev call")
	}
	if ok {
		t.Fatal("a Jev that outlives its budget must report not-ok so the fast-model audit runs")
	}
	if took >= time.Second {
		t.Fatalf("budget must cut the call short, took %v", took)
	}
}
