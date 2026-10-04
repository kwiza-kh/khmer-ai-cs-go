package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/textutil"
)

// ============================================
// SLA policies
// ============================================

type slaRequest struct {
	Name              string `json:"name"`
	FirstResponseSecs int    `json:"first_response_secs"`
	ResolutionSecs    *int   `json:"resolution_secs"`
	BusinessHoursOnly bool   `json:"business_hours_only"`
	Priority          string `json:"priority"`
}

func (a *App) listSLA(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT sla_id, name, first_response_secs, resolution_secs, business_hours_only, priority::text, is_active "+
			"FROM sla_policies WHERE user_id = $1 ORDER BY sla_id", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, firstSecs int
		var name, priority string
		var resolutionSecs *int
		var bhOnly, isActive bool
		if err := rows.Scan(&id, &name, &firstSecs, &resolutionSecs, &bhOnly, &priority, &isActive); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"sla_id": id, "name": name, "first_response_secs": firstSecs, "resolution_secs": resolutionSecs,
			"business_hours_only": bhOnly, "priority": priority, "is_active": isActive,
		})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) upsertSLA(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req slaRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Priority == "" {
		req.Priority = "normal"
	}
	var id int
	// priority is a plain varchar column with a 'normal' default — there is no
	// sla_priority type in this schema. Casting to one made the statement fail
	// at PREPARE ("type sla_priority does not exist"), so every attempt to
	// create an SLA policy returned 创建失败.
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO sla_policies (user_id, name, first_response_secs, resolution_secs, business_hours_only, priority, is_active) "+
			"VALUES ($1,$2,$3,$4,$5,$6::varchar,true) RETURNING sla_id",
		user.UserID, req.Name, req.FirstResponseSecs, req.ResolutionSecs, req.BusinessHoursOnly, req.Priority).Scan(&id); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"sla_id": id, "message": "已保存"}, nil
}

func (a *App) deleteSLA(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM sla_policies WHERE sla_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// ============================================
// Routing rules
// ============================================

type routingRequest struct {
	Name          string   `json:"name"`
	Conditions    any      `json:"conditions"`
	TargetType    string   `json:"target_type"`
	TargetAgentID *int32   `json:"target_agent_id"`
	TargetSkills  []string `json:"target_skills"`
	Priority      int      `json:"priority"`
}

func (a *App) listRouting(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT rule_id, name, conditions, target_type, target_agent_id, target_skills, priority, is_active "+
			"FROM routing_rules WHERE user_id = $1 ORDER BY priority DESC, rule_id", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, priority int
		var name, targetType string
		var conditions []byte
		var targetAgent *int32
		var targetSkills []string
		var isActive bool
		if err := rows.Scan(&id, &name, &conditions, &targetType, &targetAgent, &targetSkills, &priority, &isActive); err != nil {
			continue
		}
		var cond any
		_ = json.Unmarshal(conditions, &cond)
		out = append(out, map[string]any{
			"rule_id": id, "name": name, "conditions": cond, "target_type": targetType,
			"target_agent_id": targetAgent, "target_skills": targetSkills, "priority": priority, "is_active": isActive,
		})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) upsertRouting(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req routingRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	condJSON, _ := json.Marshal(req.Conditions)
	var id int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO routing_rules (user_id, name, conditions, target_type, target_agent_id, target_skills, priority, is_active) "+
			"VALUES ($1,$2,$3::jsonb,$4,$5,$6::text[],$7,true) RETURNING rule_id",
		user.UserID, req.Name, condJSON, req.TargetType, req.TargetAgentID, req.TargetSkills, req.Priority).Scan(&id); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"rule_id": id, "message": "已保存"}, nil
}

