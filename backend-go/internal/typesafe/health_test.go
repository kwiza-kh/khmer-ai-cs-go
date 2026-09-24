package typesafe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// observerRecorder captures the health callbacks Judge emits.
type observerRecorder struct {
	mu       sync.Mutex
	success  int
	failures []error
	durs     []time.Duration
}

func (o *observerRecorder) JudgeFailed(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures = append(o.failures, err)
}

func (o *observerRecorder) JudgeSucceeded(took time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.success++
	o.durs = append(o.durs, took)
}

func (o *observerRecorder) snapshot() (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.success, len(o.failures)
}

var oneQuestion = map[string]Question{"q": Noul("Is this a greeting?")}

// The wiring that makes the whole feature work: Judge must actually emit.
func TestJudgeNotifiesObserverOnSuccess(t *testing.T) {
	body := judgeResponse(t)
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	rec := &observerRecorder{}
	c.SetHealthObserver(rec)

	if _, err := c.Judge(context.Background(), "hello", oneQuestion); err != nil {
		t.Fatalf("judge: %v", err)
	}
	ok, failed := rec.snapshot()
	if ok != 1 || failed != 0 {
		t.Fatalf("want 1 success / 0 failures, got %d / %d", ok, failed)
	}
	if len(rec.durs) != 1 || rec.durs[0] <= 0 {
		t.Fatalf("a duration must be reported, got %v", rec.durs)
	}
}

func TestJudgeNotifiesObserverOnFailure(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	c := &Client{Endpoint: bad.URL, APIKey: stubAuthValue, Model: DefaultModel, HTTP: bad.Client()}
	rec := &observerRecorder{}
	c.SetHealthObserver(rec)

	if _, err := c.Judge(context.Background(), "hello", oneQuestion); err == nil {
		t.Fatal("expected failure")
	}
	ok, failed := rec.snapshot()
	if ok != 0 || failed != 1 {
		t.Fatalf("want 0 successes / 1 failure, got %d / %d", ok, failed)
	}
}

// A caller-imposed deadline is a health signal: Jev was too slow for the
// reply-path budget, which produces the same silent fallback as an error.
func TestJudgeNotifiesObserverOnDeadline(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"model":"jev","answers":{}}`))
	}))
	t.Cleanup(slow.Close)
	c := &Client{Endpoint: slow.URL, APIKey: stubAuthValue, Model: DefaultModel, HTTP: slow.Client()}
	rec := &observerRecorder{}
	c.SetHealthObserver(rec)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Judge(ctx, "hello", oneQuestion); err == nil {
		t.Fatal("expected deadline failure")
	}
	if _, failed := rec.snapshot(); failed != 1 {
		t.Fatalf("a deadline must be reported as a health failure, got %d", failed)
	}
}

// Cancellation says nothing about Jev: it means shutdown or a hung-up visitor.
// Reporting it would page operators every time the server stops.
func TestJudgeDoesNotReportCancellation(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"model":"jev","answers":{}}`))
	}))
	t.Cleanup(slow.Close)
	c := &Client{Endpoint: slow.URL, APIKey: stubAuthValue, Model: DefaultModel, HTTP: slow.Client()}
	rec := &observerRecorder{}
	c.SetHealthObserver(rec)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if _, err := c.Judge(ctx, "hello", oneQuestion); err == nil {
		t.Fatal("expected cancellation failure")
	}
	if _, failed := rec.snapshot(); failed != 0 {
		t.Fatalf("cancellation must not be reported as a health failure, got %d", failed)
	}
}

// A disabled client short-circuits before any network work, so it must not
// emit either — otherwise every site would alert when TYPESAFE_API_KEY is
// simply unset, which is a supported configuration.
func TestDisabledClientDoesNotNotifyObserver(t *testing.T) {
	var nilClient *Client
	nilClient.SetHealthObserver(&observerRecorder{}) // must not panic

	noKey := &Client{Endpoint: "http://127.0.0.1:1", Model: DefaultModel}
	rec := &observerRecorder{}
	noKey.SetHealthObserver(rec)
	if _, err := noKey.Judge(context.Background(), "hello", oneQuestion); err == nil {
		t.Fatal("expected a disabled-client error")
	}
	if ok, failed := rec.snapshot(); ok != 0 || failed != 0 {
		t.Fatalf("a disabled client must stay silent, got %d / %d", ok, failed)
	}
}

// A 429 that exhausts its retries inside the backoff window must still report.
func TestJudgeReportsExhaustedRetries(t *testing.T) {
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(limited.Close)
	c := &Client{Endpoint: limited.URL, APIKey: stubAuthValue, Model: DefaultModel, HTTP: limited.Client()}
	rec := &observerRecorder{}
	c.SetHealthObserver(rec)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Judge(ctx, "hello", oneQuestion); err == nil {
		t.Fatal("expected failure after exhausted retries")
	}
	if _, failed := rec.snapshot(); failed != 1 {
		t.Fatalf("exhausted retries must be reported once, got %d", failed)
	}
}

// Sanity: the recorder really sees the error text, so the page can carry it.
func TestJudgeFailureCarriesTheError(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(bad.Close)
	c := &Client{Endpoint: bad.URL, APIKey: stubAuthValue, Model: DefaultModel, HTTP: bad.Client()}
	rec := &observerRecorder{}
	c.SetHealthObserver(rec)

	_, _ = c.Judge(context.Background(), "hello", oneQuestion)
	if len(rec.failures) != 1 || rec.failures[0] == nil {
		t.Fatalf("expected one non-nil error, got %v", rec.failures)
	}
	if !errors.Is(rec.failures[0], rec.failures[0]) {
		t.Fatal("unreachable")
	}
}
