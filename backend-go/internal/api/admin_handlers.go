package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"khmer-ai-cs-go/internal/gemini"
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
	now := time.Now()
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

// listModelConfigs — all model configs.
func (a *App) listModelConfigs(w http.ResponseWriter, r *http.Request) (any, error) {
	rows, err := a.DB.Query(r.Context(),
		"SELECT config_id, name, provider, model_name, temperature, max_tokens, context_cache_ttl, is_default, "+
			"COALESCE(system_prompt,''), (api_key IS NOT NULL AND api_key <> '') AS has_api_key FROM model_configs ORDER BY config_id")
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
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
		})
	}
	return out, nil
}

type updateModelRequest struct {
	Name          *string  `json:"name"`
	ModelName     *string  `json:"model_name"`
	SystemPrompt  *string  `json:"system_prompt"`
	Temperature   *float64 `json:"temperature"`
	MaxTokens     *int     `json:"max_tokens"`
	ContextCache  *int     `json:"context_cache_ttl"`
	IsDefault     *bool    `json:"is_default"`
	APIKey        *string  `json:"api_key"`
}

// updateModelConfig — update one model config (admin).
func (a *App) updateModelConfig(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
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
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET system_prompt = $1 WHERE config_id = $2", *req.SystemPrompt, configID)
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
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET api_key = $1 WHERE config_id = $2", *req.APIKey, configID)
	}
	// Hot-reload the serving Gemini service so edits take effect without restart.
	a.reloadGeminiFromDB(r.Context())
	return map[string]string{"message": "已更新"}, nil
}

// reloadGeminiFromDB rebuilds the serving Gemini service from the default DB
// model config, so admin edits apply without a process restart.
func (a *App) reloadGeminiFromDB(ctx context.Context) {
	var apiKey, modelName, systemPrompt string
	var maxTokens int
	err := a.DB.QueryRow(ctx,
		"SELECT api_key, model_name, COALESCE(system_prompt,''), COALESCE(max_tokens,2048) FROM model_configs WHERE is_default = true ORDER BY config_id LIMIT 1").
		Scan(&apiKey, &modelName, &systemPrompt, &maxTokens)
	if err != nil || apiKey == "" {
		return
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
	if apiKey == "" {
		return nil, ErrBadRequest("该配置未设置 API Key")
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

// listAvailableModels — list generative models available for the config's key.
func (a *App) listAvailableModels(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	var apiKey string
	err := a.DB.QueryRow(r.Context(), "SELECT api_key FROM model_configs WHERE config_id = $1", configID).Scan(&apiKey)
	if err != nil || apiKey == "" {
		// Fall back to the default config's key.
		_ = a.DB.QueryRow(r.Context(), "SELECT api_key FROM model_configs WHERE is_default = true ORDER BY config_id LIMIT 1").Scan(&apiKey)
	}
	if apiKey == "" {
		return nil, ErrBadRequest("未设置 API Key")
	}
	names, err := gemini.ListModels(r.Context(), apiKey)
	if err != nil {
		return nil, &ApiError{Status: http.StatusBadGateway, Message: "获取模型列表失败"}
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

// listUsers — all users (admin).
func (a *App) listUsers(w http.ResponseWriter, r *http.Request) (any, error) {
	rows, err := a.DB.Query(r.Context(),
		"SELECT user_id, username, email, role::text, is_active, created_at FROM users ORDER BY user_id")
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var uid int
		var username, email, role string
		var isActive bool
		var createdAt time.Time
		if err := rows.Scan(&uid, &username, &email, &role, &isActive, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"user_id": uid, "username": username, "email": email, "role": role,
			"is_active": isActive, "created_at": createdAt,
		})
	}
	return out, nil
}

// updateUserRole — change a user's role / active state (admin).
func (a *App) updateUserRole(w http.ResponseWriter, r *http.Request, userID int32) (any, error) {
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
	if req.Role != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE users SET role = $1::user_role WHERE user_id = $2", *req.Role, userID)
	}
	if req.IsActive != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE users SET is_active = $1 WHERE user_id = $2", *req.IsActive, userID)
	}
	return map[string]string{"message": "已更新"}, nil
}

// analyticsOverview — high-level counts.
func (a *App) analyticsOverview(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var totalSessions, activeSessions int64
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM sessions WHERE user_id = $1", user.UserID).Scan(&totalSessions)
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND status = 'active'", user.UserID).Scan(&activeSessions)
	var totalMessages int64
	_ = a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id WHERE s.user_id = $1", user.UserID).Scan(&totalMessages)
	return map[string]any{
		"total_sessions":  totalSessions,
		"active_sessions": activeSessions,
		"total_messages":  totalMessages,
	}, nil
}

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