func (a *App) deleteRouting(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM routing_rules WHERE rule_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// ============================================
// Macros
// ============================================

type macroRequest struct {
	Title    string `json:"title"`
	Steps    any    `json:"steps"`
	Category string `json:"category"`
}

func (a *App) listMacros(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT macro_id, title, steps, category, is_active FROM macros WHERE user_id = $1 ORDER BY macro_id", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var title string
		var steps []byte
		var category *string
		var isActive bool
		if err := rows.Scan(&id, &title, &steps, &category, &isActive); err != nil {
			continue
		}
		var s any
		_ = json.Unmarshal(steps, &s)
		out = append(out, map[string]any{"macro_id": id, "title": title, "steps": s, "category": category, "is_active": isActive})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) createMacro(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req macroRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Title == "" {
		return nil, ErrBadRequest("标题不能为空")
	}
	stepsJSON, _ := json.Marshal(req.Steps)
	var id int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO macros (user_id, title, steps, category, is_active) VALUES ($1,$2,$3::jsonb,$4,true) RETURNING macro_id",
		user.UserID, req.Title, stepsJSON, req.Category).Scan(&id); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"macro_id": id, "message": "已创建"}, nil
}

func (a *App) updateMacro(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	var req macroRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	stepsJSON, _ := json.Marshal(req.Steps)
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE macros SET title = $1, steps = $2::jsonb, category = $3 WHERE macro_id = $4 AND user_id = $5",
		req.Title, stepsJSON, req.Category, id, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已更新"}, nil
}

func (a *App) deleteMacro(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM macros WHERE macro_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// ============================================
// Roles
// ============================================

type roleRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

func (a *App) listRoles(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT role_id, name, permissions, is_system FROM roles WHERE user_id = $1 ORDER BY role_id", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var name string
		var perms []string
		var isSystem bool
		if err := rows.Scan(&id, &name, &perms, &isSystem); err != nil {
			continue
		}
		out = append(out, map[string]any{"role_id": id, "name": name, "permissions": perms, "is_system": isSystem})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) createRole(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req roleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Name == "" {
		return nil, ErrBadRequest("名称不能为空")
	}
	if req.Permissions == nil {
		req.Permissions = []string{}
	}
	var id int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO roles (user_id, name, permissions, is_system) VALUES ($1,$2,$3::text[],false) RETURNING role_id",
		user.UserID, req.Name, req.Permissions).Scan(&id); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"role_id": id, "message": "已创建"}, nil
}

func (a *App) deleteRole(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM roles WHERE role_id = $1 AND user_id = $2 AND is_system = false", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在或为系统角色")
	}
	return map[string]string{"message": "已删除"}, nil
}

type roleAssignRequest struct {
	UserID int32 `json:"user_id"`
}

func (a *App) assignRole(w http.ResponseWriter, r *http.Request, roleID int32) (any, error) {
	user, _ := UserFrom(r)
	var req roleAssignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	// Verify the role belongs to the caller.
	var rid int
	if err := a.DB.QueryRow(r.Context(), "SELECT role_id FROM roles WHERE role_id = $1 AND user_id = $2", roleID, user.UserID).Scan(&rid); err != nil {
		return nil, ErrNotFound("角色不存在")
	}
	if ok, err := a.userInCallerTenant(r.Context(), user, req.UserID); err != nil {
		return nil, ErrInternal("查询失败")
	} else if !ok {
		return nil, ErrForbidden("无权操作该用户")
	}
	if _, err := a.DB.Exec(r.Context(),
		"INSERT INTO user_roles (role_id, user_id) VALUES ($1,$2) ON CONFLICT (user_id, role_id) DO NOTHING", roleID, req.UserID); err != nil {
		return nil, ErrInternal("分配失败")
	}
	return map[string]string{"message": "已分配"}, nil
}

func (a *App) unassignRole(w http.ResponseWriter, r *http.Request, roleID int32) (any, error) {
	user, _ := UserFrom(r)
	var req roleAssignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	// The role must belong to the caller — the previous version deleted any
	// (role_id, user_id) pair a caller named, so one tenant could strip role
	// assignments off another tenant's roles.
	var rid int
	if err := a.DB.QueryRow(r.Context(), "SELECT role_id FROM roles WHERE role_id = $1 AND user_id = $2", roleID, user.UserID).Scan(&rid); err != nil {
		return nil, ErrNotFound("角色不存在")
	}
	if ok, err := a.userInCallerTenant(r.Context(), user, req.UserID); err != nil {
		return nil, ErrInternal("查询失败")
	} else if !ok {
		return nil, ErrForbidden("无权操作该用户")
	}
	if _, err := a.DB.Exec(r.Context(), "DELETE FROM user_roles WHERE role_id = $1 AND user_id = $2", roleID, req.UserID); err != nil {
		return nil, ErrInternal("取消失败")
	}
	return map[string]string{"message": "已取消"}, nil
}

