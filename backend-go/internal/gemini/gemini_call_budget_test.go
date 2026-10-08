package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The per-call budget exists because the only ceiling used to be per attempt
// (the shared HTTP client's 60s): a hanging upstream could hold a pipeline worker
// for the whole retry worst case. These tests pin the arithmetic, the environment
// override, and — with a server that never answers — that the budget really ends
// the call and really stops the retries.

func TestCallBudgetDefaultAndOverride(t *testing.T) {
	// The default must be STRICTLY tighter than the retry worst case. Until
	// 2026-10-04 it was retryWorstCase() itself — a budget set to the exact worst
	// case it exists to bound bounds nothing, and three attempts could each spend
	// their full 60s (181.2s) while the customer waited. Retries survive because
	// they are for fast transient failures, which leave the budget unspent.
	if got := callBudget(); got != postAttemptTimeout {
		t.Fatalf("default budget = %s, want one attempt (%s)", got, postAttemptTimeout)
	}
	// The retry worst case, spelled out here instead of in a helper nothing else
	// read: postMaxAttempts attempts at postAttemptTimeout each, plus the 400ms and
	// 800ms waits between them. The budget must stay strictly tighter.
	worstCase := time.Duration(postMaxAttempts)*postAttemptTimeout + 1200*time.Millisecond
	if callBudget() >= worstCase {
		t.Fatalf("default budget %s must be tighter than the retry worst case %s",
			callBudget(), worstCase)
	}
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", postAttemptTimeout},
		{"150", 150 * time.Millisecond},
		{"0", postAttemptTimeout},   // zero must not disable the budget
		{"-5", postAttemptTimeout},  // nor may a negative value
		{"abc", postAttemptTimeout}, // nor a malformed one
	}
	for _, tc := range cases {
		t.Setenv("GEMINI_CALL_BUDGET_MS", tc.env)
		if got := callBudget(); got != tc.want {
			t.Errorf("GEMINI_CALL_BUDGET_MS=%q -> %s, want %s", tc.env, got, tc.want)
		}
	}
}

func TestWithCallBudgetAddsADeadline(t *testing.T) {
	t.Setenv("GEMINI_CALL_BUDGET_MS", "250")
	ctx, cancel := withCallBudget(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("withCallBudget must add a deadline when the caller has none")
	}
	if left := time.Until(deadline); left > 250*time.Millisecond || left <= 0 {
		t.Fatalf("deadline %s away, want <= 250ms", left)
	}
}

// A caller that already asked for something sooner must not be granted the
// longer default: the tighter deadline wins.
func TestWithCallBudgetNeverExtendsAnEarlierDeadline(t *testing.T) {
	t.Setenv("GEMINI_CALL_BUDGET_MS", "60000")
	parent, parentCancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer parentCancel()

	ctx, cancel := withCallBudget(parent)
	defer cancel()

	got, _ := ctx.Deadline()
	want, _ := parent.Deadline()
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want the caller's %s", got, want)
	}
}

// hangingServer accepts the request and never answers, so only the budget can
// end the call. Every request is counted.
func hangingServer(t *testing.T, calls *int32, stream bool) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hel"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func TestChatHonoursCallBudgetAndStopsRetrying(t *testing.T) {
	var calls int32
	srv := hangingServer(t, &calls, false)
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_FAST_MODEL", "")
	t.Setenv("GEMINI_CALL_BUDGET_MS", "200")

	s := New("test-key", "gemini-test", 128)

	start := time.Now()
	_, err := s.Chat(context.Background(), "hi", nil, "en")
	took := time.Since(start)

	if err == nil {
		t.Fatal("a call that never answers must fail, not hang")
	}
	// The point of the budget: nowhere near the 60s per-attempt ceiling, let
	// alone the 181s worst case.
	if took > 3*time.Second {
		t.Fatalf("Chat took %s; the 200ms budget did not reach the transport", took)
	}
	if !strings.Contains(err.Error(), "GEMINI_CALL_BUDGET_MS") {
		t.Errorf("error must name the budget knob, got %v", err)
	}
	// One attempt only: the budget covers the retries rather than letting each
	// one start a fresh 60s wait.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream attempts = %d, want 1 (the budget must stop the retries)", got)
	}
}

func TestChatStreamHonoursCallBudget(t *testing.T) {
	var calls int32
	srv := hangingServer(t, &calls, true)
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_FAST_MODEL", "")
	t.Setenv("GEMINI_CALL_BUDGET_MS", "200")

	s := New("test-key", "gemini-test", 128)

	start := time.Now()
	_, err := s.ChatStream(context.Background(), "hi", nil, "en", func(string) {})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a stream that goes silent must fail, not hang")
	}
	if took > 3*time.Second {
		t.Fatalf("ChatStream took %s; the budget did not reach the stream", took)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("the stub was never called: the test proves nothing")
	}
}

// The budget must not disturb the normal path: a stub that answers immediately
// still produces a reply, and does so in one attempt.
func TestCallBudgetLeavesAFastCallAlone(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_FAST_MODEL", "")
	t.Setenv("GEMINI_CALL_BUDGET_MS", "5000")

	s := New("test-key", "gemini-test", 128)
	res, err := s.Chat(context.Background(), "ping", nil, "en")
	if err != nil {
		t.Fatalf("fast call failed: %v", err)
	}
	if res.Reply != "pong" {
		t.Fatalf("reply = %q, want pong", res.Reply)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream attempts = %d, want 1", got)
	}
}
