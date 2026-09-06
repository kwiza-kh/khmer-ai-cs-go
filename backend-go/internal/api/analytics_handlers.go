package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"khmer-ai-cs-go/internal/platform"
)

// ============================================
// Analytics overview / timeline / breakdowns
// ============================================

// daysParam parses ?days= with sane bounds.
func daysParam(r *http.Request, def int) int {
	d := parseIntOr(r.URL.Query().Get("days"), def)
	if d < 1 {
		d = 1
	}
	if d > 365 {
		d = 365
	}
	return d
}

// analyticsOverview — the full KPI set the dashboard renders (sessions,
// response times, CSAT, escalation/deflection, token cost).
func (a *App) analyticsOverview(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	ctx := r.Context()
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days)

	var totalSessions, activeSessions, pendingHandoff int64
	_ = a.DB.QueryRow(ctx, "SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND is_test=FALSE AND created_at>=$2", user.UserID, since).Scan(&totalSessions)
	_ = a.DB.QueryRow(ctx, "SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND is_test=FALSE AND status='active'", user.UserID).Scan(&activeSessions)
	_ = a.DB.QueryRow(ctx, "SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND is_test=FALSE AND status='handoff'", user.UserID).Scan(&pendingHandoff)

	var avgFirstMs, avgResolutionMs float64
	_ = a.DB.QueryRow(ctx, `SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (first_response_at - created_at))*1000),0)
		FROM sessions WHERE user_id=$1 AND is_test=FALSE AND created_at>=$2 AND first_response_at IS NOT NULL`, user.UserID, since).Scan(&avgFirstMs)
	_ = a.DB.QueryRow(ctx, `SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (resolved_at - created_at))*1000),0)
		FROM sessions WHERE user_id=$1 AND is_test=FALSE AND created_at>=$2 AND resolved_at IS NOT NULL`, user.UserID, since).Scan(&avgResolutionMs)

	var rated, positive int64
	_ = a.DB.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE cm.feedback_rating IS NOT NULL),
			COUNT(*) FILTER (WHERE cm.feedback_rating = 1)
		FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id
		WHERE s.user_id=$1 AND s.is_test=FALSE AND cm.created_at>=$2`, user.UserID, since).Scan(&rated, &positive)
	csat := 0.0
	if rated > 0 {
		csat = float64(positive) / float64(rated)
	}

	var escalated int64
	_ = a.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND is_test=FALSE AND created_at>=$2 AND escalated_at IS NOT NULL`, user.UserID, since).Scan(&escalated)
	var deflected int64
	_ = a.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND is_test=FALSE AND created_at>=$2 AND status='resolved' AND escalated_at IS NULL`, user.UserID, since).Scan(&deflected)

	var totalTokens int64
	var totalCost, cacheHit float64
	_ = a.DB.QueryRow(ctx, `SELECT COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0),
			CASE WHEN COALESCE(SUM(prompt_tokens),0) = 0 THEN 0
				ELSE COALESCE(SUM(cached_tokens),0)::float8 / SUM(prompt_tokens)::float8 * 100.0 END
		FROM token_usage WHERE user_id=$1 AND created_at>=$2`, user.UserID, since).Scan(&totalTokens, &totalCost, &cacheHit)

	escalationRate, deflectionRate := 0.0, 0.0
	if totalSessions > 0 {
		escalationRate = float64(escalated) / float64(totalSessions)
		deflectionRate = float64(deflected) / float64(totalSessions)
	}

	return map[string]any{
		"total_sessions":        totalSessions,
		"active_sessions":       activeSessions,
		"pending_handoff":       pendingHandoff,
		"avg_first_response_ms": avgFirstMs,
		"avg_resolution_ms":     avgResolutionMs,
		"csat":                  csat,
		"escalation_rate":       escalationRate,
		"deflection_rate":       deflectionRate,
		"total_tokens":          totalTokens,
		"total_cost":            totalCost,
		"cache_hit_rate":        cacheHit,
	}, nil
}

// analyticsTimeline — one point per day for the dashboard charts.
func (a *App) analyticsTimeline(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	ctx := r.Context()
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days+1)
	since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, since.Location())

	type point struct {
		Date          string  `json:"date"`
		Tokens        int64   `json:"tokens"`
		Cost          float64 `json:"cost"`
		Sessions      int64   `json:"sessions"`
		Messages      int64   `json:"messages"`
		AvgResponseMs float64 `json:"avg_response_ms"`
		Deflection    float64 `json:"deflection_rate"`
	}
	byDate := map[string]*point{}
	ensure := func(d string) *point {
		if p, ok := byDate[d]; ok {
			return p
		}
		p := &point{Date: d}
		byDate[d] = p
		return p
	}

	if rows, err := a.DB.Query(ctx, `SELECT to_char(created_at::date,'YYYY-MM-DD'), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0)
		FROM token_usage WHERE user_id=$1 AND created_at>=$2 GROUP BY 1`, user.UserID, since); err == nil {
		for rows.Next() {
			var d string
			var t int64
			var c float64
			if rows.Scan(&d, &t, &c) == nil {
				p := ensure(d)
				p.Tokens, p.Cost = t, c
			}
		}
		rows.Close()
	}
	if rows, err := a.DB.Query(ctx, `SELECT to_char(created_at::date,'YYYY-MM-DD'), COUNT(*),
			COALESCE(AVG(EXTRACT(EPOCH FROM (first_response_at - created_at))*1000) FILTER (WHERE first_response_at IS NOT NULL),0),
			COUNT(*) FILTER (WHERE status='resolved' AND escalated_at IS NULL)
		FROM sessions WHERE user_id=$1 AND is_test=FALSE AND created_at>=$2 GROUP BY 1`, user.UserID, since); err == nil {
		for rows.Next() {
			var d string
			var n, defl int64
			var avg float64
			if rows.Scan(&d, &n, &avg, &defl) == nil {
				p := ensure(d)
				p.Sessions, p.AvgResponseMs = n, avg
				if n > 0 {
					p.Deflection = float64(defl) / float64(n)
				}
			}
		}
		rows.Close()
	}
	if rows, err := a.DB.Query(ctx, `SELECT to_char(cm.created_at::date,'YYYY-MM-DD'), COUNT(*)
		FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id
		WHERE s.user_id=$1 AND s.is_test=FALSE AND cm.created_at>=$2 GROUP BY 1`, user.UserID, since); err == nil {
		for rows.Next() {
			var d string
			var n int64
			if rows.Scan(&d, &n) == nil {
				ensure(d).Messages = n
			}
		}
		rows.Close()
	}

	out := make([]point, 0, days)
	for i := 0; i < days; i++ {
		d := since.AddDate(0, 0, i).Format("2006-01-02")
		if p, ok := byDate[d]; ok {
			out = append(out, *p)
		} else {
			out = append(out, point{Date: d})
		}
	}
	return out, nil
}

// topQueries — most-asked KB queries in the window (from rag telemetry).
func (a *App) topQueries(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	days := daysParam(r, 30)
	limit := parseIntOr(r.URL.Query().Get("limit"), 10)
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := a.DB.Query(r.Context(),
		"SELECT query, COUNT(*)::bigint AS hits FROM rag_query_logs WHERE user_id=$1 AND created_at > NOW() - ($2 || ' days')::interval GROUP BY query ORDER BY hits DESC LIMIT $3",
		user.UserID, days, limit)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var q string
		var n int64
		if rows.Scan(&q, &n) == nil {
			out = append(out, map[string]any{"query": q, "count": n})
		}
	}
	return out, nil
}

// languageBreakdown — session distribution per language.
func (a *App) languageBreakdown(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	days := daysParam(r, 30)
	rows, err := a.DB.Query(r.Context(),
		`SELECT COALESCE(NULLIF(language,''),'km'), COUNT(*) FROM sessions
		 WHERE user_id=$1 AND is_test=FALSE AND created_at > NOW() - ($2 || ' days')::interval GROUP BY 1 ORDER BY 2 DESC`,
		user.UserID, days)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var lang string
		var n int64
		if rows.Scan(&lang, &n) == nil {
			out = append(out, map[string]any{"language": lang, "count": n})
		}
	}
	return out, nil
}

// ============================================
// Token stats
// ============================================

// tokenStats — totals + daily series for /admin/tokens.
func (a *App) tokenStats(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	days := daysParam(r, 30)
	since := time.Now().AddDate(0, 0, -days)

	var totalTokens int64
	var totalCost, cacheHit float64
	err := a.DB.QueryRow(r.Context(), `SELECT COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0),
			CASE WHEN COALESCE(SUM(prompt_tokens),0) = 0 THEN 0
				ELSE COALESCE(SUM(cached_tokens),0)::float8 / SUM(prompt_tokens)::float8 * 100.0 END
		FROM token_usage WHERE user_id=$1 AND created_at>=$2`, user.UserID, since).
		Scan(&totalTokens, &totalCost, &cacheHit)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	rows, err := a.DB.Query(r.Context(), `SELECT to_char(created_at::date,'YYYY-MM-DD'), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0)
		FROM token_usage WHERE user_id=$1 AND created_at>=$2 GROUP BY 1 ORDER BY 1`, user.UserID, since)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	daily := make([]map[string]any, 0)
	for rows.Next() {
		var d string
		var t int64
		var c float64
		if rows.Scan(&d, &t, &c) == nil {
			daily = append(daily, map[string]any{"date": d, "tokens": t, "cost": c})
		}
	}
	return map[string]any{
		"total_tokens":   totalTokens,
		"total_cost":     totalCost,
		"cache_hit_rate": cacheHit,
		"daily_usage":    daily,
	}, nil
}

// ============================================
// Feedback list
// ============================================

// feedbackList — rated model messages for the dashboard feedback tab.
func (a *App) feedbackList(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	page := parseIntOr(r.URL.Query().Get("page"), 1)
	pageSize := parseIntOr(r.URL.Query().Get("page_size"), 50)
	if pageSize > 200 {
		pageSize = 200
	}
	rating := r.URL.Query().Get("rating")

	where := "WHERE s.user_id=$1 AND cm.role='model' AND cm.feedback_rating IS NOT NULL"
	args := []any{user.UserID}
	if rating == "1" || rating == "-1" {
		where += " AND cm.feedback_rating = $" + strconv.Itoa(len(args)+1)
		args = append(args, rating)
	}
	var total int64
	_ = a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id "+where, args...).Scan(&total)
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := a.DB.Query(r.Context(),
		"SELECT cm.message_id, cm.session_id::text, cm.content, cm.feedback_rating, cm.feedback_comment, cm.feedback_at, cm.role, cm.message_type, cm.created_at "+
			"FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id "+
			where+" ORDER BY cm.feedback_at DESC NULLS LAST LIMIT $"+strconv.Itoa(len(args)-1)+" OFFSET $"+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var (
			mid                       int64
			sid, content, role, mtype string
			ratingCol                 int16
			comment                   *string
			fbAt                      *time.Time
			createdAt                 time.Time
		)
		if err := rows.Scan(&mid, &sid, &content, &ratingCol, &comment, &fbAt, &role, &mtype, &createdAt); err != nil {
			continue
		}
		items = append(items, map[string]any{
			"message_id": mid, "session_id": sid, "content": content,
			"feedback_rating": int(ratingCol), "feedback_comment": derefStr(comment), "feedback_at": fbAt,
			"role": role, "message_type": mtype, "created_at": createdAt,
		})
	}
	return map[string]any{"data": items, "total": total, "page": page, "page_size": pageSize}, nil
}

// ============================================
// Enterprise analytics
// ============================================

// agentPerformance — per-agent throughput + quality for the dashboard.
func (a *App) agentPerformance(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	days := daysParam(r, 30)
	rows, err := a.DB.Query(r.Context(), `
		SELECT u.user_id, u.username,
			COALESCE(at.display_name, u.username) AS display_name,
			(SELECT COUNT(*) FROM sessions s WHERE s.user_id=$1 AND s.assigned_agent_id=u.user_id AND s.resolved_at IS NOT NULL AND s.resolved_at >= NOW() - ($2 || ' days')::interval) AS resolved_count,
			(SELECT COUNT(*) FROM sessions s WHERE s.user_id=$1 AND s.assigned_agent_id=u.user_id AND s.created_at >= NOW() - ($2 || ' days')::interval) AS handled_count,
			(SELECT COALESCE(AVG(EXTRACT(EPOCH FROM (COALESCE(s.resolved_at, NOW()) - COALESCE(s.escalated_at, s.created_at)))),0) FROM sessions s WHERE s.user_id=$1 AND s.assigned_agent_id=u.user_id AND s.escalated_at IS NOT NULL AND s.escalated_at >= NOW() - ($2 || ' days')::interval) AS avg_handle_secs,
			(SELECT COALESCE(AVG(cm.feedback_rating),0) FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id
				WHERE s.user_id=$1 AND s.assigned_agent_id=u.user_id AND cm.role='model' AND cm.feedback_rating IS NOT NULL AND cm.created_at >= NOW() - ($2 || ' days')::interval) AS avg_csat,
			(SELECT COUNT(*) FROM chat_messages cm WHERE cm.role='agent' AND cm.content <> '' AND cm.created_at >= NOW() - ($2 || ' days')::interval
				AND cm.session_id IN (SELECT session_id FROM sessions WHERE user_id=$1 AND assigned_agent_id=u.user_id)) AS reply_count
		FROM users u
		LEFT JOIN agent_teams at ON at.agent_user_id = u.user_id AND at.owner_id = $1
		WHERE u.user_id IN (
			SELECT DISTINCT assigned_agent_id FROM sessions WHERE user_id=$1 AND assigned_agent_id IS NOT NULL
		)
		ORDER BY handled_count DESC, u.user_id
		LIMIT 50`, user.UserID, days)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			uid                             int32
			username, displayName            string
			resolved, handled, reply         int64
			avgHandle, avgCsat               float64
		)
		if err := rows.Scan(&uid, &username, &displayName, &resolved, &handled, &avgHandle, &avgCsat, &reply); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"user_id": uid, "username": username, "display_name": displayName,
			"resolved_count": resolved, "handled_count": handled,
			"avg_handle_secs": avgHandle, "avg_csat": avgCsat, "reply_count": reply,
		})
	}
	return out, nil
}

// intentAnalytics — session intent distribution (populated by the classifier).
func (a *App) intentAnalytics(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	days := daysParam(r, 30)
	rows, err := a.DB.Query(r.Context(),
		`SELECT intent, COUNT(*)::bigint, AVG(confidence) FROM sessions
		 WHERE user_id=$1 AND is_test=FALSE AND intent IS NOT NULL AND intent <> ''
		 AND created_at > NOW() - ($2 || ' days')::interval
		 GROUP BY intent ORDER BY 2 DESC LIMIT 20`, user.UserID, days)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var intent string
		var n int64
		var conf *float64
		if rows.Scan(&intent, &n, &conf) == nil {
			out = append(out, map[string]any{"intent": intent, "count": n, "avg_confidence": conf})
		}
	}
	return out, nil
}

// integrationsStatus — env-derived capability matrix for the Enterprise tab.
func (a *App) integrationsStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{
		"email": map[string]any{
			"enabled":         a.Cfg.Email.Enabled,
			"configured":      a.Cfg.Email.SMTPHost != "",
			"inbound_domains": a.Cfg.Email.InboundDomains,
		},
		"voice": map[string]any{
			"enabled":     a.Cfg.Voice.Enabled,
			"configured":  a.Cfg.Voice.TwilioAccountSID != "" && a.Cfg.Voice.FromNumber != "",
			"from_number": a.Cfg.Voice.FromNumber,
		},
		"sso": map[string]any{
			"enabled":    a.Cfg.SSO.Enabled,
			"configured": a.Cfg.SSO.OIDCClientID != "" && a.Cfg.SSO.OIDCIssuer != "",
			"provider":   a.Cfg.SSO.Provider,
		},
	}, nil
}

// ============================================
// CSV report export
// ============================================

// reportCSV streams GET /api/v1/reports/{sessions|messages|tokens}?from=&to=.
func (a *App) reportCSV(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r)
	kind := r.PathValue("kind")
	from, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil {
		// Tolerate plain dates (the frontend sends ISO timestamps, but an
		// operator hitting the URL directly deserves a working export).
		from, err1 = time.Parse("2006-01-02", r.URL.Query().Get("from"))
		to, err2 = time.Parse("2006-01-02", r.URL.Query().Get("to"))
		if err1 != nil || err2 != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "from/to 需为日期"})
			return
		}
		to = to.Add(24*time.Hour - time.Second)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-report.csv\"", kind))
	cw := csv.NewWriter(w)
	// csvCell defuses spreadsheet formula injection (=, +, -, @ prefixes
	// from user-controlled content would execute as formulas in Excel).
	csvCell := func(s string) string {
		if s == "" {
			return s
		}
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			return "'" + s
		}
		return s
	}

	switch kind {
	case "sessions":
		_ = cw.Write([]string{"session_id", "platform", "status", "language", "title", "customer", "user_msgs", "model_msgs", "sentiment", "intent", "created_at", "first_response_at", "resolved_at", "escalated_at"})
		cw.Flush()
		rows, err := a.DB.Query(r.Context(), `SELECT s.session_id::text, COALESCE(s.platform::text,'web'), s.status::text, s.language, COALESCE(s.title,''),
				COALESCE(pus.user_display_name, s.platform_user_id, ''), s.user_message_count, s.model_message_count,
				COALESCE(s.sentiment,''), COALESCE(s.intent,''), s.created_at, s.first_response_at, s.resolved_at, s.escalated_at
			FROM sessions s LEFT JOIN platform_user_sessions pus ON pus.session_id = s.session_id
			WHERE s.user_id=$1 AND s.is_test=FALSE AND s.created_at BETWEEN $2 AND $3 ORDER BY s.created_at`,
			user.UserID, from, to)
		if err == nil {
			for rows.Next() {
				var sid, plat, status, lang, title, cust string
				var umc, mmc int32
				var sent, intent string
				var created time.Time
				var firstResp, resolved, escalated *time.Time
				if rows.Scan(&sid, &plat, &status, &lang, &title, &cust, &umc, &mmc, &sent, &intent, &created, &firstResp, &resolved, &escalated) == nil {
					_ = cw.Write([]string{sid, plat, status, lang, csvCell(title), csvCell(cust),
						strconv.Itoa(int(umc)), strconv.Itoa(int(mmc)), sent, intent,
						created.Format(time.RFC3339), fmtTime(firstResp), fmtTime(resolved), fmtTime(escalated)})
				}
			}
			rows.Close()
		}
	case "messages":
		_ = cw.Write([]string{"message_id", "session_id", "role", "type", "content", "tokens_used", "model", "used_mock", "feedback_rating", "feedback_comment", "created_at"})
		cw.Flush()
		rows, err := a.DB.Query(r.Context(), `SELECT cm.message_id, cm.session_id::text, cm.role, cm.message_type, cm.content,
				COALESCE(cm.tokens_used,0), COALESCE(cm.model_name,''), COALESCE(cm.used_mock,false), cm.feedback_rating, COALESCE(cm.feedback_comment,''), cm.created_at
			FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id
			WHERE s.user_id=$1 AND s.is_test=FALSE AND cm.created_at BETWEEN $2 AND $3 ORDER BY cm.message_id`,
			user.UserID, from, to)
		if err == nil {
			for rows.Next() {
				var (
					mid                 int64
					sid, role, mtype    string
					content             string
					tokens              int32
					model               string
					usedMock            bool
					rating              *int16
					comment             string
					createdAt           time.Time
				)
				if rows.Scan(&mid, &sid, &role, &mtype, &content, &tokens, &model, &usedMock, &rating, &comment, &createdAt) == nil {
					r := ""
					if rating != nil {
						r = strconv.Itoa(int(*rating))
					}
					_ = cw.Write([]string{strconv.FormatInt(mid, 10), sid, role, mtype, csvCell(content),
						strconv.Itoa(int(tokens)), model, strconv.FormatBool(usedMock), r, csvCell(comment), createdAt.Format(time.RFC3339)})
				}
			}
			rows.Close()
		}
	case "tokens":
		_ = cw.Write([]string{"date", "model", "prompt_tokens", "completion_tokens", "total_tokens", "cached_tokens", "cost_estimate"})
		cw.Flush()
		rows, err := a.DB.Query(r.Context(), `SELECT created_at, COALESCE(model,''), prompt_tokens, completion_tokens, total_tokens, COALESCE(cached_tokens,0), COALESCE(cost_estimate,0)
			FROM token_usage WHERE user_id=$1 AND created_at BETWEEN $2 AND $3 ORDER BY usage_id`, user.UserID, from, to)
		if err == nil {
			for rows.Next() {
				var (
					createdAt            time.Time
					model                string
					pt, ct, tt, cached     int32
					cost                 float64
				)
				if rows.Scan(&createdAt, &model, &pt, &ct, &tt, &cached, &cost) == nil {
					_ = cw.Write([]string{createdAt.Format(time.RFC3339), model,
						strconv.Itoa(int(pt)), strconv.Itoa(int(ct)), strconv.Itoa(int(tt)), strconv.Itoa(int(cached)),
						strconv.FormatFloat(cost, 'f', 8, 64)})
				}
			}
			rows.Close()
		}
	default:
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "未知报表类型"})
		return
	}
	cw.Flush()
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ============================================
// WhatsApp approved templates (for rich replies)
// ============================================

var waParamRe = regexp.MustCompile(`\{\{\s*[0-9]+\s*\}\}`)

// sessionWhatsAppTemplates lists approved WhatsApp message templates available
// to the session's connected WhatsApp account.
func (a *App) sessionWhatsAppTemplates(w http.ResponseWriter, r *http.Request, sessionID string) (any, error) {
	user, _ := UserFrom(r)
	if err := a.ensureSessionOwner(r.Context(), sessionID, user.UserID); err != nil {
		return nil, err
	}
	var configID int32
	var plat string
	err := a.DB.QueryRow(r.Context(),
		"SELECT pus.config_id, s.platform::text FROM sessions s JOIN platform_user_sessions pus ON pus.session_id = s.session_id WHERE s.session_id = $1",
		sessionID).Scan(&configID, &plat)
	if err != nil || plat != "whatsapp" {
		return map[string]any{"templates": []map[string]any{}}, nil
	}
	var accessEnc, waAccount *string
	if err := a.DB.QueryRow(r.Context(),
		"SELECT access_token, whatsapp_business_account_id FROM platform_configs WHERE config_id = $1", configID).Scan(&accessEnc, &waAccount); err != nil || accessEnc == nil {
		return nil, ErrNotFound("平台配置不存在")
	}
	token, derr := a.Sealer.Decrypt(*accessEnc)
	if derr != nil {
		return nil, ErrInternal("凭据解密失败")
	}
	account := derefStr(waAccount)
	rows, apiErr := platform.NewMetaClient(token, account, "", a.Cfg.Meta.GraphAPIVersion).
		ListWhatsAppTemplates(r.Context(), account)
	if apiErr != nil {
		return map[string]any{"templates": []map[string]any{}}, nil
	}
	out := make([]map[string]any, 0)
	for _, row := range rows {
		status, _ := row["status"].(string)
		if status != "" && status != "APPROVED" {
			continue
		}
		name, _ := row["name"].(string)
		lang, _ := row["language"].(string)
		category, _ := row["category"].(string)
		bodyPreview, paramCount := waTemplateBody(row["components"])
		if name == "" {
			continue
		}
		out = append(out, map[string]any{
			"name": name, "language": lang, "category": category,
			"body_preview": bodyPreview, "body_parameter_count": paramCount,
		})
	}
	return map[string]any{"templates": out}, nil
}

// waTemplateBody digs the BODY text + {{n}} parameter count out of the
// Graph API template components array.
func waTemplateBody(components any) (string, int) {
	list, _ := components.([]any)
	for _, c := range list {
		cm, _ := c.(map[string]any)
		if t, _ := cm["type"].(string); t != "BODY" {
			continue
		}
		text, _ := cm["text"].(string)
		if text == "" {
			if body, ok := cm["body"].(map[string]any); ok {
				text, _ = body["text"].(string)
			}
		}
		return truncateForTitle(text, 300), len(waParamRe.FindAllString(text, -1))
	}
	return "", 0
}
