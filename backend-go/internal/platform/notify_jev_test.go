package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/typesafe"
)

func jevWorthServer(t *testing.T, v float64) *typesafe.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"worth_pinging":{"type":"noul","noul":` +
			strconv.FormatFloat(v, 'f', -1, 64) + `}}}`))
	}))
	t.Cleanup(srv.Close)
	return &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
}

func TestWorthPingingSilencesClearFiller(t *testing.T) {
	p := &Pipeline{Jev: jevWorthServer(t, 0.05), Logger: quietLogger()}
	if p.worthPinging(context.Background(), "ok 👍") {
		t.Fatal("filler at 0.05 must not ping")
	}
}

func TestWorthPingingKeepsRealMessages(t *testing.T) {
	p := &Pipeline{Jev: jevWorthServer(t, 0.8), Logger: quietLogger()}
	if !p.worthPinging(context.Background(), "the delivery arrived damaged") {
		t.Fatal("a real complaint must ping")
	}
}

func TestWorthPingingFailOpen(t *testing.T) {
	// Disabled.
	if !(&Pipeline{Jev: nil}).worthPinging(context.Background(), "x") {
		t.Fatal("disabled Jev must ping")
	}
	// Erroring endpoint.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	p := &Pipeline{Jev: &typesafe.Client{Endpoint: bad.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: bad.Client(), Logger: quietLogger()}}
	if !p.worthPinging(context.Background(), "x") {
		t.Fatal("Jev failure must ping (fail-open)")
	}
	// Mid-range probability: doubt keeps today's behaviour.
	mid := &Pipeline{Jev: jevWorthServer(t, 0.5), Logger: quietLogger()}
	if !mid.worthPinging(context.Background(), "x") {
		t.Fatal("mid-range noul must ping; only clear filler is silenced")
	}
}

func TestWorthPingingBudgetToleratesSlowJev(t *testing.T) {
	// Regression guard: this budget was a hardcoded 1s, so a Jev call that took
	// longer (routine during an upstream spike) timed out and fail-opened —
	// pinging the owner for "ok 👍". The default budget must clear ~1.2s and
	// return the real filler verdict instead.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"worth_pinging":{"type":"noul","noul":0.05}}}`))
	}))
	t.Cleanup(srv.Close)
	p := &Pipeline{Jev: &typesafe.Client{Endpoint: srv.URL, APIKey: stubAuthValue, Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}}
	if p.worthPinging(context.Background(), "ok 👍") {
		t.Fatal("a 1.2s Jev call within budget must return the real verdict (filler → no ping), not fail open")
	}
}

func TestWorthPingingBarIsAKnob(t *testing.T) {
	t.Setenv("JEV_NOTIFY_WORTH_MIN", "0.9")
	p := &Pipeline{Jev: jevWorthServer(t, 0.8), Logger: quietLogger()}
	if p.worthPinging(context.Background(), "x") {
		t.Fatal("raised bar must silence an 0.8 verdict")
	}
}
