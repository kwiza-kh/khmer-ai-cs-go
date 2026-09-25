package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
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
		// Fall back to the default config's key.
		_ = db.QueryRow(ctx, "SELECT api_key FROM model_configs WHERE is_default = true ORDER BY config_id LIMIT 1").Scan(&apiKey)
	}
	return apiKey
}

// defaultModelConfigFromDB reads the default config row the hot-reload pushes
// into the serving service: (api_key, model_name, system_prompt, max_tokens),
// with ok=false when there is no default row. Same seam, same reason: the
// hot-reload's behaviour under vertex (empty key must still reload) is only
// observable if the row can be supplied without a database.
var defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (apiKey, modelName, systemPrompt string, maxTokens int, ok bool) {
	err := db.QueryRow(ctx,
		"SELECT api_key, model_name, COALESCE(system_prompt,''), COALESCE(max_tokens,2048) FROM model_configs WHERE is_default = true ORDER BY config_id LIMIT 1").
		Scan(&apiKey, &modelName, &systemPrompt, &maxTokens)
	if err != nil {
		return "", "", "", 0, false
	}
	return apiKey, modelName, systemPrompt, maxTokens, true
}

// The model-config routes below were written when there was only AI Studio, so
// they treated an empty model_configs.api_key as "no credential configured" and
// refused. The transport decides that, not the column: under vertex the
// credential is the service-account file (GEMINI_VERTEX_SA_FILE) and the private
// key deliberately never enters the database (see gemini.CredentialSource), so
// an empty column is the NORMAL state there and the old refusal told the
// operator to paste a key that could never be used. Every branch on
// gemini.CredentialSourceOf().IsAPIKey() below is about that difference; the
// studio branch of each is the expression it had before.