// ============================================
// Webhook subscriptions
// ============================================

type webhookSubRequest struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events"`
}

func (a *App) listWebhooks(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT subscription_id, url, events, is_active, created_at FROM webhook_subscriptions WHERE user_id = $1 ORDER BY created_at DESC",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var url string
		var events []string
		var isActive bool
		var createdAt time.Time
		if err := rows.Scan(&id, &url, &events, &isActive, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{"subscription_id": id, "url": url, "events": events, "is_active": isActive, "created_at": createdAt})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) createWebhook(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req webhookSubRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.URL == "" {
		return nil, ErrBadRequest("url 不能为空")
	}
	var id int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO webhook_subscriptions (user_id, url, secret, events, is_active) VALUES ($1,$2,$3,$4::text[],true) RETURNING subscription_id",
		user.UserID, req.URL, req.Secret, req.Events).Scan(&id); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"subscription_id": id, "message": "已创建"}, nil
}

func (a *App) deleteWebhook(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM webhook_subscriptions WHERE subscription_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// ============================================
// Handoff requests
// ============================================

// listHandoffs — human-handoff queue (request_id is a UUID → string).
func (a *App) listHandoffs(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	statusFilter := r.URL.Query().Get("status")
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("page_size"), 100)
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (page - 1) * pageSize

	// Tenant users only see their own queue; the platform super-admin sees
	// every tenant's open requests (cross-tenant operations view).
	args := []any{}
	where := "WHERE 1=1"
	if !user.IsPlatformAdmin() {
		where += " AND h.user_id = $" + strconv.Itoa(len(args)+1)
		args = append(args, user.UserID)
	}
	if statusFilter != "" {
		where += " AND h.status::text = $" + strconv.Itoa(len(args)+1)
		args = append(args, statusFilter)
	}
	var total int64
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM human_handoff_requests h "+where, args...).Scan(&total)

	args = append(args, pageSize, offset)
	rows, err := a.DB.Query(r.Context(),
		"SELECT h.request_id::text, h.session_id::text, h.status::text, h.priority::text, h.trigger::text, h.reason, h.assigned_agent_id, h.created_at, h.assigned_at, h.resolved_at, h.resolution_note, "+
			"s.platform::text, s.platform_user_id, s.title, pus.user_display_name, u.username, "+
			"last_message.content AS last_message, last_message.created_at AS last_message_at "+
			"FROM human_handoff_requests h "+
			"JOIN sessions s ON s.session_id = h.session_id "+
			"LEFT JOIN platform_user_sessions pus ON pus.session_id = s.session_id "+
			"LEFT JOIN users u ON u.user_id = h.assigned_agent_id "+
			"LEFT JOIN LATERAL (SELECT content, created_at FROM chat_messages WHERE session_id = s.session_id ORDER BY message_id DESC LIMIT 1) last_message ON TRUE "+
			where+" ORDER BY (h.priority::text='high') DESC, h.created_at DESC LIMIT $"+strconv.Itoa(len(args)-1)+" OFFSET $"+strconv.Itoa(len(args)),
		args...)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, sessionID, status, priority, trigger, reason string
			assignedAgent                                    *int32
			createdAt                                        time.Time
			assignedAt, resolvedAt                           *time.Time
			resolutionNote                                   *string
			platform, puid, sessionTitle                     *string
			displayName, agentName                           *string
			lastMsg                                          *string
			lastMsgAt                                        *time.Time
		)
		if err := rows.Scan(&id, &sessionID, &status, &priority, &trigger, &reason, &assignedAgent, &createdAt,
			&assignedAt, &resolvedAt, &resolutionNote, &platform, &puid, &sessionTitle, &displayName, &agentName,
			&lastMsg, &lastMsgAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"request_id": id, "session_id": sessionID, "status": status, "priority": priority,
			"trigger": trigger, "reason": reason, "assigned_agent_id": assignedAgent,
			"assigned_agent_name": textutil.DerefString(agentName), "created_at": createdAt,
			"assigned_at": assignedAt, "resolved_at": resolvedAt, "resolution_note": textutil.DerefString(resolutionNote),
			"platform": textutil.DerefString(platform), "platform_user_id": textutil.DerefString(puid),
			"user_display_name": textutil.DerefString(displayName), "session_title": textutil.DerefString(sessionTitle),
			"last_message": textutil.DerefString(lastMsg), "last_message_at": lastMsgAt,
		})
	}
	// `total` is counted by its own query, so a short read here shows the page
	// as complete while the pager offers more rows that never arrive.
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{"data": out, "total": total, "page": page, "page_size": pageSize}, nil
}

