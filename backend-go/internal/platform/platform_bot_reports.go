package platform

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Operator console reports. Every query here reads across all tenants, which is
// why the console sits behind the allow-list and writes to audit_logs.

// adminStatusReport — health and queue depth. The first thing to ask for when
// something feels wrong.
func (p *Pipeline) adminStatusReport(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("🩺 RelayChat status\n\n")

	dbOK := p.DB.Ping(ctx) == nil
	redisOK := p.Redis != nil && p.Redis.Ping(ctx)
	b.WriteString(fmt.Sprintf("db     %s\nredis  %s\n", mark(dbOK), mark(redisOK)))

	var inboundPending, inboundFailed, outboundPending, outboundFailed int64
	_ = p.DB.QueryRow(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE status IN ('pending','processing')),
		   COUNT(*) FILTER (WHERE status = 'failed')
		 FROM platform_inbound_events`).Scan(&inboundPending, &inboundFailed)
	_ = p.DB.QueryRow(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE status IN ('pending','processing')),
		   COUNT(*) FILTER (WHERE status = 'failed')
		 FROM platform_outbox`).Scan(&outboundPending, &outboundFailed)

	var activeSessions, msgsToday int64
	_ = p.DB.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE status = 'active' AND is_test = false`).Scan(&activeSessions)
	_ = p.DB.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE created_at >= CURRENT_DATE`).Scan(&msgsToday)

	b.WriteString(fmt.Sprintf(
		"\n收件队列   %d 待处理 / %d 失败\n发件队列   %d 待发 / %d 失败\n进行中会话 %d\n今日消息   %d",
		inboundPending, inboundFailed, outboundPending, outboundFailed, activeSessions, msgsToday))

	// A channel marked errored is the most common silent breakage: the merchant
	// never notices until a customer complains.
	var brokenChannels int64
	_ = p.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM platform_connection_health WHERE status = 'error'`).Scan(&brokenChannels)
	if brokenChannels > 0 {
		b.WriteString(fmt.Sprintf("\n\n⚠️ %d 个渠道连接异常 — 见 /platforms", brokenChannels))
	}
	return b.String()
}

// adminTenantsReport — merchants ranked by activity.
func (p *Pipeline) adminTenantsReport(ctx context.Context) string {
	rows, err := p.DB.Query(ctx,
		`SELECT u.user_id, u.username, COALESCE(b.plan,'free'), u.is_active,
		        COALESCE(b.messages_used,0), COALESCE(b.message_quota,0),
		        (SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.user_id AND s.is_test = false)
		 FROM users u
		 LEFT JOIN tenant_billing b ON b.user_id = u.user_id
		 WHERE u.role <> 'platform_admin'
		 ORDER BY 7 DESC, u.user_id LIMIT 12`)
	if err != nil {
		return "查询失败：" + err.Error()
	}
	defer rows.Close()

	var b strings.Builder
	b.WriteString("🏢 商家（按会话数）\n\n")
	n := 0
	for rows.Next() {
		var id int32
		var name, plan string
		var active bool
		var used, quota, sessions int64
		if rows.Scan(&id, &name, &plan, &active, &used, &quota, &sessions) != nil {
			continue
		}
		n++
		flag := ""
		if !active {
			flag = " ⛔"
		}
		b.WriteString(fmt.Sprintf("#%d %s%s\n   %s · %d 会话 · %d/%d 消息\n",
			id, name, flag, plan, sessions, used, quota))
	}
	if n == 0 {
		return "还没有商家。"
	}
	b.WriteString("\n/tenant <id> 看详情")
	return b.String()
}

// adminTenantReport — one merchant in detail.
func (p *Pipeline) adminTenantReport(ctx context.Context, userID int32) string {
	var name, role, plan string
	var active bool
	var createdAt time.Time
	err := p.DB.QueryRow(ctx,
		`SELECT u.username, u.role::text, u.is_active, u.created_at, COALESCE(b.plan,'free')
		 FROM users u LEFT JOIN tenant_billing b ON b.user_id = u.user_id
		 WHERE u.user_id = $1`, userID).Scan(&name, &role, &active, &createdAt, &plan)
	if err != nil {
		return fmt.Sprintf("找不到商家 #%d", userID)
	}

	var sessions, messages, tokens int64
	var cost float64
	_ = p.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND is_test = false`, userID).Scan(&sessions)
	_ = p.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM chat_messages m JOIN sessions s ON s.session_id = m.session_id
		 WHERE s.user_id = $1`, userID).Scan(&messages)
	_ = p.DB.QueryRow(ctx,
		`SELECT COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_estimate),0)
		 FROM token_usage WHERE user_id = $1`, userID).Scan(&tokens, &cost)

	// Channels are the part most likely to be misconfigured, so surface them.
	rows, _ := p.DB.Query(ctx,
		`SELECT platform::text, is_active FROM platform_configs WHERE user_id = $1 ORDER BY platform`, userID)
	channels := ""
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var plat string
			var on bool
			if rows.Scan(&plat, &on) == nil {
				channels += plat + " " + mark(on) + "  "
			}
		}
	}
	if channels == "" {
		channels = "（未连接任何渠道）"
	}

	return fmt.Sprintf(
		"🏢 #%d %s\n\n角色   %s\n状态   %s\n套餐   %s\n注册   %s\n\n会话   %d\n消息   %d\nToken  %d (≈$%.4f)\n\n渠道   %s",
		userID, name, role, mark(active), plan,
		createdAt.In(PhnomPenhLoc()).Format("2006-01-02"),
		sessions, messages, tokens, cost, channels)
}

// adminDigestReport — the day's numbers at a glance.
func (p *Pipeline) adminDigestReport(ctx context.Context) string {
	var newUsers, activeMerchants, msgs, aiReplies, handoffs int64
	_ = p.DB.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE created_at >= CURRENT_DATE`).Scan(&newUsers)
	_ = p.DB.QueryRow(ctx,
		`SELECT COUNT(DISTINCT s.user_id) FROM chat_messages m JOIN sessions s ON s.session_id = m.session_id
		 WHERE m.created_at >= CURRENT_DATE`).Scan(&activeMerchants)
	_ = p.DB.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE created_at >= CURRENT_DATE`).Scan(&msgs)
	_ = p.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM chat_messages WHERE role = 'model' AND created_at >= CURRENT_DATE`).Scan(&aiReplies)
	_ = p.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM human_handoff_requests WHERE created_at >= CURRENT_DATE`).Scan(&handoffs)

	var tokens, cost float64
	var tokenCount int64
	_ = p.DB.QueryRow(ctx,
		`SELECT COALESCE(SUM(total_tokens),0)::float8, COALESCE(SUM(cost_estimate),0)
		 FROM token_usage WHERE created_at >= CURRENT_DATE`).Scan(&tokens, &cost)
	_ = p.DB.QueryRow(ctx, `SELECT COUNT(*) FROM token_usage WHERE created_at >= CURRENT_DATE`).Scan(&tokenCount)

	return fmt.Sprintf(
		"📊 今日 · %s\n\n新注册    %d\n活跃商家  %d\n消息总数  %d\nAI 回复   %d\n转人工    %d\n\nAI 调用   %d 次\nToken     %.0f\n成本      ≈$%.4f",
		time.Now().In(PhnomPenhLoc()).Format("2006-01-02"),
		newUsers, activeMerchants, msgs, aiReplies, handoffs, tokenCount, tokens, cost)
}

func mark(ok bool) string {
	if ok {
		return "✅"
	}
	return "❌"
}
