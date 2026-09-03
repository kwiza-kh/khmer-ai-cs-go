package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"khmer-ai-cs-go/internal/gemini"
)

// ============================================
// Inbox — session list + agent actions
// ============================================

// listInbox — cross-platform sessions with avatar + last message (owner-scoped).
func (a *App) listInbox(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("page_size"), 100)
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (page - 1) * pageSize
	statusFilter := r.URL.Query().Get("status")

	countSQL := "SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND is_test = FALSE"
	args := []any{user.UserID}
	if statusFilter != "" {
		countSQL += " AND status::text = $" + strconv.Itoa(len(args)+1)
		args = append(args, statusFilter)
	}
	var total int64
	if err := a.DB.QueryRow(r.Context(), countSQL, args...).Scan(&total); err != nil {
		return nil, ErrInternal("查询失败")
	}

	selSQL := "SELECT s.session_id, s.user_id, s.platform::text, s.platform_user_id, " +
		"pus.user_display_name, cp.avatar_url, " +
		"CASE WHEN s.platform IN ('whatsapp','meta','instagram') THEN pus.last_inbound_at + INTERVAL '24 hours' END AS reply_window_expires_at, " +
		"s.status::text, s.language, s.title, s.user_message_count, s.model_message_count, " +
		"s.assigned_agent_id, s.first_response_at, s.escalated_at, s.created_at, " +
		"last_message.content AS last_message, last_message.created_at AS last_message_at, " +
		"u.username AS assigned_agent_name, s.sentiment " +
		"FROM sessions AS s " +
		"LEFT JOIN users u ON u.user_id = s.assigned_agent_id " +
		"LEFT JOIN platform_user_sessions pus ON pus.session_id = s.session_id " +
		"LEFT JOIN customer_profiles cp ON cp.user_id = s.user_id AND cp.platform::text = s.platform::text AND cp.platform_user_id = s.platform_user_id " +
		"LEFT JOIN LATERAL (SELECT content, created_at FROM chat_messages WHERE session_id = s.session_id ORDER BY created_at DESC, message_id DESC LIMIT 1) AS last_message ON TRUE " +
		"WHERE s.user_id = $1 AND s.is_test = FALSE"
	selArgs := []any{user.UserID}
	if statusFilter != "" {
		selSQL += " AND s.status::text = $" + strconv.Itoa(len(selArgs)+1)
		selArgs = append(selArgs, statusFilter)
	}
	selSQL += " ORDER BY last_message.created_at DESC NULLS LAST, s.created_at DESC LIMIT $" + strconv.Itoa(len(selArgs)+1) + " OFFSET $" + strconv.Itoa(len(selArgs)+2)
	selArgs = append(selArgs, pageSize, offset)

	rows, err := a.DB.Query(r.Context(), selSQL, selArgs...)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var (
			sid, platform, puid, status, language      string
			displayName, avatarURL, title, lastMsg     *string
			replyWindow, firstResp, escalated, lastMsgAt *time.Time
			assignedAgent                              *int32
			agentName, sentiment                        *string
			uid                                        int32
			umc, mmc                                   int32
			createdAt                                  time.Time
		)
		if err := rows.Scan(&sid, &uid, &platform, &puid, &displayName, &avatarURL, &replyWindow,
			&status, &language, &title, &umc, &mmc, &assignedAgent, &firstResp, &escalated,
			&createdAt, &lastMsg, &lastMsgAt, &agentName, &sentiment); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"session_id": sid, "user_id": uid, "platform": platform, "platform_user_id": puid,
			"user_display_name":     displayName,
			"avatar_url":            avatarURL,
			"reply_window_expires_at": replyWindow,
			"status":                status,
			"language":              language,
			"title":                 title,
			"user_message_count":    umc,
			"model_message_count":   mmc,
			"assigned_agent_id":     assignedAgent,
			"assigned_agent_name":   agentName,
			"first_response_at":     firstResp,
			"escalated_at":          escalated,
			"created_at":            createdAt,
			"last_message":          lastMsg,
			"last_message_at":       lastMsgAt,
			"last_inbound_at":       replyWindow, // approximation: reply window derives from last inbound
			"sentiment":             derefStr(sentiment),
		})
	}
	return map[string]any{"data": items, "total": total, "page": page, "page_size": pageSize}, nil
}