type resolveHandoffRequest struct {
	ResolutionNote string `json:"resolution_note"`
}

// resolveHandoff closes one open request (request_id is a UUID string).
func (a *App) resolveHandoff(w http.ResponseWriter, r *http.Request, requestID string) (any, error) {
	user, _ := UserFrom(r)
	var req resolveHandoffRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE human_handoff_requests SET status='resolved', resolved_at=NOW(), resolution_note=$1 WHERE request_id = $2::uuid AND user_id = $3",
		req.ResolutionNote, requestID, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已解决"}, nil
}

// ============================================
// Customers (360 + notes)
// ============================================

func (a *App) listCustomers(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT profile_id, platform, platform_user_id, display_name, email, total_sessions, total_messages, last_seen_at "+
			"FROM customer_profiles WHERE user_id = $1 ORDER BY last_seen_at DESC NULLS LAST LIMIT 200", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, totalSessions, totalMessages int
		var platform, puid, displayName string
		var email *string
		var lastSeen *time.Time
		if err := rows.Scan(&id, &platform, &puid, &displayName, &email, &totalSessions, &totalMessages, &lastSeen); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"profile_id": id, "platform": platform, "platform_user_id": puid, "display_name": displayName,
			"email": email, "total_sessions": totalSessions, "total_messages": totalMessages, "last_seen_at": lastSeen,
		})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) customer360(w http.ResponseWriter, r *http.Request, profileID int32) (any, error) {
	user, _ := UserFrom(r)
	var id int
	var platform, puid, displayName, notes string
	var email, phone *string
	err := a.DB.QueryRow(r.Context(),
		"SELECT profile_id, platform, platform_user_id, display_name, email, phone, notes FROM customer_profiles WHERE profile_id = $1 AND user_id = $2",
		profileID, user.UserID).Scan(&id, &platform, &puid, &displayName, &email, &phone, &notes)
	if err != nil {
		return nil, ErrNotFound("客户不存在")
	}
	// Related sessions.
	rows, err := a.DB.Query(r.Context(),
		"SELECT session_id, status::text, title, created_at FROM sessions WHERE user_id = $1 AND platform_user_id = $2 ORDER BY created_at DESC LIMIT 20",
		user.UserID, puid)
	sessions := make([]map[string]any, 0)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sid string
			var status string
			var title *string
			var createdAt time.Time
			if rows.Scan(&sid, &status, &title, &createdAt) == nil {
				sessions = append(sessions, map[string]any{"session_id": sid, "status": status, "title": title, "created_at": createdAt})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, ErrInternal("查询失败")
		}
	}
	return map[string]any{
		"profile_id": id, "platform": platform, "platform_user_id": puid, "display_name": displayName,
		"email": email, "phone": phone, "notes": notes, "sessions": sessions,
	}, nil
}

type customerNotesRequest struct {
	Notes string `json:"notes"`
}

func (a *App) updateCustomerNotes(w http.ResponseWriter, r *http.Request, profileID int32) (any, error) {
	user, _ := UserFrom(r)
	var req customerNotesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	tag, err := a.DB.Exec(r.Context(), "UPDATE customer_profiles SET notes = $1, updated_at = NOW() WHERE profile_id = $2 AND user_id = $3",
		req.Notes, profileID, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("客户不存在")
	}
	return map[string]string{"message": "已保存"}, nil
}

// ============================================
// FAQ suggestions
// ============================================

func (a *App) listFaqSuggestions(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT suggestion_id, question, answer, status::text, created_at FROM faq_suggestions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 100",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var question, answer, status string
		var createdAt time.Time
		if err := rows.Scan(&id, &question, &answer, &status, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{"suggestion_id": id, "question": question, "answer": answer, "status": status, "created_at": createdAt})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

func (a *App) acceptFaqSuggestion(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "UPDATE faq_suggestions SET status='accepted' WHERE suggestion_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已接受"}, nil
}

