package platform

import (
	"testing"
	"time"
)

// A panic inside one turn must not take the process with it: Go kills the whole
// program on an unrecovered panic in any goroutine, and these workers are the
// only entry point for every tenant's messages and replies. Without the guard
// this test does not fail — it crashes the test binary, which is exactly what
// would happen in production (audited 2026-10-16).
func TestRunTurnGuardedSurvivesPanic(t *testing.T) {
	p := &Pipeline{Logger: quietLogger()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurnGuarded("inbound", func() { panic("boom from a turn") })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the guarded worker never returned after a panic")
	}
}

// The guard must not swallow the work itself — a worker that recovers from
// everything would be a worker that does nothing.
func TestRunTurnGuardedRunsTheWork(t *testing.T) {
	p := &Pipeline{Logger: quietLogger()}
	ran := false
	p.runTurnGuarded("outbound", func() { ran = true })
	if !ran {
		t.Fatal("the guarded call never ran its function")
	}
}

// The alert paths start here, and they are reached from recover() handlers: a
// nil Cfg must answer "no bot configured", never become a second panic.
func TestPlatformBotEnabledWithoutConfig(t *testing.T) {
	p := &Pipeline{}
	if p.PlatformBotEnabled() {
		t.Fatal("a pipeline with no config cannot have an alert bot")
	}
	// The full alert path must survive the same shape.
	p.PlatformAlert(t.Context(), "test-key", "title", "detail")
	p.PlatformAlertResolved(t.Context(), "test-key")
}

// Guards the recovery path's own assumptions: the panic value is formatted into
// the alert detail, so a non-string panic value must not break it.
func TestRunTurnGuardedHandlesNonStringPanic(t *testing.T) {
	p := &Pipeline{Logger: quietLogger()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runTurnGuarded("outbound", func() { panic(struct{ Code int }{Code: 7}) })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the guarded worker never returned")
	}
}
