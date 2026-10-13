package api

import (
	"encoding/json"
	"net/http"
	"time"
)

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
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
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
	// Escalating a conversation to a human is inbox work: a seat needs the
	// takeover grant, and the tenant it acts in is the owner's — never the
	// seat's own user id, which would file the request in a tenant nobody
	// works in.
	if err := requirePermission(user, PermInboxTakeover); err != nil {
		return nil, err
	}
	tid := user.Tenant()
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
	err := a.DB.QueryRow(r.Context(), "SELECT is_test FROM sessions WHERE session_id = $1 AND user_id = $2", req.SessionID, tid).Scan(&isTest)
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
			req.SessionID, tid, priority, reason)
		if err != nil {
			return nil, ErrInternal("创建失败")
		}
		a.notifyUser(r.Context(), tid, "handoff", "New human-handoff request", "manual: "+reason, req.SessionID)
	}
	// Mark the session as handoff.
	_, _ = a.DB.Exec(r.Context(), "UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at,NOW()) WHERE session_id = $1", req.SessionID)
	a.publishSessionEvent(r.Context(), tid, req.SessionID)

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