func (a *App) dismissFaqSuggestion(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "UPDATE faq_suggestions SET status='dismissed' WHERE suggestion_id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已忽略"}, nil
}

// ============================================
// Billing
// ============================================

func (a *App) getBilling(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var plan string
	var msgQuota, docQuota, msgUsed, docUsed int64
	var cycleStart, cycleEnd *time.Time
	err := a.DB.QueryRow(r.Context(),
		"SELECT plan, monthly_message_quota, monthly_doc_quota, messages_used, docs_used, cycle_start, cycle_end FROM tenant_billing WHERE user_id = $1",
		user.UserID).Scan(&plan, &msgQuota, &docQuota, &msgUsed, &docUsed, &cycleStart, &cycleEnd)
	if err != nil {
		return map[string]any{"plan": "free", "messages_used": 0, "docs_used": 0}, nil
	}
	return map[string]any{
		"plan": plan, "monthly_message_quota": msgQuota, "monthly_doc_quota": docQuota,
		"messages_used": msgUsed, "docs_used": docUsed, "cycle_start": cycleStart, "cycle_end": cycleEnd,
	}, nil
}

type setPlanRequest struct {
	Plan string `json:"plan"`
}

func (a *App) setPlan(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	// Plan changes move real money (quota limits): tenant admins upgrading
	// themselves to enterprise is a billing bypass — platform-side only.
	if user.Role != "platform_admin" {
		return nil, ErrForbidden("套餐变更请联系平台管理员")
	}
	var req setPlanRequest
	var targetID int32
	var body struct {
		UserID *int32 `json:"user_id"`
		setPlanRequest
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	req = body.setPlanRequest
	targetID = user.UserID
	if body.UserID != nil {
		targetID = *body.UserID
	}
	if req.Plan != "free" && req.Plan != "pro" && req.Plan != "enterprise" {
		return nil, ErrBadRequest("invalid plan")
	}
	msgQ, docQ := int64(500), int64(20)
	switch req.Plan {
	case "pro":
		msgQ, docQ = 5000, 500
	case "enterprise":
		msgQ, docQ = 1<<62, 1<<62
	}
	_, _ = a.DB.Exec(r.Context(), "INSERT INTO tenant_billing (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING", targetID)
	_, _ = a.DB.Exec(r.Context(),
		"UPDATE tenant_billing SET plan = $1, monthly_message_quota = $2, monthly_doc_quota = $3 WHERE user_id = $4",
		req.Plan, msgQ, docQ, targetID)
	return map[string]string{"message": "套餐已更新"}, nil
}

// ============================================
// Teams (agent management)
// ============================================

func (a *App) listTeam(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT t.team_id, t.agent_user_id, t.display_name, t.skills, t.is_active, u.username, COALESCE(u.email,'') "+
			"FROM agent_teams t JOIN users u ON u.user_id = t.agent_user_id WHERE t.owner_user_id = $1 ORDER BY t.team_id", user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var teamID, agentUserID int
		var displayName, username, email string
		var skills []string
		var isActive bool
		if err := rows.Scan(&teamID, &agentUserID, &displayName, &skills, &isActive, &username, &email); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"team_id": teamID, "agent_user_id": agentUserID, "display_name": displayName,
			"skills": skills, "is_active": isActive, "username": username, "email": email,
		})
	}
	// A short read must not be published as a short list: the client cannot tell
	// the two apart, so an unchecked Err() turns a dropped connection into
	// "this tenant has no more data".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

type addAgentRequest struct {
	AgentUserID int32    `json:"agent_user_id"`
	DisplayName string   `json:"display_name"`
	Skills      []string `json:"skills"`
}

