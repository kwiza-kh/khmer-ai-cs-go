package typesafe

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeepWarmIntervalKnob(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", keepWarmDefaultSec * time.Second},       // unset -> default
		{"0", 0},                                     // explicit off
		{"120", 120 * time.Second},                   // explicit on
		{"banana", keepWarmDefaultSec * time.Second}, // garbage -> default
		{"-5", keepWarmDefaultSec * time.Second},     // negative -> default
	}
	for _, c := range cases {
		t.Setenv("JEV_KEEPWARM_SEC", c.env)
		if got := KeepWarmInterval(); got != c.want {
			t.Errorf("JEV_KEEPWARM_SEC=%q: got %v, want %v", c.env, got, c.want)
		}
	}
}

func TestStartKeepWarmProbesImmediatelyThenRepeats(t *testing.T) {
	body := judgeResponse(t)
	var calls atomic.Int32
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartKeepWarm(ctx, 20*time.Millisecond)

	// The first probe must fire before any tick, so the first customer turn
	// after a restart is already on a hot connection.
	if !waitForCalls(&calls, 1, time.Second) {
		t.Fatal("expected an immediate warm-up probe")
	}
	if !waitForCalls(&calls, 3, 3*time.Second) {
		t.Fatalf("expected repeated probes, got %d", calls.Load())
	}

	// Cancelling must actually stop the loop, not just quiet it.
	cancel()
	time.Sleep(80 * time.Millisecond)
	settled := calls.Load()
	time.Sleep(150 * time.Millisecond)
	if got := calls.Load(); got != settled {
		t.Fatalf("probes continued after cancel: %d -> %d", settled, got)
	}
}

func waitForCalls(n *atomic.Int32, target int32, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if n.Load() >= target {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return n.Load() >= target
}

// TestStartKeepWarmIsANoopWhenDisabled pins the contract that the warmer can be
// called unconditionally at startup: a nil client (the TYPESAFE_API_KEY-unset
// case), a keyless client, and an interval of 0 must all be silent no-ops.
func TestStartKeepWarmIsANoopWhenDisabled(t *testing.T) {
	var nilClient *Client
	nilClient.StartKeepWarm(context.Background(), time.Second)

	noKey := &Client{Endpoint: "http://127.0.0.1:1", Model: DefaultModel}
	noKey.StartKeepWarm(context.Background(), time.Second)

	enabled := &Client{APIKey: stubAuthValue, Endpoint: "http://127.0.0.1:1", Model: DefaultModel}
	enabled.StartKeepWarm(context.Background(), 0)
}
