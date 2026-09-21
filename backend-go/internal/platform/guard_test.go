package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"khmer-ai-cs-go/internal/typesafe"
)

func jevGuardServer(t *testing.T, handoff, leaks, unsafe float64) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{
			"promises_handoff":{"type":"noul","noul":` + f(handoff) + `},
			"leaks_sources":{"type":"noul","noul":` + f(leaks) + `},
			"unsafe_claim":{"type":"noul","noul":` + f(unsafe) + `}}}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: "k", Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func f(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func TestGuardReplyFlagsAllThree(t *testing.T) {
	p := &Pipeline{Jev: jevGuardServer(t, 0.9, 0.8, 0.75), Logger: quietLogger()}
	g, ok := p.GuardReply(context.Background(), "I will transfer you… according to the document… price is fixed at 5$", nil)
	if !ok {
		t.Fatal("guard expected")
	}
	if !g.PromisesHandoff || !g.LeaksSources || !g.UnsafeClaim {
		t.Fatalf("all three above the 0.70 bar must flag: %+v", g)
	}
}

func TestGuardReplyBelowBarIsClean(t *testing.T) {
	p := &Pipeline{Jev: jevGuardServer(t, 0.2, 0.1, 0.3), Logger: quietLogger()}
	g, ok := p.GuardReply(context.Background(), "The 5cm board costs about market rate.", nil)
	if !ok {
		t.Fatal("guard expected")
	}
	if g.PromisesHandoff || g.LeaksSources || g.UnsafeClaim {
		t.Fatalf("low probabilities must stay clean: %+v", g)
	}
}

func TestGuardReplyBarIsAKnob(t *testing.T) {
	t.Setenv("JEV_GUARD_MIN", "0.95")
	t.Setenv("JEV_GUARD_HANDOFF_MIN", "0.95")
	p := &Pipeline{Jev: jevGuardServer(t, 0.9, 0.8, 0.75), Logger: quietLogger()}
	g, ok := p.GuardReply(context.Background(), "x", nil)
	if !ok {
		t.Fatal("guard expected")
	}
	if g.PromisesHandoff || g.LeaksSources || g.UnsafeClaim {
		t.Fatalf("raised bar must clear 0.9/0.8/0.75: %+v", g)
	}
}

func TestGuardReplyDisabledAndIncomplete(t *testing.T) {
	p := &Pipeline{Jev: nil, Logger: quietLogger()}
	if _, ok := p.GuardReply(context.Background(), "x", nil); ok {
		t.Fatal("disabled Jev must report not-ok")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"promises_handoff":{"type":"noul","noul":0.9}}}`))
	}))
	t.Cleanup(bad.Close)
	p2 := &Pipeline{Jev: &typesafe.Client{Endpoint: bad.URL, APIKey: "k", Model: "jev-latest", HTTP: bad.Client(), Logger: quietLogger()}}
	if _, ok := p2.GuardReply(context.Background(), "x", nil); ok {
		t.Fatal("incomplete guard must report not-ok, never half-act")
	}
}

func TestStripCitationLines(t *testing.T) {
	reply := "The board is 5cm thick.\nAccording to the document \"Price List v3\", it costs 5$.\nឯកសារយោង៖ តម្លៃ\nHope this helps!"
	got := StripCitationLines(reply)
	want := "The board is 5cm thick.\nHope this helps!"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if StripCitationLines("plain answer") != "plain answer" {
		t.Fatal("clean reply must pass through untouched")
	}
}
