package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/usage"
)

// widgetRateLimit caps requests per client IP for the public widget endpoints
// (Redis fixed window; fails CLOSED when Redis is unreachable — matching the
// authenticated rateLimit middleware, because this is the one public surface
// whose accepted requests cost real LLM spend. The old fail-open choice left
// the tenant-billed endpoint uncapped during Redis incidents).
func (a *App) widgetRateLimit(maxRPM int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "widget-ip:" + clientIP(r)
			allowed, err := a.Redis.CheckRateLimit(r.Context(), key, uint32(maxRPM))
			if err != nil {
				WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务暂时不可用，请稍后再试"})
				return
			}
			if !allowed {
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
	// Per-token visitor rate limit. Keyed on the resolved client address only —
	// never on attacker-suppliable input like the raw session id (rotating it
	// used to mint a fresh window per request). Fails CLOSED with the rest of
	// the widget gates.
	if ok, err := a.Redis.CheckRateLimit(r.Context(), "widget:"+clientIP(r), 20); err != nil {
		WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务暂时不可用，请稍后再试"})
		return
	} else if !ok {
		WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "发送太快，请稍候"})
		return
	}
	// Layered abuse guard: the widget token ships publicly inside embed.js,
	// so per-IP limits alone cannot stop a token-burning bot that rotates
	// IPs. Cap total messages per token per day and new-session creation per
	// token per hour — IP rotation cannot bypass either (fail-closed: these
	// are the last spend caps on a metered public surface).
	dayKey := fmt.Sprintf("widget-msg-day:%d:%s", t.TokenID, time.Now().UTC().Format("20060102"))
	if ok, err := a.Redis.IncrWindow(r.Context(), dayKey, 200, 26*time.Hour); err != nil {
		WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务暂时不可用，请稍后再试"})
		return
	} else if !ok {
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
		// persistence would flood the inbox with sessions (fail-closed).
		hourKey := fmt.Sprintf("widget-sess-hour:%d:%s", t.TokenID, time.Now().UTC().Format("2006010215"))
		if ok, err := a.Redis.IncrWindow(r.Context(), hourKey, 20, 2*time.Hour); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "服务暂时不可用，请稍后再试"})
			return
		} else if !ok {
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

	// Reply persistence must survive a visitor hangup: r.Context() dies with
	// the connection, and a lost model row erases the answer from every later
	// turn's context and from the agent inbox. The handoff writes below use the
	// same detached context for the same reason — a visitor who closes the tab
	// during a quota stop still needs a human to pick the conversation up.
	persistCtx := context.WithoutCancel(ctx)

	// Human request → immediate handoff (AI stays silent). Shared by the
	// keyword match, the Jev route and the quota/spend degradations below so
	// every path behaves identically — including queue priority, computed the
	// same way the platform pipeline computes it instead of a hardcoded "high".
	webEscalate := func(trigger, reason, urgency string) {
		priority := platform.HandoffPriority(trigger, urgency)
		_, _ = a.DB.Exec(persistCtx,
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending',$3,$4::human_handoff_trigger,$5,NOW()) ON CONFLICT DO NOTHING",
			sid, t.ownerID, priority, trigger, reason)
		_, _ = a.DB.Exec(persistCtx, "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id=$1 AND status='active'", sid)
		ack := platform.HandoffAcknowledgement(language)
		a.persistSystemReply(ctx, t.ownerID, sid, ack)
		a.notifyUser(ctx, t.ownerID, "handoff", "New human-handoff request", trigger+" (widget)", sid)
		a.publishSessionEvent(ctx, t.ownerID, sid)
		sendEvent("token", map[string]string{"text": ack})
		sendEvent("done", map[string]any{"reply": ack, "tokens_used": 0, "cached_tokens": 0, "used_mock": false, "escalated": true})
	}
	if keyword, matched := platform.HumanRequestKeyword(req.Message); matched {
		webEscalate("customer_request", "Customer asked for a human agent (matched: "+keyword+")", "")
		return
	}

	history := a.chatHistory(ctx, sid, nil, userMsgID)

	// Retrieval is the slowest pre-generation step and does not depend on the
	// routing decision — only on whether we keep its result. Start it
	// concurrently with Jev's routing call below so Jev's latency stops being
	// purely additive. The widget is the primary customer channel, so it pays
	// that cost on nearly every turn.
	groundCh := make(chan rag.GroundingContext, 1)
	go func() {
		res := rag.GroundingContext{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					a.Logger.Warn("widget background retrieval panic recovered", "panic", r)
				}
			}()
			res = a.RAG.Ground(ctx, t.ownerID, &sid, req.Message, language, history, 0)
		}()
		groundCh <- res // exactly one send, so the channel never leaks
	}()

	// Jev pre-routing: catches semantic human requests the keyword list
	// misses, skips retrieval outright for pure chit-chat, and drops junk
	// with a thanks-only line (an SSE visitor cannot be left hanging, unlike
	// a platform channel where silence is natural). Unknown route
	// (Jev off/slow) keeps today's full path.
	skipGround := false
	inboundUrgency := platform.UrgencyUnknown
	if a.Pipe != nil {
		if r, ok := a.Pipe.RouteInbound(ctx, req.Message); ok {
			escalate, skip, silent := platform.RouteDecision(r.Route, r.Prob)
			if escalate {
				webEscalate("customer_request", "Jev routed the message as a human request (p="+strconv.FormatFloat(r.Prob, 'f', 2, 64)+")", r.Urgency)
				return
			}
			if silent {
				a.Logger.Info("junk dropped: jev routed the widget message as no-reply",
					"session_id", sid, "p", r.Prob)
				ack := platform.JunkAcknowledgement(language)
				a.persistSystemReply(ctx, t.ownerID, sid, ack)
				sendEvent("token", map[string]string{"text": ack})
				sendEvent("done", map[string]any{"reply": ack, "tokens_used": 0, "cached_tokens": 0, "used_mock": false, "ignored": true})
				return
			}
			skipGround = skip
			inboundUrgency = r.Urgency
		}
	}

	// Semantic reply cache: a hit answers before the speculative retrieval
	// finishes, skipping both the retrieval wait and generation. Small talk
	// skips the cache — conversational replies depend on history.
	cacheHit := false
	replyText := ""
	if !skipGround && a.Cache != nil && a.Cache.Enabled() {
		if cached, hit := a.Cache.Lookup(ctx, t.ownerID, req.Message, language); hit {
			cacheHit = true
			replyText = cached
			a.Logger.Info("reply cache hit", "session_id", sid)
		}
	}

	var groundCtx rag.GroundingContext
	var result gemini.ChatResult
	if !cacheHit {
		if skipGround {
			// Small talk: Jev decided retrieval is unnecessary, so drop the
			// speculative result. The buffered channel lets the worker finish
			// without blocking; one unused retrieval on chit-chat is cheaper than
			// making every real question wait for the routing call.
		} else {
			select {
			case groundCtx = <-groundCh:
			case <-ctx.Done():
				return
			}
		}
	}
	// Reply persistence must survive a visitor hangup: r.Context() dies with
	// the connection, and a lost model row erases the answer from every later
	// turn's context and from the agent inbox.
	// Cache hit: the stored answer was generated, guarded and persisted for
	// this tenant before — deliver it as one token, skipping generation and
	// the guard (re-judging every replay would erase the latency win).
	// The owner still gets the new-message ping: a cache hit is a real
	// visitor turn, and skipping the ping would make the owner blind to
	// exactly the turns the bot answered fastest.
	if cacheHit {
		a.persistModelReply(persistCtx, t.ownerID, sid, gemini.ChatResult{}, replyText, &rag.GroundingContext{}, language, time.Now(), "reply-cache")
		if a.Pipe != nil {
			ownerID, sessID, visitorMsg := t.ownerID, sid, req.Message
			platform.SpawnCritical(func() {
				a.Pipe.NotifyNewCustomerMessage(persistCtx, ownerID, sessID, "web", "", visitorMsg)
			})
		}
		sendEvent("token", map[string]string{"text": replyText})
		sendEvent("done", map[string]any{
			"reply": replyText, "tokens_used": 0, "cached_tokens": 0,
			"used_mock": false, "cached": true,
		})
		return
	}

	// Small talk: Jev was confident the message carries no request, so the
	// visitor gets the fixed template instead of a paid generation (see
	// platform.SmallTalkReply) — chit-chat is the most repeated turn type there
	// is, and the widget is the channel that pays for it most. The owner still
	// gets the ping, like any other visitor turn. JEV_CHITCHAT_CANNED=0
	// restores the previous path.
	if skipGround && platform.SmallTalkCanned() {
		replyText := platform.SmallTalkReply(language)
		a.persistModelReply(persistCtx, t.ownerID, sid, gemini.ChatResult{}, replyText, &rag.GroundingContext{}, language, time.Now(), "smalltalk-template")
		if a.Pipe != nil {
			ownerID, sessID, visitorMsg := t.ownerID, sid, req.Message
			platform.SpawnCritical(func() {
				a.Pipe.NotifyNewCustomerMessage(persistCtx, ownerID, sessID, "web", "", visitorMsg)
			})
		}
		sendEvent("token", map[string]string{"text": replyText})
		sendEvent("done", map[string]any{
			"reply": replyText, "tokens_used": 0, "cached_tokens": 0,
			"used_mock": false, "smalltalk": true,
		})
		return
	}

	// Spend gate (see usage.Budget): the rolling Gemini window is nearly full,
	// so hand the turn to a human now instead of collecting Google's 429 after
	// paying for retrieval and generation — the visitor sees the same handoff
	// message either way, but this one is not preceded by a failure.
	if spent, limit, over := usage.Budget(ctx, a.DB, a.Redis); over {
		if a.Pipe != nil {
			a.Pipe.AlertSpendGate(ctx, spent, limit)
		}
		webEscalate("ai_decision", "Gemini 消费速率接近上限，AI 主动让路给人工", inboundUrgency)
		return
	}

	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
		sendEvent("sources", groundCtx.Sources)
	}
	var streamed strings.Builder
	result, cerr := a.Gemini.ChatStream(ctx, message, history, language, func(chunk string) {
		streamed.WriteString(chunk)
		sendEvent("token", map[string]string{"text": chunk})
	})
	if cerr != nil {
		a.Logger.Error("widget generation failed", "session_id", sid, "token_id", t.TokenID,
			"grounded", groundCtx.HasMatch, "error", cerr.Error())
		if platform.IsQuotaExhausted(cerr) {
			// An exhausted quota will not clear inside this conversation, and a
			// visitor must not be left holding a bare error: keep whatever
			// streamed into the transcript, page the operator, then hand the
			// session to a human and tell the visitor so.
			if partial := strings.TrimSpace(streamed.String()); partial != "" {
				a.persistModelReply(persistCtx, t.ownerID, sid, gemini.ChatResult{}, partial, &groundCtx, language, time.Now(), "")
			}
			if a.Pipe != nil {
				a.Pipe.AlertQuotaExhausted(persistCtx, cerr)
			}
			webEscalate("ai_decision", "Gemini 配额/余额耗尽，AI 无法生成回复", inboundUrgency)
			return
		}
		sendEvent("error", map[string]string{"message": "生成回答失败"})
		// Keep whatever already streamed: the visitor may have read part of
		// it, and the next turn must not see a question with no answer.
		if partial := strings.TrimSpace(streamed.String()); partial != "" {
			a.persistModelReply(persistCtx, t.ownerID, sid, gemini.ChatResult{}, partial, &groundCtx, language, time.Now(), "")
		}
		return
	}
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}
	// The stream already reached the visitor, so the semantic guard is
	// after-the-fact here: it can escalate and page the owner, not unsend.
	// persistCtx because the visitor may hang up mid-guard (r.Context dies).
	claimsHandoff := false
	if a.Pipe != nil {
		srcTexts := make([]string, 0, len(groundCtx.Sources))
		for _, src := range groundCtx.Sources {
			srcTexts = append(srcTexts, src.Content)
		}
		if g, ok := a.Pipe.GuardReply(persistCtx, reply, srcTexts); ok {
			claimsHandoff = g.PromisesHandoff
			if g.UnsafeClaim {
				a.Pipe.AlertQuality(ctx, t.ownerID, sid, "AI 回复包含待确认承诺",
					"Jev 标记该回复做出了需店员确认的承诺（价格/交期/库存等），请在收件箱检查该会话。")
			}
			if !g.SupportedBySources {
				a.Pipe.AlertQuality(ctx, t.ownerID, sid, "AI 回复脱离知识库作答",
					"Jev 标记该回复的事实性断言没有命中知识库原文（可能是幻觉），请核对后回复客户。")
			}
		}
	}
	// Store the post-stream answer for future identical asks (mock replies are
	// never cached). persistCtx: the visitor may hang up before the store.
	if !skipGround && !result.UsedMock && a.Cache != nil && a.Cache.Enabled() {
		ownerID, cachedQuery, cachedLang, cachedReply := t.ownerID, req.Message, language, reply
		platform.SpawnClassifier(func() {
			a.Cache.Store(persistCtx, ownerID, cachedQuery, cachedLang, cachedReply, a.Gemini.ModelName())
		})
	}
	usage.Record(persistCtx, a.DB, t.ownerID, &sid, a.Gemini.ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens)
	// The visitor row was persisted above; persistChatTurn would store it a
	// second time (and double-count it), so persist only the model turn.
	a.persistModelReply(persistCtx, t.ownerID, sid, result, reply, &groundCtx, language, time.Now(), "")
	// The reply announced a handoff ("已为您转接人工…") — create the real
	// request so an agent is actually notified. No canned ack: the reply
	// itself already told the customer. Priority computed the same way the
	// platform pipeline computes it, urgent turns jumping the line.
	escalated := false
	if platform.ReplyClaimsHandoff(reply) || claimsHandoff {
		priority := platform.HandoffPriority("ai_decision", inboundUrgency)
		if _, err := a.DB.Exec(ctx,
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending',$3,'ai_decision'::human_handoff_trigger,$4,NOW()) ON CONFLICT DO NOTHING",
			sid, t.ownerID, priority, "AI reply announced a handoff to the customer"); err == nil {
			_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id=$1 AND status='active'", sid)
			a.notifyUser(ctx, t.ownerID, "handoff", "New human-handoff request", "ai_decision (widget): AI announced a transfer", sid)
			a.publishSessionEvent(ctx, t.ownerID, sid)
			escalated = true
		}
	}
	if !escalated {
		// Telegram parity with platform channels: ping the owner about the
		// new customer message (handoff turns are covered by the 🔔 ping
		// instead; throttled per session; background). persistCtx so a
		// visitor hangup cannot cancel the ping mid-send.
		if a.Pipe != nil {
			ownerID, sessID, visitorMsg := t.ownerID, sid, req.Message
			platform.SpawnCritical(func() {
				a.Pipe.NotifyNewCustomerMessage(persistCtx, ownerID, sessID, "web", "", visitorMsg)
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
// and escalation decision as platform channels, Jev first with the fast model
// as fallback, so intent-based handoffs and topic tags behave identically
// everywhere (this path drifted before: it skipped Jev and never wrote tags).
func (a *App) classifyWebTurnAsync(userID int32, sessionID, message, reply string, hasMatch bool) {
	if a.Pipe == nil {
		return
	}
	platform.SpawnClassifier(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 20*time.Second)
		defer cancel()
		verdict, topic, raw, fromJev, ok := a.Pipe.JudgeTurnFor(ctx, message, reply, hasMatch)
		if !ok {
			slog.Warn("widget turn classifier failed", "session_id", sessionID)
			return
		}
		a.Pipe.PersistTurnVerdict(ctx, sessionID, verdict, topic)
		var status string
		_ = a.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id=$1", sessionID).Scan(&status)
		if status != "active" {
			return
		}
		var trigger, reason string
		if fromJev {
			trigger, reason = platform.TurnTriggerFor(verdict, raw, hasMatch, a.Pipe.HasReadyDocs(ctx, userID))
		} else {
			trigger, reason = platform.TurnTrigger(verdict, hasMatch, a.Pipe.HasReadyDocs(ctx, userID))
		}
		if trigger == "" {
			return
		}
		a.Pipe.EscalateHuman(ctx, userID, sessionID, trigger, reason+" (widget)")
	})
}
