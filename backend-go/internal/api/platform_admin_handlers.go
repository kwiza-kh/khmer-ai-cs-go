package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/usage"
)

// ============================================
// Platform super-admin (cross-tenant) management
// ============================================

// listTenants — all tenants (excluding platform admins).
func (a *App) listTenants(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query().Get("q")
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	size := parseIntOr(r.URL.Query().Get("page_size"), 20)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 1
	}
	if size > 200 {
		size = 200
	}
	offset := (page - 1) * size

	var total int64
	var rows []map[string]any
	if q == "" {
		_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users WHERE role <> 'platform_admin'").Scan(&total)
		rows = a.queryTenantOverview(r, "", size, offset)
	} else {
		pattern := "%" + q + "%"
		_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users WHERE role <> 'platform_admin' AND (username ILIKE $1 OR email ILIKE $1)", pattern).Scan(&total)
		rows = a.queryTenantOverview(r, pattern, size, offset)
	}
	return map[string]any{"data": rows, "total": total, "page": page, "page_size": size}, nil
}

func (a *App) queryTenantOverview(r *http.Request, pattern string, size, offset int) []map[string]any {
	rows := make([]map[string]any, 0)
	var query string
	if pattern == "" {
		query = "SELECT user_id, username, COALESCE(email,''), role, is_active, created_at, plan, messages_used, message_quota, docs_used, doc_quota, total_sessions, total_messages, total_documents FROM tenant_overview WHERE role <> 'platform_admin' ORDER BY created_at DESC LIMIT $1 OFFSET $2"
	} else {
		query = "SELECT user_id, username, COALESCE(email,''), role, is_active, created_at, plan, messages_used, message_quota, docs_used, doc_quota, total_sessions, total_messages, total_documents FROM tenant_overview WHERE role <> 'platform_admin' AND (username ILIKE $3 OR email ILIKE $3) ORDER BY created_at DESC LIMIT $1 OFFSET $2"
	}
	var rws pgx.Rows
	var qerr error
	if pattern == "" {
		rws, qerr = a.DB.Query(r.Context(), query, size, offset)
	} else {
		rws, qerr = a.DB.Query(r.Context(), query, size, offset, pattern)
	}
	if qerr != nil {
		return rows
	}
	defer rws.Close()
	for rws.Next() {
		var userID int32
		var username, email, role, plan string
		var isActive bool
		var createdAt time.Time
		var messagesUsed, messageQuota, docsUsed, docQuota, totalSessions, totalMessages, totalDocuments int64
		if err := rws.Scan(&userID, &username, &email, &role, &isActive, &createdAt, &plan, &messagesUsed, &messageQuota, &docsUsed, &docQuota, &totalSessions, &totalMessages, &totalDocuments); err != nil {
			continue
		}
		rows = append(rows, map[string]any{
			"user_id": userID, "username": username, "email": email, "role": role, "is_active": isActive,
			"created_at": createdAt, "plan": plan, "messages_used": messagesUsed, "message_quota": messageQuota,
			"docs_used": docsUsed, "doc_quota": docQuota, "total_sessions": totalSessions,
			"total_messages": totalMessages, "total_documents": totalDocuments,
		})
	}
	if err := rws.Err(); err != nil {
		// No error channel here (the caller renders whatever comes back), so at
		// least make the truncation visible: an operator looking at a short
		// tenant list should not have to guess whether that is all of them.
		a.Logger.Error("tenant overview scan incomplete", "error", err.Error())
	}
	return rows
}

// tenantDetail — rollup + recent sessions.
func (a *App) tenantDetail(w http.ResponseWriter, r *http.Request, userID int32) (any, error) {
	var tenant map[string]any
	var tUserID int32
	var username, email, role, plan string
	var isActive bool
	var createdAt time.Time
	var messagesUsed, messageQuota, docsUsed, docQuota, totalSessions, totalMessages, totalDocuments int64
	err := a.DB.QueryRow(r.Context(),
		"SELECT user_id, username, COALESCE(email,''), role, is_active, created_at, plan, messages_used, message_quota, docs_used, doc_quota, total_sessions, total_messages, total_documents FROM tenant_overview WHERE user_id = $1", userID).
		Scan(&tUserID, &username, &email, &role, &isActive, &createdAt, &plan, &messagesUsed, &messageQuota, &docsUsed, &docQuota, &totalSessions, &totalMessages, &totalDocuments)
	if err != nil {
		return nil, ErrNotFound("tenant not found")
	}
	tenant = map[string]any{
		"user_id": tUserID, "username": username, "email": email, "role": role, "is_active": isActive,
		"created_at": createdAt, "plan": plan, "messages_used": messagesUsed, "message_quota": messageQuota,
		"docs_used": docsUsed, "doc_quota": docQuota, "total_sessions": totalSessions,
		"total_messages": totalMessages, "total_documents": totalDocuments,
	}
	sessions := make([]map[string]any, 0)
	rows, err := a.DB.Query(r.Context(),
		"SELECT session_id, platform::text, status::text, title, sentiment, user_message_count, model_message_count, created_at FROM sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 20", userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sid string
			var platform, status *string
			var title *string
			var sentiment string
			var umc, mmc int32
			var createdAt time.Time
			if rows.Scan(&sid, &platform, &status, &title, &sentiment, &umc, &mmc, &createdAt) == nil {
				sessions = append(sessions, map[string]any{
					"session_id": sid, "platform": platform, "status": status, "title": title,
					"sentiment": sentiment, "user_message_count": umc, "model_message_count": mmc, "created_at": createdAt,
				})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, ErrInternal("查询失败")
		}
	}
	return map[string]any{"tenant": tenant, "sessions": sessions}, nil
}

