package typesafe

import "time"

// HealthObserver receives the outcome of every Judge call.
//
// Jev is optional by design: every call site degrades to a slower path when it
// fails, so an outage silently changes product behaviour — a turn that timed
// out is decided by the fast model, which over-escalates — and nothing in the
// system notices. This hook is what makes that audible. Instrumenting Judge
// covers all seven integration points at once.
//
// Both methods run INLINE on the goroutine that called Judge, so an
// implementation must not block: hand real work (a page, an HTTP call) to a
// background lane.
type HealthObserver interface {
	// JudgeFailed reports one Judge call that exhausted its retries, or whose
	// caller-imposed deadline expired before it could answer. A deadline is a
	// health signal too: a Jev that answers in 5s is reachable but useless to a
	// reply path budgeted at 3s.
	JudgeFailed(err error)

	// JudgeSucceeded reports a completed call and how long it took.
	JudgeSucceeded(took time.Duration)
}

// SetHealthObserver installs an observer. Call once during startup, before the
// server begins serving — the field is read without synchronisation, matching
// the other Client knobs (HTTP, Logger).
func (c *Client) SetHealthObserver(o HealthObserver) {
	if c == nil {
		return
	}
	c.observer = o
}

func (c *Client) notifyFailed(err error) {
	if c != nil && c.observer != nil {
		c.observer.JudgeFailed(err)
	}
}

func (c *Client) notifySucceeded(took time.Duration) {
	if c != nil && c.observer != nil {
		c.observer.JudgeSucceeded(took)
	}
}
