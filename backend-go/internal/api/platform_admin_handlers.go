package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

	// The search covers identity, not just the login name: a Telegram signup has
	// NO e-mail and its username is either the Telegram @handle or "tg_<id>"
	// when the account has no handle at all — asking an operator to type that is
	// asking them not to find the person. display_name, phone and the Telegram
	// subject are searchable for the same reason. auth= narrows to one sign-in
	// method (telegram | google | password).
	auth := r.URL.Query().Get("auth")
	pattern := "%"
	if q != "" {
		pattern = "%" + q + "%"
	}

	var total int64
	if err := a.DB.QueryRow(r.Context(), tenantCountQuery, q, pattern, auth).Scan(&total); err != nil {
		return nil, ErrInternal("查询失败")
	}
	rows := a.queryTenantOverview(r, q, pattern, auth, size, offset)
	return map[string]any{"data": rows, "total": total, "page": page, "page_size": size}, nil
}

// tenantListWhere is shared by the tenant list and its count, so a page's total
// can never disagree with the rows it returns. Both queries are constant
// strings — every user-supplied value is a bound parameter ($1 = "the search is
// empty" marker, $2 = the LIKE pattern, $3 = the auth filter) — so no request
// input is ever concatenated into SQL. The list joins the users table for the
// identity columns (the view carries aggregates only); the count does not need
// it, because tenant_billing is 1:1 with users.
const tenantListWhere = `u.role <> 'platform_admin'
		AND ($1 = '' OR u.username ILIKE $2 OR COALESCE(u.email,'') ILIKE $2
			OR COALESCE(u.display_name,'') ILIKE $2 OR COALESCE(u.phone,'') ILIKE $2
			OR COALESCE(u.telegram_sub,'') ILIKE $2)
		AND ($3 = '' OR ($3 = 'telegram' AND u.telegram_sub IS NOT NULL)
			OR ($3 = 'google' AND u.google_sub IS NOT NULL)
			OR ($3 = 'password' AND COALESCE(u.password_hash,'') <> ''))`

const tenantCountQuery = "SELECT COUNT(*) FROM users u WHERE " + tenantListWhere

const tenantListQuery = `SELECT t.user_id, t.username, COALESCE(t.email,''), t.role, t.is_active, t.created_at,
			t.plan, t.messages_used, t.message_quota, t.docs_used, t.doc_quota,
			t.total_sessions, t.total_messages, t.total_documents,
			COALESCE(u.display_name,''), COALESCE(u.phone,''),
			u.telegram_sub IS NOT NULL, u.google_sub IS NOT NULL, COALESCE(u.password_hash,'') <> ''
		FROM tenant_overview t JOIN users u ON u.user_id = t.user_id
		WHERE ` + tenantListWhere + `
		ORDER BY t.created_at DESC LIMIT $4 OFFSET $5`

func (a *App) queryTenantOverview(r *http.Request, q, pattern, auth string, size, offset int) []map[string]any {
	rows := make([]map[string]any, 0)
	rws, qerr := a.DB.Query(r.Context(), tenantListQuery, q, pattern, auth, size, offset)
	if qerr != nil {
		// The caller renders whatever comes back; an empty page would look like
		// "no tenants match" instead of "the query failed".
		a.Logger.Error("tenant overview query failed", "error", qerr.Error())
		return rows
	}
	defer rws.Close()
	for rws.Next() {
		var userID int32
		var username, email, role, plan string
		var isActive bool
		var createdAt time.Time
		var messagesUsed, messageQuota, docsUsed, docQuota, totalSessions, totalMessages, totalDocuments int64
		var displayName, phone string
		var hasTelegram, hasGoogle, hasPassword bool
		if err := rws.Scan(&userID, &username, &email, &role, &isActive, &createdAt, &plan, &messagesUsed, &messageQuota, &docsUsed, &docQuota, &totalSessions, &totalMessages, &totalDocuments, &displayName, &phone, &hasTelegram, &hasGoogle, &hasPassword); err != nil {
			continue
		}
		authMethods := make([]string, 0, 3)
		if hasPassword {
			authMethods = append(authMethods, "password")
		}
		if hasGoogle {
			authMethods = append(authMethods, "google")
		}
		if hasTelegram {
			authMethods = append(authMethods, "telegram")
		}
		rows = append(rows, map[string]any{
			"user_id": userID, "username": username, "email": email, "role": role, "is_active": isActive,
			"created_at": createdAt, "plan": plan, "messages_used": messagesUsed, "message_quota": messageQuota,
			"docs_used": docsUsed, "doc_quota": docQuota, "total_sessions": totalSessions,
			"total_messages": totalMessages, "total_documents": totalDocuments,
			"display_name": displayName, "phone": phone, "auth_methods": authMethods,
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
	var totalCost, cacheHit float64
	var cachedTokens, promptTokens int64
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users WHERE role <> 'platform_admin'").Scan(&totalTenants)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users WHERE role <> 'platform_admin' AND is_active = true").Scan(&activeTenants)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM sessions").Scan(&totalSessions)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM chat_messages").Scan(&totalMessages)
	// One query for the three money numbers: they come from the same rows, and a
	// second query could disagree with the first if a turn lands between them.
	_ = a.DB.QueryRow(r.Context(), `SELECT COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0),
			COALESCE(SUM(cached_tokens),0), COALESCE(SUM(prompt_tokens),0) FROM token_usage`).
		Scan(&totalTokens, &totalCost, &cachedTokens, &promptTokens)
	cacheHit = cachedPct(cachedTokens, promptTokens)
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
		"total_cost": totalCost, "cache_hit_rate": cacheHit,
		"plan_distribution": planDist, "daily_messages": daily,
	}, nil
}