// setTenantStatus — enable/disable a tenant (never platform admins).
func (a *App) setTenantStatus(w http.ResponseWriter, r *http.Request, userID int32) (any, error) {
	var req struct {
		IsActive bool `json:"is_active"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	var role string
	err := a.DB.QueryRow(r.Context(), "SELECT role::text FROM users WHERE user_id = $1", userID).Scan(&role)
	if err != nil {
		return nil, ErrNotFound("tenant not found")
	}
	if role == "platform_admin" {
		return nil, ErrBadRequest("不能禁用平台管理员账号")
	}
	_, err = a.DB.Exec(r.Context(), "UPDATE users SET is_active = $1 WHERE user_id = $2", req.IsActive, userID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	return map[string]string{"message": "已更新"}, nil
}

// setTenantPlan — change a tenant's plan.
func (a *App) setTenantPlan(w http.ResponseWriter, r *http.Request, userID int32) (any, error) {
	var req struct {
		Plan string `json:"plan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := a.applyPlan(r.Context(), userID, req.Plan); err != nil {
		return nil, err
	}
	return map[string]string{"message": "套餐已更新"}, nil
}

// applyPlan sets plan + quotas (shared by setPlan and setTenantPlan).
//
// The quotas come from usage.Plans — the same table the upgrade card renders and
// the gates compare against — so a plan change cannot leave a tenant metered on
// numbers that do not match the tier they were shown.
func (a *App) applyPlan(ctx context.Context, userID int32, plan string) error {
	spec, ok := usage.PlanByName(plan)
	if !ok {
		return ErrBadRequest("invalid plan")
	}
	_, _ = a.DB.Exec(ctx, "INSERT INTO tenant_billing (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING", userID)
	_, err := a.DB.Exec(ctx, "UPDATE tenant_billing SET plan = $1, monthly_message_quota = $2, monthly_doc_quota = $3 WHERE user_id = $4",
		spec.Name, spec.Messages, spec.Documents, userID)
	return err
}

// platformAnalytics — platform-wide dashboard.
// getSpendBudget — the rolling Gemini spend window against the ceiling Google
// enforces ($10 per 10 minutes on Tier 1, see usage.SpendLimitUSD). The same
// figure drives the local gate that hands turns to a human before the 429
// arrives, so this endpoint is how an operator sees the wall coming — and how
// they confirm the gate is shedding for the right reason rather than because
// the limit was left at the wrong tier.
func (a *App) getSpendBudget(w http.ResponseWriter, r *http.Request) (any, error) {
	spent, limit, over := usage.Budget(r.Context(), a.DB, a.Redis)
	ratio := 0.0
	if limit > 0 {
		ratio = spent / limit
	}
	return map[string]any{
		"window_minutes": 10,
		"spent_usd":      spent,
		"limit_usd":      limit,
		"used_ratio":     ratio,
		"gate_ratio":     usage.GateRatio(),
		"over_gate":      over,
	}, nil
}

