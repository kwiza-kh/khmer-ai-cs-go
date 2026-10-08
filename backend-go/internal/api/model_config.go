package api

// The model-config console and the hot reload behind it.
//
// A row of model_configs names its provider. The default row (is_default) decides
// which client answers generation: the Gemini client, or the Claude client built
// from that row. The Gemini client is always configured from its own row, because
// embeddings and retrieval run on it whichever provider generates, so its
// credentials stay in place while Claude serves.
//
// A credential belongs to the provider it was entered for. A Gemini key is not an
// Anthropic key, so a provider change never carries the stored key across: the new
// provider needs its own key in the same request, or the change is refused.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/anthropic"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/llm"
)

// vertexKeyRefusal is the answer to a key sent to a provider whose credential is the
// service-account file. The key would be stored and never read, which reads as a
// rotation that did not happen.
const vertexKeyRefusal = "Vertex 模式下凭据来自服务器上的服务账号文件（GEMINI_VERTEX_SA_FILE），API Key 不是聊天凭据，此接口不接受写入"

// The database reads of this file are package variables, so the tests can answer
// them without a database. Production code never reassigns them.
var (
	// defaultModelConfigFromDB reads the default row: the one that serves.
	defaultModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (llm.Row, bool) {
		return llm.LoadDefault(ctx, db)
	}
	// geminiModelConfigFromDB reads the row the Gemini client is configured from.
	geminiModelConfigFromDB = func(ctx context.Context, db *pgxpool.Pool) (llm.Row, bool) {
		return llm.LoadGemini(ctx, db)
	}
	// modelRowFromDB reads one row for an update: its provider, its stored key and
	// region, and whether it is the default.
	modelRowFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) (modelRow, error) {
		var row modelRow
		var region string
		err := db.QueryRow(ctx,
			"SELECT provider, api_key, COALESCE(vertex_region,''), is_default FROM model_configs WHERE config_id = $1", configID).
			Scan(&row.provider, &row.apiKey, &region, &row.isDefault)
		row.region = gemini.NormalizeRegion(region)
		return row, err
	}
)

// modelRow is what the write path needs to know about a row before it writes.
type modelRow struct {
	provider  string
	apiKey    string // sealed, as stored
	region    string // normalised vertex_region
	isDefault bool
}

// updateModelRequest is one partial update of a row. A nil field is "do not touch".
type updateModelRequest struct {
	Name         *string  `json:"name"`
	Provider     *string  `json:"provider"`
	ModelName    *string  `json:"model_name"`
	SystemPrompt *string  `json:"system_prompt"`
	Temperature  *float64 `json:"temperature"`
	MaxTokens    *int     `json:"max_tokens"`
	ContextCache *int     `json:"context_cache_ttl"`
	IsDefault    *bool    `json:"is_default"`
	APIKey       *string  `json:"api_key"`
	// VertexRegion is the row's own location (model_configs.vertex_region). For a
	// Gemini row it is the serving location, applied by the reload; for a Claude row
	// it is where that row's calls go. Absent means "do not touch it": the difference
	// between that and an empty string keeps a settings save from silently relocating
	// serving back to the environment's default.
	VertexRegion *string `json:"vertex_region"`
}

