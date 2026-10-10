package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/llm"
	"khmer-ai-cs-go/internal/platform"
)

// ============================================
// Business hours
// ============================================

type businessHoursEntry struct {
	Weekday   int    `json:"weekday"`
	OpenTime  string `json:"open_time"`
	CloseTime string `json:"close_time"`
	Platform  string `json:"platform"`
	IsActive  bool   `json:"is_active"`
}

// listBusinessHours — the caller's schedule.
func (a *App) listBusinessHours(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT id, weekday, open_time, close_time, platform::text, is_active FROM business_hours WHERE user_id = $1 ORDER BY weekday ASC",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, weekday int
		var openTime, closeTime string
		var platform *string
		var isActive bool
		if err := rows.Scan(&id, &weekday, &openTime, &closeTime, &platform, &isActive); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "weekday": weekday, "open_time": openTime, "close_time": closeTime,
			"platform": platform, "is_active": isActive,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

// upsertBusinessHours — replace the caller's schedule.
func (a *App) upsertBusinessHours(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		Entries []businessHoursEntry `json:"entries"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	for _, e := range req.Entries {
		var platformParam any
		if e.Platform != "" {
			platformParam = e.Platform
		}
		if e.Platform == "" {
			_, _ = a.DB.Exec(r.Context(),
				"INSERT INTO business_hours (user_id, weekday, open_time, close_time, platform, is_active) "+
					"VALUES ($1,$2,$3,$4,NULL,$5) ON CONFLICT (user_id, weekday) WHERE platform IS NULL "+
					"DO UPDATE SET open_time=EXCLUDED.open_time, close_time=EXCLUDED.close_time, is_active=EXCLUDED.is_active",
				user.UserID, e.Weekday, e.OpenTime, e.CloseTime, e.IsActive)
		} else {
			_, _ = a.DB.Exec(r.Context(),
				"INSERT INTO business_hours (user_id, weekday, open_time, close_time, platform, is_active) "+
					"VALUES ($1,$2,$3,$4,$5::platform_type,$6) ON CONFLICT (user_id, weekday, platform) WHERE platform IS NOT NULL "+
					"DO UPDATE SET open_time=EXCLUDED.open_time, close_time=EXCLUDED.close_time, is_active=EXCLUDED.is_active",
				user.UserID, e.Weekday, e.OpenTime, e.CloseTime, platformParam, e.IsActive)
		}
	}
	return map[string]string{"message": "营业时间已保存"}, nil
}

// isBusinessOpen — whether the caller is open right now.
func (a *App) isBusinessOpen(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	now := time.Now().In(platform.PhnomPenhLoc())
	weekday := int(now.Weekday())
	hm := now.Format("15:04")
	var openTime, closeTime string
	err := a.DB.QueryRow(r.Context(),
		"SELECT open_time, close_time FROM business_hours WHERE user_id = $1 AND weekday = $2 AND is_active = true LIMIT 1",
		user.UserID, weekday).Scan(&openTime, &closeTime)
	if err != nil {
		return map[string]any{"open": true, "reason": "no_schedule_configured"}, nil
	}
	if openTime == "" || closeTime == "" {
		return map[string]any{"open": false, "reason": "closed_today"}, nil
	}
	open := hm >= openTime && hm < closeTime
	reason := "open"
	if !open {
		reason = "closed"
	}
	return map[string]any{"open": open, "reason": reason, "open_time": openTime, "close_time": closeTime}, nil
}

// ============================================
// Canned responses (quick replies)
// ============================================

type cannedRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Category string `json:"category"`
	Language string `json:"language"`
}

// listCannedResponses — the caller's quick replies.
func (a *App) listCannedResponses(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT id, title, body, category, language FROM canned_responses WHERE user_id = $1 AND is_active = true ORDER BY category ASC, title ASC",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int
		var title, body, language string
		var category *string
		if err := rows.Scan(&id, &title, &body, &category, &language); err != nil {
			continue
		}
		out = append(out, map[string]any{"id": id, "title": title, "body": body, "category": category, "language": language})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

// createCannedResponse — add a quick reply.
func (a *App) createCannedResponse(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req cannedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Title == "" || req.Body == "" {
		return nil, ErrBadRequest("标题和内容不能为空")
	}
	if req.Language == "" {
		req.Language = "km"
	}
	var id int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO canned_responses (user_id, title, body, category, language, created_by, is_active) VALUES ($1,$2,$3,$4,$5,$6,true) RETURNING id",
		user.UserID, req.Title, req.Body, req.Category, req.Language, user.UserID).Scan(&id); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"id": id, "message": "已创建"}, nil
}

// deleteCannedResponse — soft-delete a quick reply.
func (a *App) deleteCannedResponse(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "UPDATE canned_responses SET is_active = false WHERE id = $1 AND user_id = $2", id, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已删除"}, nil
}

// ============================================
// Model configs (admin)
// ============================================

// studioAPIKeyFromDB reads the AI Studio credential the admin model routes
// list/verify with: this config's key, falling back to the default config's.
//
// It is a package-level seam for one reason: it is the ONLY database read on
// these routes, so a test that swaps it can prove the vertex path never touches
// the api_key column at all (and a studio test can pin the key it sends without
// a database).
var studioAPIKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) string {
	var apiKey string
	if err := db.QueryRow(ctx, "SELECT api_key FROM model_configs WHERE config_id = $1", configID).Scan(&apiKey); err != nil || apiKey == "" {
		// Fall back to the deployment's Gemini key — NOT the default row: after a provider
		// switch the default row can be a Claude or DeepSeek row, and neither key is an AI
		// Studio credential. Sending one to Gemini answers 401, which reads as "the region
		// is broken". Only rows that hold a key qualify, so the fallback cannot blank out
		// a good one.
		_ = db.QueryRow(ctx,
			"SELECT api_key FROM model_configs WHERE provider NOT IN ('anthropic', 'deepseek') AND api_key <> '' "+
				"ORDER BY is_default DESC, config_id LIMIT 1").Scan(&apiKey)
	}
	return apiKey
}

// defaultSystemPrompt — the built-in prompt a config falls back to when its own
// system_prompt is empty (gemini.FromPartsFull / buildRequestBody).
//
// This exists because the admin UI can otherwise only show the STORED value,
// which is empty on a healthy deployment, leaving an operator unable to tell
// whether a prompt is in effect at all — let alone what it says. It also makes
// the inverse visible: once anything is written to system_prompt, the built-in
// default stops applying and future code-side prompt fixes go silently
// unnoticed.
func (a *App) defaultSystemPrompt(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"system_prompt": gemini.DefaultSystemPrompt}, nil
}

// testModelConfig — run a test prompt against one model config.
func (a *App) testModelConfig(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	if row, ok := a.providerRow(r.Context(), configID); ok && !llm.IsGemini(row.Provider) {
		return a.testProviderConfig(r.Context(), row)
	}
	var apiKey, modelName, systemPrompt string
	var maxTokens int
	err := a.DB.QueryRow(r.Context(),
		"SELECT api_key, model_name, COALESCE(system_prompt,''), COALESCE(max_tokens,2048) FROM model_configs WHERE config_id = $1", configID).
		Scan(&apiKey, &modelName, &systemPrompt, &maxTokens)
	if err != nil {
		return nil, ErrNotFound("model config not found")
	}
	if gemini.CredentialSourceOf().IsAPIKey() {
		// Studio only: an empty key means mock mode, and a "connection test" that
		// answered from a template would be a lie.
		if apiKey == "" {
			return nil, ErrBadRequest("该配置未设置 API Key")
		}
		apiKey = a.Sealer.DecryptOrKeep(apiKey)
	} else {
		// Vertex: FromPartsFull("") is a LIVE service there — the transport mints
		// a service-account token — so the test reaches the platform instead of
		// demanding a key this deployment does not use.
		apiKey = ""
	}
	client := gemini.FromPartsFull(apiKey, modelName, systemPrompt, maxTokens)
	result, err := client.Chat(r.Context(), "Reply with the single word: ok", nil, "en")
	if err != nil || result.UsedMock {
		return nil, &ApiError{Status: http.StatusBadGateway, Message: "模型连接测试失败"}
	}
	return map[string]any{
		"reply": result.Reply, "model_name": modelName,
		"prompt_tokens": result.PromptTokens, "output_tokens": result.OutputTokens,
	}, nil
}

// modelListWarningHeader carries the reason a model listing is incomplete.
//
// The body of this endpoint is a bare array the console already consumes, so a
// partial answer cannot be signalled inside it without changing that shape —
// and the entries' own `available` flags say WHAT is unconfirmed, never WHY.
// The header says why. It is deliberately best-effort: the console is served
// from another origin and CORS exposes only the headers listed in middleware.go,
// so an operator reading a log line or a curl response is the audience today.
const modelListWarningHeader = "X-Model-List-Warning"

// listAvailableModels — list the models an operator can pick for this config,
// optionally in a caller-chosen Vertex region.
//
// The credential comes from the TRANSPORT, which is the point of the split: on
// studio it is the config's stored key (the default config's key as fallback),
// and on vertex it is the service account — so there an empty api_key column is
// normal, the database is not read at all, and the list still loads.
//
// ?region= selects the LISTING only. Omitting it means the region the deployment
// is serving from RIGHT NOW (Service.Region), resolved from the serving service
// rather than from the environment: since the console can move it, the
// environment's value is only the boot default and the selector would otherwise
// open on a region the deployment has left. It never selects what serves
// traffic — that is the same Service.Region, set by the console or by boot.
func (a *App) listAvailableModels(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	if row, ok := a.providerRow(r.Context(), configID); ok && !llm.IsGemini(row.Provider) {
		return providerAvailableModels(row, r.URL.Query().Get("region"))
	}
	// Validated before it can reach a URL: the region becomes part of the
	// request HOST on the vertex path, and that request carries a bearer token.
	region := gemini.NormalizeRegion(r.URL.Query().Get("region"))
	if region != "" && !gemini.ValidVertexRegion(region) {
		return nil, ErrBadRequest("无效的 region: " + region)
	}
	if region == "" {
		region = a.servingRegion()
	}
	apiKey := ""
	if gemini.CredentialSourceOf().IsAPIKey() {
		raw := studioAPIKeyFromDB(r.Context(), a.DB, configID)
		if raw == "" {
			return nil, ErrBadRequest("未设置 API Key")
		}
		apiKey = a.Sealer.DecryptOrKeep(raw)
	}
	models, warning, err := gemini.ModelCatalog(r.Context(), apiKey, region, a.servingModelName())
	if err != nil {
		// The cause is reported, not just "failed": the failures that matter here
		// are configuration ones (a missing GEMINI_VERTEX_SA_FILE, an unreadable
		// or malformed key file, a region the platform does not serve), and the
		// operator reading this toast is the only one who can fix them. The old
		// blanket message turned every one of them into the same sentence.
		return nil, &ApiError{Status: http.StatusBadGateway, Message: "获取模型列表失败: " + err.Error()}
	}
	if warning != "" {
		// A degraded listing is a 200 with what we have — never a 500 that
		// leaves the operator with no picker at all — so the reason travels
		// beside the body and into the log, where it names the region.
		w.Header().Set(modelListWarningHeader, truncateForHeader(warning))
		if a.Logger != nil {
			a.Logger.Warn("model list incomplete", "region", region, "config_id", configID, "reason", warning)
		}
	}
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		out = append(out, map[string]any{
			"name":         m.Name,
			"display_name": m.DisplayName,
			"launch_stage": m.LaunchStage,
			"capability":   m.Capability,
			"available":    m.Available,
		})
	}
	return out, nil
}

// truncateForHeader keeps a diagnostic string inside what a header can carry:
// an unescaped >4KB value (a platform error body is echoed into the message)
// makes the whole response fail to write, turning a degraded 200 into no answer
// at all. The cut moves back to a rune boundary first — the echoed body is not
// necessarily ASCII, and a header value that ends mid-rune is invalid UTF-8.
func truncateForHeader(msg string) string {
	const max = 512
	if len(msg) <= max {
		return msg
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

// vertexRegions — the region selector's data: the candidate locations, the
// deployment's own region first, and which one is currently serving.
//
// `current` is the SERVING region (Service.Region): the environment's value at
// boot, then whatever was last switched from this console. It is the same value
// the request path puts into the host and the save guard reasons about, so the
// selector cannot show one region while traffic goes to another — which is
// exactly how "picked global, kept serving from asia-southeast1" happened.
//
// Read-only and configuration-only (no network), so it deliberately does not
// contact Google: a selector that needs the network to render is a selector that
// breaks exactly when the network does.
func (a *App) vertexRegions(w http.ResponseWriter, r *http.Request) (any, error) {
	regions, current := gemini.VertexRegions(a.servingRegion())
	out := make([]map[string]any, 0, len(regions))
	for _, reg := range regions {
		out = append(out, map[string]any{"id": reg.ID, "label": reg.Label})
	}
	return map[string]any{"regions": out, "current": current}, nil
}

// servingModelName is the model this deployment actually answers customers
// with — the one the picker must never hide. Empty when no serving service is
// wired (a test App), which the catalog reads as "nothing to union".
func (a *App) servingModelName() string {
	if a.Gemini == nil {
		return ""
	}
	return a.Gemini.ModelName()
}

// servingRegion is the Vertex location this deployment sends calls to right now
// (see gemini.Service.Region): the env default, then the console's last switch.
// Empty when no serving service is wired (a test App) — which on the studio
// transport is also the normal, permanent answer.
func (a *App) servingRegion() string {
	if a.Gemini == nil {
		return ""
	}
	return a.Gemini.Region()
}

// ============================================
// Users + analytics (admin)
// ============================================

// listUsers — the caller's tenant's users, paginated with the same
// {data,total,...} envelope every other list endpoint uses; the admin users
// page reads .data/.total, so a bare array rendered the whole list (Google
// sign-ups included) as empty. Supports ?search= over username/email and
// reports per-user token consumption plus header stats for the stat cards.
//
// Scope is the caller's tenant; a platform_admin sees every user. See
// tenantUserScope.
func (a *App) listUsers(w http.ResponseWriter, r *http.Request) (any, error) {
	caller, ok := UserFrom(r)
	if !ok {
		return nil, ErrUnauthorized("未提供认证令牌")
	}
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("page_size"), 100)
	if pageSize > 200 {
		pageSize = 200
	}
	offset := (page - 1) * pageSize
	search := strings.TrimSpace(r.URL.Query().Get("search"))

	// One static WHERE for every caller, with every value bound:
	//
	//   $1 — the tenant scope: the caller's own user id, or NULL for a
	//        platform_admin, whose view is unrestricted. The IS NULL guard makes
	//        the scope transparent for that role and keeps ONE qualifier for both
	//        readers. Without the restriction, any tenant's admin could enumerate
	//        the whole platform's users (usernames, e-mails, token spend) through
	//        this page.
	//   $2 — the ?search= term, '' when absent.
	//
	// The clause used to be assembled with Sprintf from a fragment list. Nothing
	// user-controlled ever reached the SQL text (the term travelled in args), but
	// the placeholder numbers came from len(args) — a length derived from input —
	// so every scanner and every reader had to re-derive that it was safe. A
	// constant string with bound values is the same query without the argument.
	var scopeID any
	if !caller.IsPlatformAdmin() {
		scopeID = caller.UserID
	}
	where := " WHERE ($1::int4 IS NULL OR u.user_id = $1 OR u.user_id IN " +
		"(SELECT agent_user_id FROM agent_teams WHERE owner_user_id = $1))" +
		" AND ($2::text = '' OR u.username ILIKE '%'||$2||'%' OR COALESCE(u.email,'') ILIKE '%'||$2||'%')"
	args := []any{scopeID, search}

	var total int64
	if err := a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users u"+where, args...).Scan(&total); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// Stats for the header cards — same tenant scope as the list itself.
	var stats struct {
		Total   int64
		Active  int64
		NewWeek int64
		Admins  int64
	}
	if err := a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*), COUNT(*) FILTER (WHERE u.is_active), "+
			"COUNT(*) FILTER (WHERE u.created_at >= NOW() - INTERVAL '7 days'), "+
			"COUNT(*) FILTER (WHERE u.role IN ('admin','platform_admin')) FROM users u"+where,
		args...).
		Scan(&stats.Total, &stats.Active, &stats.NewWeek, &stats.Admins); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// No token/cost columns: model spend is operator-metered data and this list
	// is served to tenant owners (tenantAdminOnly).
	rows, err := a.DB.Query(r.Context(),
		"SELECT u.user_id, u.username, COALESCE(u.email,''), u.role::text, u.is_active, u.created_at, "+
			"u.google_sub IS NOT NULL, u.telegram_sub IS NOT NULL, COALESCE(u.password_hash,'') <> '' "+
			"FROM users u"+
			where+" ORDER BY u.user_id LIMIT $3 OFFSET $4",
		scopeID, search, pageSize, offset)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0, pageSize)
	skipped := 0
	for rows.Next() {
		var uid int
		var username, email, role string
		var isActive, hasGoogle, hasTelegram, hasPassword bool
		var createdAt *time.Time
		if err := rows.Scan(&uid, &username, &email, &role, &isActive, &createdAt,
			&hasGoogle, &hasTelegram, &hasPassword); err != nil {
			skipped++
			continue
		}
		// Every method the account can actually sign in with, not just one.
		// These genuinely combine: changePassword lets a passwordless account —
		// Google or Telegram — set an initial password without proving an old
		// one, so "telegram + password" is a state that exists. Reporting a
		// single method would silently hide the others.
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
		out = append(out, map[string]any{
			"user_id": uid, "username": username, "email": email, "role": role,
			"is_active": isActive, "created_at": createdAt, "auth_methods": authMethods,
		})
	}
	// Skipped rows are reported to the client; an iteration error is not a
	// skipped row, it is a missing tail. Fail rather than let the page silently
	// end early while `total` says there is more.
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{
		"data": out, "total": total, "page": page, "page_size": pageSize, "skipped": skipped,
		"stats": map[string]any{
			"total": stats.Total, "active": stats.Active, "new_week": stats.NewWeek, "admins": stats.Admins,
		},
	}, nil
}

// userInCallerTenant reports whether targetID belongs to caller's tenant: the
// caller themselves, or an agent they own through agent_teams. A platform_admin
// is the cross-tenant role and matches anyone.
//
// This is the guard for every handler that takes a foreign user id — role and
// status changes, session assignment, role grants. See tenantUserScope for the
// matching read-side boundary.
func (a *App) userInCallerTenant(ctx context.Context, caller *CurrentUser, targetID int32) (bool, error) {
	if caller == nil {
		return false, nil
	}
	if caller.IsPlatformAdmin() || caller.UserID == targetID {
		return true, nil
	}
	var ok bool
	err := a.DB.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM agent_teams WHERE owner_user_id = $1 AND agent_user_id = $2)",
		caller.UserID, targetID).Scan(&ok)
	return ok, err
}

// updateUserRole — change a user's role / active state (admin).
// platform_admin is the cross-tenant super role: a tenant admin must never
// be able to grant it to themselves or demote a real platform admin.
func (a *App) updateUserRole(w http.ResponseWriter, r *http.Request, userID int32) (any, error) {
	caller, _ := UserFrom(r)
	var req struct {
		Role     *string `json:"role"`
		IsActive *bool   `json:"is_active"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Role == nil && req.IsActive == nil {
		return nil, ErrBadRequest("无更新字段")
	}
	var targetRole string
	if err := a.DB.QueryRow(r.Context(), "SELECT role::text FROM users WHERE user_id = $1", userID).Scan(&targetRole); err != nil {
		return nil, ErrNotFound("用户不存在")
	}
	// Tenant boundary: an admin may only touch their own tenant's users.
	// Without this, a tenant admin could demote or disable any non-platform
	// admin account on the platform — including a competitor's.
	allowed, err := a.userInCallerTenant(r.Context(), caller, userID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	if !allowed {
		return nil, ErrForbidden("无权修改该用户")
	}
	// Super-admin accounts must stay loginable — disabling one (especially
	// by accident, e.g. clicking the wrong row's status pill) locks the
	// owner out of the whole admin panel until someone patches the DB.
	if req.IsActive != nil && !*req.IsActive && targetRole == "platform_admin" {
		return nil, ErrBadRequest("超级管理员账号不可禁用")
	}
	// Empty-string role means "not changing the role" (status-only update);
	// validating it as a role would reject the request with 未知角色.
	if req.Role != nil && *req.Role != "" {
		switch *req.Role {
		case "user", "admin":
			// Tenant roles — grantable by tenant admins.
		case "platform_admin":
			// Only an existing platform_admin may grant the super role.
			if caller.Role != "platform_admin" {
				return nil, ErrForbidden("无权分配该角色")
			}
		default:
			return nil, ErrBadRequest("未知角色")
		}
		// Protect platform_admin accounts from tenant-level changes.
		if targetRole == "platform_admin" && caller.Role != "platform_admin" {
			return nil, ErrForbidden("无权修改平台管理员")
		}
		if _, err := a.DB.Exec(r.Context(), "UPDATE users SET role = $1::user_role WHERE user_id = $2", *req.Role, userID); err != nil {
			return nil, ErrInternal("更新失败")
		}
	}
	if req.IsActive != nil {
		if targetRole == "platform_admin" && caller.Role != "platform_admin" {
			return nil, ErrForbidden("无权修改平台管理员")
		}
		// Disabling an account is a platform action, not a tenant one. An agent
		// who joins by invite keeps their own account: the tenant boundary
		// (agent_teams membership) must not become a way to lock a person out of
		// the whole platform, which is what made the old claim-by-user_id flow a
		// takeover primitive. Owners offboard by removing the seat.
		if caller.Role != "platform_admin" {
			return nil, ErrForbidden("只有平台管理员可以启用或停用账号")
		}
		if _, err := a.DB.Exec(r.Context(), "UPDATE users SET is_active = $1 WHERE user_id = $2", *req.IsActive, userID); err != nil {
			return nil, ErrInternal("更新失败")
		}
	}
	// Role and activation changes retire the target's existing sessions, so a
	// demotion or disable takes effect at once rather than whenever their
	// current JWT happens to expire. The role lives in the token, so without
	// this the old privileges kept working for up to 24h.
	a.bumpTokenVersion(r.Context(), userID)
	return map[string]string{"message": "已更新"}, nil
}

// analyticsOverview moved to analytics_handlers.go (full KPI set).
