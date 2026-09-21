package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

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
	return &typesafe.Client{Endpoint: srv.URL, APIKey: "k", Model: "jev-latest", HTTP: srv.Client(), Logger: quietLogger()}
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
	p := &Pipeline{Jev: &typesafe.Client{Endpoint: bad.URL, APIKey: "k", Model: "jev-latest", HTTP: bad.Client(), Logger: quietLogger()}}
	if !p.worthPinging(context.Background(), "x") {
		t.Fatal("Jev failure must ping (fail-open)")
	}
	// Mid-range probability: doubt keeps today's behaviour.
	mid := &Pipeline{Jev: jevWorthServer(t, 0.5), Logger: quietLogger()}
	if !mid.worthPinging(context.Background(), "x") {
		t.Fatal("mid-range noul must ping; only clear filler is silenced")
	}
}

func TestWorthPingingBarIsAKnob(t *testing.T) {
	t.Setenv("JEV_NOTIFY_WORTH_MIN", "0.9")
	p := &Pipeline{Jev: jevWorthServer(t, 0.8), Logger: quietLogger()}
	if p.worthPinging(context.Background(), "x") {
		t.Fatal("raised bar must silence an 0.8 verdict")
	}
}
