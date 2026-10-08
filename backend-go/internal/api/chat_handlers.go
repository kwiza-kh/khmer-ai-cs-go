package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/textutil"
	"khmer-ai-cs-go/internal/usage"
)

// ============================================
// Chat (plain + SSE streaming)
// ============================================

type chatRequest struct {
	Message   string  `json:"message"`
	SessionID *string `json:"session_id"`
	Language  *string `json:"language"`
	Test      bool    `json:"test"`
	History   []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
}

// resolveChatSession returns the session for a chat turn, creating one when
// the caller omitted session_id. Test turns land in is_test sessions (never
// shown in the customer inbox); real turns create platform='web' sessions so
// the web widget conversations reach agents.
func (a *App) resolveChatSession(ctx context.Context, userID int32, req *chatRequest) (string, bool, error) {
	isTest := req.Test
	if req.SessionID != nil && *req.SessionID != "" {
		var sid, status string
		err := a.DB.QueryRow(ctx,
			"SELECT session_id, status::text FROM sessions WHERE session_id = $1 AND user_id = $2",
			*req.SessionID, userID).Scan(&sid, &status)
		if err != nil {
			return "", false, ErrNotFound("会话不存在")
		}
		var test bool
		_ = a.DB.QueryRow(ctx, "SELECT is_test FROM sessions WHERE session_id = $1", sid).Scan(&test)
		// Customer replied on a finished session → reopen (same rule as the
		// platform pipeline).
		if status == "resolved" || status == "closed" {
			_, _ = a.DB.Exec(ctx, "UPDATE sessions SET status='active', resolved_at=NULL, closed_at=NULL WHERE session_id=$1", sid)
		}
		// New activity revives an archived conversation so it is not hidden.
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET archived_at=NULL WHERE session_id=$1 AND archived_at IS NOT NULL", sid)
		return sid, test, nil
	}
	sid := newUUID()
	platformVal := any(nil)
	if !isTest {
		platformVal = "web"
	}
	title := textutil.TruncateRunes(req.Message, 60)
	_, err := a.DB.Exec(ctx,
		"INSERT INTO sessions (session_id, user_id, platform, language, status, title, is_test, created_at) "+
			"VALUES ($1,$2,$3::platform_type,$4,'active',$5,$6,NOW())",
		sid, userID, platformVal, req.Language, title, isTest)
	if err != nil {
		return "", false, ErrInternal("创建会话失败")
	}
	req.SessionID = &sid
	return sid, isTest, nil
}

