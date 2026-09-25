package api

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/realtime"
)

// StartBackgroundTasks launches the periodic jobs that keep the platform
// alive: campaign dispatch, SLA breach scanning, knowledge URL freshness, and
// billing cycle rollover. These run for the lifetime of the process.
//
// Any job added here that fetches a URL must go through the SSRF-guarded
// fetcher, rag.FetchURLContent (ValidateFetchURL + dial-time Control + a
// recheck on every redirect) — the URL freshness sweep in
// rag.SpawnIndexWorkers is the model. A bare http.Client{} is NOT equivalent:
// it will happily reach 169.254.169.254 or any internal service the caller
// names. This file used to carry exactly such a helper (fetchURLText and its
// HTML stripper); both had zero callers and were deleted, because the only
// thing left uncalled code does is tempt the next person to call it.
func (a *App) StartBackgroundTasks(ctx context.Context) {
	// Recover campaigns stranded in 'sending' by a crash/restart — the
	// scanner only picks up 'scheduled', so without this reset they would be
	// stuck forever.
	_, _ = a.DB.Exec(ctx, "UPDATE marketing_campaigns SET status='scheduled' WHERE status='sending' AND updated_at < NOW() - INTERVAL '10 minutes'")
	go a.loop(ctx, 30*time.Second, a.dispatchDueCampaigns)
	go a.loop(ctx, 60*time.Second, a.scanSLABreaches)
	go a.loop(ctx, 1*time.Hour, a.resetBillingCycles)
	// Watchdog: nothing else notices a degraded dependency. The process keeps
	// running, the HTTP port stays open, and customers simply stop getting
	// answers — the operator finds out from a complaint.
	go a.loop(ctx, 60*time.Second, a.checkPlatformHealth)
	// Support relays only matter while the operator might still hit reply.
	// Every merchant message creates one, so without this they accumulate
	// forever.
	go a.loop(ctx, 6*time.Hour, a.pruneStaleSupportRelays)
	// Daily digest at 08:00 Phnom Penh. The 接管/解决 button presses no longer
	// need a poller of their own: since 056 every notification goes out through
	// the platform bot, whose webhook receives the presses directly. The old
	// per-tenant getUpdates loop was also a real hazard here — it ran one
	// Telegram call per merchant every 12s, and for a platform-bot merchant it
	// polled a bot that has a webhook registered, which Telegram rejects.
	go a.loop(ctx, 15*time.Minute, a.sendDueDigests)
}

// checkPlatformHealth pages the operator when a dependency is down, and clears
// the alert once it recovers so a later outage pages again.
func (a *App) checkPlatformHealth(ctx context.Context) {
	if a.Pipe == nil || !a.Pipe.PlatformBotEnabled() {
		return
	}
	dbOK := a.DB.Ping(ctx) == nil
	redisOK := a.Redis != nil && a.Redis.Ping(ctx)

	if dbOK {
		a.Pipe.PlatformAlertResolved(ctx, "health-db")
	} else {
		a.Pipe.PlatformAlert(ctx, "health-db", "数据库不可用",
			"Postgres 连接失败 — 登录、收件箱、消息管道全部受影响")
	}
	if redisOK {
		a.Pipe.PlatformAlertResolved(ctx, "health-redis")
	} else {
		a.Pipe.PlatformAlert(ctx, "health-redis", "Redis 不可用",
			"限流、实时推送、会话缓存受影响")
	}
}

