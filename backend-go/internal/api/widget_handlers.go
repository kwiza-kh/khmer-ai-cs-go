package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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
	TokenID            int64 `json:"token_id"`
	ownerID            int32
	Name               string   `json:"name"`
	Token              string   `json:"token"`
	IsActive           bool     `json:"is_active"`
	AllowedOrigin      []string `json:"allowed_origins"`
	Theme              string   `json:"theme"`
	PrimaryColor       string   `json:"primary_color"`
	GreetingKm         string   `json:"greeting_km"`
	GreetingEn         string   `json:"greeting_en"`
	SuggestedQuestions []string `json:"suggested_questions"`
	CreatedAt          string   `json:"created_at"`
}

func generateWidgetToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("wt_%x", time.Now().UnixNano())
	}
	return "wt_" + hex.EncodeToString(b)
}

const widgetCols = "token_id, user_id, name, token, is_active, allowed_origins, theme, primary_color, COALESCE(greeting_km,''), COALESCE(greeting_en,''), suggested_questions, created_at"

func scanWidgetToken(scan func(dest ...any) error) (widgetTokenRow, error) {
	var t widgetTokenRow
	var origins, questions []string
	var createdAt time.Time
	err := scan(&t.TokenID, &t.ownerID, &t.Name, &t.Token, &t.IsActive, &origins, &t.Theme, &t.PrimaryColor, &t.GreetingKm, &t.GreetingEn, &questions, &createdAt)
	t.AllowedOrigin = origins
	t.SuggestedQuestions = questions
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
	Questions  []string `json:"suggested_questions"`
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
	// Opening-question chips: trim, drop blanks, cap the list so the widget
	// never renders a wall of chips.
	questions := make([]string, 0, len(req.Questions))
	for _, q := range req.Questions {
		if q = strings.TrimSpace(q); q != "" {
			questions = append(questions, q)
		}
		if len(questions) == 6 {
			break
		}
	}
	token := generateWidgetToken()
	var id int64
	err := a.DB.QueryRow(r.Context(),
		"INSERT INTO widget_tokens (user_id, name, token, is_active, allowed_origins, theme, primary_color, greeting_km, greeting_en, suggested_questions) "+
			"VALUES ($1,$2,$3,true,$4::text[],$5,$6,$7,$8,$9::text[]) RETURNING token_id",
		user.UserID, req.Name, token, req.Origins, req.Theme, req.Color, req.GreetingKm, req.GreetingEn, questions).Scan(&id)
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
	suggested := t.SuggestedQuestions
	if suggested == nil {
		suggested = []string{}
	}
	return map[string]any{
		"name":                t.Name,
		"theme":               t.Theme,
		"primary_color":       t.PrimaryColor,
		"greeting_km":         t.GreetingKm,
		"greeting_en":         t.GreetingEn,
		"suggested_questions": suggested,
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
	// Layered abuse guard: the widget token ships publicly inside embed.js,
	// so per-IP limits alone cannot stop a token-burning bot that rotates
	// IPs. Cap total messages per token per day and new-session creation per
	// token per hour — IP rotation cannot bypass either (fail-open).
	dayKey := fmt.Sprintf("widget-msg-day:%d:%s", t.TokenID, time.Now().UTC().Format("20060102"))
	if ok, err := a.Redis.IncrWindow(r.Context(), dayKey, 200, 26*time.Hour); err == nil && !ok {
		WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "今日咨询量已达上限，请明日再来或直接致电我们"})
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
	// A returning visitor un-archives their thread so the reply is not hidden.
	if req.SessionID != "" && owner == t.ownerID {
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET archived_at=NULL WHERE session_id=$1 AND archived_at IS NOT NULL", req.SessionID)
	}
	if req.SessionID == "" || owner != t.ownerID {
		// New-session throttle: scripted abuse without client-side session
		// persistence would flood the inbox with sessions (fail-open).
		hourKey := fmt.Sprintf("widget-sess-hour:%d:%s", t.TokenID, time.Now().UTC().Format("2006010215"))
		if ok, err := a.Redis.IncrWindow(r.Context(), hourKey, 20, 2*time.Hour); err == nil && !ok {
			WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "会话创建过于频繁，请稍后再试"})
			return
		}
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
	// A customer cancellation ("不用转人工了") or an all-resolved queue hands
	// the session back to the AI, mirroring the platform pipeline.
	var status string
	_ = a.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id = $1", sid).Scan(&status)
	if status == "handoff" || status == "pending" {
		if a.releaseWebHandoff(ctx, t.ownerID, sid, req.Message) {
			status = "active"
		} else {
			ack := platform.HandoffAcknowledgement(language)
			a.persistSystemReply(ctx, t.ownerID, sid, ack)
			sendEvent("token", map[string]string{"text": ack})
			sendEvent("done", map[string]any{"reply": ack, "tokens_used": 0, "cached_tokens": 0, "used_mock": false, "handoff": true})
			return
		}
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

	history := a.chatHistory(ctx, sid, nil, userMsgID)
	groundCtx := a.RAG.Ground(ctx, t.ownerID, &sid, req.Message, language, history, 0)
	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
		sendEvent("sources", groundCtx.Sources)
	}
	// Reply persistence must survive a visitor hangup: r.Context() dies with
	// the connection, and a lost model row erases the answer from every later
	// turn's context and from the agent inbox.
	persistCtx := context.WithoutCancel(ctx)
	var streamed strings.Builder
	result, cerr := a.Gemini.ChatStream(ctx, message, history, language, func(chunk string) {
		streamed.WriteString(chunk)
		sendEvent("token", map[string]string{"text": chunk})
	})
	if cerr != nil {
		sendEvent("error", map[string]string{"message": "生成回答失败"})
		// Keep whatever already streamed: the visitor may have read part of
		// it, and the next turn must not see a question with no answer.
		if partial := strings.TrimSpace(streamed.String()); partial != "" {
			a.persistModelReply(persistCtx, t.ownerID, sid, gemini.ChatResult{}, partial, &groundCtx, language, time.Now())
		}
		return
	}
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}
	usage.Record(persistCtx, a.DB, t.ownerID, &sid, a.Gemini.ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens)
	// The visitor row was persisted above; persistChatTurn would store it a
	// second time (and double-count it), so persist only the model turn.
	a.persistModelReply(persistCtx, t.ownerID, sid, result, reply, &groundCtx, language, time.Now())
	// The reply announced a handoff ("已为您转接人工…") — create the real
	// request so an agent is actually notified. No canned ack: the reply
	// itself already told the customer.
	escalated := false
	if platform.ReplyClaimsHandoff(reply) {
		if _, err := a.DB.Exec(ctx,
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending','high','ai_decision'::human_handoff_trigger,$3,NOW()) ON CONFLICT DO NOTHING",
			sid, t.ownerID, "AI reply announced a handoff to the customer"); err == nil {
			_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id=$1 AND status='active'", sid)
			a.notifyUser(ctx, t.ownerID, "handoff", "New human-handoff request", "ai_decision (widget): AI announced a transfer", sid)
			a.publishSessionEvent(ctx, t.ownerID, sid)
			escalated = true
		}
	}
	if !escalated {
		// Telegram parity with platform channels: ping the owner about the
		// new customer message (handoff turns are covered by the 🔔 ping
		// instead; throttled per session; background).
		if a.Pipe != nil {
			ownerID, sessID, visitorMsg := t.ownerID, sid, req.Message
			platform.SpawnCritical(func() {
				a.Pipe.NotifyNewCustomerMessage(ctx, ownerID, sessID, "web", "", visitorMsg)
			})
		}
		a.classifyWebTurnAsync(t.ownerID, sid, req.Message, reply, groundCtx.HasMatch)
	}
	sendEvent("done", map[string]any{
		"reply": reply, "tokens_used": result.PromptTokens + result.OutputTokens,
		"cached_tokens": result.CachedTokens, "used_mock": result.UsedMock,
		"escalated": escalated,
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

// releaseWebHandoff mirrors the platform pipeline's maybeReleaseHandoff for
// website-widget sessions: an explicit customer cancellation ("不用转人工了")
// or an all-resolved request queue hands the session back to the AI.
func (a *App) releaseWebHandoff(ctx context.Context, ownerID int32, sessionID, content string) bool {
	var open int
	_ = a.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM human_handoff_requests WHERE session_id=$1 AND status IN ('pending','assigned')",
		sessionID).Scan(&open)
	if open == 0 {
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='active' WHERE session_id=$1 AND status='handoff'", sessionID)
		a.publishSessionEvent(ctx, ownerID, sessionID)
		return true
	}
	if _, ok := platform.ReleaseHandoffKeyword(content); !ok {
		return false
	}
	tag, err := a.DB.Exec(ctx,
		"UPDATE human_handoff_requests SET status='resolved', resolved_at=NOW(), resolution_note='customer cancelled handoff' "+
			"WHERE session_id=$1 AND status='pending'", sessionID)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='active' WHERE session_id=$1 AND status='handoff'", sessionID)
	a.publishSessionEvent(ctx, ownerID, sessionID)
	return true
}

// classifyWebTurnAsync — widget turns go through the SAME shared classifier
// and escalation decision as platform channels (TurnTrigger), so intent-based
// handoffs behave identically everywhere.
func (a *App) classifyWebTurnAsync(userID int32, sessionID, message, reply string, hasMatch bool) {
	if !a.Gemini.IsConfigured() {
		return
	}
	platform.SpawnClassifier(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 20*time.Second)
		defer cancel()
		verdict, ok := a.Gemini.JudgeTurn(ctx, message, reply, hasMatch)
		if !ok {
			slog.Warn("widget turn classifier failed", "session_id", sessionID)
			return
		}
		_, _ = a.DB.Exec(ctx,
			"UPDATE sessions SET sentiment=$1, sentiment_at=NOW(), intent=$2, confidence=$3 WHERE session_id=$4 AND status='active'",
			verdict.Sentiment, verdict.Intent, verdict.Confidence, sessionID)
		var status string
		_ = a.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id=$1", sessionID).Scan(&status)
		if status != "active" {
			return
		}
		if a.Pipe == nil {
			return
		}
		trigger, reason := platform.TurnTrigger(verdict, hasMatch, a.Pipe.HasReadyDocs(ctx, userID))
		if trigger == "" {
			return
		}
		a.Pipe.EscalateHuman(ctx, userID, sessionID, trigger, reason+" (widget)")
	})
}