// listModelConfigs — all model configs, each with the provider it names and the
// credential that provider authenticates with.
func (a *App) listModelConfigs(w http.ResponseWriter, r *http.Request) (any, error) {
	rows, err := a.DB.Query(r.Context(),
		"SELECT config_id, name, provider, model_name, temperature, max_tokens, context_cache_ttl, is_default, "+
			"COALESCE(system_prompt,''), (api_key IS NOT NULL AND api_key <> '') AS has_api_key, COALESCE(vertex_region,'') "+
			"FROM model_configs ORDER BY config_id")
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	// Reported per config so the console can render the truth about credentials.
	// A deployment can serve Gemini through the service account and Claude through a
	// key stored in its own row, so the answer is per row, not per deployment.
	out := make([]map[string]any, 0)
	for rows.Next() {
		var configID, maxTokens, cacheTTL int
		var name, provider, modelName, systemPrompt, region string
		var temperature *float64
		var isDefault, hasKey bool
		if err := rows.Scan(&configID, &name, &provider, &modelName, &temperature, &maxTokens, &cacheTTL, &isDefault, &systemPrompt, &hasKey, &region); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"config_id": configID, "name": name, "provider": provider, "model_name": modelName,
			"temperature": temperature, "max_tokens": maxTokens, "context_cache_ttl": cacheTTL,
			"is_default": isDefault, "has_api_key": hasKey, "system_prompt": systemPrompt,
			"credential_source": llm.CredentialSource(provider),
			"region":            gemini.NormalizeRegion(region),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return out, nil
}

// updateModelConfig — update one model config (platform admin only; the resource is
// platform-global with no tenant column, so a tenant admin must never reach it even
// if a route gate is misconfigured elsewhere).
//
// Every rule below is checked before the first write, so a request that breaks one
// changes nothing.
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
	hasAny := req.Name != nil || req.Provider != nil || req.ModelName != nil || req.SystemPrompt != nil ||
		req.Temperature != nil || req.MaxTokens != nil || req.ContextCache != nil || req.IsDefault != nil ||
		req.VertexRegion != nil || (req.APIKey != nil && *req.APIKey != "")
	if !hasAny {
		return nil, ErrBadRequest("无更新字段")
	}
	current, err := modelRowFromDB(r.Context(), a.DB, configID)
	if err != nil {
		return nil, ErrNotFound("model config not found")
	}
	// The provider the row ends up with decides which rules the rest of the request
	// must satisfy.
	nextProvider := current.provider
	if req.Provider != nil {
		nextProvider = strings.TrimSpace(*req.Provider)
		if !llm.Valid(nextProvider) {
			return nil, ErrBadRequest("未知的服务商: " + nextProvider)
		}
	}
	switching := nextProvider != current.provider
	keyGiven := req.APIKey != nil && *req.APIKey != ""

	// Credentials. A key is accepted only where the provider authenticates with one.
	switch nextProvider {
	case llm.ProviderGemini:
		// A key is the STUDIO credential, checked BEFORE the first write: under vertex it
		// is not a credential at all, so accepting one would store a billable key in a
		// column nothing reads.
		if keyGiven && !gemini.CredentialSourceOf().IsAPIKey() {
			return nil, ErrBadRequest(vertexKeyRefusal)
		}
		if switching && !keyGiven && gemini.CredentialSourceOf().IsAPIKey() {
			return nil, ErrBadRequest("切换到 Gemini（AI Studio）需要同时填写 API Key：旧服务商的 key 不会沿用")
		}
	case llm.ProviderAnthropic:
		if req.VertexRegion != nil {
			return nil, ErrBadRequest("region 只用于 Claude on Vertex（服务账号）：直连 Anthropic 没有区域")
		}
		if switching && !keyGiven {
			return nil, ErrBadRequest("切换到 Claude API 需要同时填写 Anthropic API Key：旧服务商的 key 不会沿用")
		}
		if !keyGiven && (current.apiKey == "" || switching) {
			return nil, ErrBadRequest("Claude API 需要 API Key")
		}
	case llm.ProviderAnthropicVertex:
		if keyGiven {
			return nil, ErrBadRequest(vertexKeyRefusal)
		}
	}

	// Model names. A Claude row takes a model from the catalog, because pricing,
	// sampling and thinking are per model. A Gemini row keeps the probes below.
	if llm.IsClaude(nextProvider) && req.ModelName != nil {
		if _, ok := anthropic.Lookup(strings.TrimSpace(*req.ModelName)); !ok {
			return nil, ErrBadRequest("未知的 Claude 模型: " + strings.TrimSpace(*req.ModelName) + "（可选：" + claudeModelIDs() + "）")
		}
	}
	if nextProvider == llm.ProviderAnthropicVertex && req.VertexRegion != nil {
		region := gemini.NormalizeRegion(*req.VertexRegion)
		if !anthropic.SupportsRegion(region) {
			return nil, ErrBadRequest("Claude on Vertex 只在 global / us / eu 提供，无效的 region: " + region)
		}
	}

	// Leaving the AI Studio credential row takes the only Gemini key with it when no
	// other Gemini row holds one, and embeddings and retrieval read that key.
	if !llm.IsClaude(current.provider) && nextProvider != llm.ProviderGemini &&
		current.apiKey != "" && gemini.CredentialSourceOf().IsAPIKey() && !otherGeminiRowHasKeyFromDB(r.Context(), a.DB, configID) {
		return nil, ErrBadRequest("这是 AI Studio 唯一的 Gemini 凭据：embedding 与检索依赖它。" +
			"请先保留另一行 Gemini 配置并保存其 API Key，再切换此行。本次未写入任何改动。")
	}

	// Gemini probes. They ask Google whether a region or model can serve calls, so they
	// only apply to a Gemini row.
	if nextProvider == llm.ProviderGemini {
		if err := a.checkGeminiEdit(r, req); err != nil {
			return nil, err
		}
	}
	if req.Temperature != nil {
		// Temperature is sent with every chat request of a Gemini row, so an out-of-range
		// value is a 400 on every reply. Claude rows store it and do not send it to models
		// that refuse sampling parameters.
		if *req.Temperature < 0 || *req.Temperature > 2 {
			return nil, ErrBadRequest(fmt.Sprintf(
				"temperature %.2f 超出平台范围 [0, 2]：该值会随每次对话请求发给模型，越界会让这个配置的每一次回复都失败。本次未写入任何改动。",
				*req.Temperature))
		}
	}

	// Writes. Provider and credentials first, so the reload below sees a consistent row.
	if req.IsDefault != nil && *req.IsDefault {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET is_default = false WHERE config_id <> $1", configID)
	}
	if switching {
		if _, err := a.DB.Exec(r.Context(), "UPDATE model_configs SET provider = $1 WHERE config_id = $2", nextProvider, configID); err != nil {
			return nil, ErrInternal("更新服务商失败")
		}
		if !keyGiven {
			// The stored key belongs to the old provider. It does not follow the row.
			_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET api_key = '' WHERE config_id = $1", configID)
		}
		// The region column means a different thing per provider, so it is reset on a
		// change: Claude on Vertex starts at global, and every other provider starts empty
		// unless the request names a region.
		if req.VertexRegion == nil {
			region := any(nil)
			if nextProvider == llm.ProviderAnthropicVertex {
				region = "global"
			}
			_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET vertex_region = $1 WHERE config_id = $2", region, configID)
		}
	}
	if req.Name != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET name = $1 WHERE config_id = $2", *req.Name, configID)
	}
	if req.ModelName != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET model_name = $1 WHERE config_id = $2", strings.TrimSpace(*req.ModelName), configID)
	}
	if req.SystemPrompt != nil {
		// Read the current value first. The admin UI submits every field on every save, so
		// recording unconditionally would fill the history with identical rows and bury the
		// one change that actually happened.
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
	if keyGiven {
		// Sealed like every other credential column: a database read (backup, replica,
		// query log) must not hand over a working key.
		sealed, err := a.Sealer.Encrypt(*req.APIKey)
		if err != nil {
			return nil, ErrInternal("加密失败")
		}
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET api_key = $1 WHERE config_id = $2", sealed, configID)
	}
	if req.VertexRegion != nil {
		_, _ = a.DB.Exec(r.Context(), "UPDATE model_configs SET vertex_region = $1 WHERE config_id = $2",
			gemini.NormalizeRegion(*req.VertexRegion), configID)
	}
	// Hot-reload the serving clients, so edits take effect without a restart.
	a.reloadServingFromDB(r.Context())
	return map[string]string{"message": "已更新"}, nil
}

