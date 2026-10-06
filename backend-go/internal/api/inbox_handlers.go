package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/textutil"
)

// ============================================
// Inbox — session list + agent actions
// ============================================

// listInbox — cross-platform sessions with avatar + last message (owner-scoped).
func (a *App) listInbox(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermInboxView); err != nil {
		return nil, err
	}
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("page_size"), 100)
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (page - 1) * pageSize
	statusFilter := r.URL.Query().Get("status")
	// "" / "active" → not archived (the default view); "archived" → only the
	// archive. Archiving hides a conversation without destroying anything.
	archiveFilter := r.URL.Query().Get("archived")
	archivedOnly := archiveFilter == "1" || archiveFilter == "true" || archiveFilter == "archived"

	// Free-text search across who the conversation is with (platform user id,
	// customer display name), who handles it (agent username), its title and
	// its messages — applied identically to the count and the page query so
	// pagination stays honest. This is what the top-bar search box feeds:
	// without it a username search only matched whatever happened to be in
	// the already-loaded first page.
	searchQ := strings.TrimSpace(r.URL.Query().Get("q"))

	// Both statements below are single literals with FIXED placeholder numbers,
	// and every optional filter is spelled `$N IS NULL OR …` instead of being
	// appended conditionally. The aliased `s` is shared by both on purpose: one
	// predicate, two arg lists.
	//
	//   * sqlcheck can only PREPARE a statement that exists verbatim in the
	//     source. The previous version rewrote a "$Q" sentinel to a real number
	//     at runtime (there is no $Q in SQL), so this predicate — the one query
	//     in this file spanning four tables (sessions, platform_user_sessions,
	//     users, chat_messages) with five ILIKEs — was invisible to the check
	//     and to every reader. A column that does not exist would ship.
	//   * One statement for every filter combination cannot drift between the
	//     count query and the page query, which is what keeps pagination honest.
	//
	// PostgreSQL lets one placeholder be referenced any number of times, so the
	// search term is bound once for all five ILIKEs (the count query passes four
	// args, the page query the same four plus limit/offset). The cost is that a
	// NULL-guarded `status` cannot drive the planner to a status index; the
	// `s.user_id` predicate carries the selectivity either way.
	const countSQL = "SELECT COUNT(*) FROM sessions AS s WHERE s.user_id = $1 AND s.is_test = FALSE" +
		" AND ($2::session_status IS NULL OR s.status = $2)" +
		" AND ((s.archived_at IS NOT NULL) = $3::boolean)" +
		" AND ($4::text IS NULL OR s.title ILIKE $4 OR s.platform_user_id ILIKE $4" +
		" OR EXISTS (SELECT 1 FROM platform_user_sessions pus_s WHERE pus_s.session_id = s.session_id AND pus_s.user_display_name ILIKE $4)" +
		" OR EXISTS (SELECT 1 FROM users u_s WHERE u_s.user_id = s.assigned_agent_id AND u_s.username ILIKE $4)" +
		" OR EXISTS (SELECT 1 FROM chat_messages cm_s WHERE cm_s.session_id = s.session_id AND cm_s.content ILIKE $4))"

	const selSQL = "SELECT s.session_id, s.user_id, s.platform::text, s.platform_user_id, " +
		"pus.user_display_name, cp.avatar_url, " +
		"CASE WHEN s.platform IN ('whatsapp','meta','instagram') THEN pus.last_inbound_at + INTERVAL '24 hours' END AS reply_window_expires_at, " +
		"s.status::text, s.language, s.title, s.user_message_count, s.model_message_count, " +
		"s.assigned_agent_id, s.first_response_at, s.escalated_at, s.created_at, s.archived_at, " +
		"last_message.content AS last_message, last_message.created_at AS last_message_at, " +
		"u.username AS assigned_agent_name, s.sentiment, s.tags, s.intent " +
		"FROM sessions AS s " +
		"LEFT JOIN users u ON u.user_id = s.assigned_agent_id " +
		"LEFT JOIN platform_user_sessions pus ON pus.session_id = s.session_id " +
		"LEFT JOIN customer_profiles cp ON cp.user_id = s.user_id AND cp.platform::text = s.platform::text AND cp.platform_user_id = s.platform_user_id " +
		"LEFT JOIN LATERAL (SELECT content, created_at FROM chat_messages WHERE session_id = s.session_id ORDER BY created_at DESC, message_id DESC LIMIT 1) AS last_message ON TRUE " +
		"WHERE s.user_id = $1 AND s.is_test = FALSE" +
		" AND ($2::session_status IS NULL OR s.status = $2)" +
		" AND ((s.archived_at IS NOT NULL) = $3::boolean)" +
		" AND ($4::text IS NULL OR s.title ILIKE $4 OR s.platform_user_id ILIKE $4" +
		" OR EXISTS (SELECT 1 FROM platform_user_sessions pus_s WHERE pus_s.session_id = s.session_id AND pus_s.user_display_name ILIKE $4)" +
		" OR EXISTS (SELECT 1 FROM users u_s WHERE u_s.user_id = s.assigned_agent_id AND u_s.username ILIKE $4)" +
		" OR EXISTS (SELECT 1 FROM chat_messages cm_s WHERE cm_s.session_id = s.session_id AND cm_s.content ILIKE $4))" +
		" ORDER BY last_message.created_at DESC NULLS LAST, s.created_at DESC LIMIT $5 OFFSET $6"

	// nil, not "": an absent filter must be NULL so the `$N IS NULL` guard
	// short-circuits. An empty session_status would fail the enum cast.
	var statusArg, searchArg any
	if statusFilter != "" {
		statusArg = statusFilter
	}
	if searchQ != "" {
		searchArg = "%" + searchQ + "%"
	}
	// The tenant the caller works in: their own for an owner, the owner's for a
	// seat — so an invited agent sees the conversations they came to handle.
	scopedArgs := []any{user.Tenant(), statusArg, archivedOnly, searchArg}

	var total int64
	if err := a.DB.QueryRow(r.Context(), countSQL, scopedArgs...).Scan(&total); err != nil {
		return nil, ErrInternal("查询失败")
	}

	rows, err := a.DB.Query(r.Context(), selSQL,
		append(append(make([]any, 0, len(scopedArgs)+2), scopedArgs...), pageSize, offset)...)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	skipped := 0
	for rows.Next() {
		var (
			sid, status, language                        string
			platform, puid                               *string // NULL for every web/widget session
			displayName, avatarURL, title, lastMsg       *string
			replyWindow, firstResp, escalated, lastMsgAt *time.Time
			archivedAt                                   *time.Time
			assignedAgent                                *int32
			agentName, sentiment, intent                 *string
			tagsPtr                                      *[]string
			uid                                          int32
			umc, mmc                                     int32
			createdAt                                    time.Time
		)
		// Nullable columns MUST scan into pointers. platform_user_id is NULL
		// for web/widget sessions, and scanning NULL into a plain string made
		// every one of those rows fail — the error was swallowed by `continue`,
		// so those conversations silently vanished from the inbox.
		if err := rows.Scan(&sid, &uid, &platform, &puid, &displayName, &avatarURL, &replyWindow,
			&status, &language, &title, &umc, &mmc, &assignedAgent, &firstResp, &escalated,
			&createdAt, &archivedAt, &lastMsg, &lastMsgAt, &agentName, &sentiment, &tagsPtr, &intent); err != nil {
			skipped++
			a.Logger.Warn("inbox row skipped", "session_id", sid, "error", err.Error())
			continue
		}
		tags := []string{}
		if tagsPtr != nil {
			tags = *tagsPtr
		}
		items = append(items, map[string]any{
			"session_id": sid, "user_id": uid, "platform": textutil.DerefString(platform), "platform_user_id": textutil.DerefString(puid),
			"user_display_name":       displayName,
			"avatar_url":              avatarURL,
			"reply_window_expires_at": replyWindow,
			"status":                  status,
			"language":                language,
			"title":                   title,
			"user_message_count":      umc,
			"model_message_count":     mmc,
			"assigned_agent_id":       assignedAgent,
			"assigned_agent_name":     agentName,
			"first_response_at":       firstResp,
			"escalated_at":            escalated,
			"created_at":              createdAt,
			"archived_at":             archivedAt,
			"last_message":            lastMsg,
			"last_message_at":         lastMsgAt,
			"last_inbound_at":         replyWindow, // approximation: reply window derives from last inbound
			"sentiment":               textutil.DerefString(sentiment),
			"tags":                    tags,
			"intent":                  textutil.DerefString(intent),
		})
	}
	// A short read must not pass for a complete page: `total` above was counted
	// separately and the client renders "showing N of total", so silently
	// dropping the tail makes conversations look deleted.
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	// Surface how many rows could not be decoded: a non-zero value means the
	// client is seeing fewer conversations than `total`, which used to happen
	// silently (NULL platform_user_id on web sessions) and looked like data
	// vanishing.
	return map[string]any{"data": items, "total": total, "page": page, "page_size": pageSize, "skipped": skipped}, nil
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
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxAssign); err != nil {
		return nil, err
	}
	// The target agent must belong to the caller's tenant — otherwise any
	// tenant could park its sessions on another tenant's user.
	ok, err := a.userInCallerTenant(r.Context(), user, req.AgentID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	if !ok {
		return nil, ErrForbidden("无权指派给该客服")
	}
	if _, err := a.DB.Exec(r.Context(),
		"UPDATE sessions SET assigned_agent_id = $1, status = 'handoff', escalated_at = COALESCE(escalated_at, NOW()) WHERE session_id = $2",
		req.AgentID, sessionID); err != nil {
		return nil, ErrInternal("分配失败")
	}
	a.markHandoffAssigned(r.Context(), sessionID, req.AgentID)
	a.publishSessionEvent(r.Context(), user.UserID, sessionID)
	return map[string]string{"message": "已分配"}, nil
}