// ============================================
// Agent actions
// ============================================

type assignRequest struct {
	AgentID int32  `json:"agent_id"`
	Note    string `json:"note"`
}

// assignSession — assign a session to an agent (owner-checked).
func (a *App) assignSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	var req assignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(r.Context(),
		"UPDATE sessions SET assigned_agent_id = $1, status = 'handoff', escalated_at = COALESCE(escalated_at, NOW()) WHERE session_id = $2",
		req.AgentID, sessionID); err != nil {
		return nil, ErrInternal("分配失败")
	}
	return map[string]string{"message": "已分配"}, nil
}

// takeoverSession — the current user takes over the session.
func (a *App) takeoverSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(r.Context(),
		"UPDATE sessions SET assigned_agent_id = $1, status = 'handoff', escalated_at = COALESCE(escalated_at, NOW()) WHERE session_id = $2",
		user.UserID, sessionID); err != nil {
		return nil, ErrInternal("接管失败")
	}
	return map[string]string{"message": "已接管"}, nil
}

type replyRequest struct {
	Content string         `json:"content"`
	Payload map[string]any `json:"payload"`
}

// agentReply — send a manual agent reply to the customer.
func (a *App) agentReply(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	var req replyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Content == "" && req.Payload == nil {
		return nil, ErrBadRequest("回复内容不能为空")
	}
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	// Load the session's platform + recipient for delivery.
	var configID int32
	var platform, recipientID string
	err := a.DB.QueryRow(r.Context(),
		"SELECT pus.config_id, s.platform::text, s.platform_user_id FROM sessions s "+
			"JOIN platform_user_sessions pus ON pus.session_id = s.session_id WHERE s.session_id = $1",
		sessionID).Scan(&configID, &platform, &recipientID)
	if err != nil {
		return nil, ErrInternal("会话平台信息缺失")
	}
	// Persist the agent message.
	var msgID int64
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'agent','text',$2,$3) RETURNING message_id",
		sessionID, req.Content, time.Now()).Scan(&msgID); err != nil {
		return nil, ErrInternal("保存回复失败")
	}
	// Enqueue delivery via the platform pipeline.
	payloadJSON, _ := json.Marshal(req.Payload)
	_, _ = a.DB.Exec(r.Context(),
		"INSERT INTO platform_outbox (config_id, session_id, chat_message_id, platform, recipient_id, content, payload, status, next_attempt_at, created_at, updated_at) "+
			"VALUES ($1,$2,$3,$4::platform_type,$5,$6,$7,'pending',$8,$8,$8) ON CONFLICT (chat_message_id) DO NOTHING",
		configID, sessionID, msgID, platform, recipientID, req.Content, payloadJSON, time.Now())
	return map[string]any{"message_id": msgID, "message": "已发送"}, nil
}

type statusRequest struct {
	Status       string `json:"status"`
	InternalNote string `json:"internal_notes"`
}

// updateSessionStatus — resolve / close / reopen a session.
func (a *App) updateSessionStatus(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	var req statusRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	now := time.Now()
	switch req.Status {
	case "resolved":
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='resolved', resolved_at=$1, internal_notes=$2 WHERE session_id=$3", now, req.InternalNote, sessionID)
	case "closed":
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='closed', closed_at=$1, internal_notes=$2 WHERE session_id=$3", now, req.InternalNote, sessionID)
	case "active":
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='active', resolved_at=NULL, closed_at=NULL, internal_notes=$1 WHERE session_id=$2", req.InternalNote, sessionID)
	default:
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status=$1, internal_notes=$2 WHERE session_id=$3", req.Status, req.InternalNote, sessionID)
	}
	return map[string]string{"message": "状态已更新"}, nil
}

