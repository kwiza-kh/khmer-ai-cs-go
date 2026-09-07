package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/realtime"
)

// StartBackgroundTasks launches the periodic jobs that keep the platform
// alive: campaign dispatch, SLA breach scanning, knowledge URL freshness, and
// billing cycle rollover. These run for the lifetime of the process.
func (a *App) StartBackgroundTasks(ctx context.Context) {
	// Recover campaigns stranded in 'sending' by a crash/restart — the
	// scanner only picks up 'scheduled', so without this reset they would be
	// stuck forever.
	_, _ = a.DB.Exec(ctx, "UPDATE marketing_campaigns SET status='scheduled' WHERE status='sending' AND updated_at < NOW() - INTERVAL '10 minutes'")
	go a.loop(ctx, 30*time.Second, a.dispatchDueCampaigns)
	go a.loop(ctx, 60*time.Second, a.scanSLABreaches)
	go a.loop(ctx, 6*time.Hour, a.refreshURLDocuments)
	go a.loop(ctx, 1*time.Hour, a.resetBillingCycles)
	// Telegram notify bots: poll inline-button presses (接管/解决) and send
	// the 08:00 Phnom Penh daily digest.
	go a.loop(ctx, 12*time.Second, a.pollNotifyBotCallbacks)
	go a.loop(ctx, 15*time.Minute, a.sendDueDigests)
}

// pollNotifyBotCallbacks — handle 接管/解决 button presses for every tenant
// with a configured notify bot (getUpdates; the notify bot has no webhook).
func (a *App) pollNotifyBotCallbacks(ctx context.Context) {
	rows, err := a.DB.Query(ctx, "SELECT user_id FROM telegram_notify_settings")
	if err != nil {
		return
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		a.Pipe.ProcessNotifyCallbacks(ctx, id)
	}
}

// sendDueDigests — push the daily digest when Phnom Penh local time first
// enters the 08:00 hour for each configured tenant (once per day).
func (a *App) sendDueDigests(ctx context.Context) {
	now := time.Now().In(platform.PhnomPenhLoc())
	if now.Hour() != 8 {
		return
	}
	day := now.Format("20060102")
	rows, err := a.DB.Query(ctx, "SELECT user_id FROM telegram_notify_settings")
	if err != nil {
		return
	}
	defer rows.Close()
	var ids []int32
	for rows.Next() {
		var id int32
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		flag := "tg-digest:" + strconv.FormatInt(int64(id), 10) + ":" + day
		ok, err := a.Redis.IncrWindow(ctx, flag, 1, 26*time.Hour)
		if err != nil || !ok {
			continue
		}
		a.Pipe.SendDailyDigest(ctx, id)
	}
}

