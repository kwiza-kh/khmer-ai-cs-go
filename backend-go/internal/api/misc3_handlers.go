package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// ============================================
// Marketing campaigns
// ============================================

type campaignInput struct {
	Name             string     `json:"name"`
	Platform         *string    `json:"platform"`
	ConfigID         int32      `json:"config_id"`
	TemplateName     string     `json:"template_name"`
	TemplateLanguage *string    `json:"template_language"`
	BodyParams       []string   `json:"body_params"`
	RecipientFilter  *string    `json:"recipient_filter"`
	TagFilter        *string    `json:"tag_filter"`
	ScheduledAt      *time.Time `json:"scheduled_at"`
}

func (a *App) createCampaign(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req campaignInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.ConfigID <= 0 || req.TemplateName == "" {
		return nil, ErrBadRequest("config_id and template_name are required")
	}
	platform := "whatsapp"
	if req.Platform != nil && *req.Platform != "" {
		platform = *req.Platform
	}
	language := "km"
	if req.TemplateLanguage != nil && *req.TemplateLanguage != "" {
		language = *req.TemplateLanguage
	}
	filter := "all"
	if req.RecipientFilter != nil && *req.RecipientFilter != "" {
		filter = *req.RecipientFilter
	}
	var scheduledAt time.Time
	if req.ScheduledAt != nil {
		scheduledAt = *req.ScheduledAt
	} else {
		scheduledAt = time.Now()
	}
	bodyParams, _ := json.Marshal(orDefaultSlice(req.BodyParams))
	var campaignID int64
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO marketing_campaigns (user_id, name, platform, config_id, template_name, template_language, body_params, recipient_filter, tag_filter, scheduled_at, status) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,'scheduled') RETURNING campaign_id",
		user.UserID, req.Name, platform, req.ConfigID, req.TemplateName, language, string(bodyParams), filter, req.TagFilter, scheduledAt).Scan(&campaignID); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"campaign_id": campaignID, "message": "已创建"}, nil
}

func (a *App) listCampaigns(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT campaign_id, name, platform, config_id, template_name, template_language, body_params, recipient_filter, tag_filter, scheduled_at, status::text, sent_count, created_at FROM marketing_campaigns WHERE user_id = $1 ORDER BY created_at DESC LIMIT 100",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var campaignID int64
		var name, platform, templateName, templateLanguage, recipientFilter, status string
		var configID, sentCount int32
		var bodyParams []byte
		var tagFilter *string
		var scheduledAt, createdAt time.Time
		if err := rows.Scan(&campaignID, &name, &platform, &configID, &templateName, &templateLanguage, &bodyParams, &recipientFilter, &tagFilter, &scheduledAt, &status, &sentCount, &createdAt); err != nil {
			continue
		}
		var params any
		_ = json.Unmarshal(bodyParams, &params)
		out = append(out, map[string]any{
			"campaign_id": campaignID, "name": name, "platform": platform, "config_id": configID,
			"template_name": templateName, "template_language": templateLanguage, "body_params": params,
			"recipient_filter": recipientFilter, "tag_filter": tagFilter, "scheduled_at": scheduledAt,
			"status": status, "sent_count": sentCount, "created_at": createdAt,
		})
	}
	return out, nil
}

func (a *App) cancelCampaign(w http.ResponseWriter, r *http.Request, campaignID int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE marketing_campaigns SET status='cancelled' WHERE campaign_id=$1 AND user_id=$2 AND status='scheduled'",
		campaignID, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrBadRequest("campaign not scheduled or not found")
	}
	return map[string]string{"message": "已取消"}, nil
}

func orDefaultSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ============================================
// SLA breaches
// ============================================

func (a *App) listSLABreaches(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	openOnly := r.URL.Query().Get("open") == "true"
	rows, err := a.DB.Query(r.Context(),
		"SELECT b.breach_id, b.session_id, b.sla_id, b.breach_type, b.breached_at, b.resolved, b.resolved_at, s.title, s.platform::text "+
			"FROM sla_breaches b JOIN sessions s ON s.session_id = b.session_id "+
			"WHERE b.user_id = $1 AND ($2 = false OR b.resolved = false) ORDER BY b.breached_at DESC LIMIT 200",
		user.UserID, openOnly)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var breachID int64
		var sessionID string
		var slaID *int32
		var breachType string
		var breachedAt time.Time
		var resolved bool
		var resolvedAt *time.Time
		var title, platform *string
		if err := rows.Scan(&breachID, &sessionID, &slaID, &breachType, &breachedAt, &resolved, &resolvedAt, &title, &platform); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"breach_id": breachID, "session_id": sessionID, "sla_id": slaID, "breach_type": breachType,
			"breached_at": breachedAt, "resolved": resolved, "resolved_at": resolvedAt, "title": title, "platform": platform,
		})
	}
	return out, nil
}

// ============================================
// Handoff request creation
// ============================================

type createHandoffRequest struct {
	SessionID string  `json:"session_id"`
	Reason    string  `json:"reason"`
	Priority  *string `json:"priority"`
}

func (a *App) createHandoffRequest(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req createHandoffRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	priority := "normal"
	if req.Priority != nil {
		switch *req.Priority {
		case "", "normal":
			priority = "normal"
		case "high":
			priority = "high"
		default:
			return nil, ErrBadRequest("invalid handoff priority")
		}
	}
	reason := req.Reason
	if reason == "" {
		return nil, ErrBadRequest("reason is required")
	}
	var isTest bool
	err := a.DB.QueryRow(r.Context(), "SELECT is_test FROM sessions WHERE session_id = $1 AND user_id = $2", req.SessionID, user.UserID).Scan(&isTest)
	if err != nil {
		return nil, ErrNotFound("session not found")
	}
	if isTest {
		return nil, ErrBadRequest("test sessions cannot be transferred")
	}
	// Insert the handoff request (the partial unique index dedupes an already
	// open request for the same session).
	var openExists bool
	if err := a.DB.QueryRow(r.Context(),
		"SELECT EXISTS(SELECT 1 FROM human_handoff_requests WHERE session_id = $1 AND status IN ('pending','assigned'))",
		req.SessionID).Scan(&openExists); err != nil {
		return nil, ErrInternal("查询失败")
	}
	if !openExists {
		_, err = a.DB.Exec(r.Context(),
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) VALUES ($1,$2,'pending',$3,'manual',$4,NOW()) ON CONFLICT DO NOTHING",
			req.SessionID, user.UserID, priority, reason)
		if err != nil {
			return nil, ErrInternal("创建失败")
		}
		a.notifyUser(r.Context(), user.UserID, "handoff", "New human-handoff request", "manual: "+reason, req.SessionID)
	}
	// Mark the session as handoff.
	_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id = $1", req.SessionID)
	a.publishSessionEvent(r.Context(), user.UserID, req.SessionID)

	// Return the open request row (data + created flag → CreateHumanHandoffRequestResult).
	data := map[string]any{
		"session_id": req.SessionID, "status": "pending", "priority": priority,
		"trigger": "manual", "reason": reason, "created": !openExists,
	}
	if !openExists {
		var rid string
		var createdAt time.Time
		if err := a.DB.QueryRow(r.Context(),
			"SELECT request_id::text, created_at FROM human_handoff_requests WHERE session_id = $1 AND status IN ('pending','assigned') ORDER BY created_at DESC LIMIT 1",
			req.SessionID).Scan(&rid, &createdAt); err == nil {
			data["request_id"] = rid
			data["created_at"] = createdAt
		}
	}
	return map[string]any{"data": data, "created": !openExists}, nil
}
