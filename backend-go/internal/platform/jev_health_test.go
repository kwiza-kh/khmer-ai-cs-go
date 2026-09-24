package platform

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// alertRecorder captures what the health observer would page.
type alertRecorder struct {
	mu       sync.Mutex
	alerts   []string // "key"
	resolved []string
}

func newAlertRecorder() *alertRecorder { return &alertRecorder{} }

func (r *alertRecorder) alert(key, _, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, key)
}

func (r *alertRecorder) resolve(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved = append(r.resolved, key)
}

func (r *alertRecorder) counts() (map[string]int, map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := map[string]int{}
	for _, k := range r.alerts {
		a[k]++
	}
	d := map[string]int{}
	for _, k := range r.resolved {
		d[k]++
	}
	return a, d
}

func newTestHealth() (*jevHealth, *alertRecorder) {
	r := newAlertRecorder()
	return &jevHealth{alert: r.alert, resolve: r.resolve}, r
}

// A single dropped connection must not page; the page comes on the Nth
// consecutive failure and only once per outage.
func TestJevHealthPagesOnceAfterThreshold(t *testing.T) {
	h, r := newTestHealth()
	err := errors.New("context deadline exceeded")

	for i := 1; i < jevDownAfter; i++ {
		h.JudgeFailed(err)
		if a, _ := r.counts(); a["jev-down"] != 0 {
			t.Fatalf("paged after %d failure(s); threshold is %d", i, jevDownAfter)
		}
	}
	h.JudgeFailed(err)
	a, _ := r.counts()
	if a["jev-down"] != 1 {
		t.Fatalf("expected exactly one page at the threshold, got %d", a["jev-down"])
	}

	// Further failures during the same outage must not page again.
	for i := 0; i < 10; i++ {
		h.JudgeFailed(err)
	}
	if a, _ := r.counts(); a["jev-down"] != 1 {
		t.Fatalf("outage paged more than once: %d", a["jev-down"])
	}
}

// Recovery clears the dedup key and announces itself.
func TestJevHealthRecoveryClearsAndAnnounces(t *testing.T) {
	h, r := newTestHealth()
	for i := 0; i < jevDownAfter; i++ {
		h.JudgeFailed(errors.New("down"))
	}
	h.JudgeSucceeded(200 * time.Millisecond)

	a, d := r.counts()
	if d["jev-down"] != 1 {
		t.Fatalf("recovery must clear the jev-down dedup key, resolved=%v", d)
	}
	if a["jev-recovered"] != 1 {
		t.Fatalf("recovery must be announced, alerts=%v", a)
	}

	// A second outage after recovery must page again.
	for i := 0; i < jevDownAfter; i++ {
		h.JudgeFailed(errors.New("down again"))
	}
	if a, _ := r.counts(); a["jev-down"] != 2 {
		t.Fatalf("a fresh outage must page again, got %d", a["jev-down"])
	}
}

// A successful-but-slow call is degradation too: it produces the same silent
// fallback as an error, because the reply-path budgets are 3s/4s.
func TestJevHealthPagesOnSlowSuccess(t *testing.T) {
	h, r := newTestHealth()

	h.JudgeSucceeded(jevSlowAfter - time.Millisecond) // fast enough, silent
	if a, _ := r.counts(); a["jev-slow"] != 0 {
		t.Fatal("a fast call must not page")
	}

	h.JudgeSucceeded(jevSlowAfter + time.Second)
	a, _ := r.counts()
	if a["jev-slow"] != 1 {
		t.Fatalf("a slow call must page once, got %d", a["jev-slow"])
	}

	// Throttled: a second slow call inside the window stays silent.
	h.JudgeSucceeded(jevSlowAfter + time.Second)
	if a, _ := r.counts(); a["jev-slow"] != 1 {
		t.Fatalf("slow page was not throttled: %d", a["jev-slow"])
	}
}

// The fast path must stay completely silent.
func TestJevHealthSilentWhenHealthy(t *testing.T) {
	h, r := newTestHealth()
	for i := 0; i < 50; i++ {
		h.JudgeSucceeded(300 * time.Millisecond)
	}
	a, d := r.counts()
	if len(a) != 0 || len(d) != 0 {
		t.Fatalf("healthy traffic must not alert at all: alerts=%v resolved=%v", a, d)
	}
}

// A failure streak interrupted by one success restarts the count: this is what
// keeps intermittent single-call failures from paging.
func TestJevHealthSuccessResetsFailureStreak(t *testing.T) {
	h, r := newTestHealth()
	for i := 0; i < 20; i++ {
		h.JudgeFailed(errors.New("blip"))
		if i%2 == 1 {
			h.JudgeSucceeded(200 * time.Millisecond)
		}
	}
	if a, _ := r.counts(); a["jev-down"] != 0 {
		t.Fatalf("interleaved blips must never page, got %d", a["jev-down"])
	}
}
