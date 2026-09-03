package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
)

// ============================================
// Chat (plain + SSE streaming)
// ============================================

type chatRequest struct {
	Message   string   `json:"message"`
	SessionID *string  `json:"session_id"`
	Language  *string  `json:"language"`
	Test      bool     `json:"test"`
	History   []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
}

// chatPlain — synchronous chat turn.
func (a *App) chatPlain(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, ErrBadRequest("请求格式错误")
	}
	language := "km"
	if req.Language != nil && *req.Language != "" {
		language = *req.Language
	} else if saved := a.savedLanguage(r.Context(), user.UserID); saved != "" {
		language = saved
	}

	// Ground in the knowledge base.
	var sessionID *string
	if req.SessionID != nil {
		sid := *req.SessionID
		sessionID = &sid
	}
	history := historyFromRequest(req)
	groundCtx := a.RAG.Ground(r.Context(), user.UserID, sessionID, req.Message, language, history, 0)
	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
	}
	result, err := a.Gemini.Chat(r.Context(), message, history, language)
	if err != nil {
		return nil, ErrInternal("生成回答失败")
	}
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}
	resp := map[string]any{
		"reply":       reply,
		"tokens":      result.PromptTokens + result.OutputTokens,
		"used_mock":   result.UsedMock,
	}
	if groundCtx.HasMatch {
		resp["sources"] = groundCtx.Sources
	}
	if sessionID != nil {
		resp["session_id"] = *sessionID
	}
	return resp, nil
}

// chatStream — SSE streaming chat turn.
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
	language := "km"
	if req.Language != nil && *req.Language != "" {
		language = *req.Language
	} else if saved := a.savedLanguage(r.Context(), user.UserID); saved != "" {
		language = saved
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

	history := historyFromRequest(req)
	var sessionID *string
	if req.SessionID != nil {
		sid := *req.SessionID
		sessionID = &sid
	}

	// Ground first (stream the retrieval sources before the reply).
	groundCtx := a.RAG.Ground(r.Context(), user.UserID, sessionID, req.Message, language, history, 0)
	message := req.Message
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(req.Message, &groundCtx)
	}

	sendEvent := func(event, data any) {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\n", event)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	if sessionID != nil {
		sendEvent("session", map[string]any{"session_id": *sessionID})
	}
	if groundCtx.HasMatch {
		sendEvent("sources", map[string]any{"sources": groundCtx.Sources})
	}

	// Stream tokens. The Gemini service has no token-level streaming in this
	// port, so we emit the full reply as one token chunk, then done.
	result, err := a.Gemini.Chat(r.Context(), message, history, language)
	if err != nil {
		sendEvent("error", map[string]any{"error": "生成回答失败"})
		return
	}
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}
	sendEvent("token", map[string]any{"token": reply})
	sendEvent("done", map[string]any{
		"tokens":    result.PromptTokens + result.OutputTokens,
		"used_mock": result.UsedMock,
	})
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
	var total int64
	if err := a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND is_test = FALSE", user.UserID).Scan(&total); err != nil {
		return nil, ErrInternal("查询失败")
	}
	rows, err := a.DB.Query(r.Context(),
		"SELECT session_id, user_id, platform::text, platform_user_id, status::text, language, title, "+
			"user_message_count, model_message_count, created_at FROM sessions "+
			"WHERE user_id = $1 AND is_test = FALSE ORDER BY created_at DESC LIMIT $2 OFFSET $3",
		user.UserID, pageSize, offset)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	sessions := make([]map[string]any, 0)
	for rows.Next() {
		var (
			sid, platform, puid, status, language, title string
			uid                                          int32
			umc, mmc                                     int32
			createdAt                                    time.Time
			platformN, puidN, titleN                     *string
		)
		if err := rows.Scan(&sid, &uid, &platformN, &puidN, &status, &language, &titleN, &umc, &mmc, &createdAt); err != nil {
			continue
		}
		platform, puid, title = derefStr(platformN), derefStr(puidN), derefStr(titleN)
		sessions = append(sessions, map[string]any{
			"session_id": sid, "user_id": uid, "platform": platform, "platform_user_id": puid,
			"status": status, "language": language, "title": title,
			"user_message_count": umc, "model_message_count": mmc, "created_at": createdAt,
		})
	}
	return map[string]any{"data": sessions, "total": total, "page": page, "page_size": pageSize}, nil
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
		"session_id": sid, "status": status, "language": language, "title": derefStr(title),
		"user_message_count": umc, "model_message_count": mmc, "created_at": createdAt,
	}, nil
}

// listSessionMessages — messages for a session (owner-checked).
func (a *App) listSessionMessages(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	// Owner check.
	var sid string
	if err := a.DB.QueryRow(r.Context(), "SELECT session_id FROM sessions WHERE session_id = $1 AND user_id = $2", sessionID, user.UserID).Scan(&sid); err != nil {
		return nil, ErrNotFound("会话不存在")
	}
	limit := parseIntOr(r.URL.Query().Get("limit"), 100)
	rows, err := a.DB.Query(r.Context(),
		"SELECT message_id, role, message_type, content, created_at FROM chat_messages WHERE session_id = $1 ORDER BY created_at ASC LIMIT $2",
		sessionID, limit)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	msgs := make([]map[string]any, 0)
	for rows.Next() {
		var (
			mid                int64
			role, mtype, content string
			createdAt          time.Time
		)
		if err := rows.Scan(&mid, &role, &mtype, &content, &createdAt); err != nil {
			continue
		}
		msgs = append(msgs, map[string]any{
			"message_id": mid, "role": role, "message_type": mtype, "content": content, "created_at": createdAt,
		})
	}
	return msgs, nil
}

// deleteSession — remove a session (owner-checked).
func (a *App) deleteSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
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

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