// checkGeminiEdit runs the two Gemini probes for an edit that targets a Gemini row:
// the region switch and the model. It is unchanged from the single-provider console:
// the probes ask Google whether the serving region can answer the model in use.
func (a *App) checkGeminiEdit(r *http.Request, req updateModelRequest) error {
	// Switching the SERVING region. The picker browses catalogs, and a listing is NOT
	// callability (see gemini.ModelCatalog): a region can list the serving model and
	// still 404 every call. Probed with the model this deployment answers customers
	// with, because that pair is the one that has to keep working.
	if req.VertexRegion != nil {
		if gemini.CredentialSourceOf().IsAPIKey() {
			return ErrBadRequest("region 只在 Vertex（服务账号）传输下有意义：" +
				"AI Studio 只有一个全局端点，写入后不会被任何请求读取")
		}
		target := gemini.NormalizeRegion(*req.VertexRegion)
		if !gemini.ValidVertexRegion(target) {
			return ErrBadRequest("无效的 region: " + target)
		}
		if serving := a.servingRegion(); target != serving {
			if model := a.servingModelName(); model != "" &&
				gemini.ProbeModel(r.Context(), target, model) == gemini.ProbeNotServed {
				return ErrBadRequest(fmt.Sprintf(
					"在 %s 区域测试在用的模型 %s 时返回 NOT_FOUND：切过去之后每一次调用都会 404。"+
						"Vertex 只在部分区域提供该模型（例如 gemini-3.8-flash 仅 global / us / eu 可用）。"+
						"请先保存一个在 %s 可用的模型，或改选区域。本次未写入任何改动。",
					target, model, target))
			}
		}
	}
	// Saving a model this deployment cannot serve, which the console makes easy to reach
	// by accident: selecting `global` and then that model reads as "switch to 3.8". Every
	// call then 404s, and on the default config that is a production outage. Probed
	// against the SERVING region, never the browsed one.
	if req.ModelName != nil && strings.TrimSpace(*req.ModelName) != "" {
		serving := a.servingRegion()
		candidate := gemini.NormalizeModelName(strings.TrimSpace(*req.ModelName))
		if gemini.ProbeModel(r.Context(), serving, candidate) == gemini.ProbeNotServed {
			return ErrBadRequest(fmt.Sprintf(
				"模型 %s 在本部署的服务区域（%s）不存在，保存后每次调用都会 404。"+
					"Vertex 只在部分区域提供该模型（例如 gemini-3.8-flash 仅 global / us / eu 可用）。"+
					"要用它，请在上方「区域」里切到 global / us / eu 之一并应用，再保存模型；"+
					"否则请改选在 %s 可用的模型（如 gemini-3.5-flash）。本次未写入任何改动。",
				candidate, serving, serving))
		}
	}
	return nil
}