func (a *App) addTeamAgent(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	// agent_teams doubles as the tenant boundary; only tenant owners may
	// claim agents. Defense in depth alongside the route-level adminOnly gate.
	if !user.IsAdmin() {
		return nil, ErrForbidden("仅管理员可添加客服")
	}
	var req addAgentRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	// Verify the agent user exists and can join this tenant. Membership is
	// exclusive: agent_teams doubles as the tenant boundary for every
	// foreign-user-id handler (see userInCallerTenant), so a user already
	// owned by another merchant must not be claimable here — that would hand
	// the claiming tenant management rights over a competitor's account.
	//
	// The target-side check must also refuse an account that is itself an
	// independent tenant principal. Every user row is a tenant keyed by
	// user_id (tenant_billing, platform_configs, sessions), so claiming ANY
	// foreign account would extend this tenant's authority over that account:
	// updateUserRole's tenant boundary is agent_teams membership, which the
	// inserted edge satisfies. A role='admin' row is an established tenant
	// owner; a role='user' row that already holds tenant state (billing row,
	// platform config, sessions, or knowledge) is an established tenant too.
	var targetRole string
	var claimedElsewhere, targetIsTenant bool
	if err := a.DB.QueryRow(r.Context(),
		"SELECT u.role::text, "+
			"EXISTS (SELECT 1 FROM agent_teams t "+
			"        WHERE t.agent_user_id = u.user_id AND t.owner_user_id <> $2), "+
			"EXISTS (SELECT 1 FROM tenant_billing b WHERE b.user_id = u.user_id) "+
			"  OR EXISTS (SELECT 1 FROM platform_configs pc WHERE pc.user_id = u.user_id) "+
			"  OR EXISTS (SELECT 1 FROM sessions ss WHERE ss.user_id = u.user_id) "+
			"  OR EXISTS (SELECT 1 FROM knowledge_documents kd WHERE kd.uploaded_by = u.user_id) "+
			"FROM users u WHERE u.user_id = $1",
		req.AgentUserID, user.UserID).Scan(&targetRole, &claimedElsewhere, &targetIsTenant); err != nil {
		return nil, ErrNotFound("用户不存在")
	}
	if req.AgentUserID == user.UserID {
		return nil, ErrBadRequest("不能将自己添加为客服")
	}
	if targetRole == "platform_admin" {
		return nil, ErrForbidden("无权添加该用户")
	}
	if targetRole == "admin" || targetIsTenant {
		// This account owns its own tenant data. Claiming it would grant this
		// tenant role/status authority over it, so it cannot be recruited as
		// an agent; only accounts created for this tenant are claimable.
		return nil, ErrForbidden("该账号是独立的商家账号，不能添加为客服")
	}
	if claimedElsewhere {
		return nil, ErrConflict("该用户已属于其他商家")
	}
	if req.Skills == nil {
		req.Skills = []string{}
	}
	var teamID int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO agent_teams (owner_user_id, agent_user_id, display_name, skills, is_active) VALUES ($1,$2,$3,$4::text[],true) RETURNING team_id",
		user.UserID, req.AgentUserID, req.DisplayName, req.Skills).Scan(&teamID); err != nil {
		// The SELECT above and this INSERT are a check-then-act pair, so the
		// loser of a concurrent claim — or a plain double-submit — arrives here
		// rather than at the claimedElsewhere branch. Both unique keys that can
		// fire mean "this account already belongs to a tenant" (059's
		// uq_agent_teams_agent_user_id, 024's (owner_user_id, agent_user_id)),
		// which is the same fact the pre-check reports as 409. Answering 500
		// told the client to retry a request that can never succeed, and hid a
		// normal race inside the server-error rate.
		var pge *pgconn.PgError
		if errors.As(err, &pge) && pge.Code == "23505" {
			return nil, ErrConflict("该用户已属于其他商家")
		}
		return nil, ErrInternal("添加失败")
	}
	return map[string]any{"team_id": teamID, "message": "已添加"}, nil
}

func (a *App) removeTeamAgent(w http.ResponseWriter, r *http.Request, teamID int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "DELETE FROM agent_teams WHERE team_id = $1 AND owner_user_id = $2", teamID, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已移除"}, nil
}

// ============================================
// Copilot (agent AI suggestions)
// ============================================

