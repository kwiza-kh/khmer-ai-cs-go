package platform

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The route table replaced six hand-written HandleFunc calls in cmd/server. It
// must stay in step with the capability table, and the unified dispatcher must
// agree with the provider-specific paths.

func testWebhookServer(t *testing.T) *httptest.Server {
	t.Helper()
	wh := &Webhooks{MetaVerifyToken: "verify-tok"}
	mux := http.NewServeMux()
	RegisterWebhookRoutes(mux, wh)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mustGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func mustPost(t *testing.T, url, contentType, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestWebhookRoutesAreUniqueAndUnderTheBasePath(t *testing.T) {
	wh := &Webhooks{}
	seen := map[string]bool{}
	for _, rt := range wh.Routes() {
		if !strings.HasPrefix(rt.Path, WebhookBasePath) {
			t.Errorf("route %q escapes %q", rt.Path, WebhookBasePath)
		}
		if seen[rt.Path] {
			t.Errorf("duplicate route %q", rt.Path)
		}
		seen[rt.Path] = true
		if rt.Handler == nil {
			t.Errorf("route %q has no handler", rt.Path)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no routes registered at all")
	}
}

// Every inbound platform must be a channel we know about, and every known channel
// except the website widget must have an inbound callback. This is the test that
// fails when a channel is added to one table and not the other.
//
// The inbound set is read off Routes() — the live table the dispatcher installs —
// rather than a second enumeration that could drift away from it.
func TestInboundPlatformsMatchCapabilityTable(t *testing.T) {
	inboundSet := map[string]bool{}
	for _, rt := range (&Webhooks{}).Routes() {
		if rt.Platform == "" {
			continue // a sub-resource (e.g. meta/data-deletion), not a platform
		}
		if !CapabilitiesFor(rt.Platform).Known {
			t.Errorf("inbound platform %q has no capability row", rt.Platform)
		}
		inboundSet[rt.Platform] = true
	}
	// An alias is reachable too: the dispatcher resolves the segment through
	// WebhookPlatformAliases before it looks for a handler (instagram arrives on
	// the Meta app webhook), so it counts as having an inbound path.
	for alias, target := range WebhookPlatformAliases {
		if inboundSet[target] {
			inboundSet[alias] = true
		}
	}
	if len(inboundSet) == 0 {
		t.Fatal("no inbound platforms at all")
	}
	for p := range capabilitiesTable {
		if p == "web" {
			// The website widget talks to its own SSE endpoint, not the webhook.
			if inboundSet[p] {
				t.Errorf("web must not have an inbound webhook route")
			}
			continue
		}
		if !inboundSet[p] {
			t.Errorf("known channel %q has no inbound webhook route", p)
		}
	}
}

func TestUnifiedWebhookResolution(t *testing.T) {
	wh := &Webhooks{}
	for _, p := range []string{"meta", "instagram", "whatsapp", "telegram", "line", "zalo"} {
		if _, ok := wh.handlerForPlatform(p); !ok {
			t.Errorf("%q must resolve through the unified dispatcher", p)
		}
	}
	for _, p := range []string{"", "web", "myspace", "meta/data-deletion", "telegram-platform"} {
		if _, ok := wh.handlerForPlatform(p); ok {
			t.Errorf("%q must NOT resolve through the unified dispatcher", p)
		}
	}
}

// The unified path must behave exactly like the provider-specific one: Meta's
// hub.challenge is answered on both, and the instagram alias lands on the same
// handler because Meta delivers Instagram messaging to the app webhook.
func TestUnifiedWebhookServesTheSameHandlers(t *testing.T) {
	srv := testWebhookServer(t)

	for _, path := range []string{"/api/v1/webhook/meta", "/api/v1/webhook/instagram"} {
		code, body := mustGet(t, srv.URL+path+"?hub.mode=subscribe&hub.verify_token=verify-tok&hub.challenge=abc123")
		if code != http.StatusOK || body != "abc123" {
			t.Errorf("%s: challenge = %d/%q, want 200/abc123", path, code, body)
		}
	}
	// A wrong token is refused on the unified path too.
	if code, _ := mustGet(t, srv.URL+"/api/v1/webhook/meta?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=x"); code != http.StatusForbidden {
		t.Errorf("wrong verify token = %d, want 403", code)
	}
	// An empty Meta POST is acknowledged without touching the database.
	if code, body := mustPost(t, srv.URL+"/api/v1/webhook/meta", "application/json", "{}"); code != http.StatusOK {
		t.Errorf("empty meta POST = %d/%q, want 200", code, body)
	}
}

func TestUnifiedWebhookUnknownPlatformIs404(t *testing.T) {
	srv := testWebhookServer(t)

	for _, path := range []string{
		"/api/v1/webhook/myspace",
		"/api/v1/webhook/web", // known channel, but it has no provider callback
		"/api/v1/webhook/meta/extra/deep",
	} {
		if code, body := mustGet(t, srv.URL+path); code != http.StatusNotFound {
			t.Errorf("%s = %d/%q, want 404", path, code, body)
		}
	}
}

// The exact sub-resource route must win over the unified prefix. Meta's Data
// Deletion callback answers 405 to a GET (it only accepts a POSTed
// signed_request), while the Meta handler would have echoed the challenge.
func TestDataDeletionSubResourceBeatsTheUnifiedDispatcher(t *testing.T) {
	srv := testWebhookServer(t)

	code, body := mustGet(t, srv.URL+"/api/v1/webhook/meta/data-deletion?hub.mode=subscribe&hub.verify_token=verify-tok&hub.challenge=abc123")
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET data-deletion = %d/%q, want 405 (the exact route must win)", code, body)
	}
	if strings.Contains(body, "abc123") {
		t.Fatal("the challenge leaked into the data-deletion route: the prefix handler served it")
	}

	// A POST without signed_request is rejected by the deletion handler itself,
	// before any database access.
	code, body = mustPost(t, srv.URL+"/api/v1/webhook/meta/data-deletion", "application/x-www-form-urlencoded", "")
	if code != http.StatusBadRequest || !strings.Contains(body, "signed_request") {
		t.Fatalf("POST data-deletion = %d/%q, want 400 missing signed_request", code, body)
	}
}

// Every provider-specific path must be installed as an exact route, and the
// unified dispatcher must own only the prefix. ServeMux.Handler reports the
// matching pattern without invoking the handler, so this asserts registration
// without needing a database behind the handlers.
func TestRegisterWebhookRoutesInstallsEveryPath(t *testing.T) {
	wh := &Webhooks{}
	mux := http.NewServeMux()
	RegisterWebhookRoutes(mux, wh)

	for _, rt := range wh.Routes() {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, rt.Path, nil))
		if pattern != rt.Path {
			t.Errorf("%s matched pattern %q, want its own exact route", rt.Path, pattern)
		}
	}
	for _, path := range []string{"/api/v1/webhook/instagram", "/api/v1/webhook/myspace"} {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, path, nil))
		if pattern != WebhookBasePath {
			t.Errorf("%s matched pattern %q, want the unified prefix %q", path, pattern, WebhookBasePath)
		}
	}
}
