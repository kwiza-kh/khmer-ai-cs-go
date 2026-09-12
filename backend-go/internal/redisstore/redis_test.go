package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newTestClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := Connect(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	return c, mr
}

// TestCheckRateLimitWindowActuallyCloses is the regression guard for the
// 2026-09 "poison key self-heal" fix that re-armed the window TTL on every
// call. A continuously active caller never saw its window close: the counter
// only grew, so once it passed max it stayed blocked for as long as it kept
// retrying — every retry pushing the deadline out another full minute.
//
// The caller here sends at t=0, 20s, 40s (all inside the first 60s window) and
// again at t=65s. A correct fixed window has expired by then, so the fourth
// call starts a fresh window and is allowed.
func TestCheckRateLimitWindowActuallyCloses(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	const max = 3

	if ok, err := c.CheckRateLimit(ctx, "u:1", max); err != nil || !ok {
		t.Fatalf("call 1: allowed=%v err=%v, want allowed", ok, err)
	}
	mr.FastForward(20 * time.Second)

	if ok, err := c.CheckRateLimit(ctx, "u:1", max); err != nil || !ok {
		t.Fatalf("call 2: allowed=%v err=%v, want allowed", ok, err)
	}
	mr.FastForward(20 * time.Second)

	if ok, err := c.CheckRateLimit(ctx, "u:1", max); err != nil || !ok {
		t.Fatalf("call 3: allowed=%v err=%v, want allowed", ok, err)
	}
	mr.FastForward(25 * time.Second) // t=65s, past the window opened at t=0

	ok, err := c.CheckRateLimit(ctx, "u:1", max)
	if err != nil {
		t.Fatalf("call 4: unexpected error %v", err)
	}
	if !ok {
		t.Fatal("call 4 was blocked: the 60s window never closed — the TTL is being re-armed on every call")
	}
}

// TestCheckRateLimitBlocksWithinWindow pins the other half of the contract:
// the limiter still trips once a caller exceeds max inside one window.
func TestCheckRateLimitBlocksWithinWindow(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	const max = 3

	for i := 1; i <= max; i++ {
		ok, err := c.CheckRateLimit(ctx, "u:2", max)
		if err != nil || !ok {
			t.Fatalf("call %d: allowed=%v err=%v, want allowed", i, ok, err)
		}
	}
	ok, err := c.CheckRateLimit(ctx, "u:2", max)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("call %d exceeded max=%d but was allowed", max+1, max)
	}
}

// TestCheckRateLimitHealsTTLLessKey covers the case the earlier fix was
// actually aiming at: a key leaked without an expiry (TTL = -1) by the old
// INCR-then-EXPIRE pattern must be re-armed rather than blocking its owner
// forever.
func TestCheckRateLimitHealsTTLLessKey(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	const rk = "ratelimit:poison"

	// Simulate the legacy leak: a counter far past max with no expiry.
	mr.Set(rk, "99")
	if ttl := mr.TTL(rk); ttl != 0 {
		t.Fatalf("precondition: key should have no TTL, got %v", ttl)
	}

	ok, err := c.CheckRateLimit(ctx, "poison", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("count 100 over max 3 should be blocked")
	}
	if ttl := mr.TTL(rk); ttl <= 0 {
		t.Fatal("poison key was not healed: it still has no expiry, so its owner stays blocked forever")
	}

	// Once the healed window elapses the owner is released.
	mr.FastForward(61 * time.Second)
	if ok, err := c.CheckRateLimit(ctx, "poison", 3); err != nil || !ok {
		t.Fatalf("after the healed window: allowed=%v err=%v, want allowed", ok, err)
	}
}

// TestIncrWindowSharesFixedWindowSemantics checks the daily/hourly caps that
// the widget and Telegram-notify throttles rely on.
func TestIncrWindowSharesFixedWindowSemantics(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	const key = "widget:token:daily"

	// max 2 per hour, sent at t=0 and t=30m; both land in the same window.
	if ok, err := c.IncrWindow(ctx, key, 2, time.Hour); err != nil || !ok {
		t.Fatalf("call 1: allowed=%v err=%v, want allowed", ok, err)
	}
	mr.FastForward(30 * time.Minute)
	if ok, err := c.IncrWindow(ctx, key, 2, time.Hour); err != nil || !ok {
		t.Fatalf("call 2: allowed=%v err=%v, want allowed", ok, err)
	}
	// Third call inside the same hour is over the cap.
	if ok, _ := c.IncrWindow(ctx, key, 2, time.Hour); ok {
		t.Fatal("third call within the hour exceeded max=2 but was allowed")
	}
	// Past the hour the window resets rather than staying stuck.
	mr.FastForward(31 * time.Minute)
	if ok, err := c.IncrWindow(ctx, key, 2, time.Hour); err != nil || !ok {
		t.Fatalf("after the hour: allowed=%v err=%v, want allowed", ok, err)
	}
}
