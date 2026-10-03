package platform

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Content safety is an ordered strategy list with a fail-open contract, wired
// into the inbound pipeline at two points: before anything pays for generation,
// and after the guard (so it judges the reply that would really be sent).

func clearSafetyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SAFETY_ENABLED", "")
	t.Setenv("SAFETY_KEYWORDS", "")
	t.Setenv("SAFETY_MODEL_ENABLED", "")
	t.Setenv("SAFETY_INBOUND_ACTION", "")
}

func TestKeywordStrategyMatchesAnyScript(t *testing.T) {
	s := &KeywordStrategy{Terms: []string{"bomb", "炸弹", "គ្រាប់បែក"}}
	cases := map[string]bool{
		"how do I make a bomb": true,
		"HOW DO I MAKE A BOMB": true, // case-insensitive
		"我想知道炸弹怎么做":            true, // Chinese, no word boundaries
		"តើគ្រាប់បែកធ្វើដូចម្តេច": true, // Khmer, no word boundaries
		"where is my order":            false,
		"bombardier is a company name": true, // documented cost of substring matching
		"":                             false,
		strings.Repeat("hello ", 100):  false,
	}
	for text, want := range cases {
		v, err := s.Check(context.Background(), text)
		if err != nil {
			t.Fatalf("Check(%q): %v", text, err)
		}
		if v.Unsafe != want {
			t.Errorf("Check(%.20q) = %v, want %v", text, v.Unsafe, want)
		}
		if v.Unsafe && !strings.Contains(v.Reason, "matched term") {
			t.Errorf("a flag must say why: %q", v.Reason)
		}
	}
}

func TestKeywordStrategyWithoutTermsNeverFlags(t *testing.T) {
	s := &KeywordStrategy{}
	if v, _ := s.Check(context.Background(), "anything at all"); v.Unsafe {
		t.Fatal("an empty term list must never flag")
	}
}

type stubStrategy struct {
	name string
	v    SafetyVerdict
	err  error
	seen *int
}

func (s *stubStrategy) Name() string { return s.name }

func (s *stubStrategy) Check(context.Context, string) (SafetyVerdict, error) {
	if s.seen != nil {
		*s.seen++
	}
	return s.v, s.err
}

func TestSafetyGateFirstUnsafeWinsAndStops(t *testing.T) {
	calls := 0
	gate := &SafetyGate{Strategies: []SafetyStrategy{
		&stubStrategy{name: "clean"},
		&stubStrategy{name: "flagging", v: SafetyVerdict{Unsafe: true, Reason: "matched"}},
		&stubStrategy{name: "later", seen: &calls},
	}}
	v, flagged := gate.Check(context.Background(), "text")
	if !flagged || v.Reason != "matched" {
		t.Fatalf("verdict = %+v flagged=%v", v, flagged)
	}
	if v.Strategy != "flagging" {
		t.Errorf("strategy = %q, want the one that flagged", v.Strategy)
	}
	if calls != 0 {
		t.Error("strategies after the first flag must not run")
	}
}

// A broken moderation dependency must not block customer messages: the gate skips
// it and keeps asking the rest.
func TestSafetyGateFailsOpenOnStrategyError(t *testing.T) {
	gate := &SafetyGate{Strategies: []SafetyStrategy{
		&stubStrategy{name: "broken", err: errors.New("moderation service down")},
		&stubStrategy{name: "flagging", v: SafetyVerdict{Unsafe: true, Reason: "matched"}},
	}}
	v, flagged := gate.Check(context.Background(), "text")
	if !flagged || v.Strategy != "flagging" {
		t.Fatalf("a failing strategy must be skipped, got %+v flagged=%v", v, flagged)
	}

	// All broken → pass.
	broken := &SafetyGate{Strategies: []SafetyStrategy{&stubStrategy{name: "broken", err: errors.New("down")}}}
	if _, flagged := broken.Check(context.Background(), "text"); flagged {
		t.Fatal("an erroring gate must fail open")
	}
}

