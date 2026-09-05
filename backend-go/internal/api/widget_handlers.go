package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/usage"
)

// widgetRateLimit caps requests per client IP for the public widget endpoints
// (Redis fixed window; fails open when Redis is unreachable).
func (a *App) widgetRateLimit(maxRPM int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "widget-ip:" + clientIP(r)
			allowed, err := a.Redis.CheckRateLimit(r.Context(), key, uint32(maxRPM))
			if err == nil && !allowed {
				w.Header().Set("Retry-After", "60")
				WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "请求过于频繁"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ============================================
// Website chat widget — admin token management
// (authenticated under the normal /api/v1 surface)
// ============================================

// widgetTokenRow is one embed token; ownerID is the tenant behind it.
type widgetTokenRow struct {
	TokenID       int64    `json:"token_id"`
	ownerID       int32
	Name          string   `json:"name"`
	Token         string   `json:"token"`
	IsActive      bool     `json:"is_active"`
	AllowedOrigin []string `json:"allowed_origins"`
	Theme         string   `json:"theme"`
	PrimaryColor  string   `json:"primary_color"`
	GreetingKm    string   `json:"greeting_km"`
	GreetingEn    string   `json:"greeting_en"`
	CreatedAt     string   `json:"created_at"`
}

func generateWidgetToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("wt_%x", time.Now().UnixNano())
	}
	return "wt_" + hex.EncodeToString(b)
}

const widgetCols = "token_id, user_id, name, token, is_active, allowed_origins, theme, primary_color, COALESCE(greeting_km,''), COALESCE(greeting_en,''), created_at"

func scanWidgetToken(scan func(dest ...any) error) (widgetTokenRow, error) {
	var t widgetTokenRow
	var origins []string
	var createdAt time.Time
	err := scan(&t.TokenID, &t.ownerID, &t.Name, &t.Token, &t.IsActive, &origins, &t.Theme, &t.PrimaryColor, &t.GreetingKm, &t.GreetingEn, &createdAt)
	t.AllowedOrigin = origins
	t.CreatedAt = createdAt.Format(time.RFC3339)
	return t, err
}

// listWidgetTokens — the caller's embed tokens.
func (a *App) listWidgetTokens(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT "+widgetCols+" FROM widget_tokens WHERE user_id = $1 ORDER BY created_at DESC", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]widgetTokenRow, 0)
	for rows.Next() {
		if t, err := scanWidgetToken(rows.Scan); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

type createWidgetRequest struct {
	Name       string   `json:"name"`
	Origins    []string `json:"allowed_origins"`
	Theme      string   `json:"theme"`
	Color      string   `json:"primary_color"`
	GreetingKm string   `json:"greeting_km"`
	GreetingEn string   `json:"greeting_en"`
}

// createWidgetToken — mint a new embed token.
func (a *App) createWidgetToken(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req createWidgetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Name == "" {
		req.Name = "Website"
	}
	if req.Theme != "dark" && req.Theme != "auto" {
		req.Theme = "light"
	}
	if req.Color == "" {
		req.Color = "#4f46e5"
	}
	if req.Origins == nil {
		req.Origins = []string{}
	}
	token := generateWidgetToken()
	var id int64
	err := a.DB.QueryRow(r.Context(),
		"INSERT INTO widget_tokens (user_id, name, token, is_active, allowed_origins, theme, primary_color, greeting_km, greeting_en) "+
			"VALUES ($1,$2,$3,true,$4::text[],$5,$6,$7,$8) RETURNING token_id",
		user.UserID, req.Name, token, req.Origins, req.Theme, req.Color, req.GreetingKm, req.GreetingEn).Scan(&id)
	if err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{
		"token_id": id, "token": token, "embed_src": a.widgetEmbedSrc(),
	}, nil
}

// deleteWidgetToken — revoke an embed token.
func (a *App) deleteWidgetToken(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM widget_tokens WHERE token_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// widgetEmbedSrc — absolute URL of the embed script (PUBLIC_API_URL host or
// the frontend origin; the script itself is served by the Next.js app).
func (a *App) widgetEmbedSrc() string {
	base := strings.TrimSuffix(a.Cfg.Server.PublicAPIURL, "/api/v1")
	return strings.TrimSuffix(base, "/")
}

// ============================================
// Website chat widget — public (token-authed) endpoints
// Mounted outside the auth + origin allowlist; CORS is opened in a.cors().
// ============================================

// resolveWidget validates ?token= / body token / X-Widget-Token header.
func (a *App) resolveWidget(r *http.Request) (*widgetTokenRow, *ApiError) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Widget-Token")
	}
	if token == "" || len(token) > 64 {
		return nil, ErrUnauthorized("无效的小组件令牌")
	}
	row := a.DB.QueryRow(r.Context(), "SELECT "+widgetCols+" FROM widget_tokens WHERE token = $1 AND is_active = true", token)
	t, err := scanWidgetToken(row.Scan)
	if err != nil {
		return nil, ErrUnauthorized("无效的小组件令牌")
	}
	// Origin allowlist (empty = any).
	if len(t.AllowedOrigin) > 0 {
		origin := r.Header.Get("Origin")
		ok := origin == ""
		for _, o := range t.AllowedOrigin {
			if strings.TrimSuffix(o, "/") == strings.TrimSuffix(origin, "/") {
				ok = true
				break
			}
		}
		if !ok {
			return nil, ErrForbidden("该域名未授权")
		}
	}
	return &t, nil
}

// widgetBootstrap — GET /api/v1/widget/config?token=…
func (a *App) widgetBootstrap(w http.ResponseWriter, r *http.Request) (any, error) {
	t, apiErr := a.resolveWidget(r)
	if apiErr != nil {
		return nil, apiErr
	}
	return map[string]any{
		"theme":         t.Theme,
		"primary_color": t.PrimaryColor,
		"greeting_km":   t.GreetingKm,
		"greeting_en":   t.GreetingEn,
	}, nil
}

type widgetMessageRequest struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Language  string `json:"language"`
}

