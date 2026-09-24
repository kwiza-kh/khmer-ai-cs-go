package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// System-prompt version history.
//
// The prompt is the single most behaviour-defining knob in the product: one bad
// edit silently changes every customer reply. model_configs.system_prompt holds
// only the current value, so before this the previous text was simply gone.
//
// Two rules the whole feature rests on:
//
//  1. APPEND-ONLY. A rollback inserts a new row; history is never rewritten.
//     That keeps the rollback itself auditable and reversible.
//  2. An EMPTY value means "no override, the built-in default in code is in
//     effect" — a meaningful and healthy state, not a missing value. See
//     gemini.FromPartsFull.

// maxPromptVersions bounds one history response. Each row carries a full prompt
// (~5 KB), so the cap is what keeps the payload sane; it is far more history
// than an operator needs to find the version they want.
const maxPromptVersions = 50

const (
	promptSourceBaseline  = "baseline"
	promptSourceAdminEdit = "admin_edit"
	promptSourceRollback  = "rollback"
)

// promptValue collapses the nullable column and a request value into the single
// representation the history stores: an empty string for "no override". Without
// this, a NULL column and an empty value would look like different versions of
// the same state.
func promptValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ensurePromptBaseline records the pre-change value once, before the first
// recorded edit of a config that has no history at all.
//
// Migration 063 seeds a baseline for every config that existed when it ran, but
// a config created afterwards would start with an empty history — so its very
// first edit would leave nothing to roll back to, which is precisely when the
// feature is needed. This closes that hole without any create-path coupling.
func (a *App) ensurePromptBaseline(ctx context.Context, configID int32, previous *string) {
	var n int
	if err := a.DB.QueryRow(ctx,
		"SELECT count(*) FROM model_prompt_versions WHERE config_id = $1", configID).Scan(&n); err != nil || n > 0 {
		return
	}
	a.recordPromptVersion(ctx, configID, promptValue(previous), nil, promptSourceBaseline, "首次编辑前自动记录")
}

// recordPromptVersion appends one row. Best-effort: a missing history entry
// must not fail the edit the operator actually asked for.
func (a *App) recordPromptVersion(ctx context.Context, configID int32, value string, changedBy *int32, source, note string) {
	if _, err := a.DB.Exec(ctx,
		"INSERT INTO model_prompt_versions (config_id, system_prompt, source, changed_by, note) VALUES ($1,$2,$3,$4,$5)",
		configID, value, source, changedBy, note); err != nil {
		a.Logger.Warn("record prompt version failed",
			"config_id", configID, "source", source, "error", err.Error())
	}
}

// listPromptVersions — the prompt history for one config, newest first, plus
// the live value so the UI can mark which entry is current without a second
// request (and without racing a concurrent edit).
func (a *App) listPromptVersions(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	var current *string
	if err := a.DB.QueryRow(r.Context(),
		"SELECT system_prompt FROM model_configs WHERE config_id = $1", configID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound("模型配置不存在")
		}
		return nil, ErrInternal("查询失败")
	}

	rows, err := a.DB.Query(r.Context(),
		`SELECT v.version_id, v.system_prompt, v.source, v.note, v.created_at, COALESCE(u.username, '')
		   FROM model_prompt_versions v
		   LEFT JOIN users u ON u.user_id = v.changed_by
		  WHERE v.config_id = $1
		  ORDER BY v.version_id DESC
		  LIMIT $2`, configID, maxPromptVersions)
	if err != nil {
		return nil, ErrInternal("查询历史失败")
	}
	defer rows.Close()

	type version struct {
		VersionID int64  `json:"version_id"`
		Prompt    string `json:"system_prompt"`
		Source    string `json:"source"`
		Note      string `json:"note"`
		CreatedAt string `json:"created_at"`
		ChangedBy string `json:"changed_by"`
	}
	out := make([]version, 0)
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.VersionID, &v.Prompt, &v.Source, &v.Note, &v.CreatedAt, &v.ChangedBy); err != nil {
			continue
		}
		out = append(out, v)
	}
	return map[string]any{
		"config_id": configID,
		"current":   promptValue(current),
		"versions":  out,
	}, nil
}

// restorePromptVersion rolls the config back to an earlier version.
//
// The write and the history row happen in one transaction: a rollback that
// applied but was not recorded would leave the audit trail claiming a prompt
// that is not the live one, which is worse than not recording at all.
func (a *App) restorePromptVersion(ctx context.Context, configID int32, versionID int64, callerID int32) (any, error) {
	// Scope the lookup to this config: without it, a caller who can edit one
	// model could restore another model's prompt by guessing a version id.
	var value string
	var source string
	if err := a.DB.QueryRow(ctx,
		"SELECT system_prompt, source FROM model_prompt_versions WHERE version_id = $1 AND config_id = $2",
		versionID, configID).Scan(&value, &source); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound("该版本不存在")
		}
		return nil, ErrInternal("读取版本失败")
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, ErrInternal("开启事务失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		"UPDATE model_configs SET system_prompt = $1 WHERE config_id = $2", value, configID); err != nil {
		return nil, ErrInternal("回滚失败")
	}
	note := "回滚到版本 " + strconv.FormatInt(versionID, 10) + "（来源 " + source + "）"
	if _, err := tx.Exec(ctx,
		"INSERT INTO model_prompt_versions (config_id, system_prompt, source, changed_by, note) VALUES ($1,$2,$3,$4,$5)",
		configID, value, promptSourceRollback, callerID, note); err != nil {
		return nil, ErrInternal("记录回滚失败")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, ErrInternal("提交失败")
	}

	// Same hot-reload the normal edit path does, so the restored prompt is live
	// immediately rather than at the next restart.
	a.reloadGeminiFromDB(ctx)

	return map[string]any{
		"message": "已回滚",
		"note":    note,
	}, nil
}

// handlePromptRestore wraps restorePromptVersion for the two-wildcard route.
func (a *App) handlePromptRestore() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configID := parseIntOr(r.PathValue("id"), 0)
		versionID, _ := strconv.ParseInt(r.PathValue("version"), 10, 64)
		if configID == 0 || versionID == 0 {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的参数"})
			return
		}
		caller, ok := UserFrom(r)
		if !ok || !caller.IsPlatformAdmin() {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "模型配置仅平台管理员可修改"})
			return
		}
		val, err := a.restorePromptVersion(r.Context(), int32(configID), versionID, caller.UserID)
		if err != nil {
			apiErr, isAPI := err.(*ApiError)
			if !isAPI {
				apiErr = ErrInternal(err.Error())
			}
			WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
			return
		}
		WriteJSON(w, http.StatusOK, val)
	}
}