// takeoverSession — the current user takes over the session.
func (a *App) takeoverSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxTakeover); err != nil {
		return nil, err
	}
	if _, err := a.DB.Exec(r.Context(),
		"UPDATE sessions SET assigned_agent_id = $1, status = 'handoff', escalated_at = COALESCE(escalated_at, NOW()) WHERE session_id = $2",
		user.UserID, sessionID); err != nil {
		return nil, ErrInternal("接管失败")
	}
	a.markHandoffAssigned(r.Context(), sessionID, user.UserID)
	a.publishSessionEvent(r.Context(), user.UserID, sessionID)
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
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxReply); err != nil {
		return nil, err
	}
	// Load the session's platform + recipient for delivery. Web/NULL-platform
	// sessions (the website widget) have no provider mapping: the reply is
	// still persisted and the visitor sees it by polling the transcript.
	var configID int32
	var platform, recipientID string
	mapped := true
	err := a.DB.QueryRow(r.Context(),
		"SELECT pus.config_id, s.platform::text, s.platform_user_id FROM sessions s "+
			"JOIN platform_user_sessions pus ON pus.session_id = s.session_id WHERE s.session_id = $1",
		sessionID).Scan(&configID, &platform, &recipientID)
	if err != nil {
		mapped = false
	}
	// Persist the agent message.
	var msgID int64
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'agent','text',$2,$3) RETURNING message_id",
		sessionID, req.Content, time.Now()).Scan(&msgID); err != nil {
		return nil, ErrInternal("保存回复失败")
	}
	// Enqueue delivery via the platform pipeline (platform sessions only).
	if mapped {
		payloadJSON, _ := json.Marshal(req.Payload)
		if _, err := a.DB.Exec(r.Context(),
			"INSERT INTO platform_outbox (config_id, session_id, chat_message_id, platform, recipient_id, content, payload, status, next_attempt_at, created_at, updated_at) "+
				"VALUES ($1,$2,$3,$4::platform_type,$5,$6,$7,'pending',$8,$8,$8) ON CONFLICT (chat_message_id) DO NOTHING",
			configID, sessionID, msgID, platform, recipientID, req.Content, payloadJSON, time.Now()); err != nil {
			// The agent message is persisted, but without an outbox row it
			// will never reach the customer — surface the failure instead of
			// reporting a fake "sent".
			return nil, ErrInternal("投递入队失败，请重试")
		}
		// Wake the outbound worker (otherwise the reply waits for the next
		// poll tick).
		if a.Pipe != nil {
			a.Pipe.SignalOutbound()
		}
	}
	// Push the message to the inbox in real time.
	realtime.Publish(r.Context(), a.Redis, realtime.Event{
		Type:      realtime.EventMessage,
		UserID:    user.UserID,
		SessionID: sessionID,
		MessageID: msgID,
		Role:      "agent",
	})
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
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxReply); err != nil {
		return nil, err
	}
	now := time.Now()
	switch req.Status {
	case "resolved":
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='resolved', resolved_at=$1, internal_notes=$2 WHERE session_id=$3", now, req.InternalNote, sessionID)
		a.markHandoffResolved(r.Context(), sessionID, req.InternalNote)
	case "closed":
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='closed', closed_at=$1, internal_notes=$2 WHERE session_id=$3", now, req.InternalNote, sessionID)
		a.markHandoffResolved(r.Context(), sessionID, req.InternalNote)
	case "active", "pending", "handoff":
		_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status=$1, internal_notes=$2 WHERE session_id=$3", req.Status, req.InternalNote, sessionID)
	default:
		return nil, ErrBadRequest("未知的状态值")
	}
	a.publishSessionEvent(r.Context(), user.UserID, sessionID)
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
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxReply); err != nil {
		return nil, err
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if _, err := a.DB.Exec(r.Context(), "UPDATE sessions SET tags = $1::text[] WHERE session_id = $2", req.Tags, sessionID); err != nil {
		return nil, ErrInternal("更新标签失败")
	}
	a.publishSessionEvent(r.Context(), user.UserID, sessionID)
	return map[string]string{"message": "标签已更新"}, nil
}