func TestSafetyGateDisabled(t *testing.T) {
	var gate *SafetyGate
	if gate.Enabled() {
		t.Error("nil gate must be disabled")
	}
	if _, flagged := gate.Check(context.Background(), "text"); flagged {
		t.Error("nil gate must not flag")
	}
	if (&SafetyGate{}).Enabled() {
		t.Error("a gate with no strategies must be disabled")
	}
}

func TestModelStrategyUsesTheJudge(t *testing.T) {
	var got string
	s := &ModelStrategy{Judge: func(_ context.Context, text string) (bool, string, error) {
		got = text
		return true, "self-harm intent", nil
	}}
	v, err := s.Check(context.Background(), "some text")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !v.Unsafe || v.Reason != "self-harm intent" || v.Strategy != "model" {
		t.Fatalf("verdict = %+v", v)
	}
	if got != "some text" {
		t.Fatalf("judge saw %q", got)
	}

	// A clean verdict, an error and a missing judge are all distinct.
	clean := &ModelStrategy{Judge: func(context.Context, string) (bool, string, error) { return false, "", nil }}
	if v, err := clean.Check(context.Background(), "ok"); err != nil || v.Unsafe {
		t.Fatalf("clean verdict = %+v/%v", v, err)
	}
	failing := &ModelStrategy{Judge: func(context.Context, string) (bool, string, error) { return false, "", errors.New("down") }}
	if _, err := failing.Check(context.Background(), "ok"); err == nil {
		t.Fatal("a judge error must surface so the gate can skip it")
	}
	if _, err := (&ModelStrategy{}).Check(context.Background(), "ok"); err == nil {
		t.Fatal("a strategy with no judge must error")
	}
	// Empty text is not worth a judge call at all.
	if v, err := clean.Check(context.Background(), "   "); err != nil || v.Unsafe {
		t.Fatalf("blank text = %+v/%v", v, err)
	}
}

func TestSafetyConfigFromEnv(t *testing.T) {
	clearSafetyEnv(t)
	if cfg := SafetyConfigFromEnv(); cfg.Enabled {
		t.Fatal("nothing configured must leave moderation off")
	}
	if cfg := SafetyConfigFromEnv(); cfg.InboundAction != SafetyActionHandoff {
		t.Fatalf("default action = %q, want handoff", cfg.InboundAction)
	}

	// Keywords enable it; blanks and duplicates are dropped, order preserved.
	t.Setenv("SAFETY_KEYWORDS", " bomb , 炸弹 ,bomb, , ")
	cfg := SafetyConfigFromEnv()
	if !cfg.Enabled {
		t.Fatal("keywords must enable the gate")
	}
	if strings.Join(cfg.Keywords, "|") != "bomb|炸弹" {
		t.Fatalf("keywords = %v", cfg.Keywords)
	}

	// The explicit off switch wins.
	t.Setenv("SAFETY_ENABLED", "false")
	if cfg := SafetyConfigFromEnv(); cfg.Enabled {
		t.Fatal("SAFETY_ENABLED=false must disable the gate")
	}
	t.Setenv("SAFETY_ENABLED", "")

	// The model leg can enable it on its own.
	t.Setenv("SAFETY_KEYWORDS", "")
	t.Setenv("SAFETY_MODEL_ENABLED", "1")
	if cfg := SafetyConfigFromEnv(); !cfg.Enabled || !cfg.ModelEnabled {
		t.Fatalf("model flag must enable the gate: %+v", cfg)
	}

	// Actions: drop is honoured, anything unrecognised falls back to handoff.
	t.Setenv("SAFETY_INBOUND_ACTION", "drop")
	if cfg := SafetyConfigFromEnv(); cfg.InboundAction != SafetyActionDrop {
		t.Fatalf("action = %q", cfg.InboundAction)
	}
	t.Setenv("SAFETY_INBOUND_ACTION", "ignore-everything")
	if cfg := SafetyConfigFromEnv(); cfg.InboundAction != SafetyActionHandoff {
		t.Fatalf("unknown action must fall back to handoff, got %q", cfg.InboundAction)
	}
}