// chatHistory loads the newest 20 persisted turns (chronological) so web/test
// sessions keep multi-turn memory without trusting client-sent history.
// excludeMessageID drops the triggering message when the caller persisted it
// before loading (it is appended to the request separately). TTS echo rows and
// cancelled deliveries are excluded — see Pipeline.loadHistory for why.
func (a *App) chatHistory(ctx context.Context, sessionID string, reqHistory []gemini.HistoryItem, excludeMessageID int64) []gemini.HistoryItem {
	if sessionID == "" {
		return reqHistory
	}
	rows, err := a.DB.Query(ctx,
		"SELECT role, content FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent') "+
			"AND message_id <> $2 AND NOT (role = 'model' AND message_type = 'audio') AND cancelled_at IS NULL "+
			"ORDER BY message_id DESC LIMIT 20", sessionID, excludeMessageID)
	if err != nil {
		// Do not fail the turn over a history read, but never lose the signal:
		// a silent nil here used to look like the AI "forgetting".
		a.Logger.Error("load history failed; AI will answer without context", "session_id", sessionID, "error", err.Error())
		return reqHistory
	}
	defer rows.Close()
	var rev []gemini.HistoryItem
	for rows.Next() {
		var h gemini.HistoryItem
		if rows.Scan(&h.Role, &h.Content) == nil {
			rev = append(rev, h)
		}
	}
	if err := rows.Err(); err != nil {
		// Same rule as the Query failure above: do not fail the turn, but never
		// let a truncated history look like the model forgetting the thread.
		a.Logger.Error("load history incomplete; AI context may be truncated", "session_id", sessionID, "error", err.Error())
	}
	if len(rev) == 0 {
		return reqHistory
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return gemini.TrimHistoryBudget(rev)
}

// persistModelReply stores only the model turn of a chat exchange whose user
// row already exists. It was split out of a combined "persist the whole turn"
// helper so streaming handlers can persist the reply on a request-detached
// context after the client hangs up; that combined helper had no callers left
// and was deleted — going through it would have stored the customer message
// twice on the widget path, which persists the visitor row itself.
// modelName overrides the stamped model_name — a canned reply passes
// "smalltalk-template" so the console can tell it from a paid generation; ""
// stamps the live model.
func (a *App) persistModelReply(ctx context.Context, userID int32, sessionID string, result gemini.ChatResult, reply string, groundCtx *rag.GroundingContext, language string, now time.Time, modelName string) {
	if language != "" {
		_, _ = a.DB.Exec(ctx, "UPDATE sessions SET language = $2 WHERE session_id = $1 AND (language = '' OR language IS NULL)", sessionID, language)
	}
	if modelName == "" {
		modelName = a.serving().ModelName()
	}

	var sourcesJSONArg any
	if groundCtx != nil && groundCtx.HasMatch && len(groundCtx.Sources) > 0 {
		if b, err := json.Marshal(groundCtx.Sources); err == nil {
			sourcesJSONArg = string(b)
		}
	}
	var msgID int64
	err := a.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, tokens_used, model_name, used_mock, sources_json, created_at) "+
			"VALUES ($1,'model','text',$2,$3,$4,$5,$6,$7) RETURNING message_id",
		sessionID, reply, result.PromptTokens+result.OutputTokens, modelName, result.UsedMock, sourcesJSONArg, now).Scan(&msgID)
	if err != nil {
		a.Logger.Warn("persist web model reply failed", "error", err.Error(), "session_id", sessionID)
		return
	}
	_, _ = a.DB.Exec(ctx,
		"UPDATE sessions SET model_message_count = model_message_count + 1, first_response_at = COALESCE(first_response_at, $1) WHERE session_id = $2",
		now, sessionID)
	a.publishMessageEvent(ctx, userID, sessionID, msgID, "model")
}

// publishMessageEvent nudges the tenant inbox for a persisted chat message.
func (a *App) publishMessageEvent(ctx context.Context, userID int32, sessionID string, messageID int64, role string) {
	realtime.Publish(ctx, a.Redis, realtime.Event{
		Type:      realtime.EventMessage,
		UserID:    userID,
		SessionID: sessionID,
		MessageID: messageID,
		Role:      role,
	})
}

// webHandoffCheck applies the same customer-request keyword rule as the
// platform pipeline to web/widget conversations: an explicit human request
// lands the session in the handoff queue and the AI acks instead of answering.
func (a *App) webHandoffCheck(ctx context.Context, userID int32, sessionID, message string) (bool, string) {
	keyword, matched := platform.HumanRequestKeyword(message)
	if !matched {
		return false, ""
	}
	_, _ = a.DB.Exec(ctx,
		"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
			"VALUES ($1,$2,'pending','high','customer_request'::human_handoff_trigger,$3,NOW()) ON CONFLICT DO NOTHING",
		sessionID, userID, "Customer asked for a human agent (matched: "+keyword+")")
	_, _ = a.DB.Exec(ctx,
		"UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at, NOW()) WHERE session_id=$1 AND status='active'", sessionID)
	a.notifyUser(ctx, userID, "handoff", "New human-handoff request", "customer_request: customer asked for a human", sessionID)
	a.publishSessionEvent(ctx, userID, sessionID)
	return true, platform.HandoffAcknowledgement(a.customerLanguage(ctx, userID, sessionID))
}