// copilotSuggest — suggested replies for a session's latest customer message.
func (a *App) copilotSuggest(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	latest, history := a.loadSessionLatest(r.Context(), sessionID)
	if latest == "" {
		return map[string]any{"suggestions": []string{}, "grounded": false}, nil
	}
	groundCtx := a.RAG.Ground(r.Context(), user.UserID, &sessionID, latest, "km", history, 3)
	if !groundCtx.HasMatch {
		return map[string]any{"suggestions": []string{"感谢咨询，请稍等，我为您查询。"}, "grounded": false}, nil
	}
	prompt := "Suggest one concise customer-service reply (same language as the customer):\n\n" + groundCtx.ContextStr + "\nCustomer: " + latest
	result, err := a.Gemini.Chat(r.Context(), prompt, nil, "km")
	if err != nil {
		return map[string]any{"suggestions": []string{}, "grounded": groundCtx.HasMatch}, nil
	}
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}
	return map[string]any{"suggestions": []string{reply}, "grounded": true}, nil
}

// copilotKnowledge — top KB chunks for the latest customer message.
func (a *App) copilotKnowledge(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	latest, _ := a.loadSessionLatest(r.Context(), sessionID)
	if latest == "" {
		return map[string]any{"sources": []rag.Source{}}, nil
	}
	sources, err := a.RAG.Search(r.Context(), user.UserID, latest, 5)
	if err != nil {
		return map[string]any{"sources": []rag.Source{}}, nil
	}
	return map[string]any{"sources": sources}, nil
}

// loadSessionLatest returns the latest customer message + history.
func (a *App) loadSessionLatest(ctx context.Context, sessionID string) (string, []gemini.HistoryItem) {
	rows, err := a.DB.Query(ctx,
		"SELECT role, content FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent') "+
			"AND NOT (role = 'model' AND message_type = 'audio') AND cancelled_at IS NULL "+
			"ORDER BY message_id DESC LIMIT 20", sessionID)
	if err != nil {
		a.Logger.Error("load session latest failed", "session_id", sessionID, "error", err.Error())
		return "", nil
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
		// The history is context, not the answer: a partial one is still usable,
		// so this is logged rather than surfaced as a failed turn. Silence would
		// hide that the model answered from a truncated conversation.
		a.Logger.Error("load session latest incomplete", "session_id", sessionID, "error", err.Error())
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	latest := ""
	for i := len(rev) - 1; i >= 0; i-- {
		if rev[i].Role == "user" {
			latest = rev[i].Content
			break
		}
	}
	return latest, rev
}

// ============================================
// Message feedback (👍/👎)
// ============================================

type feedbackRequest struct {
	Rating  int    `json:"rating"`
	Comment string `json:"comment"`
}

func (a *App) messageFeedback(w http.ResponseWriter, r *http.Request, messageID int32) (any, error) {
	user, _ := UserFrom(r)
	var req feedbackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Rating != -1 && req.Rating != 1 {
		return nil, ErrBadRequest("rating 必须是 -1 或 1")
	}
	// Only model messages; owner-checked via session join.
	var (
		mid       int64
		sessionID string
	)
	err := a.DB.QueryRow(r.Context(),
		"SELECT cm.message_id, cm.session_id FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id "+
			"WHERE cm.message_id = $1 AND cm.role = 'model' AND s.user_id = $2", messageID, user.UserID).Scan(&mid, &sessionID)
	if err != nil {
		return nil, ErrNotFound("消息不存在")
	}
	_, _ = a.DB.Exec(r.Context(),
		"UPDATE chat_messages SET feedback_rating = $1, feedback_comment = $2, feedback_at = NOW() WHERE message_id = $3",
		req.Rating, req.Comment, messageID)
	// A 👎 means the AI answer missed the mark → queue the conversation for a
	// human (deduped by the partial unique index on open requests).
	if req.Rating == -1 {
		_, _ = a.DB.Exec(r.Context(),
			"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
				"VALUES ($1,$2,'pending','high','negative_feedback'::human_handoff_trigger,$3,NOW()) ON CONFLICT DO NOTHING",
			sessionID, user.UserID, "Customer rated the AI reply 👎"+feedbackSuffix(req.Comment))
		_, _ = a.DB.Exec(r.Context(),
			"UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at, NOW()) WHERE session_id=$1 AND status='active'", sessionID)
		a.notifyUser(r.Context(), user.UserID, "handoff", "New human-handoff request", "negative_feedback: customer rated an AI reply 👎", sessionID)
		a.publishSessionEvent(r.Context(), user.UserID, sessionID)
	}
	return map[string]string{"message": "已记录"}, nil
}

func feedbackSuffix(comment string) string {
	if comment == "" {
		return ""
	}
	return ": " + comment
}