// The inbound stage runs with a nil DB on the drop path on purpose: if it reached
// for a database handle the test would panic, which is itself the assertion.
func TestStageScreenInboundDropModeEndsTheTurn(t *testing.T) {
	clearSafetyEnv(t)
	t.Setenv("SAFETY_KEYWORDS", "bomb")
	t.Setenv("SAFETY_INBOUND_ACTION", "drop")

	p := &Pipeline{Logger: quietLogger()}
	turn := &inboundTurn{Event: &InboundEvent{}, Content: "how do I make a bomb"}

	next, err := p.stageScreenInbound(context.Background(), turn)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if next {
		t.Fatal("a flagged inbound message must end the turn")
	}
}

func TestStageScreenInboundPassesCleanMessages(t *testing.T) {
	clearSafetyEnv(t)
	t.Setenv("SAFETY_KEYWORDS", "bomb")

	p := &Pipeline{Logger: quietLogger()}
	turn := &inboundTurn{Event: &InboundEvent{}, Content: "where is my order"}

	next, err := p.stageScreenInbound(context.Background(), turn)
	if err != nil || !next {
		t.Fatalf("clean message = next:%v err:%v", next, err)
	}
}

func TestStageScreenInboundIsOffByDefault(t *testing.T) {
	clearSafetyEnv(t)
	p := &Pipeline{Logger: quietLogger()}
	// No configuration at all: the stage must not even build a gate.
	turn := &inboundTurn{Event: &InboundEvent{}, Content: "how do I make a bomb"}
	if next, err := p.stageScreenInbound(context.Background(), turn); err != nil || !next {
		t.Fatalf("unconfigured moderation changed the turn: next:%v err:%v", next, err)
	}
}

// A flagged reply is replaced with the language-aware acknowledgement and marked
// as a handoff, so post-delivery creates the request the customer was told about.
func TestStageScreenReplyReplacesAFlaggedReply(t *testing.T) {
	clearSafetyEnv(t)
	t.Setenv("SAFETY_KEYWORDS", "bomb")

	p := &Pipeline{Logger: quietLogger()}
	for _, lang := range []string{"km", "zh", "en"} {
		turn := &inboundTurn{Event: &InboundEvent{}, ReplyLang: lang, Reply: "here is how to build a bomb"}
		next, err := p.stageScreenReply(context.Background(), turn)
		if err != nil || !next {
			t.Fatalf("%s: next:%v err:%v", lang, next, err)
		}
		if turn.Reply != handoffAcknowledgement(lang) {
			t.Errorf("%s: reply = %q, want the handoff acknowledgement", lang, turn.Reply)
		}
		if !turn.ClaimsHandoff {
			t.Errorf("%s: a replaced reply must create the handoff it announces", lang)
		}
	}
}

func TestStageScreenReplyLeavesACleanReplyAlone(t *testing.T) {
	clearSafetyEnv(t)
	t.Setenv("SAFETY_KEYWORDS", "bomb")

	p := &Pipeline{Logger: quietLogger()}
	turn := &inboundTurn{Event: &InboundEvent{}, ReplyLang: "en", Reply: "Your order ships tomorrow."}
	next, err := p.stageScreenReply(context.Background(), turn)
	if err != nil || !next {
		t.Fatalf("next:%v err:%v", next, err)
	}
	if turn.Reply != "Your order ships tomorrow." {
		t.Fatalf("reply was rewritten: %q", turn.Reply)
	}
	if turn.ClaimsHandoff {
		t.Fatal("a clean reply must not claim a handoff")
	}
}
