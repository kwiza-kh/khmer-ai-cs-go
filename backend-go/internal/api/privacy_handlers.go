package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// deletionStatus backs the public status page handed to a data subject by the
// Meta Data Deletion callback.
//
// Public by design: the confirmation code IS the capability, and it carries
// 80 bits of entropy, so it cannot be enumerated. The reply exposes only that
// one request's own outcome — never deleted content, and never another
// subject's data.
func (a *App) deletionStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || len(code) > 64 {
		return nil, ErrBadRequest("缺少确认码 / Missing confirmation code")
	}

	var (
		status      string
		requestedAt time.Time
		completedAt *time.Time
		sessions    int
		messages    int
		detail      string
	)
	err := a.DB.QueryRow(r.Context(),
		"SELECT status, requested_at, completed_at, sessions_deleted, messages_deleted, detail "+
			"FROM deletion_requests WHERE confirmation_code = $1", code).
		Scan(&status, &requestedAt, &completedAt, &sessions, &messages, &detail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound("确认码无效 / Unknown confirmation code")
		}
		return nil, err
	}

	// `detail` is operator-facing prose written by the callback. Only surface
	// it for the unresolved case, where it tells the subject what to do next;
	// a failed run's detail is an internal error string.
	note := ""
	if status == "unresolved" {
		note = "未找到与该标识关联的记录：本系统按页面级 ID 保存终端客户，" +
			"不保存 Facebook 用户 ID。请通过隐私政策中的联系方式补充可定位的信息。"
	}

	return map[string]any{
		"status":          status,
		"requested_at":    requestedAt,
		"completed_at":    completedAt,
		"records_removed": sessions + messages,
		"note":            note,
	}, nil
}