// otherGeminiRowHasKeyFromDB reports whether a Gemini row other than configID holds a stored key.
var otherGeminiRowHasKeyFromDB = func(ctx context.Context, db *pgxpool.Pool, configID int32) bool {
	var has bool
	err := db.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM model_configs WHERE config_id <> $1 AND provider NOT IN ('anthropic', 'anthropic-vertex') AND api_key <> '')",
		configID).Scan(&has)
	return err == nil && has
}

// reloadServingFromDB rebuilds the serving clients from the database after every
// model-config write, so an edit takes effect without a restart.
//
//   - The default row names the provider in force. When it is a Claude row, the Claude
//     client is configured from it, with its key unsealed here.
//   - The Gemini client is configured from its own row. When the default row is a
//     Gemini row that is the same row; when it is a Claude row, Gemini keeps the
//     credentials and settings of its own row, because embeddings and retrieval run on it.
//
// Without a default row nothing changes: the clients keep what they were configured with.
func (a *App) reloadServingFromDB(ctx context.Context) {
	def, ok := defaultModelConfigFromDB(ctx, a.DB)
	if !ok {
		return
	}
	if !llm.IsClaude(def.Provider) {
		a.applyGeminiRow(def)
	} else if row, found := geminiModelConfigFromDB(ctx, a.DB); found {
		a.applyGeminiRow(row)
	}
	if a.LLM != nil {
		if llm.IsClaude(def.Provider) {
			a.LLM.InstallClaude(def, a.claudeKey(def))
		}
		a.LLM.SetProvider(def.Provider)
	}
}

// applyGeminiRow pushes one Gemini row into the Gemini client: the model, the prompt
// and the output budget, the Vertex region, and the sampling temperature.
//
// Credentials follow the transport. On AI Studio the stored key is unsealed and
// applied, and an empty key keeps the one the client booted with. On Vertex the
// service-account file is the credential, so the key column is not read at all.
func (a *App) applyGeminiRow(row llm.Row) {
	if a.Gemini == nil {
		return
	}
	apiKey := ""
	if gemini.CredentialSourceOf().IsAPIKey() {
		if row.APIKey == "" {
			// With no stored key the serving client keeps whatever credential it booted
			// with (env, or mock), so there is nothing to push.
			return
		}
		apiKey = a.Sealer.DecryptOrKeep(row.APIKey)
	}
	a.Gemini.HotReload(apiKey, row.ModelName, row.SystemPrompt, row.MaxTokens)
	// The region is a property of the transport, applied separately and idempotently:
	// re-saving anything else on the page re-applies the region already in force. A
	// stored value that does not validate is reported, not silently ignored.
	if err := a.Gemini.SetVertexRegion(row.Region); err != nil {
		a.Logger.Warn("could not apply the stored vertex region", "region", row.Region, "error", err.Error())
	}
	// nil (a NULL column) is a real setting: "send none, use the platform default".
	a.Gemini.SetTemperature(row.Temperature)
}