func (a *App) platformAnalytics(w http.ResponseWriter, r *http.Request) (any, error) {
	var totalTenants, activeTenants, totalSessions, totalMessages, totalDocuments int64
	var totalTokens int64
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users WHERE role <> 'platform_admin'").Scan(&totalTenants)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users WHERE role <> 'platform_admin' AND is_active = true").Scan(&activeTenants)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM sessions").Scan(&totalSessions)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM chat_messages").Scan(&totalMessages)
	_ = a.DB.QueryRow(r.Context(), "SELECT COALESCE(SUM(total_tokens),0) FROM token_usage").Scan(&totalTokens)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM knowledge_documents").Scan(&totalDocuments)
	planDist := make([]map[string]any, 0)
	prows, err := a.DB.Query(r.Context(),
		"SELECT COALESCE(plan,'free'), COUNT(*) FROM users u LEFT JOIN tenant_billing b ON b.user_id = u.user_id WHERE u.role <> 'platform_admin' GROUP BY COALESCE(plan,'free')")
	if err == nil {
		defer prows.Close()
		for prows.Next() {
			var plan string
			var count int64
			if prows.Scan(&plan, &count) == nil {
				planDist = append(planDist, map[string]any{"plan": plan, "count": count})
			}
		}
		if err := prows.Err(); err != nil {
			return nil, ErrInternal("查询失败")
		}
	}
	daily := make([]map[string]any, 0)
	drows, err := a.DB.Query(r.Context(),
		"SELECT TO_CHAR(DATE(created_at),'YYYY-MM-DD'), COUNT(*) FROM chat_messages WHERE created_at > NOW() - INTERVAL '14 days' GROUP BY DATE(created_at) ORDER BY 1")
	if err == nil {
		defer drows.Close()
		for drows.Next() {
			var date string
			var count int64
			if drows.Scan(&date, &count) == nil {
				daily = append(daily, map[string]any{"date": date, "messages": count})
			}
		}
		if err := drows.Err(); err != nil {
			return nil, ErrInternal("查询失败")
		}
	}
	return map[string]any{
		"total_tenants": totalTenants, "active_tenants": activeTenants, "total_sessions": totalSessions,
		"total_messages": totalMessages, "total_tokens": totalTokens, "total_documents": totalDocuments,
		"plan_distribution": planDist, "daily_messages": daily,
	}, nil
}

// createTenant — provision a tenant account directly.
func (a *App) createTenant(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Username string  `json:"username"`
		Email    string  `json:"email"`
		Password string  `json:"password"`
		Plan     *string `json:"plan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	username := req.Username
	if len([]rune(username)) < 3 || len([]rune(username)) > 50 || len([]rune(req.Password)) < 6 {
		return nil, ErrBadRequest("请求格式错误")
	}
	var existing int32
	err := a.DB.QueryRow(r.Context(), "SELECT user_id FROM users WHERE username = $1 OR email = $2", username, req.Email).Scan(&existing)
	if err == nil {
		return nil, ErrConflict("用户名或邮箱已存在")
	}
	hashed, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, ErrInternal("密码处理失败")
	}
	var userID int32
	if err := a.DB.QueryRow(r.Context(), "INSERT INTO users (username, email, password_hash, role) VALUES ($1,$2,$3,'user') RETURNING user_id", username, req.Email, hashed).Scan(&userID); err != nil {
		return nil, ErrInternal("创建失败")
	}
	if req.Plan != nil && *req.Plan != "" {
		_ = a.applyPlan(r.Context(), userID, *req.Plan)
	}
	return map[string]any{"user_id": userID, "username": username, "message": "已创建租户"}, nil
}

// listAuditLogs — paginated admin audit trail.
func (a *App) listAuditLogs(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query().Get("q")
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	size := parseIntOr(r.URL.Query().Get("page_size"), 20)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 1
	}
	if size > 200 {
		size = 200
	}
	offset := (page - 1) * size
	pattern := fmt.Sprintf("%%%s%%", q)
	var total int64
	_ = a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM audit_logs a LEFT JOIN users u ON u.user_id = a.admin_id WHERE ($1 = '%%' OR a.action ILIKE $1 OR u.username ILIKE $1)", pattern).Scan(&total)
	data := make([]map[string]any, 0)
	rows, err := a.DB.Query(r.Context(),
		"SELECT a.log_id, u.username, a.action, a.target_type, a.target_id, COALESCE(a.details::text,''), COALESCE(a.ip_address,''), a.created_at FROM audit_logs a LEFT JOIN users u ON u.user_id = a.admin_id WHERE ($1 = '%%' OR a.action ILIKE $1 OR u.username ILIKE $1) ORDER BY a.log_id DESC LIMIT $2 OFFSET $3",
		pattern, size, offset)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var logID int64
			var username, action, targetType, targetID, details, ip *string
			var createdAt time.Time
			if rows.Scan(&logID, &username, &action, &targetType, &targetID, &details, &ip, &createdAt) == nil {
				data = append(data, map[string]any{
					"log_id": logID, "username": username, "action": action, "target_type": targetType,
					"target_id": targetID, "details": details, "ip_address": ip, "created_at": createdAt,
				})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, ErrInternal("查询失败")
		}
	}
	return map[string]any{"data": data, "total": total, "page": page, "page_size": size}, nil
}