// chatPlain — synchronous chat turn (persisted).
func (a *App) chatPlain(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, ErrBadRequest("请求格式错误")
	}
	// The request's language is a hint (the console sends its own default), not
	// the customer's: see replyLanguage. The merchant's saved preference still
	// outranks it, and the script of the message outranks the hint.
	hint := ""
	if req.Language != nil {
		hint = *req.Language
	}
	language := replyLanguage(req.Message, a.savedLanguage(r.Context(), user.UserID), hint)
	req.Language = &language

	// The console's own tester consumes a message from the tenant's quota, exactly
	// like a customer message does — the counter is what the plans are sold on.
	if err := usage.ConsumeMessageQuota(r.Context(), a.DB, user.UserID); err != nil {
		if errors.Is(err, usage.ErrMessageQuotaExhausted) {
			return nil, &ApiError{http.StatusPaymentRequired, "月度消息配额已用尽，请升级套餐"}
		}
		return nil, ErrInternal("配额校验失败")
	}

	sessionID, _, err := a.resolveChatSession(r.Context(), user.UserID, &req)
	if err != nil {
		return nil, err
	}

	// Persist the customer message before any early return (escalation ack,
	// AI failure) so the transcript and future AI context never lose it.
	var userMsgID int64
	_ = a.DB.QueryRow(r.Context(),
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'user','text',$2,NOW()) RETURNING message_id",
		sessionID, req.Message).Scan(&userMsgID)
	_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET user_message_count = user_message_count + 1 WHERE session_id = $1", sessionID)
	a.publishMessageEvent(r.Context(), user.UserID, sessionID, userMsgID, "user")

	// Human request keywords → handoff ack instead of an AI answer.
	if esc, ack := a.webHandoffCheck(r.Context(), user.UserID, sessionID, req.Message); esc {
		a.persistSystemReply(r.Context(), user.UserID, sessionID, ack)
		return map[string]any{
			"reply": ack, "session_id": sessionID, "tokens_used": 0, "cached_tokens": 0,
			"used_mock": false, "escalated": true,
		}, nil
	}

	// Spend gate (see usage.Budget): when the rolling Gemini window is nearly
	// full, this turn fails in milliseconds instead of paying for retrieval plus
	// generation and then returning Google's 429. The operator gets the number,
	// the caller gets a retryable status.
	if spent, limit, over := usage.Budget(r.Context(), a.DB, a.Redis); over {
		if a.Pipe != nil {
			a.Pipe.AlertSpendGate(r.Context(), spent, limit)
		}
		return nil, ErrServiceUnavailable("AI 服务繁忙，请稍后重试")
	}

	history := a.chatHistory(r.Context(), sessionID, historyFromRequest(req), userMsgID)
	sid := sessionID
	groundCtx := a.RAG.Ground(r.Context(), user.UserID, &sid, req.Message, language, history, 0)
	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
	}
	result, err := a.serving().Chat(r.Context(), message, history, language)
	if err != nil {
		// The upstream reason (quota 429, 5xx, transport) is the only thing that
		// makes this actionable, and returning a bare 500 threw it away: a load
		// test produced thousands of these with no way to tell an exhausted
		// spend limit from a network fault. Log the cause, answer generically,
		// and page on a quota stop — this path and the widget are the only ones
		// that do not route through the platform pipeline's alerting.
		a.Logger.Error("chat generation failed", "session_id", sessionID, "user_id", user.UserID,
			"grounded", groundCtx.HasMatch, "error", err.Error())
		if a.Pipe != nil && platform.IsQuotaExhausted(err) {
			a.Pipe.AlertQuotaExhausted(r.Context(), err)
		}
		return nil, ErrInternal("生成回答失败")
	}
	usage.Record(r.Context(), a.DB, user.UserID, &sid, a.serving().ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens)
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.SanitizeReply(reply)
	}
	a.persistModelReply(r.Context(), user.UserID, sessionID, result, reply, &groundCtx, language, time.Now(), "")

	resp := map[string]any{
		"reply":         reply,
		"session_id":    sessionID,
		"tokens_used":   result.PromptTokens + result.OutputTokens,
		"cached_tokens": result.CachedTokens,
		"used_mock":     result.UsedMock,
	}
	if groundCtx.HasMatch {
		resp["sources"] = groundCtx.Sources
	}
	return resp, nil
}