// claudeKey is the Anthropic key of a Claude row, unsealed for the client. The Vertex
// transport authenticates with the service account and has no key.
func (a *App) claudeKey(row llm.Row) string {
	if row.Provider != llm.ProviderAnthropic || row.APIKey == "" {
		return ""
	}
	if a.Sealer == nil {
		return row.APIKey
	}
	return a.Sealer.DecryptOrKeep(row.APIKey)
}

// serving is the generation client for the provider the default row selects. Callers
// that generate replies or text use it. The Gemini-only capabilities keep a.Gemini.
func (a *App) serving() llm.Model {
	if a.LLM != nil {
		return a.LLM.Model()
	}
	return a.Gemini
}

// testClaudeConfig answers one test prompt through a Claude row. It builds a client
// for this call only: a test must not change what serves customers, and a row that is
// not the default still answers its own test.
func (a *App) testClaudeConfig(ctx context.Context, row llm.Row) (any, error) {
	if row.Provider == llm.ProviderAnthropic && strings.TrimSpace(row.APIKey) == "" {
		return nil, ErrBadRequest("该配置未设置 API Key")
	}
	client := anthropic.New(llm.ClaudeConfig(row, a.claudeKey(row)))
	result, err := client.Chat(ctx, "Reply with the single word: ok", nil, "en")
	if err != nil {
		// The cause is shown, not just "failed": a balance or quota stop reads very
		// differently from a bad key, and the operator is the one who can act on it.
		return nil, &ApiError{Status: http.StatusBadGateway, Message: "模型连接测试失败: " + err.Error()}
	}
	return map[string]any{
		"reply": result.Reply, "model_name": client.ModelName(),
		"prompt_tokens": result.PromptTokens, "output_tokens": result.OutputTokens,
	}, nil
}

// claudeAvailableModels lists the catalog for a Claude row. It needs no network: the
// catalog is the platform's verified list, since Claude on Vertex has no models API.
// available is false on a Vertex location Claude is not served from.
func claudeAvailableModels(row llm.Row, queryRegion string) (any, error) {
	region := gemini.NormalizeRegion(queryRegion)
	if region == "" {
		region = row.Region
	}
	if row.Provider == llm.ProviderAnthropicVertex {
		if region == "" {
			region = "global"
		}
		if !anthropic.SupportsRegion(region) {
			return nil, ErrBadRequest("Claude on Vertex 只在 global / us / eu 提供，无效的 region: " + region)
		}
	}
	served := row.Provider == llm.ProviderAnthropic || anthropic.SupportsRegion(region)
	out := make([]map[string]any, 0)
	for _, m := range anthropic.Catalog() {
		out = append(out, map[string]any{
			"name": m.ID, "display_name": m.DisplayName, "launch_stage": m.LaunchStage,
			"capability": "chat", "available": served,
		})
	}
	return out, nil
}

// claudeVertexRegions are the locations Claude on Vertex AI is served from. Like the
// Gemini list it is configuration-only (no network). current is empty: a Claude row's
// location is a setting of that row, not the deployment's serving location.
func claudeVertexRegions() map[string]any {
	out := []map[string]any{
		{"id": "global", "label": "global (multi-region)"},
		{"id": "us", "label": "us (multi-region, +10% vs global)"},
		{"id": "eu", "label": "eu (multi-region, +10% vs global)"},
	}
	return map[string]any{"regions": out, "current": ""}
}

// claudeRow returns the row when it names a Claude provider. Without a database there
// is no row to read, so the answer is no and the Gemini path runs, as it always did.
func (a *App) claudeRow(ctx context.Context, configID int32) (llm.Row, bool) {
	if a.DB == nil {
		return llm.Row{}, false
	}
	row, ok := llm.LoadByID(ctx, a.DB, configID)
	return row, ok && llm.IsClaude(row.Provider)
}

// claudeModelIDs lists the catalog's ids for an error message.
func claudeModelIDs() string {
	ids := make([]string, 0)
	for _, m := range anthropic.Catalog() {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}
