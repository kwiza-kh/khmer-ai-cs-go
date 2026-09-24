package platform

import (
	"testing"

	"khmer-ai-cs-go/internal/gemini"
)

// The two escalation bars are separate knobs. JEV_TURN_ESCALATE_MIN gates the
// verdict flag judgeTurnJev sets; JEV_RULE_SOLO_MIN gates TurnTriggerFor's solo
// valve. They used to share one variable while carrying different defaults
// (0.60 vs 0.90), so with no JEV_* set each site used its own default and
// tuning one would silently have moved the other. Pin the independence.
func TestSoloBarIsIndependentOfTheVerdictKnob(t *testing.T) {
	// Intent "question" + neutral sentiment keeps the rule-confirm path out of
	// the way, and hasMatch=true keeps the no-knowledge-base rule out, so the
	// only path that can fire is the solo valve.
	v := gemini.TurnVerdict{Intent: "question", Sentiment: "neutral", Confidence: 0.9}
	const noul = 0.80

	// Default solo bar is 0.90: 0.80 must not clear it.
	if trig, reason := TurnTriggerFor(v, noul, true, true); trig != "" {
		t.Fatalf("noul 0.80 must stay below the default solo bar 0.90, got %q (%s)", trig, reason)
	}

	// Moving the *verdict* knob must not move the solo bar at all.
	t.Setenv("JEV_TURN_ESCALATE_MIN", "0.10")
	if trig, reason := TurnTriggerFor(v, noul, true, true); trig != "" {
		t.Fatalf("JEV_TURN_ESCALATE_MIN must not affect the solo bar, got %q (%s)", trig, reason)
	}

	// The solo knob itself still works, and reports the ai_decision trigger.
	t.Setenv("JEV_RULE_SOLO_MIN", "0.50")
	trig, reason := TurnTriggerFor(v, noul, true, true)
	if trig != "ai_decision" {
		t.Fatalf("noul 0.80 must clear a 0.50 solo bar, got %q (%s)", trig, reason)
	}
}

// The rule-confirm path keeps its own knob, unaffected by the solo bar.
func TestRuleConfirmKnobIsIndependentOfSoloBar(t *testing.T) {
	v := gemini.TurnVerdict{Intent: "refund", Sentiment: "neutral", Confidence: 0.9}

	// A refund intent at 0.75 clears the default confirm bar (0.70) and yields
	// negative_feedback, which is checked before the solo valve.
	if trig, _ := TurnTriggerFor(v, 0.75, true, true); trig != "negative_feedback" {
		t.Fatalf("refund at 0.75 must confirm at the default 0.70 bar, got %q", trig)
	}

	// Raise only the solo bar far above: the confirm path must still fire.
	t.Setenv("JEV_RULE_SOLO_MIN", "0.99")
	if trig, _ := TurnTriggerFor(v, 0.75, true, true); trig != "negative_feedback" {
		t.Fatalf("raising the solo bar must not disable the confirm path, got %q", trig)
	}

	// Raising the confirm bar above the noul does suppress it.
	t.Setenv("JEV_RULE_CONFIRM_MIN", "0.90")
	if trig, _ := TurnTriggerFor(v, 0.75, true, true); trig != "" {
		t.Fatalf("0.75 must not clear a 0.90 confirm bar, got %q", trig)
	}
}