// chatStream — SSE streaming chat turn (persisted).
//
// Wire protocol (matches lib/api.ts streamChat):
//
//	event: session  data: {"session_id":"..."}
//	event: sources  data: [ {doc_id,title,content,score}, ... ]
//	event: token    data: {"text":"chunk"}            (repeated)
//	event: done     data: {"reply","tokens_used","cached_tokens","used_mock"}
//	event: error    data: {"message":"..."}
func (a *App) chatStream(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r)
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}
	// Same hint-not-choice rule as chatPlain: see replyLanguage.
	hint := ""
	if req.Language != nil {
		hint = *req.Language
	}
	language := replyLanguage(req.Message, a.savedLanguage(r.Context(), user.UserID), hint)
	req.Language = &language

	// Same gate as chatPlain: see the comment there.
	if err := usage.ConsumeMessageQuota(r.Context(), a.DB, user.UserID); err != nil {
		if errors.Is(err, usage.ErrMessageQuotaExhausted) {
			WriteJSON(w, http.StatusPaymentRequired, map[string]string{"error": "月度消息配额已用尽，请升级套餐"})
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "配额校验失败"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sendEvent := func(event string, data any) {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\n", event)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	sessionID, _, err := a.resolveChatSession(r.Context(), user.UserID, &req)
	if err != nil {
		sendEvent("error", map[string]string{"message": "会话不存在"})
		return
	}
	sendEvent("session", map[string]any{"session_id": sessionID})

	if esc, ack := a.webHandoffCheck(r.Context(), user.UserID, sessionID, req.Message); esc {
		a.persistSystemReply(r.Context(), user.UserID, sessionID, ack)
		sendEvent("token", map[string]string{"text": ack})
		sendEvent("done", map[string]any{"reply": ack, "tokens_used": 0, "cached_tokens": 0, "used_mock": false, "escalated": true})
		return
	}

	// The user row is persisted on every path below, so record it before the
	// AI turn and keep that id out of the history window.
	var userMsgID int64
	userNow := time.Now()
	_ = a.DB.QueryRow(r.Context(),
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'user','text',$2,$3) RETURNING message_id",
		sessionID, req.Message, userNow).Scan(&userMsgID)
	_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET user_message_count = user_message_count + 1 WHERE session_id = $1", sessionID)
	a.publishMessageEvent(r.Context(), user.UserID, sessionID, userMsgID, "user")

	// Spend gate (see usage.Budget): refuse before paying for retrieval and
	// generation only to collect Google's 429.
	if spent, limit, over := usage.Budget(r.Context(), a.DB, a.Redis); over {
		if a.Pipe != nil {
			a.Pipe.AlertSpendGate(r.Context(), spent, limit)
		}
		sendEvent("error", map[string]string{"message": "AI 服务繁忙，请稍后重试"})
		return
	}

	history := a.chatHistory(r.Context(), sessionID, historyFromRequest(req), userMsgID)
	sid := sessionID
	groundCtx := a.RAG.Ground(r.Context(), user.UserID, &sid, req.Message, language, history, 0)
	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
		sendEvent("sources", groundCtx.Sources)
	}

	// Reply persistence must survive a client hangup: the visitor closing the
	// tab cancels r.Context(), and losing the model row would erase the answer
	// from every later turn's context and from the agent inbox.
	persistCtx := context.WithoutCancel(r.Context())
	var streamed strings.Builder
	result, err := a.serving().ChatStream(r.Context(), message, history, language, func(chunk string) {
		streamed.WriteString(chunk)
		sendEvent("token", map[string]string{"text": chunk})
	})
	if err != nil {
		a.Logger.Error("chat stream generation failed", "session_id", sessionID, "user_id", user.UserID,
			"grounded", groundCtx.HasMatch, "error", err.Error())
		if a.Pipe != nil && platform.IsQuotaExhausted(err) {
			a.Pipe.AlertQuotaExhausted(r.Context(), err)
		}
		sendEvent("error", map[string]string{"message": "生成回答失败"})
		if partial := strings.TrimSpace(streamed.String()); partial != "" {
			a.persistModelReply(persistCtx, user.UserID, sessionID, gemini.ChatResult{}, partial, &groundCtx, language, time.Now(), "")
		}
		return
	}
	usage.Record(persistCtx, a.DB, user.UserID, &sid, a.serving().ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens)
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.SanitizeReply(reply)
	}
	a.persistModelReply(persistCtx, user.UserID, sessionID, result, reply, &groundCtx, language, time.Now(), "")

	sendEvent("done", map[string]any{
		"reply":         reply,
		"tokens_used":   result.PromptTokens + result.OutputTokens,
		"cached_tokens": result.CachedTokens,
		"used_mock":     result.UsedMock,
	})
}

