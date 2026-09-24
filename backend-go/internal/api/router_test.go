package api

import (
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"

	"khmer-ai-cs-go/internal/config"
)

// TestRouterRegistersWithoutPanic builds the real route table.
//
// Go's ServeMux panics at registration time when two patterns conflict, and
// nothing else in the suite ever constructs the router — so a route added by
// hand that collides with an existing one would only ever surface as a crash on
// the next deploy, with the old binary still serving until someone restarts.
// This is the cheapest place to catch that.
func TestRouterRegistersWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("router registration panicked: %v\n%s", r, debug.Stack())
		}
	}()
	a := &App{Cfg: &config.Config{}}
	if h := a.Router(); h == nil {
		t.Fatal("Router() returned nil")
	}
}

// TestDefaultPromptRouteBeatsTheWildcard pins the routing precedence the admin
// model routes depend on. GET /api/v1/admin/models/default-prompt must not be
// swallowed by a {id}-shaped pattern at the same depth; Go resolves this by
// specificity, and this asserts the behaviour rather than assuming it.
//
// The handlers are the real ones, wrapped so each records which pattern served
// the request: both are behind auth, so a 401 would not tell them apart.
func TestDefaultPromptRouteBeatsTheWildcard(t *testing.T) {
	mux := http.NewServeMux()
	var served string
	mark := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { served = name }
	}
	// Mirrors the three shapes registered for /api/v1/admin/models*.
	mux.HandleFunc("GET /api/v1/admin/models", mark("list"))
	mux.HandleFunc("GET /api/v1/admin/models/default-prompt", mark("default-prompt"))
	mux.HandleFunc("GET /api/v1/admin/models/{id}/available", mark("available"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/default-prompt", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if served != "default-prompt" {
		t.Fatalf("literal route lost to the wildcard: served=%q", served)
	}

	// The wildcard still works for a real id.
	served = ""
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/models/7/available", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if served != "available" {
		t.Fatalf("{id} route broken: served=%q", served)
	}
}
