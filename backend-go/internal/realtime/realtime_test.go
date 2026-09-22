package realtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/auth"
)

func TestEventJSONShape(t *testing.T) {
	ev := Event{Type: EventMessage, UserID: 7, SessionID: "s1", MessageID: 42, Role: "user"}
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != EventMessage || got["session_id"] != "s1" || got["role"] != "user" {
		t.Fatalf("unexpected payload: %s", payload)
	}
	if got["message_id"] != float64(42) {
		t.Fatalf("message_id missing: %s", payload)
	}
	if got["occurred_at"] == nil || got["occurred_at"] == "" {
		t.Fatalf("occurred_at missing: %s", payload)
	}
}

func TestEventOmitsZeroMessageID(t *testing.T) {
	payload, _ := json.Marshal(Event{Type: EventSession, UserID: 1, SessionID: "s"})
	if strings.Contains(string(payload), "message_id") {
		t.Fatalf("zero message_id must be omitted: %s", payload)
	}
}

func TestPublishNilRedis(t *testing.T) {
	// Must be a no-op, not a panic (best-effort contract).
	Publish(context.Background(), nil, Event{Type: EventMessage, UserID: 1})
}

func TestRouteDeliversToOwningUserOnly(t *testing.T) {
	h := &Hub{conns: make(map[int32]map[*conn]struct{})}
	mine := &conn{hub: h, userID: 5, send: make(chan []byte, 4)}
	theirs := &conn{hub: h, userID: 6, send: make(chan []byte, 4)}
	if !h.register(mine) || !h.register(theirs) {
		t.Fatal("registration failed")
	}

	payload, _ := json.Marshal(Event{Type: EventMessage, UserID: 5, SessionID: "s1", MessageID: 9})
	h.route(string(payload))

	select {
	case got := <-mine.send:
		if !strings.Contains(string(got), `"session_id":"s1"`) {
			t.Fatalf("wrong payload delivered: %s", got)
		}
	default:
		t.Fatal("event not delivered to the owning user")
	}
	select {
	case <-theirs.send:
		t.Fatal("event leaked to another user")
	default:
	}
}

func TestRouteIgnoresMalformedPayloads(t *testing.T) {
	h := &Hub{conns: make(map[int32]map[*conn]struct{})}
	c := &conn{hub: h, userID: 1, send: make(chan []byte, 4)}
	_ = h.register(c)

	h.route("not json")
	h.route(`{"user_id":1}`)            // no type
	h.route(`{"type":"inbox.message"}`) // no user
	select {
	case <-c.send:
		t.Fatal("malformed event must not be delivered")
	default:
	}
}

func TestDeliverDropsSlowConsumers(t *testing.T) {
	h := &Hub{conns: make(map[int32]map[*conn]struct{})}
	slow := &conn{hub: h, userID: 3, send: make(chan []byte, 1)}
	if !h.register(slow) {
		t.Fatal("registration failed")
	}
	slow.send <- []byte("blocker") // fill the queue

	h.deliver(3, []byte(`{"type":"inbox.message"}`))

	h.mu.Lock()
	remaining := len(h.conns[3])
	h.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("slow consumer must be dropped, %d conn(s) left", remaining)
	}
}

func TestAuthenticateSubprotocolToken(t *testing.T) {
	jwt := auth.NewJWT(strings.Repeat("s", 32), 1)
	token, err := jwt.GenerateToken(11, "agent", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHub(jwt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	r := httptest.NewRequest("GET", "/api/v1/realtime/inbox", nil)
	r.Header.Set("Sec-WebSocket-Protocol", ProtocolName+", "+token)
	if uid, ok := h.authenticate(r); !ok || uid != 11 {
		t.Fatalf("subprotocol auth failed: uid=%d ok=%v", uid, ok)
	}

	// Header fallback for non-browser clients.
	r2 := httptest.NewRequest("GET", "/api/v1/realtime/inbox", nil)
	r2.Header.Set("Authorization", "Bearer "+token)
	if uid, ok := h.authenticate(r2); !ok || uid != 11 {
		t.Fatalf("header auth failed: uid=%d ok=%v", uid, ok)
	}

	// Bad token rejected.
	r3 := httptest.NewRequest("GET", "/api/v1/realtime/inbox", nil)
	r3.Header.Set("Sec-WebSocket-Protocol", ProtocolName+", garbage.token.here")
	if _, ok := h.authenticate(r3); ok {
		t.Fatal("invalid token must be rejected")
	}

	// Inactive tenant rejected even with a valid token.
	h.IsActive = func(context.Context, int32) (bool, int) { return false, 0 }
	r4 := httptest.NewRequest("GET", "/api/v1/realtime/inbox", nil)
	r4.Header.Set("Authorization", "Bearer "+token)
	if _, ok := h.authenticate(r4); ok {
		t.Fatal("inactive user must be rejected")
	}

	// A token whose tv claim no longer matches the account's token_version is
	// rejected — the same revocation predicate the HTTP chain enforces.
	h.IsActive = func(context.Context, int32) (bool, int) { return true, 999 }
	r5 := httptest.NewRequest("GET", "/api/v1/realtime/inbox", nil)
	r5.Header.Set("Authorization", "Bearer "+token)
	if _, ok := h.authenticate(r5); ok {
		t.Fatal("stale token_version must be rejected")
	}
}
