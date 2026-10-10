package main

import (
	"testing"

	"khmer-ai-cs-go/internal/replyscore"
)

// The reply gate's pass/fail rule, pinned.
//
// The case that motivated this file: faq-s16's judge call exhausted its 45s
// budget, judgeReply returned ScoreTotal 0 / "judge unavailable", and the run
// still reported "24/24 cases pass; judge average 11.83/12" — the one reply
// nobody scored was the one reported as fine. Any future edit that makes an
// unscored case pass again fails here.
func TestJudgeOffKeepsTheDeterministicGate(t *testing.T) {
	mode := evalOptions{judge: false, gate: 8}
	r := caseResult{c: replyscore.Case{Category: "price"}}
	if !r.ok(mode) {
		t.Fatal("-judge=false must not fail a case for having no score")
	}
	r.missing = []string{"$45.00"}
	if r.ok(mode) {
		t.Fatal("a missing fact must fail the case with or without the judge")
	}
}

func TestUnjudgedCaseFailsWhenJudgingIsOn(t *testing.T) {
	mode := evalOptions{judge: true, gate: 8}

	// What judgeReply leaves behind when GenerateFast fails or its budget expires.
	unavailable := caseResult{
		c:       replyscore.Case{Category: "ordering"},
		judged:  true,
		verdict: judgeVerdict{Reason: "judge unavailable"},
	}
	if unavailable.ok(mode) {
		t.Fatal("an unscored case must FAIL when -judge is on (this is the 2026-10-04 false green)")
	}

	// A judge that really did score everything zero is a bad reply, not a pass.
	zeroed := caseResult{
		c:       replyscore.Case{Category: "ordering"},
		judged:  true,
		verdict: judgeVerdict{Language: 0, Register: 0, Natural: 0, Format: 0, Reason: "unusable"},
	}
	if zeroed.ok(mode) {
		t.Fatal("a 0/12 verdict must fail")
	}
}

func TestScoredCaseUsesTheGate(t *testing.T) {
	mode := evalOptions{judge: true, gate: 8}
	for _, tc := range []struct {
		score int
		want  bool
	}{
		{7, false}, // below the gate
		{8, true},  // exactly at the gate is a pass
		{12, true}, // full marks
	} {
		r := caseResult{
			c:       replyscore.Case{Category: "price"},
			judged:  true,
			verdict: judgeVerdict{Total: tc.score, Reason: "scored"},
		}
		if got := r.ok(mode); got != tc.want {
			t.Errorf("score %d/12 with gate %.1f: ok() = %v, want %v", tc.score, mode.gate, got, tc.want)
		}
	}
}

// A knowledge-base miss on a category that needs grounding fails regardless of
// the judge: the run measured the wrong world, and a fluent reply about nothing
// must not paper over it.
func TestGroundingMissStillFails(t *testing.T) {
	mode := evalOptions{judge: true, gate: 8}
	for _, category := range []string{"price", "product", "company", "ordering", "delivery", "payment"} {
		r := caseResult{
			c:       replyscore.Case{Category: category},
			noKB:    true,
			judged:  true,
			verdict: judgeVerdict{Total: 12, Reason: "would score full marks"},
		}
		if r.ok(mode) {
			t.Errorf("category %q: a KB miss must fail even with a 12/12 verdict", category)
		}
	}
	// handoff and trap are procedural: a miss there is normal (see requiresGrounding).
	for _, category := range []string{"handoff", "trap"} {
		r := caseResult{
			c:       replyscore.Case{Category: category},
			noKB:    true,
			judged:  true,
			verdict: judgeVerdict{Total: 12},
		}
		if !r.ok(mode) {
			t.Errorf("category %q: a KB miss is expected there and must not fail", category)
		}
	}
}
