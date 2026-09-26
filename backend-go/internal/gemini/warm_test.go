package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbedKeepWarmInterval(t *testing.T) {
	t.Setenv("GEMINI_EMBED_KEEPWARM_SEC", "")
	if got := EmbedKeepWarmInterval(); got != 45*time.Second {
		t.Errorf("default = %v, want 45s (matches the Jev warmer, under the 90s idle timeout)", got)
	}
	t.Setenv("GEMINI_EMBED_KEEPWARM_SEC", "90")
	if got := EmbedKeepWarmInterval(); got != 90*time.Second {
		t.Errorf("override = %v, want 90s", got)
	}
	// 0 is the documented off switch; anything unparseable keeps the default
	// rather than silently disabling the warmer.
	t.Setenv("GEMINI_EMBED_KEEPWARM_SEC", "0")
	if got := EmbedKeepWarmInterval(); got != 0 {
		t.Errorf("0 must disable, got %v", got)
	}
	for _, bad := range []string{"-5", "soon", "45s"} {
		t.Setenv("GEMINI_EMBED_KEEPWARM_SEC", bad)
		if got := EmbedKeepWarmInterval(); got != 45*time.Second {
			t.Errorf("%q = %v, want the 45s default", bad, got)
		}
	}
}

// TestEmbedKeepWarmProbesAndStops — the probe must actually reach the network
// (not be answered from the query cache) and must stop with its context.
func TestEmbedKeepWarmProbesAndStops(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"predictions":[{"embeddings":{"values":[0.1]}}]}`))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)
	t.Setenv("GEMINI_PROVIDER", "studio")

	ctx, cancel := context.WithCancel(context.Background())
	s := New("test-key", "gemini-test", 128)
	s.StartEmbedKeepWarm(ctx, 20*time.Millisecond)

	// The first probe is synchronous-within-the-goroutine and immediate; give it
	// a moment to land before asserting.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&hits) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("no probe reached the server — the warmer would warm nothing")
	}
	// And it repeats on its interval.
	first := atomic.LoadInt32(&hits)
	time.Sleep(120 * time.Millisecond)
	if atomic.LoadInt32(&hits) <= first {
		t.Errorf("probes stopped at %d; the ticker should keep firing", first)
	}

	cancel()
	time.Sleep(80 * time.Millisecond)
	after := atomic.LoadInt32(&hits)
	time.Sleep(120 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != after {
		t.Errorf("probes continued after cancel: %d → %d", after, got)
	}
}

// TestEmbedKeepWarmDisabledCases — an unconfigured service makes no network
// call (nothing to warm), and a non-positive interval is the off switch.
func TestEmbedKeepWarmDisabledCases(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	t.Setenv("GEMINI_API_BASE", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No key → mock mode → nothing to warm.
	New("", "gemini-test", 128).StartEmbedKeepWarm(ctx, 10*time.Millisecond)
	// Configured, but explicitly off.
	New("test-key", "gemini-test", 128).StartEmbedKeepWarm(ctx, 0)

	time.Sleep(120 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("disabled warmer made %d requests, want 0", got)
	}
}
