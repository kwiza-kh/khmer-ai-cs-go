// Package realtime implements the live inbox stream: a per-process WebSocket
// hub fed by a Redis pub/sub channel, so every instance fans events out to
// the browsers connected to it (port of the Rust inbox hub referenced by
// migrations 006/029).
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/redisstore"
)

const (
	// Channel is the Redis pub/sub channel used for cross-instance fan-out.
	Channel = "khmer-ai:realtime:inbox"

	// ProtocolName is the WebSocket subprotocol. Browsers cannot set custom
	// headers on the handshake, so the JWT rides as the second subprotocol
	// value (the frontend sends ["khmer-ai-cs", token]).
	ProtocolName = "khmer-ai-cs"

	// Event types understood by the frontend (lib/realtime.ts).
	EventMessage      = "inbox.message"
	EventSession      = "inbox.session"
	EventNotification = "inbox.notification"

	writeWait       = 10 * time.Second
	pongWait        = 60 * time.Second
	pingPeriod      = 25 * time.Second
	maxMessageSize  = 512
	sendQueueSize   = 64
	maxConnsPerUser = 10
)

// Event is one realtime notification. UserID routes the event to the
// tenant's connections and is not rendered by the client.
type Event struct {
	Type       string    `json:"type"`
	UserID     int32     `json:"user_id"`
	SessionID  string    `json:"session_id"`
	MessageID  int64     `json:"message_id,omitempty"`
	Role       string    `json:"role,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Publish fans one event out to every instance via Redis. Best-effort: a
// Redis hiccup must never block the request path.
func Publish(ctx context.Context, rdb *redisstore.Client, ev Event) {
	if rdb == nil {
		return
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_ = rdb.Publish(ctx, Channel, string(payload))
}

// Hub tracks the authenticated WebSocket connections of this process and
// replays Redis events to them.
type Hub struct {
	JWT      *auth.JWT
	Redis    *redisstore.Client
	Logger   *slog.Logger
	IsActive func(ctx context.Context, userID int32) bool
	OriginOK func(origin string) bool

	mu    sync.Mutex
	conns map[int32]map[*conn]struct{}

	upgrader websocket.Upgrader
}

// NewHub wires a hub. isActive mirrors the auth middleware's disabled-tenant
// guard; originOK enforces the CORS allowlist on the handshake.
func NewHub(jwt *auth.JWT, rdb *redisstore.Client, logger *slog.Logger,
	isActive func(context.Context, int32) bool, originOK func(string) bool) *Hub {
	h := &Hub{
		JWT:      jwt,
		Redis:    rdb,
		Logger:   logger,
		IsActive: isActive,
		OriginOK: originOK,
		conns:    make(map[int32]map[*conn]struct{}),
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		Subprotocols:    []string{ProtocolName},
		CheckOrigin: func(r *http.Request) bool {
			if h.OriginOK == nil {
				return true
			}
			return h.OriginOK(r.Header.Get("Origin"))
		},
	}
	return h
}

// Start launches the Redis subscriber (cross-instance fan-in).
func (h *Hub) Start(ctx context.Context) {
	go h.runSubscriber(ctx)
}

// ServeHTTP upgrades an authenticated request to a WebSocket connection.
// It implements http.Handler so it can be mounted directly on the router.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.authenticate(r)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"令牌无效或已过期"}`))
		return
	}
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &conn{hub: h, userID: userID, ws: ws, send: make(chan []byte, sendQueueSize)}
	if !h.register(c) {
		_ = ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "too many connections"),
			time.Now().Add(writeWait))
		_ = ws.Close()
		return
	}
	go c.writePump()
	go c.readPump()
}

// authenticate extracts the JWT. Browsers cannot set the Authorization header
// on a WebSocket handshake, so the token travels as the subprotocol after
// "khmer-ai-cs"; the header form stays as a fallback for non-browser clients.
func (h *Hub) authenticate(r *http.Request) (int32, bool) {
	token := ""
	protos := websocket.Subprotocols(r)
	for i, p := range protos {
		if p == ProtocolName && i+1 < len(protos) {
			token = protos[i+1]
			break
		}
	}
	if token == "" {
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		}
	}
	if token == "" {
		return 0, false
	}
	claims, err := h.JWT.ParseToken(token)
	if err != nil {
		return 0, false
	}
	if h.IsActive != nil && !h.IsActive(r.Context(), claims.UserID) {
		return 0, false
	}
	return claims.UserID, true
}

func (h *Hub) register(c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.conns[c.userID]
	if len(set) >= maxConnsPerUser {
		return false
	}
	if set == nil {
		set = make(map[*conn]struct{})
		h.conns[c.userID] = set
	}
	set[c] = struct{}{}
	return true
}

func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.conns[c.userID]
	if set == nil {
		return
	}
	delete(set, c)
	if len(set) == 0 {
		delete(h.conns, c.userID)
	}
}

// deliver pushes one encoded event to every connection of the user. Slow
// consumers (full send queue) are dropped instead of stalling the fan-out.
func (h *Hub) deliver(userID int32, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.conns[userID]
	for c := range set {
		if !c.enqueue(payload) {
			delete(set, c)
			go c.shutdown()
		}
	}
	if len(set) == 0 {
		delete(h.conns, userID)
	}
}

// route parses one Redis payload and delivers it to the right user.
func (h *Hub) route(payload string) {
	var ev Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return
	}
	if ev.UserID == 0 || ev.Type == "" {
		return
	}
	h.deliver(ev.UserID, []byte(payload))
}

// runSubscriber consumes the Redis channel forever, reconnecting with
// backoff. Events missed while disconnected are unrecoverable — the client
// compensates with a catch-up refetch on reconnect plus a fallback poll.
func (h *Hub) runSubscriber(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		ps, err := h.Redis.Subscribe(ctx, Channel)
		if err != nil {
			h.Logger.Warn("realtime subscribe failed; retrying", "error", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		h.Logger.Info("realtime subscriber connected", "channel", Channel)
		for msg := range ps.Channel() {
			h.route(msg.Payload)
		}
		_ = ps.Close()
		if ctx.Err() != nil {
			return
		}
		h.Logger.Warn("realtime subscriber disconnected; reconnecting")
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// conn is one authenticated WebSocket.
type conn struct {
	hub    *Hub
	userID int32
	ws     *websocket.Conn
	send   chan []byte

	closeOnce sync.Once
}

func (c *conn) enqueue(payload []byte) bool {
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

func (c *conn) shutdown() {
	c.closeOnce.Do(func() {
		if c.ws != nil {
			_ = c.ws.Close()
		}
	})
}

// readPump drains client frames (they carry no semantics) and detects
// disconnects; pong replies refresh the read deadline.
func (c *conn) readPump() {
	defer func() {
		c.hub.unregister(c)
		c.shutdown()
	}()
	c.ws.SetReadLimit(maxMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.ws.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump serializes sends and keeps the connection alive through proxies
// (ping < nginx's default 60s proxy_read_timeout). It must not watch the
// request context: the handler returns right after upgrading (the connection
// is hijacked), which cancels r.Context() and would tear down a healthy
// socket. Lifetime is driven by read/write errors instead.
func (c *conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.shutdown()
	}()
	for {
		select {
		case payload := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				return
			}
		}
	}
}