// listModelConfigs — all model configs.
func (a *App) listModelConfigs(w http.ResponseWriter, r *http.Request) (any, error) {
	rows, err := a.DB.Query(r.Context(),
		"SELECT config_id, name, provider, model_name, temperature, max_tokens, context_cache_ttl, is_default, "+
			"COALESCE(system_prompt,''), (api_key IS NOT NULL AND api_key <> '') AS has_api_key FROM model_configs ORDER BY config_id")
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	// Reported per config so the console can render the truth about
	// credentials: which secret is in force, and that the api_key column is or
	// is not a credential. Without it the page has only has_api_key and paints
	// a vertex deployment as "needs an API key" with an input box that would
	// mislead whoever filled it in.
	credentialSource := string(gemini.CredentialSourceOf())
	out := make([]map[string]any, 0)
	for rows.Next() {
		var configID, maxTokens, cacheTTL int
		var name, provider, modelName, systemPrompt string
		var temperature *float64
		var isDefault, hasKey bool
		if err := rows.Scan(&configID, &name, &provider, &modelName, &temperature, &maxTokens, &cacheTTL, &isDefault, &systemPrompt, &hasKey); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"config_id": configID, "name": name, "provider": provider, "model_name": modelName,
			"temperature": temperature, "max_tokens": maxTokens, "context_cache_ttl": cacheTTL,
			"is_default": isDefault, "has_api_key": hasKey, "system_prompt": systemPrompt,
			"credential_source": credentialSource,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

type updateModelRequest struct {
	Name         *string  `json:"name"`
	ModelName    *string  `json:"model_name"`
	SystemPrompt *string  `json:"system_prompt"`
	Temperature  *float64 `json:"temperature"`
	MaxTokens    *int     `json:"max_tokens"`
	ContextCache *int     `json:"context_cache_ttl"`
	IsDefault    *bool    `json:"is_default"`
	APIKey       *string  `json:"api_key"`
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

// updateModelConfig — update one model config (platform admin only; the
// resource is platform-global with no tenant column, so a tenant admin must
// never reach it even if a route gate is misconfigured elsewhere).
func (a *App) updateModelConfig(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	caller, ok := UserFrom(r)
	if !ok || !caller.IsPlatformAdmin() {
		return nil, ErrForbidden("模型配置仅平台管理员可修改")
	}
	callerID := caller.UserID
	var req updateModelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	hasAny := req.Name != nil || req.ModelName != nil || req.SystemPrompt != nil || req.Temperature != nil ||
		req.MaxTokens != nil || req.ContextCache != nil || req.IsDefault != nil || (req.APIKey != nil && *req.APIKey != "")
	if !hasAny {
		return nil, ErrBadRequest("无更新字段")
	}
	if req.IsDefault != nil && *req.IsDefault {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET is_default = false WHERE config_id <> $1", configID)
	}
	if req.Name != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET name = $1 WHERE config_id = $2", *req.Name, configID)
	}
	if req.ModelName != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET model_name = $1 WHERE config_id = $2", *req.ModelName, configID)
	}
	if req.SystemPrompt != nil {
		// Read the current value first. The admin UI submits every field on
		// every save, so recording unconditionally would fill the history with
		// identical rows and bury the one change that actually happened.
		var before *string
		_ = a.DB.QueryRow(r.Context(),
			"SELECT system_prompt FROM model_configs WHERE config_id = $1", configID).Scan(&before)
		if _, err := a.DB.Exec(r.Context(),
			"UPDATE model_configs SET system_prompt = $1 WHERE config_id = $2", *req.SystemPrompt, configID); err != nil {
			return nil, ErrInternal("更新系统提示词失败")
		}
		if promptValue(before) != *req.SystemPrompt {
			a.ensurePromptBaseline(r.Context(), configID, before)
			a.recordPromptVersion(r.Context(), configID, *req.SystemPrompt, &callerID, promptSourceAdminEdit, "")
		}
	}
	if req.Temperature != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET temperature = $1 WHERE config_id = $2", *req.Temperature, configID)
	}
	if req.MaxTokens != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET max_tokens = $1 WHERE config_id = $2", *req.MaxTokens, configID)
	}
	if req.ContextCache != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET context_cache_ttl = $1 WHERE config_id = $2", *req.ContextCache, configID)
	}
	if req.IsDefault != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET is_default = $1 WHERE config_id = $2", *req.IsDefault, configID)
	}
	if req.APIKey != nil && *req.APIKey != "" {
		// A key is the STUDIO credential. Writing one under vertex would store a
		// billable AI Studio key in a column nothing reads, while the operator
		// reasonably believes they just rotated the credential — the illusion
		// this branch exists to refuse. The credential there is the
		// service-account file on the server, which no request to this API can
		// change (and must not: the private key stays on the filesystem at 0600,
		// never in a column that flows through the UI, request logs and backups).
		if !gemini.CredentialSourceOf().IsAPIKey() {
			return nil, ErrBadRequest("Vertex 模式下凭据来自服务器上的服务账号文件（GEMINI_VERTEX_SA_FILE），" +
				"API Key 不是聊天凭据，此接口不接受写入")
		}
		// Stored sealed, like every other credential column: the platform key is
		// a billable bearer credential, so a database read (backup, replica,
		// query log) must not hand over a working key.
		sealed, err := a.Sealer.Encrypt(*req.APIKey)
		if err != nil {
			return nil, ErrInternal("加密失败")
		}
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET api_key = $1 WHERE config_id = $2", sealed, configID)
	}
	// Hot-reload the serving Gemini service so edits take effect without restart.
	a.reloadGeminiFromDB(r.Context())
	return map[string]string{"message": "已更新"}, nil
}

// reloadGeminiFromDB rebuilds the serving Gemini service from the default DB
// model config, so admin edits apply without a process restart.
func (a *App) reloadGeminiFromDB(ctx context.Context) {
	apiKey, modelName, systemPrompt, maxTokens, ok := defaultModelConfigFromDB(ctx, a.DB)
	if !ok {
		return
	}
	if gemini.CredentialSourceOf().IsAPIKey() {
		// Studio: with no stored key the serving client must keep whatever
		// credential it booted with (env, or mock), so there is nothing to push.
		if apiKey == "" {
			return
		}
		apiKey = a.Sealer.DecryptOrKeep(apiKey)
	} else {
		// Vertex: the column is not a credential (the service account is), and an
		// empty one must NOT cancel the reload — that early return was this bug:
		// on a vertex deployment an edited model / prompt / max_tokens was stored
		// and answered "已更新", but never applied until the next restart. Passing
		// "" keeps it that way in both directions: the reload carries only the
		// settings, never a key, so it cannot look like the credential rotation
		// it is not (HotReload ignores the key on this path regardless).
		apiKey = ""
	}
	a.Gemini.HotReload(apiKey, modelName, systemPrompt, maxTokens)
}