// ============================================
// Platform token board (cross-tenant god view)
// ============================================

// platformTokenDay is one day of the board's series.
type platformTokenDay struct {
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
	Calls  int64   `json:"calls"`
	Date   string  `json:"date"`
}

// bytesCachedPct is "what share of the prompt was served from cache", the one
// number that explains a cost curve that is not following the token curve.
// 0 when nothing was sent (no prompts → no ratio, not 0%).
func cachedPct(cached, prompt int64) float64 {
	if prompt <= 0 {
		return 0
	}
	return float64(cached) / float64(prompt) * 100.0
}

// platformTokens — the cross-tenant token/cost board: totals, a daily series,
// the model mix, and every account that can spend (including the ones that
// never spent a token and the operator's own, because "tenant X has not sent a
// message" is exactly what an operator needs to see). Read-only: no writes, no
// budgets, no gates.
func (a *App) platformTokens(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx := r.Context()
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days+1)
	since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, since.Location())

	// --- totals ---
	var (
		totalTokens, totalCalls                int64
		totalCost                              float64
		promptTokens, completionTokens, cached int64
	)
	if err := a.DB.QueryRow(ctx, `SELECT COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0), COUNT(*),
			COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(cached_tokens),0)
		FROM token_usage WHERE created_at >= $1`, since).
		Scan(&totalTokens, &totalCost, &totalCalls, &promptTokens, &completionTokens, &cached); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// --- daily series (zero-filled so the chart has a continuous x-axis) ---
	byDate := map[string]*platformTokenDay{}
	rows, err := a.DB.Query(ctx, `SELECT to_char(date_trunc('day', created_at), 'YYYY-MM-DD'),
			COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0), COUNT(*)
		FROM token_usage WHERE created_at >= $1 GROUP BY 1`, since)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	for rows.Next() {
		var d string
		var t, c int64
		var cost float64
		if rows.Scan(&d, &t, &cost, &c) == nil {
			byDate[d] = &platformTokenDay{Date: d, Tokens: t, Cost: cost, Calls: c}
		}
	}
	rows.Close()
	// A short read must not be published as a short series: the caller cannot
	// tell "no traffic" from "the query stopped early".
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	daily := make([]platformTokenDay, 0, days)
	for i := 0; i < days; i++ {
		d := since.AddDate(0, 0, i).Format("2006-01-02")
		if p, ok := byDate[d]; ok {
			daily = append(daily, *p)
		} else {
			daily = append(daily, platformTokenDay{Date: d})
		}
	}

	// --- model mix ---
	byModel := make([]map[string]any, 0)
	mrows, err := a.DB.Query(ctx, `SELECT COALESCE(model,''), COALESCE(SUM(total_tokens),0),
			COALESCE(SUM(cost_estimate),0), COUNT(*)
		FROM token_usage WHERE created_at >= $1 GROUP BY 1 ORDER BY 2 DESC`, since)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	for mrows.Next() {
		var name string
		var t, c int64
		var cost float64
		if mrows.Scan(&name, &t, &cost, &c) == nil {
			if name == "" {
				name = "(unknown)"
			}
			byModel = append(byModel, map[string]any{"model": name, "tokens": t, "cost": cost, "calls": c})
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// --- every account, whether or not it ever spent a token ---
	//
	// Deliberately NOT filtered to role <> 'platform_admin' the way the tenant
	// list is: the Gemini bill does not care about roles, and on this deployment
	// almost all traffic so far came from the operator's own account. Filtering
	// it out made this table sum to 5k tokens against a 640k total — a board
	// whose numbers do not reconcile is worse than no board. The role rides
	// along so the console can label that row honestly.
	byTenant := make([]map[string]any, 0)
	trows, err := a.DB.Query(ctx, `SELECT u.user_id, u.username, u.role::text, COALESCE(b.plan,'free'),
			COALESCE(SUM(t.total_tokens),0), COALESCE(SUM(t.cost_estimate),0), COUNT(t.usage_id),
			COALESCE(SUM(t.prompt_tokens),0), COALESCE(SUM(t.cached_tokens),0), MAX(t.created_at)
		FROM users u
		LEFT JOIN tenant_billing b ON b.user_id = u.user_id
		LEFT JOIN token_usage t ON t.user_id = u.user_id AND t.created_at >= $1
		GROUP BY u.user_id, u.username, u.role, b.plan
		ORDER BY 5 DESC, u.user_id`, since)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	for trows.Next() {
		var uid int32
		var username, role, plan string
		var tokens, calls, prompt, cachedTok int64
		var cost float64
		var lastCall *time.Time
		if trows.Scan(&uid, &username, &role, &plan, &tokens, &cost, &calls, &prompt, &cachedTok, &lastCall) == nil {
			byTenant = append(byTenant, map[string]any{
				"user_id": uid, "username": username, "role": role, "plan": plan,
				"tokens": tokens, "cost": cost, "calls": calls,
				"cache_hit_rate": cachedPct(cachedTok, prompt),
				"last_call_at":   lastCall,
			})
		}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}

	return map[string]any{
		"days": days,
		"totals": map[string]any{
			"tokens": totalTokens, "cost": totalCost, "calls": totalCalls,
			"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
			"cached_tokens": cached, "cache_hit_rate": cachedPct(cached, promptTokens),
		},
		"daily":     daily,
		"by_model":  byModel,
		"by_tenant": byTenant,
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
