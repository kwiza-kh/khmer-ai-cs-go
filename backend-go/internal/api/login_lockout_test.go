package api

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"khmer-ai-cs-go/internal/redisstore"
)

func newLockoutApp(t *testing.T) *App {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := redisstore.Connect(mr.Addr(), "", 0)
	if err != nil {
		t.Fatalf("redis connect: %v", err)
	}
	return &App{Redis: c}
}

// TestLoginLockKeysScopeTheLockToTheSourceAddress is the regression test for the
// account-lockout denial of service: with the lock keyed on the username alone,
// one stranger could lock any named account for lockoutTTL with five requests
// and the real merchant's correct password still got a 429. The failure counter
// and the high account ceiling must stay per-username (so distributed guessing
// still accumulates), but the cheap lock must differ per source address.
func TestLoginLockKeysScopeTheLockToTheSourceAddress(t *testing.T) {
	failA, lockA, accountA := loginLockKeys("Victim", "203.0.113.9")
	failB, lockB, accountB := loginLockKeys("victim", "198.51.100.7")

	if failA != failB {
		t.Fatalf("the failure counter must be per-username so a rotation still accumulates: %q vs %q", failA, failB)
	}
	if accountA != accountB {
		t.Fatalf("the account ceiling must be per-username: %q vs %q", accountA, accountB)
	}
	if lockA == lockB {
		t.Fatal("the lock must be scoped to the source address, or anyone can lock any account out")
	}
	// Usernames are case-insensitive at login, so the keys must be too — a
	// mixed-case request must not mint a fresh counter that resets the
	// attacker's progress.
	failUpper, _, _ := loginLockKeys("VICTIM", "203.0.113.9")
	if failA != failUpper {
		t.Fatalf("keys must be case-folded, got %q for %q", failUpper, failA)
	}
}

// TestRecordLoginFailureArmsPerAddressLockBeforeAccountCeiling pins the two-lock
// behaviour: the per-address lock trips at lockoutThreshold, and the account
// ceiling — which a stranger must not be able to reach — stays armed only after
// lockoutAccountThreshold failures.
func TestRecordLoginFailureArmsPerAddressLockBeforeAccountCeiling(t *testing.T) {
	a := newLockoutApp(t)
	ctx := context.Background()
	failKey, lockKey, accountKey := loginLockKeys("victim", "203.0.113.9")

	for i := 0; i < lockoutThreshold; i++ {
		a.recordLoginFailure(ctx, failKey, lockKey, accountKey)
	}
	if locked, _ := a.Redis.GetJSON(ctx, lockKey); locked == nil {
		t.Fatalf("after %d failures the source address must be locked", lockoutThreshold)
	}
	if locked, _ := a.Redis.GetJSON(ctx, accountKey); locked != nil {
		t.Fatalf("the account ceiling (%d) must NOT trip at %d failures, or one stranger can lock a merchant out",
			lockoutAccountThreshold, lockoutThreshold)
	}

	for i := lockoutThreshold; i < lockoutAccountThreshold; i++ {
		a.recordLoginFailure(ctx, failKey, lockKey, accountKey)
	}
	if locked, _ := a.Redis.GetJSON(ctx, accountKey); locked == nil {
		t.Fatalf("after %d failures spread over rotating addresses the account must lock", lockoutAccountThreshold)
	}
}