func (a *App) loop(ctx context.Context, every time.Duration, fn func(context.Context)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

type campRow struct {
	id, userID, configID             int32
	platform, templateName, templateLanguage, recipientFilter, tagFilter, bodyParams string
}

// dispatchDueCampaigns turns due scheduled campaigns into outbound template
// deliveries for recipients that already have a session on the platform.
// Idempotent: a campaign moves scheduled -> sending -> done.
func (a *App) dispatchDueCampaigns(ctx context.Context) {
	rows, err := a.DB.Query(ctx, `SELECT campaign_id, user_id, platform::text, config_id, template_name,
		template_language, body_params, recipient_filter, tag_filter
		FROM marketing_campaigns WHERE status='scheduled' AND scheduled_at <= NOW() LIMIT 10`)
	if err != nil {
		return
	}
	var due []campRow
	for rows.Next() {
		var c campRow
		var tagFilter *string
		if rows.Scan(&c.id, &c.userID, &c.platform, &c.configID, &c.templateName, &c.templateLanguage, &c.bodyParams, &c.recipientFilter, &tagFilter) == nil {
			c.tagFilter = ""
			if tagFilter != nil {
				c.tagFilter = *tagFilter
			}
			due = append(due, c)
		}
	}
	rows.Close()

	for _, c := range due {
		if _, err := a.DB.Exec(ctx, "UPDATE marketing_campaigns SET status='sending' WHERE campaign_id=$1 AND status='scheduled'", c.id); err != nil {
			continue
		}
		sent := a.dispatchOneCampaign(ctx, c)
		_, _ = a.DB.Exec(ctx, "UPDATE marketing_campaigns SET status='done', sent_count=$1, updated_at=NOW() WHERE campaign_id=$2", sent, c.id)
	}
}

// dispatchOneCampaign enqueues a template delivery per recipient that has an
// existing session on the platform. Returns the number enqueued.
func (a *App) dispatchOneCampaign(ctx context.Context, c campRow) int {
	q := "SELECT platform_user_id FROM customer_profiles WHERE user_id = $1 AND platform = $2"
	args := []any{c.userID, c.platform}
	if c.recipientFilter == "tagged" && c.tagFilter != "" {
		q += " AND tags @> ARRAY[$3]::text[]"
		args = append(args, c.tagFilter)
	}
	rows, err := a.DB.Query(ctx, q, args...)
	if err != nil {
		return 0
	}
	var recipients []string
	for rows.Next() {
		var puid string
		if rows.Scan(&puid) == nil {
			recipients = append(recipients, puid)
		}
	}
	rows.Close()

	sent := 0
	for _, puid := range recipients {
		// Only reach recipients that already have a session (avoids inbox spam).
		var sessionID string
		err := a.DB.QueryRow(ctx, `SELECT s.session_id FROM sessions s JOIN platform_user_sessions pus ON pus.session_id = s.session_id
			WHERE pus.config_id = $1 AND pus.platform_user_id = $2 LIMIT 1`, c.configID, puid).Scan(&sessionID)
		if err != nil {
			continue
		}
		var msgID int64
		err = a.DB.QueryRow(ctx,
			"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'system','template',$2,NOW()) RETURNING message_id",
			sessionID, "Campaign: "+c.templateName).Scan(&msgID)
		if err != nil {
			continue
		}
		payload := map[string]any{
			"kind": "template", "template_name": c.templateName, "template_language": c.templateLanguage,
		}
		var params []any
		if err := json.Unmarshal([]byte(c.bodyParams), &params); err == nil {
			payload["template_body_params"] = params
		} else {
			payload["template_body_params"] = []any{}
		}
		pj2, _ := json.Marshal(payload)
		if _, err := a.DB.Exec(ctx, `INSERT INTO platform_outbox (config_id, session_id, chat_message_id, platform, recipient_id, content, payload, status, next_attempt_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4::platform_type,$5,$6,$7::jsonb,'pending',NOW(),NOW(),NOW()) ON CONFLICT (chat_message_id) DO NOTHING`,
			c.configID, sessionID, msgID, c.platform, puid, "", pj2); err == nil {
			sent++
			realtime.Publish(ctx, a.Redis, realtime.Event{
				Type:      realtime.EventMessage,
				UserID:    c.userID,
				SessionID: sessionID,
				MessageID: msgID,
				Role:      "system",
			})
		}
	}
	if sent > 0 && a.Pipe != nil {
		a.Pipe.SignalOutbound()
	}
	return sent
}

type slaPol struct {
	userID, slaID    int32
	firstSecs        int
	resolutionSecs   *int
	businessHoursOnly bool
}

// scanSLABreaches records first_response and resolution breaches for active
// sessions against the tenant's active SLA policies. Idempotent via UNIQUE
// (session_id, breach_type).
func (a *App) scanSLABreaches(ctx context.Context) {
	rows, err := a.DB.Query(ctx,
		"SELECT user_id, sla_id, first_response_secs, resolution_secs, business_hours_only FROM sla_policies WHERE is_active = true")
	if err != nil {
		return
	}
	var pols []slaPol
	for rows.Next() {
		var p slaPol
		if rows.Scan(&p.userID, &p.slaID, &p.firstSecs, &p.resolutionSecs, &p.businessHoursOnly) == nil {
			pols = append(pols, p)
		}
	}
	rows.Close()

	for _, p := range pols {
		if p.firstSecs > 0 {
			sess, err := a.DB.Query(ctx, `SELECT session_id FROM sessions WHERE user_id=$1 AND status IN ('active','handoff')
				AND first_response_at IS NULL AND created_at < NOW() - ($2 || ' seconds')::interval`, p.userID, p.firstSecs)
			if err == nil {
				for sess.Next() {
					var sid string
					if sess.Scan(&sid) == nil {
						tag, _ := a.DB.Exec(ctx,
							"INSERT INTO sla_breaches (user_id, session_id, sla_id, breach_type) VALUES ($1,$2,$3,'first_response') ON CONFLICT (session_id, breach_type) DO NOTHING",
							p.userID, sid, p.slaID)
						if tag.RowsAffected() > 0 {
							a.notifyUser(ctx, p.userID, "sentiment", "SLA 违约：首次响应超时", "A session breached the first-response SLA", sid)
						}
					}
				}
				sess.Close()
			}
		}
		if p.resolutionSecs != nil && *p.resolutionSecs > 0 {
			sess, err := a.DB.Query(ctx, `SELECT session_id FROM sessions WHERE user_id=$1 AND status IN ('active','handoff')
				AND resolved_at IS NULL AND created_at < NOW() - ($2 || ' seconds')::interval`, p.userID, *p.resolutionSecs)
			if err == nil {
				for sess.Next() {
					var sid string
					if sess.Scan(&sid) == nil {
						_, _ = a.DB.Exec(ctx,
							"INSERT INTO sla_breaches (user_id, session_id, sla_id, breach_type) VALUES ($1,$2,$3,'resolution') ON CONFLICT (session_id, breach_type) DO NOTHING",
							p.userID, sid, p.slaID)
					}
				}
				sess.Close()
			}
		}
	}
}

// refreshURLDocuments re-fetches URL-derived knowledge documents older than
// 24h; re-index if the content changed, otherwise postpone.
func (a *App) refreshURLDocuments(ctx context.Context) {
	rows, err := a.DB.Query(ctx, `SELECT doc_id, COALESCE(source_url,''), COALESCE(content,'') FROM knowledge_documents
		WHERE source='url' AND source_url IS NOT NULL AND source_url <> '' AND index_status='ready' AND updated_at < NOW() - INTERVAL '24 hours' ORDER BY updated_at ASC LIMIT 20`)
	if err != nil {
		return
	}
	type doc struct {
		id       int32
		url, old string
	}
	var docs []doc
	for rows.Next() {
		var d doc
		if rows.Scan(&d.id, &d.url, &d.old) == nil {
			docs = append(docs, d)
		}
	}
	rows.Close()
	for _, d := range docs {
		_, newText, err := fetchURLText(ctx, d.url)
		if err == nil && newText != "" && newText != d.old {
			_, _ = a.DB.Exec(ctx, "UPDATE knowledge_documents SET content=$1, index_status='pending', index_error='', updated_at=NOW() WHERE doc_id=$2", newText, d.id)
		} else {
			_, _ = a.DB.Exec(ctx, "UPDATE knowledge_documents SET updated_at=NOW() WHERE doc_id=$1", d.id)
		}
	}
}

// resetBillingCycles rolls over lapsed billing cycles (reset usage counters).
func (a *App) resetBillingCycles(ctx context.Context) {
	_, _ = a.DB.Exec(ctx,
		"UPDATE tenant_billing SET messages_used=0, docs_used=0, cycle_start=NOW(), cycle_end=NOW()+INTERVAL '30 days' WHERE cycle_end <= NOW()")
}

// fetchURLText fetches a URL and extracts its visible text (script/style
// stripped). Returns (title, text, err).
func fetchURLText(ctx context.Context, rawURL string) (string, string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", &ApiError{Status: int(resp.StatusCode), Message: "upstream error"}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return "", "", err
	}
	return extractHTMLTitleText(string(body))
}