// persistSystemReply stores a canned system message (handoff ack).
func (a *App) persistSystemReply(ctx context.Context, userID int32, sessionID, text string) {
	var msgID int64
	err := a.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'system','text',$2,NOW()) RETURNING message_id",
		sessionID, text).Scan(&msgID)
	if err == nil {
		a.publishMessageEvent(ctx, userID, sessionID, msgID, "system")
	}
}

// customerLanguage prefers the session language, then the owner preference.
func (a *App) customerLanguage(ctx context.Context, userID int32, sessionID string) string {
	var lang *string
	_ = a.DB.QueryRow(ctx, "SELECT language FROM sessions WHERE session_id = $1", sessionID).Scan(&lang)
	if l := textutil.DerefString(lang); l != "" {
		return l
	}
	return a.savedLanguage(ctx, userID)
}

func (a *App) savedLanguage(ctx context.Context, userID int32) string {
	var lang *string
	_ = a.DB.QueryRow(ctx, "SELECT language FROM users WHERE user_id = $1", userID).Scan(&lang)
	if lang == nil {
		return ""
	}
	return *lang
}

func historyFromRequest(req chatRequest) []gemini.HistoryItem {
	var out []gemini.HistoryItem
	for _, h := range req.History {
		out = append(out, gemini.HistoryItem{Role: h.Role, Content: h.Content})
	}
	return out
}

// ============================================
// Sessions CRUD + messages + feedback
// ============================================

