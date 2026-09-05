package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	token, err := jwt.GenerateToken(7, "agent", "admin")
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

	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/realtime/inbox"

	dialer := websocket.Dialer{Subprotocols: []string{realtime.ProtocolName, token}}
	ws, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("realtime route must upgrade, got err=%v resp=%v", err, resp)
	}
	defer ws.Close()
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != realtime.ProtocolName {
		t.Fatalf("subprotocol not negotiated: %q", got)
	}

	// Sanity: a plain REST route without a Bearer token still gets 401 from the
	// auth chain, confirming the two paths are distinct.
	r, err := http.Get(srv.URL + "/api/v1/inbox")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("expected 401 on unauthenticated REST route, got %d", r.StatusCode)
	}
}
