package platform

import (
	"errors"
	"testing"
)

// The exact refusal production hit on 2026-10-06 (two Meta sends, 5 attempts
// each, hours of backoff for something only an App Review approval can fix).
const metaTagRefusal = `meta post /101849376095561/messages failed (400): (#100) Cannot tag messages with "HUMAN_AGENT" without prior approval.`

// Only a tagged send that was refused *for the tag* is terminal. A tagless 400
// with the same wording (or a tag attached to a different complaint) must stay
// retryable — otherwise a transient provider problem becomes a dead letter.
func TestHumanTagNotApproved(t *testing.T) {
	cases := []struct {
		name   string
		tag    string
		errTxt string
		want   bool
	}{
		{"tagged refusal is terminal", "HUMAN_AGENT", metaTagRefusal, true},
		{"tagless 400 stays retryable", "", metaTagRefusal, false},
		{"tagged but unrelated 400 stays retryable", "HUMAN_AGENT", "meta post /x/messages failed (400): (#2) Service temporarily unavailable", false},
		{"tagged throttle stays retryable", "HUMAN_AGENT", "meta post /x/messages failed (613): Calls to this api have exceeded the rate limit", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanTagNotApproved(tc.tag, tc.errTxt); got != tc.want {
				t.Fatalf("humanTagNotApproved(%q, %q) = %v, want %v", tc.tag, tc.errTxt, got, tc.want)
			}
		})
	}
}

// The pipeline classifies by errors.Is, so the wrap the client builds has to
// keep the sentinel reachable — and the two negative cases keep a transient
// provider problem from becoming a dead letter.
func TestClassifyMetaSendError(t *testing.T) {
	if err := classifyMetaSendError("HUMAN_AGENT", errors.New(metaTagRefusal)); !errors.Is(err, ErrHumanTagNotApproved) {
		t.Fatalf("tagged refusal did not match the sentinel: %v", err)
	}
	if err := classifyMetaSendError("", errors.New(metaTagRefusal)); errors.Is(err, ErrHumanTagNotApproved) {
		t.Fatalf("tagless refusal matched the sentinel: %v", err)
	}
	if err := classifyMetaSendError("HUMAN_AGENT", errors.New("boom")); errors.Is(err, ErrHumanTagNotApproved) {
		t.Fatalf("unrelated failure matched the sentinel: %v", err)
	}
}