// extractHTMLTitleText strips script/style/svg and returns (title, text).
func extractHTMLTitleText(html string) (string, string, error) {
	var title, out strings.Builder
	var inTitle, inTag bool
	skip := 0
	var tag strings.Builder
	i := 0
	for i < len(html) {
		c := html[i]
		if c == '<' {
			inTag = true
			tag.Reset()
			i++
			continue
		}
		if inTag {
			if c == '>' {
				inTag = false
				raw := strings.TrimSpace(tag.String())
				name := strings.ToLower(strings.TrimPrefix(raw, "/"))
				if idx := strings.IndexAny(name, " \t\n/"); idx >= 0 {
					name = name[:idx]
				}
				switch {
				case name == "script" || name == "style" || name == "svg" || name == "noscript" || name == "template":
					if strings.HasPrefix(raw, "/") {
						if skip > 0 {
							skip--
						}
					} else {
						skip++
					}
				case name == "title":
					inTitle = !strings.HasPrefix(raw, "/")
				case name == "br" || name == "p" || name == "div" || name == "li" || name == "tr" || strings.HasPrefix(name, "h"):
					if out.Len() > 0 {
						out.WriteByte('\n')
					}
				}
			} else {
				tag.WriteByte(c)
			}
			i++
			continue
		}
		if skip == 0 {
			if inTitle {
				title.WriteByte(c)
			} else {
				out.WriteByte(c)
			}
		}
		i++
	}
	return strings.TrimSpace(title.String()), strings.TrimSpace(out.String()), nil
}