// listSessions — paginated sessions for the current user.
func (a *App) listSessions(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	q := r.URL.Query()
	page := parseIntOr(q.Get("page"), 1)
	pageSize := parseIntOr(q.Get("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize
	search := strings.TrimSpace(q.Get("q"))

	// Static WHERE, values bound: $1 the owner, $2 the ?q= term ('' = no filter).
	// The clause used to be concatenated with the placeholder numbers computed
	// from len(args), which is what made it read (to a person and to a scanner)
	// like user input could reach the SQL text. It could not — and now the string
	// is a constant, so there is nothing to re-derive.
	where := "WHERE user_id = $1 AND is_test = FALSE" +
		" AND ($2::text = '' OR title ILIKE '%'||$2||'%' OR COALESCE(platform_user_id,'') ILIKE '%'||$2||'%')"
	var total int64
	if err := a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM sessions "+where, user.UserID, search).Scan(&total); err != nil {
		return nil, ErrInternal("查询失败")
	}
	rows, err := a.DB.Query(r.Context(),
		"SELECT session_id, user_id, platform::text, platform_user_id, status::text, language, title, "+
			"user_message_count, model_message_count, created_at FROM sessions "+where+
			" ORDER BY created_at DESC LIMIT $3 OFFSET $4",
		user.UserID, search, pageSize, offset)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	sessions := make([]map[string]any, 0)
	for rows.Next() {
		var (
			sid, status, language    string
			platformVal, puid, title *string
			uid                      int32
			umc, mmc                 int32
			createdAt                time.Time
		)
		if err := rows.Scan(&sid, &uid, &platformVal, &puid, &status, &language, &title, &umc, &mmc, &createdAt); err != nil {
			continue
		}
		sessions = append(sessions, map[string]any{
			"session_id": sid, "user_id": uid, "platform": textutil.DerefString(platformVal), "platform_user_id": textutil.DerefString(puid),
			"status": status, "language": language, "title": textutil.DerefString(title),
			"user_message_count": umc, "model_message_count": mmc, "created_at": createdAt,
		})
	}
	// `total` is counted by its own query, so a short read here renders the page
	// as complete while the pager offers rows that never arrive.
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{"data": sessions, "total": total, "page": page, "page_size": pageSize}, nil
}

// createSession — start a session explicitly (web widget / test bench).
func (a *App) createSession(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		Title    string `json:"title"`
		Language string `json:"language"`
		Test     bool   `json:"test"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	language := req.Language
	if language == "" {
		language = "km"
	}
	sid := newUUID()
	platformVal := any(nil)
	if !req.Test {
		platformVal = "web"
	}
	if _, err := a.DB.Exec(r.Context(),
		"INSERT INTO sessions (session_id, user_id, platform, language, status, title, is_test, created_at) "+
			"VALUES ($1,$2,$3::platform_type,$4,'active',$5,$6,NOW())",
		sid, user.UserID, platformVal, language, textutil.TruncateRunes(req.Title, 60), req.Test); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{
		"session_id": sid, "user_id": user.UserID, "platform": textutil.DerefString(ptrIfSet(platformVal)), "language": language,
		"status": "active", "title": req.Title, "user_message_count": 0, "model_message_count": 0,
	}, nil
}

func ptrIfSet(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// updateSession — rename or re-status a session (owner-checked).
func (a *App) updateSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		Title  *string `json:"title"`
		Status *string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxReply); err != nil {
		return nil, err
	}
	if req.Title != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET title = $1 WHERE session_id = $2", textutil.TruncateRunes(*req.Title, 200), sessionID)
	}
	if req.Status != nil {
		status := *req.Status
		switch status {
		case "active", "pending", "handoff", "resolved", "closed":
		default:
			return nil, ErrBadRequest("无效的状态")
		}
		extra := ""
		if status == "resolved" {
			extra = ", resolved_at=NOW()"
		}
		if status == "closed" {
			extra = ", closed_at=NOW()"
		}
		if status == "active" {
			extra = ", resolved_at=NULL, closed_at=NULL"
		}
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status=$1::session_status"+extra+" WHERE session_id=$2", status, sessionID)
		a.publishSessionEvent(r.Context(), user.UserID, sessionID)
	}
	return map[string]string{"message": "已更新"}, nil
}

// getSession — one session with owner check.
func (a *App) getSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	row := a.DB.QueryRow(r.Context(),
		"SELECT session_id, status::text, language, title, user_message_count, model_message_count, created_at "+
			"FROM sessions WHERE session_id = $1 AND user_id = $2", sessionID, user.UserID)
	var (
		sid, status, language string
		title                 *string
		umc, mmc              int32
		createdAt             time.Time
	)
	if err := row.Scan(&sid, &status, &language, &title, &umc, &mmc, &createdAt); err != nil {
		return nil, ErrNotFound("会话不存在")
	}
	return map[string]any{
		"session_id": sid, "status": status, "language": language, "title": textutil.DerefString(title),
		"user_message_count": umc, "model_message_count": mmc, "created_at": createdAt,
	}, nil
}

// listSessionMessages — messages for a session (owner-checked), enriched with
// feedback / RAG sources / metadata / outbound delivery state so the inbox UI
// can render 👍/👎 badges, citations and delivery ticks without extra calls.
//
// By default it returns the newest `limit` messages in chronological order.
// Pass ?after=<message_id> to fetch only messages newer than that cursor
// (oldest-first), which is what the realtime inbox uses to append deltas
// without re-downloading the whole transcript.
func (a *App) listSessionMessages(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	var sid string
	if err := a.DB.QueryRow(r.Context(), "SELECT session_id FROM sessions WHERE session_id = $1 AND user_id = $2", sessionID, user.UserID).Scan(&sid); err != nil {
		return nil, ErrNotFound("会话不存在")
	}
	limit := parseIntOr(r.URL.Query().Get("limit"), 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	after := parseIntOr(r.URL.Query().Get("after"), 0)
	before := parseIntOr(r.URL.Query().Get("before"), 0)

	cols := "cm.message_id, cm.role, cm.message_type, cm.content, cm.created_at, " +
		"cm.tokens_used, cm.model_name, cm.used_mock, cm.feedback_rating, cm.feedback_comment, cm.feedback_at, " +
		"cm.sources_json, cm.media_url, cm.metadata::text, " +
		"po.status, po.provider_status, po.sent_at, po.delivered_at, po.read_at, po.last_error, po.payload::text"
	query := "SELECT " + cols + " FROM chat_messages cm " +
		"LEFT JOIN LATERAL (SELECT status, provider_status, sent_at, delivered_at, read_at, last_error, payload " +
		"FROM platform_outbox ob WHERE ob.chat_message_id = cm.message_id ORDER BY ob.delivery_id DESC LIMIT 1) po ON TRUE " +
		"WHERE cm.session_id = $1"
	args := []any{sessionID}
	if after > 0 {
		query += " AND cm.message_id > $2 ORDER BY cm.message_id ASC LIMIT $3"
		args = append(args, after, limit)
	} else if before > 0 {
		// History paging: the page immediately older than the cursor.
		query += " AND cm.message_id < $2 ORDER BY cm.message_id DESC LIMIT $3"
		args = append(args, before, limit)
	} else {
		query += " ORDER BY cm.message_id DESC LIMIT $2"
		args = append(args, limit)
	}

	rows, err := a.DB.Query(r.Context(), query, args...)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	msgs := make([]map[string]any, 0)
	for rows.Next() {
		var (
			mid                                    int64
			role, mtype, content                   string
			createdAt                              time.Time
			tokensUsed                             *int32
			modelName                              *string
			usedMock                               *bool
			feedbackRating                         *int16
			feedbackComment                        *string
			feedbackAt                             *time.Time
			sourcesJSON, mediaURL, metadataText    *string
			delStatus, delProviderStatus, delError *string
			sentAt, deliveredAt, readAt            *time.Time
			deliveryPayload                        *string
		)
		if err := rows.Scan(&mid, &role, &mtype, &content, &createdAt,
			&tokensUsed, &modelName, &usedMock, &feedbackRating, &feedbackComment, &feedbackAt,
			&sourcesJSON, &mediaURL, &metadataText,
			&delStatus, &delProviderStatus, &sentAt, &deliveredAt, &readAt, &delError, &deliveryPayload); err != nil {
			a.Logger.Warn("message row skipped", "message_id", mid, "error", err.Error())
			continue
		}
		msg := map[string]any{
			"message_id": mid, "session_id": sessionID, "role": role, "message_type": mtype,
			"content": content, "created_at": createdAt,
		}
		if tokensUsed != nil {
			msg["tokens_used"] = *tokensUsed
		}
		if modelName != nil {
			msg["model_name"] = *modelName
		}
		if usedMock != nil {
			msg["used_mock"] = *usedMock
		}
		if feedbackRating != nil {
			msg["feedback_rating"] = int(*feedbackRating)
			msg["feedback_comment"] = textutil.DerefString(feedbackComment)
			msg["feedback_at"] = feedbackAt
		}
		if sourcesJSON != nil {
			msg["sources_json"] = *sourcesJSON
		}
		if mediaURL != nil {
			msg["media_url"] = *mediaURL
		}
		if metadataText != nil {
			msg["metadata"] = *metadataText
		}
		if delStatus != nil {
			delivery := map[string]any{"status": *delStatus}
			if delProviderStatus != nil {
				delivery["provider_status"] = *delProviderStatus
			}
			delivery["sent_at"] = sentAt
			delivery["delivered_at"] = deliveredAt
			delivery["read_at"] = readAt
			delivery["last_error"] = textutil.DerefString(delError)
			if deliveryPayload != nil {
				var payload any
				if json.Unmarshal([]byte(*deliveryPayload), &payload) == nil && payload != nil {
					delivery["payload"] = payload
				}
			}
			msg["delivery"] = delivery
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		// Rows that fail to scan are already logged one by one; an iteration
		// error is different — the page ends early and the UI would show a
		// conversation that just stops.
		return nil, ErrInternal("查询失败")
	}
	// Initial + history pages come back newest-first; flip to chronological.
	if after <= 0 {
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}
	return msgs, nil
}

// deleteSession — permanently remove a session (owner-checked).
//
// This cascades to every child row (messages, handoff requests, delivery
// queue entries, notes), so it is destructive and irreversible. Ordinary
// agents must use archiveSession instead; only tenant admins may hard-delete.
func (a *App) deleteSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	// Owner, not role — the same fix as addTeamAgent: a self-service or SSO
	// merchant carries role "user". The DELETE below is already scoped to the
	// caller's own user_id, and membership in another tenant's team is refused.
	if !a.isTenantOwner(r.Context(), user) {
		return nil, ErrForbidden("只有租户所有者可以永久删除会话，请使用归档")
	}
	if r.URL.Query().Get("confirm") != "1" {
		return nil, ErrBadRequest("缺少确认参数")
	}
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM sessions WHERE session_id = $1 AND user_id = $2", sessionID, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("会话不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// parseIntOr parses an int query value with a fallback.
func parseIntOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return fallback
}