// pruneStaleSupportRelays drops reply mappings nobody ever answered. 30 days is
// far beyond any realistic reply window, and the inbox row itself is kept — the
// mapping is just dead weight once it can no longer be replied to.
func (a *App) pruneStaleSupportRelays(ctx context.Context) {
	res, err := a.DB.Exec(ctx,
		"DELETE FROM platform_support_relay WHERE created_at < NOW() - INTERVAL '30 days'")
	if err != nil {
		return
	}
	if n := res.RowsAffected(); n > 0 {
		a.Logger.Info("pruned stale support relays", "count", n)
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
	rows, err := a.DB.Query(ctx, "SELECT n.user_id FROM telegram_notify_settings n JOIN users u ON u.user_id = n.user_id AND u.is_active")
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
	// An iteration error is not "end of rows". Unchecked, a connection that
	// dropped mid-scan would quietly shrink the recipient list and the rest of
	// the tenants would just not get their digest, with nothing in the log.
	if err := rows.Err(); err != nil {
		a.Logger.Warn("digest recipient scan failed", "error", err.Error())
		return
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
	id, userID, configID                                                             int32
	platform, templateName, templateLanguage, recipientFilter, tagFilter, bodyParams string
}

// dispatchDueCampaigns turns due scheduled campaigns into outbound template
// deliveries for recipients that already have a session on the platform.
// Idempotent: a campaign moves scheduled -> sending -> done.
func (a *App) dispatchDueCampaigns(ctx context.Context) {
	rows, err := a.DB.Query(ctx, `SELECT c.campaign_id, c.user_id, c.platform::text, c.config_id, c.template_name,
		c.template_language, c.body_params, c.recipient_filter, c.tag_filter
		FROM marketing_campaigns c JOIN users u ON u.user_id = c.user_id AND u.is_active
		WHERE c.status='scheduled' AND c.scheduled_at <= NOW() LIMIT 10`)
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
	// Same reasoning as sendDueDigests: a half-read batch would leave campaigns
	// scheduled and silently skip them until the next tick, hiding the cause.
	if err := rows.Err(); err != nil {
		a.Logger.Warn("campaign scan failed", "error", err.Error())
		return
	}

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
	if err := rows.Err(); err != nil {
		// Send nobody rather than a partial batch: the caller marks the
		// campaign done with whatever count comes back, so half a recipient
		// list would be recorded as a complete delivery.
		a.Logger.Warn("campaign recipient scan failed", "campaign_id", c.id, "error", err.Error())
		return 0
	}

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
	userID, slaID     int32
	firstSecs         int
	resolutionSecs    *int
	businessHoursOnly bool
}

// scanSLABreaches records first_response and resolution breaches for active
// sessions against the tenant's active SLA policies. Idempotent via UNIQUE
// (session_id, breach_type).
func (a *App) scanSLABreaches(ctx context.Context) {
	rows, err := a.DB.Query(ctx,
		"SELECT p.user_id, p.sla_id, p.first_response_secs, p.resolution_secs, p.business_hours_only FROM sla_policies p JOIN users u ON u.user_id = p.user_id AND u.is_active WHERE p.is_active = true")
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
	if err := rows.Err(); err != nil {
		// A truncated policy list means some tenant's overdue sessions are
		// never scanned. Say so: the next tick retries, but the gap has to be
		// visible instead of looking like "no breaches".
		a.Logger.Warn("sla policy scan failed", "error", err.Error())
		return
	}

	for _, p := range pols {
		if p.firstSecs > 0 {
			// make_interval, not ($2 || ' seconds')::interval: Postgres resolves
			// the parameter of a `||` expression as text (OID 25), and pgx's
			// extended protocol cannot encode a Go int into a text parameter —
			// the query failed to send on every single scan
			// ("unable to encode 300 into text format for text"). psql PREPARE
			// and sqlcheck both pass, because neither exercises parameter
			// encoding; only a real bound execution does. The `if err == nil`
			// below used to swallow the failure, so the whole SLA engine was a
			// silent no-op.
			sess, err := a.DB.Query(ctx, `SELECT session_id FROM sessions WHERE user_id=$1 AND status IN ('active','handoff')
				AND first_response_at IS NULL AND created_at < NOW() - make_interval(secs => $2)`, p.userID, p.firstSecs)
			if err != nil {
				a.Logger.Warn("sla first-response scan failed", "user_id", p.userID, "error", err.Error())
			}
			if err == nil {
				for sess.Next() {
					var sid string
					if sess.Scan(&sid) == nil {
						tag, ierr := a.DB.Exec(ctx,
							"INSERT INTO sla_breaches (user_id, session_id, sla_id, breach_type) VALUES ($1,$2,$3,'first_response') ON CONFLICT (session_id, breach_type) DO NOTHING",
							p.userID, sid, p.slaID)
						if ierr != nil {
							a.Logger.Warn("sla breach insert failed", "session_id", sid, "error", ierr.Error())
							continue
						}
						if tag.RowsAffected() > 0 {
							a.notifyUser(ctx, p.userID, "sla", "SLA 违约：首次响应超时", "A session breached the first-response SLA", sid)
						}
					}
				}
				if serr := sess.Err(); serr != nil {
					// Breaches missed here are the expensive kind: an SLA clock
					// nobody gets alerted about. The scan retries next tick, but
					// the partial pass must not look clean.
					a.Logger.Warn("sla first-response scan incomplete", "user_id", p.userID, "error", serr.Error())
				}
				sess.Close()
			}
		}
		if p.resolutionSecs != nil && *p.resolutionSecs > 0 {
			// Same make_interval reasoning as the first-response scan above.
			// NOTE: this branch records the breach but sends no notification —
			// the first-response branch does. Asymmetric on purpose or not is
			// an open question; see docs/DEVELOPMENT.md.
			sess, err := a.DB.Query(ctx, `SELECT session_id FROM sessions WHERE user_id=$1 AND status IN ('active','handoff')
				AND resolved_at IS NULL AND created_at < NOW() - make_interval(secs => $2)`, p.userID, *p.resolutionSecs)
			if err != nil {
				a.Logger.Warn("sla resolution scan failed", "user_id", p.userID, "error", err.Error())
			}
			if err == nil {
				for sess.Next() {
					var sid string
					if sess.Scan(&sid) == nil {
						_, ierr := a.DB.Exec(ctx,
							"INSERT INTO sla_breaches (user_id, session_id, sla_id, breach_type) VALUES ($1,$2,$3,'resolution') ON CONFLICT (session_id, breach_type) DO NOTHING",
							p.userID, sid, p.slaID)
						if ierr != nil {
							a.Logger.Warn("sla resolution breach insert failed", "session_id", sid, "error", ierr.Error())
						}
					}
				}
				if serr := sess.Err(); serr != nil {
					a.Logger.Warn("sla resolution scan incomplete", "user_id", p.userID, "error", serr.Error())
				}
				sess.Close()
			}
		}
	}
}

// resetBillingCycles rolls over lapsed billing cycles (reset usage counters).
func (a *App) resetBillingCycles(ctx context.Context) {
	_, _ = a.DB.Exec(ctx,
		"UPDATE tenant_billing SET messages_used=0, docs_used=0, cycle_start=NOW(), cycle_end=NOW()+INTERVAL '30 days' WHERE cycle_end <= NOW()")
}