// widgetMessages — GET /api/v1/widget/messages?token=…&session=…
func (a *App) widgetMessages(w http.ResponseWriter, r *http.Request) (any, error) {
	t, apiErr := a.resolveWidget(r)
	if apiErr != nil {
		return nil, apiErr
	}
	sid := r.URL.Query().Get("session")
	if sid == "" {
		return []any{}, nil
	}
	var owner int32
	if err := a.DB.QueryRow(r.Context(), "SELECT user_id FROM sessions WHERE session_id = $1", sid).Scan(&owner); err != nil || owner != t.ownerID {
		return []any{}, nil
	}
	limit := parseIntOr(r.URL.Query().Get("limit"), 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := a.DB.Query(r.Context(),
		"SELECT message_id, role, content, feedback_rating, created_at FROM chat_messages WHERE session_id = $1 ORDER BY message_id DESC LIMIT $2",
		sid, limit)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	msgs := make([]map[string]any, 0)
	for rows.Next() {
		var (
			mid       int64
			role, c   string
			rating    *int16
			createdAt time.Time
		)
		if rows.Scan(&mid, &role, &c, &rating, &createdAt) == nil {
			msgs = append(msgs, map[string]any{
				"message_id": mid, "role": role, "content": c, "created_at": createdAt,
				"feedback_rating": rating,
			})
		}
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// widgetChat — POST /api/v1/widget/chat (SSE). Body: {token, session_id?, message, language?}.
// Creates the anonymous web session on first turn; every reply is persisted
// so agents see the conversation in the inbox and can answer (the visitor
// polls /widget/messages while the panel is open).
func (a *App) widgetChat(w http.ResponseWriter, r *http.Request) {
	t, apiErr := a.resolveWidget(r)
	if apiErr != nil {
		WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
		return
	}
	var req widgetMessageRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil || strings.TrimSpace(req.Message) == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "消息不能为空"})
		return
	}
	// Per-token visitor rate limit (fail-open).
	if ok, err := a.Redis.CheckRateLimit(r.Context(), "widget:"+req.SessionID+":"+clientIP(r), 20); err == nil && !ok {
		WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "发送太快，请稍候"})
		return
	}
	language := req.Language
	if language != "km" && language != "en" && language != "zh" {
		language = "km"
	}
	ctx := r.Context()

	// Validate / bind the session to this token's tenant (create on demand).
	var owner int32
	if req.SessionID != "" {
		_ = a.DB.QueryRow(ctx, "SELECT user_id FROM sessions WHERE session_id = $1", req.SessionID).Scan(&owner)
	}
	if req.SessionID == "" || owner != t.ownerID {
		req.SessionID = newUUID()
		if _, err := a.DB.Exec(ctx,
			"INSERT INTO sessions (session_id, user_id, platform, language, status, title, is_test, created_at) "+
				"VALUES ($1,$2,'web'::platform_type,$3,'active','Website visitor',false,NOW())",
			req.SessionID, t.ownerID, language); err != nil {
			WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "创建会话失败"})
			return
		}
		a.publishSessionEvent(ctx, t.ownerID, req.SessionID)
	}
	sid := req.SessionID

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	sendEvent := func(event string, data any) {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\n", event)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	sendEvent("session", map[string]any{"session_id": sid})

	// Persist the visitor message.
	var userMsgID int64
	_ = a.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'user','text',$2,NOW()) RETURNING message_id",
		sid, req.Message).Scan(&userMsgID)
	_, _ = a.DB.Exec(ctx, "UPDATE sessions SET user_message_count = user_message_count + 1 WHERE session_id = $1", sid)
	a.publishMessageEvent(ctx, t.ownerID, sid, userMsgID, "user")

	// Session state: handoff/resolved → canned ack instead of an AI answer.
	var status string
	_ = a.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id = $1", sid).Scan(&status)
	if status == "handoff" || status == "pending" {
		ack := platform.HandoffAcknowledgement(language)
		a.persistSystemReply(ctx, t.ownerID, sid, ack)
		sendEvent("token", map[string]string{"text": ack})
		sendEvent("done", map[string]any{"reply": ack, "tokens_used": 0, "cached_tokens": 0, "used_mock": false, "handoff": true})
		return
	}
	if status == "resolved" || status == "closed" {
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='active', resolved_at=NULL, closed_at=NULL WHERE session_id=$1", sid)
	}

	// Human request keyword → immediate handoff (AI stays silent).
	if keyword, matched := platform.HumanRequestKeyword(req.Message); matched {
		_, _ = a.DB.Exec(ctx,
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending','high','customer_request'::human_handoff_trigger,$3,NOW()) ON CONFLICT DO NOTHING",
			sid, t.ownerID, "Customer asked for a human agent (matched: "+keyword+")")
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id=$1 AND status='active'", sid)
		ack := platform.HandoffAcknowledgement(language)
		a.persistSystemReply(ctx, t.ownerID, sid, ack)
		a.notifyUser(ctx, t.ownerID, "handoff", "New human-handoff request", "customer_request (widget)", sid)
		a.publishSessionEvent(ctx, t.ownerID, sid)
		sendEvent("token", map[string]string{"text": ack})
		sendEvent("done", map[string]any{"reply": ack, "tokens_used": 0, "cached_tokens": 0, "used_mock": false, "escalated": true})
		return
	}

	history := a.chatHistory(ctx, sid, nil)
	groundCtx := a.RAG.Ground(ctx, t.ownerID, &sid, req.Message, language, history, 0)
	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
		sendEvent("sources", groundCtx.Sources)
	}
	result, cerr := a.Gemini.ChatStream(ctx, message, history, language, func(chunk string) {
		sendEvent("token", map[string]string{"text": chunk})
	})
	if cerr != nil {
		sendEvent("error", map[string]string{"message": "生成回答失败"})
		return
	}
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}
	usage.Record(ctx, a.DB, t.ownerID, &sid, a.Gemini.ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens)
	a.persistChatTurn(ctx, t.ownerID, sid, req.Message, result, reply, &groundCtx, language)
	a.classifyWebTurnAsync(t.ownerID, sid, req.Message, reply, groundCtx.HasMatch)
	sendEvent("done", map[string]any{
		"reply": reply, "tokens_used": result.PromptTokens + result.OutputTokens,
		"cached_tokens": result.CachedTokens, "used_mock": result.UsedMock,
	})
}

