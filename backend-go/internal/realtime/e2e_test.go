package realtime

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"khmer-ai-cs-go/internal/auth"
)

// TestEndToEndSubprotocolHandshakeAndDelivery exercises the risky path: a
// browser-style client that can only carry the JWT as a subprotocol, plus an
// event published via the Redis route. No real Redis is needed because we
// inject the payload straight into route().
func TestEndToEndSubprotocolHandshakeAndDelivery(t *testing.T) {
	jwt := auth.NewJWT(strings.Repeat("k", 32), 1)
	token, err := jwt.GenerateToken(42, "agent", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}

	h := NewHub(jwt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	dialer := websocket.Dialer{Subprotocols: []string{ProtocolName, token}}
	ws, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer ws.Close()
	if got := resp.Header.Get("Sec-WebSocket-Protocol"); got != ProtocolName {
		t.Fatalf("server did not negotiate subprotocol, got %q", got)
	}

	// Give the server a moment to register the connection.
	time.Sleep(50 * time.Millisecond)

	payload, _ := json.Marshal(Event{Type: EventMessage, UserID: 42, SessionID: "sess-1", MessageID: 7, Role: "user"})
	h.route(string(payload))

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var ev Event
	if err := json.Unmarshal(msg, &ev); err != nil {
		t.Fatalf("bad payload %q: %v", msg, err)
	}
	if ev.SessionID != "sess-1" || ev.MessageID != 7 || ev.Role != "user" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

// TestHandshakeRejectsBadToken confirms the upgrade is refused without a valid
// JWT.
func TestHandshakeRejectsBadToken(t *testing.T) {
	jwt := auth.NewJWT(strings.Repeat("k", 32), 1)
	h := NewHub(jwt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	dialer := websocket.Dialer{Subprotocols: []string{ProtocolName, "not-a-jwt"}}
	if _, resp, err := dialer.Dial(wsURL, nil); err == nil {
		t.Fatal("expected handshake to fail with a bad token")
	} else if resp != nil && resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