// testModelConfig — run a test prompt against one model config.
func (a *App) testModelConfig(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
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

// listAvailableModels — list generative models available to this deployment.
//
// The credential comes from the TRANSPORT, which is the point of the split: on
// studio it is the config's stored key (the default config's key as fallback),
// and on vertex it is the service account — so there an empty api_key column is
// normal, the database is not read at all, and the list still loads.
func (a *App) listAvailableModels(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	apiKey := ""
	if gemini.CredentialSourceOf().IsAPIKey() {
		raw := studioAPIKeyFromDB(r.Context(), a.DB, configID)
		if raw == "" {
			return nil, ErrBadRequest("未设置 API Key")
		}
		apiKey = a.Sealer.DecryptOrKeep(raw)
	}
	names, err := gemini.ListModels(r.Context(), apiKey)
	if err != nil {
		// The cause is reported, not just "failed": the failures that matter here
		// are configuration ones (a missing GEMINI_VERTEX_SA_FILE, an unreadable
		// or malformed key file, a region the platform does not serve), and the
		// operator reading this toast is the only one who can fix them. The old
		// blanket message turned every one of them into the same sentence.
		return nil, &ApiError{Status: http.StatusBadGateway, Message: "获取模型列表失败: " + err.Error()}
	}
	out := make([]map[string]any, 0)
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "display_name": n})
	}
	return out, nil
}

// ============================================
// Users + analytics (admin)
// ============================================

// tenantUserScope builds the WHERE fragment restricting a `users u` query to
// the caller's tenant, i.e. the caller plus the agents they own through
// agent_teams. Returns the SQL, the args it consumes (appended to args) and
// the next free placeholder number.
//
// A platform_admin is the cross-tenant role and keeps an unrestricted view.
// Every other admin is confined to their own tenant — without this, any
// tenant's admin could enumerate the whole platform's user list (usernames,
// e-mails, token spend) through the admin users page.
func tenantUserScope(caller *CurrentUser, args []any) (string, []any) {
	if caller == nil || caller.IsPlatformAdmin() {
		return "", args
	}
	args = append(args, caller.UserID)
	n := len(args)
	return fmt.Sprintf(
		"(u.user_id = $%d OR u.user_id IN (SELECT agent_user_id FROM agent_teams WHERE owner_user_id = $%d))",
		n, n), args
}

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

	conds := []string{}
	args := []any{}
	if scope, scopedArgs := tenantUserScope(caller, args); scope != "" {
		conds = append(conds, scope)
		args = scopedArgs
	}
	if search != "" {
		args = append(args, "%"+search+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf("(u.username ILIKE $%d OR COALESCE(u.email,'') ILIKE $%d)", n, n))
	}
	whereClause := ""
	if len(conds) > 0 {
		whereClause = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int64
	if err := a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM users u"+whereClause, args...).Scan(&total); err != nil {
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
			"COUNT(*) FILTER (WHERE u.role IN ('admin','platform_admin')) FROM users u"+whereClause,
		args...).
		Scan(&stats.Total, &stats.Active, &stats.NewWeek, &stats.Admins); err != nil {
		return nil, ErrInternal("查询失败")
	}

	selArgs := append(append([]any{}, args...), pageSize, offset)
	rows, err := a.DB.Query(r.Context(),
		"SELECT u.user_id, u.username, COALESCE(u.email,''), u.role::text, u.is_active, u.created_at, "+
			"u.google_sub IS NOT NULL, u.telegram_sub IS NOT NULL, COALESCE(u.password_hash,'') <> '', "+
			"COALESCE(t.total_tokens, 0), COALESCE(t.cost_estimate, 0) "+
			"FROM users u LEFT JOIN LATERAL ("+
			"SELECT SUM(total_tokens) AS total_tokens, SUM(cost_estimate) AS cost_estimate "+
			"FROM token_usage WHERE user_id = u.user_id) t ON TRUE"+
			whereClause+" ORDER BY u.user_id LIMIT $"+strconv.Itoa(len(selArgs)-1)+" OFFSET $"+strconv.Itoa(len(selArgs)),
		selArgs...)
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
		var totalTokens int64
		var cost float64
		if err := rows.Scan(&uid, &username, &email, &role, &isActive, &createdAt,
			&hasGoogle, &hasTelegram, &hasPassword, &totalTokens, &cost); err != nil {
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
			"total_tokens": totalTokens, "cost_estimate": cost,
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

// ragGaps — knowledge-gap report.
func (a *App) ragGaps(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	days := parseIntOr(r.URL.Query().Get("days"), 7)
	gaps, err := a.RAG.KnowledgeGaps(r.Context(), user.UserID, int64(days))
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{"data": gaps}, nil
}
