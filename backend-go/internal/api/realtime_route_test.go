package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/realtime"
)

// TestRealtimeRouteBypassesAuthChain proves that GET /api/v1/realtime/inbox is
// dispatched to the realtime hub (which authenticates the subprotocol token)
// rather than the header-based auth middleware. The browser cannot send an
// Authorization header on a WebSocket handshake, so the more-specific route
// must win over the "/api/v1/" catch-all.
func TestRealtimeRouteBypassesAuthChain(t *testing.T) {
	jwt := auth.NewJWT(strings.Repeat("z", 32), 1)
	token, err := jwt.GenerateToken(7, "agent", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub(jwt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	app := &App{
		Cfg:      &config.Config{},
		JWT:      jwt,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Realtime: hub,
	}
	router := app.Router()

	srv := httptest.NewServer(router)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/realtime/inbox"

	dialer := websocket.Dialer{
		Subprotocols:     []string{realtime.ProtocolName, token},
		HandshakeTimeout: 5 * time.Second,
	}
	ws, resp := dialRealtime(t, &dialer, wsURL)
	defer ws.Close()
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != realtime.ProtocolName {
		t.Fatalf("subprotocol not negotiated: %q", got)
	}

	// Sanity: a plain REST route without a Bearer token still gets 401 from the
	// auth chain, confirming the two paths are distinct.
	//
	// Driven straight through the handler chain instead of over a socket: the
	// middleware rejects a request with no Authorization header before anything
	// reaches the database, so a listener would add nothing but a second,
	// unrelated way for this test to fail.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/inbox", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated REST route, got %d", rec.Code)
	}
}

// dialRealtime retries the WebSocket handshake when the CONNECTION fails, and
// never when the server has answered.
//
// This host intermittently refuses loopback connections to a freshly created
// httptest listener — "A connection attempt failed because the connected party
// did not properly respond" — which turned the suite red roughly one run in
// three. That is an environment artifact with nothing to do with the routing
// under test.
//
// A response is a different matter: if the server answered at all then the
// request reached it, so anything wrong after that is a real failure and must
// not be retried away. Only the no-response case is retried.
func dialRealtime(t *testing.T, d *websocket.Dialer, url string) (*websocket.Conn, *http.Response) {
	t.Helper()
	const attempts = 5
	var lastErr error
	for i := range attempts {
		if i > 0 {
			time.Sleep(time.Duration(i) * 150 * time.Millisecond)
		}
		ws, resp, err := d.Dial(url, nil)
		if err == nil {
			return ws, resp
		}
		if resp != nil {
			t.Fatalf("realtime route rejected the upgrade: HTTP %d", resp.StatusCode)
		}
		lastErr = err
	}
	t.Fatalf("realtime route unreachable after %d attempts: %v", attempts, lastErr)
	return nil, nil
}