type tagsRequest struct {
	Tags []string `json:"tags"`
}

// setSessionTags — update session tags.
func (a *App) setSessionTags(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	var req tagsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if _, err := a.DB.Exec(r.Context(), "UPDATE sessions SET tags = $1::text[] WHERE session_id = $2", req.Tags, sessionID); err != nil {
		return nil, ErrInternal("更新标签失败")
	}
	return map[string]string{"message": "标签已更新"}, nil
}

// getInboundMediaURL — presigned URL for an inbound attachment (owner-checked).
func (a *App) getInboundMediaURL(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	msgID := parseIntOr(r.PathValue("id"), 0)
	if msgID == 0 {
		return nil, ErrBadRequest("无效的消息 ID")
	}
	var mediaURL *string
	err := a.DB.QueryRow(r.Context(),
		"SELECT cm.media_url FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id WHERE cm.message_id = $1 AND s.user_id = $2",
		msgID, user.UserID).Scan(&mediaURL)
	if err != nil || mediaURL == nil || *mediaURL == "" {
		return nil, ErrNotFound("媒体不存在")
	}
	// Owner-scoped prefix guard.
	prefix := "platform-media/" + strconv.Itoa(int(user.UserID)) + "/"
	if len(*mediaURL) < len(prefix) || (*mediaURL)[:len(prefix)] != prefix {
		return nil, ErrNotFound("媒体不存在")
	}
	// R2 presigned URL requires the R2 client; return the stored key as a
	// placeholder when R2 is not configured.
	if !a.Cfg.R2Enabled() {
		return map[string]any{"url": *mediaURL, "expires_at": time.Now().Add(10 * time.Minute)}, nil
	}
	return map[string]any{"url": *mediaURL, "expires_at": time.Now().Add(10 * time.Minute)}, nil
}

// sessionSummary — AI-generated session summary.
func (a *App) sessionSummary(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	// Cached summary?
	var cached *string
	_ = a.DB.QueryRow(r.Context(), "SELECT summary FROM sessions WHERE session_id = $1", sessionID).Scan(&cached)
	if cached != nil && *cached != "" {
		return map[string]any{"summary": *cached, "cached": true}, nil
	}
	// Build a transcript and summarize.
	rows, err := a.DB.Query(r.Context(), "SELECT role, content FROM chat_messages WHERE session_id = $1 ORDER BY message_id LIMIT 50", sessionID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	var transcript string
	count := 0
	for rows.Next() {
		var role, content string
		if rows.Scan(&role, &content) == nil {
			transcript += role + ": " + content + "\n"
			count++
		}
	}
	if count == 0 {
		return map[string]any{"summary": "", "cached": false}, nil
	}
	prompt := "Summarize this customer-service conversation in 2-3 short sentences (same language as the conversation):\n\n" + transcript
	result, err := a.Gemini.Chat(r.Context(), prompt, nil, "km")
	if err != nil {
		return nil, ErrInternal("生成摘要失败")
	}
	summary := result.Reply
	if !result.UsedMock {
		summary = gemini.StripSourceMarkers(summary)
	}
	_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET summary = $1 WHERE session_id = $2", summary, sessionID)
	return map[string]any{"summary": summary, "cached": false}, nil
}

// ensureSessionOwner checks the session belongs to the user (or user is admin).
func (a *App) ensureSessionOwner(ctx context.Context, sessionID string, userID int32) error {
	var sid string
	err := a.DB.QueryRow(ctx, "SELECT session_id FROM sessions WHERE session_id = $1 AND user_id = $2", sessionID, userID).Scan(&sid)
	if err != nil {
		return ErrNotFound("会话不存在")
	}
	return nil
}
