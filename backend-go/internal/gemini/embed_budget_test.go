package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGenerateQueryEmbeddingHonoursBudget guards the per-call deadline that
// GenerateQueryEmbedding now pushes down to embed().
//
// Without it the hop ran straight into postWithRetry: up to three attempts
// against the client's 60s timeout, so an unresponsive embed endpoint stalled
// the reply path for ~3 minutes before the caller answered ungrounded anyway.
// The stub below never answers, so only the budget can end the call — a
// regression shows up as a test that hangs instead of one that fails.
func TestGenerateQueryEmbeddingHonoursBudget(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the request open; the budget is the only way out
	}))
	// LIFO: unblock the handler first, then let Close wait for it.
	defer srv.Close()
	defer close(release)

	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_EMBED_BUDGET_MS", "100")

	s := New("test-key", "gemini-test", 128)
	start := time.Now()
	if _, err := s.GenerateQueryEmbedding(context.Background(), "how much is shipping"); err == nil {
		t.Fatal("a hung embed endpoint must surface as an error, not a hang")
	}
	took := time.Since(start)
	// Generous ceiling: anything near the 60s client timeout, or the three
	// retries it would otherwise allow (~2x budget of backoff included), means
	// the budget is not reaching the transport.
	if took > 3*time.Second {
		t.Fatalf("embed budget ignored: took %v, want ~100ms", took)
	}
}

// TestEmbedBudgetDefault pins the default so a deploy that does not set
// GEMINI_EMBED_BUDGET_MS still gets the reply-path protection, and a malformed
// value falls back instead of disabling the ceiling (0 would mean "no limit").
func TestEmbedBudgetDefault(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"", 5 * time.Second},
		{"0", 5 * time.Second},
		{"-1", 5 * time.Second},
		{"not-a-number", 5 * time.Second},
		{"250", 250 * time.Millisecond},
	} {
		t.Setenv("GEMINI_EMBED_BUDGET_MS", tc.env)
		if got := embedBudget(); got != tc.want {
			t.Errorf("GEMINI_EMBED_BUDGET_MS=%q → %v, want %v", tc.env, got, tc.want)
		}
	}
}