// getInboundMediaURL — presigned URL for an inbound attachment (owner-checked).
func (a *App) getInboundMediaURL(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermInboxView); err != nil {
		return nil, err
	}
	msgID := parseIntOr(r.PathValue("id"), 0)
	if msgID == 0 {
		return nil, ErrBadRequest("无效的消息 ID")
	}
	// The media key is namespaced by the **tenant** (the uploader is whoever the
	// session belongs to), so both the query and the prefix guard use Tenant().
	tid := user.Tenant()
	var mediaURL *string
	err := a.DB.QueryRow(r.Context(),
		"SELECT cm.media_url FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id WHERE cm.message_id = $1 AND s.user_id = $2",
		msgID, tid).Scan(&mediaURL)
	if err != nil || mediaURL == nil || *mediaURL == "" {
		return nil, ErrNotFound("媒体不存在")
	}
	// Owner-scoped prefix guard.
	prefix := "platform-media/" + strconv.Itoa(int(tid)) + "/"
	if len(*mediaURL) < len(prefix) || (*mediaURL)[:len(prefix)] != prefix {
		return nil, ErrNotFound("媒体不存在")
	}
	// R2 configured → short-lived presigned download URL (what the inbox
	// player needs). Otherwise fall back to the raw key (legacy behaviour).
	if a.Media.Enabled() {
		url, err := a.Media.PresignedGET(*mediaURL, 10*time.Minute)
		if err == nil {
			return map[string]any{"url": url, "expires_at": time.Now().Add(10 * time.Minute)}, nil
		}
		a.Logger.Warn("r2 presign failed", "error", err.Error())
	}
	return map[string]any{"url": *mediaURL, "expires_at": time.Now().Add(10 * time.Minute)}, nil
}

