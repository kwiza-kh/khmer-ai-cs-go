package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"khmer-ai-cs-go/internal/typesafe"
)

// jevRouteServer stubs the Judge endpoint. urgency is the probability-weighted
// score position (0..2); pass nil to omit the answer entirely and exercise the
// unknown-urgency path.
func jevRouteServer(t *testing.T, route string, prob float64, urgency *float64) *typesafe.Client {
	t.Helper()
	answers := map[string]any{
		"route": map[string]any{
			"type": "choice", "choice": route, "confidence": 0.9,
			"probabilities": map[string]float64{route: prob},
		},
	}
	if urgency != nil {
		answers["urgency"] = map[string]any{"type": "score", "score": *urgency}
	}
	body, _ := json.Marshal(map[string]any{"model": "jev-1.13.0", "answers": answers})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func score(v float64) *float64 { return &v }

func TestRouteInboundParsesChoiceProbabilityAndUrgency(t *testing.T) {
	p := &Pipeline{Jev: jevRouteServer(t, RouteHandoff, 0.93, score(1.8)), Logger: quietLogger()}
	r, ok := p.RouteInbound(context.Background(), "I want a real person")
	if !ok || r.Route != RouteHandoff || r.Prob != 0.93 {
		t.Fatalf("route=%+v ok=%v", r, ok)
	}
	if r.Urgency != UrgencyUrgent {
		t.Fatalf("urgency = %q, want urgent (score 1.8 ≥ 1.5)", r.Urgency)
	}
}

func TestRouteInboundUrgencyBands(t *testing.T) {
	for _, tc := range []struct {
		score float64
		want  string
	}{
		{0.1, UrgencyRoutine},
		{0.5, UrgencyElevated},
		{1.2, UrgencyElevated},
		{1.5, UrgencyUrgent},
	} {
		p := &Pipeline{Jev: jevRouteServer(t, RouteKBQuestion, 0.9, score(tc.score)), Logger: quietLogger()}
		r, ok := p.RouteInbound(context.Background(), "how much is the water filter")
		if !ok || r.Urgency != tc.want {
			t.Fatalf("score %.2f: urgency = %q ok=%v, want %q", tc.score, r.Urgency, ok, tc.want)
		}
	}
}

func TestRouteInboundMissingUrgencyIsUnknown(t *testing.T) {
	p := &Pipeline{Jev: jevRouteServer(t, RouteKBQuestion, 0.9, nil), Logger: quietLogger()}
	r, ok := p.RouteInbound(context.Background(), "how much is the water filter")
	if !ok || r.Urgency != UrgencyUnknown {
		t.Fatalf("urgency = %q ok=%v, want unknown", r.Urgency, ok)
	}
}

func TestRouteInboundRejectsOutOfVocabulary(t *testing.T) {
	p := &Pipeline{Jev: jevRouteServer(t, "teleport", 0.99, nil), Logger: quietLogger()}
	if _, ok := p.RouteInbound(context.Background(), "x"); ok {
		t.Fatal("unknown route must report not-ok so callers keep the old path")
	}
}

func TestRouteInboundDisabled(t *testing.T) {
	p := &Pipeline{Jev: nil, Logger: quietLogger()}
	if _, ok := p.RouteInbound(context.Background(), "x"); ok {
		t.Fatal("disabled Jev must report not-ok")
	}
}

func TestRouteDecisionThresholds(t *testing.T) {
	if esc, skip, silent := RouteDecision(RouteHandoff, 0.85); !esc || skip || silent {
		t.Fatalf("handoff at 0.85 must escalate: %v %v %v", esc, skip, silent)
	}
	if esc, skip, silent := RouteDecision(RouteHandoff, 0.5); esc || skip || silent {
		t.Fatalf("handoff at 0.5 is below the 0.80 bar: %v %v %v", esc, skip, silent)
	}
	if esc, skip, silent := RouteDecision(RouteSmallTalk, 0.9); esc || !skip || silent {
		t.Fatalf("chit-chat at 0.9 must skip grounding: %v %v %v", esc, skip, silent)
	}
	if esc, skip, silent := RouteDecision(RouteKBQuestion, 0.99); esc || skip || silent {
		t.Fatalf("kb_question must change nothing: %v %v %v", esc, skip, silent)
	}
}

func TestRouteDecisionJunkGatesSilence(t *testing.T) {
	if _, _, silent := RouteDecision(RouteJunk, 0.9); !silent {
		t.Fatal("junk at 0.9 must go silent")
	}
	if _, _, silent := RouteDecision(RouteJunk, 0.6); silent {
		t.Fatal("junk at 0.6 is below the 0.90 bar — silence must not fire")
	}
	// Silence is the worst failure mode of the router, so its bar must sit
	// at or above every other route's default.
	if envFloat("JEV_ROUTE_JUNK_MIN", 0.90) < envFloat("JEV_ROUTE_HANDOFF_MIN", 0.80) {
		t.Fatal("junk bar lower than handoff bar — silencing is too eager")
	}
}

func TestRouteDecisionThresholdsAreKnobs(t *testing.T) {
	t.Setenv("JEV_ROUTE_HANDOFF_MIN", "0.95")
	if esc, _, _ := RouteDecision(RouteHandoff, 0.9); esc {
		t.Fatal("raised bar must hold back a 0.9 handoff route")
	}
	t.Setenv("JEV_ROUTE_CHITCHAT_MIN", "0.5")
	if _, skip, _ := RouteDecision(RouteSmallTalk, 0.6); !skip {
		t.Fatal("lowered chit-chat bar must let 0.6 skip grounding")
	}
	t.Setenv("JEV_ROUTE_JUNK_MIN", "0.95")
	if _, _, silent := RouteDecision(RouteJunk, 0.9); silent {
		t.Fatal("raised junk bar must hold back a 0.9 junk route")
	}
	t.Setenv("JEV_ROUTE_URGENT_MIN", "1.9")
	p := &Pipeline{Jev: jevRouteServer(t, RouteKBQuestion, 0.9, score(1.8)), Logger: quietLogger()}
	if r, _ := p.RouteInbound(context.Background(), "how much is the water filter"); r.Urgency == UrgencyUrgent {
		t.Fatal("raised urgent bar must demote a 1.8 score out of urgent")
	}
}

func TestHandoffPriority(t *testing.T) {
	// Urgency raises, never lowers.
	if got := handoffPriority("ai_decision", UrgencyUrgent); got != "high" {
		t.Fatalf("urgent ai_decision = %q, want high", got)
	}
	if got := handoffPriority("ai_decision", UrgencyElevated); got != "normal" {
		t.Fatalf("elevated ai_decision = %q, want the trigger default normal", got)
	}
	if got := handoffPriority("ai_decision", ""); got != "normal" {
		t.Fatalf("unknown urgency = %q, want the trigger default", got)
	}
	if got := handoffPriority("customer_request", UrgencyRoutine); got != "high" {
		t.Fatalf("routine customer_request = %q, want the trigger default high", got)
	}
	if got := handoffPriority("unknown_trigger", ""); got != "normal" {
		t.Fatalf("unknown trigger = %q, want normal", got)
	}
}
