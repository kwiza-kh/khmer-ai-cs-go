package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"khmer-ai-cs-go/internal/typesafe"
)

func jevRouteServer(t *testing.T, route string, prob float64) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"route":{"type":"choice","choice":"` +
			route + `","confidence":0.9,"probabilities":{"` + route + `":` +
			strconv.FormatFloat(prob, 'f', -1, 64) + `}}}}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func TestRouteInboundParsesChoiceAndProbability(t *testing.T) {
	p := &Pipeline{Jev: jevRouteServer(t, RouteHandoff, 0.93), Logger: quietLogger()}
	route, prob, ok := p.RouteInbound(context.Background(), "I want a real person")
	if !ok || route != RouteHandoff || prob != 0.93 {
		t.Fatalf("route=%q prob=%v ok=%v", route, prob, ok)
	}
}

func TestRouteInboundRejectsOutOfVocabulary(t *testing.T) {
	p := &Pipeline{Jev: jevRouteServer(t, "teleport", 0.99), Logger: quietLogger()}
	if _, _, ok := p.RouteInbound(context.Background(), "x"); ok {
		t.Fatal("unknown route must report not-ok so callers keep the old path")
	}
}

func TestRouteInboundDisabled(t *testing.T) {
	p := &Pipeline{Jev: nil, Logger: quietLogger()}
	if _, _, ok := p.RouteInbound(context.Background(), "x"); ok {
		t.Fatal("disabled Jev must report not-ok")
	}
}

func TestRouteDecisionThresholds(t *testing.T) {
	esc, skip := RouteDecision(RouteHandoff, 0.85)
	if !esc || skip {
		t.Fatalf("handoff at 0.85 must escalate: %v %v", esc, skip)
	}
	esc, skip = RouteDecision(RouteHandoff, 0.5)
	if esc || skip {
		t.Fatalf("handoff at 0.5 is below the 0.80 bar: %v %v", esc, skip)
	}
	esc, skip = RouteDecision(RouteSmallTalk, 0.9)
	if esc || !skip {
		t.Fatalf("chit-chat at 0.9 must skip grounding: %v %v", esc, skip)
	}
	esc, skip = RouteDecision(RouteKBQuestion, 0.99)
	if esc || skip {
		t.Fatalf("kb_question must change nothing: %v %v", esc, skip)
	}
}

func TestRouteDecisionThresholdsAreKnobs(t *testing.T) {
	t.Setenv("JEV_ROUTE_HANDOFF_MIN", "0.95")
	esc, _ := RouteDecision(RouteHandoff, 0.9)
	if esc {
		t.Fatal("raised bar must hold back a 0.9 handoff route")
	}
	t.Setenv("JEV_ROUTE_CHITCHAT_MIN", "0.5")
	_, skip := RouteDecision(RouteSmallTalk, 0.6)
	if !skip {
		t.Fatal("lowered chit-chat bar must let 0.6 skip grounding")
	}
}