// sessionSummary — AI-generated session summary (owner-scoped).
//
// Language resolution: ?language= wins, then the owner's stored preference,
// then auto-detect from the transcript. A cached summary is only reused when
// it matches the resolved language AND no messages arrived after it was
// generated; ?refresh=1 forces regeneration.
func (a *App) sessionSummary(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxView); err != nil {
		return nil, err
	}
	force := r.URL.Query().Get("refresh") == "1"

	// Language: explicit param > owner preference > auto (detected below).
	language := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("language")))
	switch language {
	case "", "auto":
		language = a.savedLanguage(r.Context(), user.UserID)
	case "km", "en", "zh":
	default:
		return nil, ErrBadRequest("不支持的语言")
	}

	// Transcript: the newest 50 messages in chronological order (the old
	// ASC LIMIT 50 kept summarizing the beginning of long conversations).
	type turn struct{ role, content string }
	turns := make([]turn, 0, 50)
	rows, err := a.DB.Query(r.Context(),
		"SELECT role, content FROM chat_messages WHERE session_id = $1 ORDER BY message_id DESC LIMIT 50", sessionID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	for rows.Next() {
		var t turn
		if rows.Scan(&t.role, &t.content) == nil {
			turns = append(turns, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		// Summarising a truncated transcript would state conclusions the
		// conversation never supported, so fail instead of guessing.
		a.Logger.Warn("summary transcript scan failed", "session_id", sessionID, "error", err.Error())
		return nil, ErrInternal("查询失败")
	}
	if len(turns) == 0 {
		return map[string]any{"summary": "", "cached": false, "language": language}, nil
	}
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	var transcript strings.Builder
	for _, t := range turns {
		transcript.WriteString(t.role)
		transcript.WriteString(": ")
		transcript.WriteString(t.content)
		transcript.WriteString("\n")
	}

	// Auto language: detect from the transcript (the Gemini request builder
	// would otherwise pin the turn to the Khmer-primary default).
	if language == "" {
		if det := gemini.DetectLanguage(transcript.String()); det != "" {
			language = det
		} else {
			language = "km"
		}
	}

	// Cached summary — valid only when the language matches and the
	// transcript has not grown since generation.
	if !force {
		var (
			cached     *string
			cachedAt   *time.Time
			cachedLang *string
		)
		_ = a.DB.QueryRow(r.Context(),
			"SELECT summary, summary_at, summary_language FROM sessions WHERE session_id = $1", sessionID).
			Scan(&cached, &cachedAt, &cachedLang)
		if cached != nil && *cached != "" && cachedAt != nil && textutil.DerefString(cachedLang) == language {
			var newer int
			_ = a.DB.QueryRow(r.Context(),
				"SELECT COUNT(*) FROM chat_messages WHERE session_id = $1 AND created_at > $2", sessionID, *cachedAt).Scan(&newer)
			if newer == 0 {
				return map[string]any{"summary": *cached, "cached": true, "language": language}, nil
			}
		}
	}

	result, err := a.Gemini.Chat(r.Context(), summaryPrompt(transcript.String(), language), nil, language)
	if err != nil {
		return nil, ErrInternal("生成摘要失败")
	}
	summary := result.Reply
	if !result.UsedMock {
		summary = gemini.StripSourceMarkers(summary)
	}
	_, _ = a.DB.Exec(r.Context(),
		"UPDATE sessions SET summary = $1, summary_at = $2, summary_language = $3 WHERE session_id = $4",
		summary, time.Now(), language, sessionID)
	return map[string]any{"summary": summary, "cached": false, "language": language}, nil
}

// summaryPrompt asks for a short summary in the requested language.
func summaryPrompt(transcript, language string) string {
	var target string
	switch language {
	case "km":
		target = "Khmer"
	case "en":
		target = "English"
	case "zh":
		target = "Chinese"
	}
	if target != "" {
		return "Summarize this customer-service conversation in 2-3 short sentences, written in " + target + ":\n\n" + transcript
	}
	return "Summarize this customer-service conversation in 2-3 short sentences (same language as the conversation):\n\n" + transcript
}

// ensureSessionAccess is the inbox's authorization funnel: it requires the
// member permission the action needs, then resolves the session inside the
// caller's effective tenant. An agent works in the owner's tenant, so a session
// that is not theirs stays a 404 (not a 403) — no existence leak — while an
// owner keeps exactly the behaviour they had before permissions existed.
func (a *App) ensureSessionAccess(ctx context.Context, sessionID string, user *CurrentUser, perm string) error {
	if err := requirePermission(user, perm); err != nil {
		return err
	}
	var sid string
	err := a.DB.QueryRow(ctx, "SELECT session_id FROM sessions WHERE session_id = $1 AND user_id = $2", sessionID, user.Tenant()).Scan(&sid)
	if err != nil {
		return ErrNotFound("会话不存在")
	}
	return nil
}

// publishSessionEvent nudges the inbox list for the tenant (status/assign/tag
// changes). Best-effort, like every realtime publish.
func (a *App) publishSessionEvent(ctx context.Context, userID int32, sessionID string) {
	realtime.Publish(ctx, a.Redis, realtime.Event{
		Type:      realtime.EventSession,
		UserID:    userID,
		SessionID: sessionID,
	})
}

// markHandoffAssigned flips the session's open handoff request to assigned
// when a human takes it over (so the queue reflects reality).
func (a *App) markHandoffAssigned(ctx context.Context, sessionID string, agentID int32) {
	_, _ = a.DB.Exec(ctx,
		"UPDATE human_handoff_requests SET status='assigned', assigned_agent_id=$1, assigned_at=NOW() "+
			"WHERE session_id=$2::uuid AND status='pending'", agentID, sessionID)
}

// markHandoffResolved closes the session's open handoff requests when the
// conversation is resolved/closed.
func (a *App) markHandoffResolved(ctx context.Context, sessionID, note string) {
	_, _ = a.DB.Exec(ctx,
		"UPDATE human_handoff_requests SET status='resolved', resolved_at=NOW(), resolution_note=COALESCE(NULLIF($2,''), resolution_note) "+
			"WHERE session_id=$1::uuid AND status IN ('pending','assigned')", sessionID, note)
}

// ============================================
// Archive / restore
// ============================================

// archiveSession hides a conversation from the default inbox list without
// deleting anything — messages, handoff records and billing history all stay
// intact, so the customer thread can be restored at any time.
func (a *App) archiveSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	return a.setSessionArchived(w, r, sessionID, true)
}

// unarchiveSession returns an archived conversation to the inbox.
func (a *App) unarchiveSession(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	return a.setSessionArchived(w, r, sessionID, false)
}

func (a *App) setSessionArchived(w http.ResponseWriter, r *http.Request, sessionID string, archived bool) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionAccess(r.Context(), sessionID, user, PermInboxReply); err != nil {
		return nil, err
	}
	var tag interface{ RowsAffected() int64 }
	var err error
	if archived {
		tag, err = a.DB.Exec(r.Context(),
			"UPDATE sessions SET archived_at = NOW() WHERE session_id = $1 AND user_id = $2", sessionID, user.UserID)
	} else {
		tag, err = a.DB.Exec(r.Context(),
			"UPDATE sessions SET archived_at = NULL WHERE session_id = $1 AND user_id = $2", sessionID, user.UserID)
	}
	if err != nil {
		return nil, ErrInternal("操作失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("会话不存在")
	}
	a.publishSessionEvent(r.Context(), user.UserID, sessionID)
	if archived {
		return map[string]string{"message": "已归档"}, nil
	}
	return map[string]string{"message": "已恢复"}, nil
}