// widgetFeedback — POST /api/v1/widget/feedback {token, message_id, rating}.
func (a *App) widgetFeedback(w http.ResponseWriter, r *http.Request) (any, error) {
	t, apiErr := a.resolveWidget(r)
	if apiErr != nil {
		return nil, apiErr
	}
	var req struct {
		MessageID int32 `json:"message_id"`
		Rating    int   `json:"rating"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Rating != 1 && req.Rating != -1 {
		return nil, ErrBadRequest("rating 必须是 -1 或 1")
	}
	var sid string
	err := a.DB.QueryRow(r.Context(),
		"SELECT cm.session_id FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id "+
			"WHERE cm.message_id = $1 AND cm.role = 'model' AND s.user_id = $2", req.MessageID, t.ownerID).Scan(&sid)
	if err != nil {
		return nil, ErrNotFound("消息不存在")
	}
	_, _ = a.DB.Exec(r.Context(), "UPDATE chat_messages SET feedback_rating = $1, feedback_at = NOW() WHERE message_id = $2", req.Rating, req.MessageID)
	if req.Rating == -1 {
		_, _ = a.DB.Exec(r.Context(),
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending','high','negative_feedback'::human_handoff_trigger,$3,NOW()) ON CONFLICT DO NOTHING",
			sid, t.ownerID, "Customer rated the AI reply 👎")
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id=$1 AND status='active'", sid)
		a.notifyUser(r.Context(), t.ownerID, "handoff", "New human-handoff request", "negative_feedback (widget)", sid)
		a.publishSessionEvent(r.Context(), t.ownerID, sid)
	}
	return map[string]string{"message": "ok"}, nil
}

// classifyWebTurnAsync mirrors the platform classifier for widget turns:
// persists sentiment/intent and escalates angry / un-resolved conversations.
func (a *App) classifyWebTurnAsync(userID int32, sessionID, message, reply string, hasMatch bool) {
	if !a.Gemini.IsConfigured() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 20*time.Second)
		defer cancel()
		prompt := "You audit one customer-service turn. Reply with ONLY a JSON object:\n" +
			`{"sentiment":"positive|neutral|negative","intent":"one of: question, complaint, refund, order_status, price, booking, small_talk, other","confidence":0.0-1.0,"escalate":true|false}` + "\n" +
			"escalate=true when the customer is angry, repeatedly unsatisfied, asks for something only a human can do, or the assistant answer clearly does not resolve the question.\n\n" +
			"[KB grounded]=" + fmt.Sprintf("%t", hasMatch) + "\nCustomer: " + truncateForTitle(message, 600) + "\nAssistant: " + truncateForTitle(reply, 600)
		out, ok := a.Gemini.GenerateFast(ctx, prompt, 10*time.Second)
		if !ok {
			return
		}
		var cls struct {
			Sentiment  string  `json:"sentiment"`
			Intent     string  `json:"intent"`
			Confidence float64 `json:"confidence"`
			Escalate   bool    `json:"escalate"`
		}
		s := strings.TrimSpace(out)
		if i := strings.Index(s, "{"); i >= 0 {
			if j := strings.LastIndex(s, "}"); j > i {
				s = s[i : j+1]
			}
		}
		if json.Unmarshal([]byte(s), &cls) != nil {
			return
		}
		sentiment := cls.Sentiment
		if sentiment != "positive" && sentiment != "negative" {
			sentiment = "neutral"
		}
		intent := cls.Intent
		if intent == "" || len(intent) > 64 {
			intent = "other"
		}
		conf := cls.Confidence
		if conf < 0 || conf > 1 {
			conf = 0.5
		}
		_, _ = a.DB.Exec(ctx,
			"UPDATE sessions SET sentiment=$1, sentiment_at=NOW(), intent=$2, confidence=$3 WHERE session_id=$4 AND status='active'",
			sentiment, intent, conf, sessionID)
		var status string
		_ = a.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id=$1", sessionID).Scan(&status)
		if status != "active" {
			return
		}
		trigger := ""
		switch {
		case sentiment == "negative":
			trigger = "negative_feedback"
		case cls.Escalate:
			trigger = "ai_decision"
		case !hasMatch && conf < 0.35 && intent != "small_talk":
			trigger = "no_knowledge_base"
		}
		if trigger == "" {
			return
		}
		_, _ = a.DB.Exec(ctx,
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending','normal',$3::human_handoff_trigger,$4,NOW()) ON CONFLICT DO NOTHING",
			sessionID, userID, trigger, "Classifier flagged widget turn ("+intent+")")
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id=$1 AND status='active'", sessionID)
		a.notifyUser(ctx, userID, "handoff", "New human-handoff request", trigger+" (widget): "+intent, sessionID)
		a.publishSessionEvent(ctx, userID, sessionID)
	}()
}
