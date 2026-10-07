package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ============================================
// Platform operations surfaces (read-only)
// ============================================
//
// Four views an operator needs before anything else, all over data the platform
// already collects:
//
//	support  — merchants messaging the platform bot (platform_support_messages)
//	revenue  — captured PayPal money + who is about to lapse (payments, tenant_billing)
//	channels — every tenant's channel state and outbound backlog (health, outbox)
//	todo     — the counts of the three above, plus quota pressure
//
// The reply to a support message happens in Telegram (the bot's reply_to_message
// branch writes replied_at/replied_by), so this surface is a read: who is
// waiting, for how long, and which tenant they belong to.
//
// The todo counts use the same predicates as the detail endpoints — a summary
// that disagrees with its own detail view is worse than no summary. The test
// file reconciles them against a real database.

// Todo/summary predicates. Kept as constants next to the queries that use them
// so a change to one view is visible to the others.
const (
	// A channel is "in error" when its health row says something other than
	// connected — including a config that has never been checked (no row).
	sqlChannelErrorPredicate = "COALESCE(h.status,'unknown') <> 'connected'"
	// Outbox rows that are still on their way vs. given up on.
	sqlOutboxBacklogPredicate = "status IN ('pending','processing')"
	sqlOutboxFailedPredicate  = "status = 'failed'"
	sqlOutboxDonePredicate    = "status IN ('sent','cancelled')"
	// Payment lapse window: anything due inside two weeks (already-overdue rows
	// included — an expired paid_until is the most urgent of them).
	paymentLapseDays = 14
)

// platformTodo — the "what needs me today" summary.
func (a *App) platformTodo(w http.ResponseWriter, r *http.Request) (any, error) {
	var (
		supportOpen, channelErrors, outboxFailed, outboxBacklog int64
		expiringPaid, quotaPressure                             int64
	)
	// One round trip: the six numbers are read together so they describe the
	// same instant.
	//
	// The lapse window uses make_interval(days => $1) rather than the
	// ($1 || ' days')::interval shape that rag/service.go uses: with an `unknown`
	// parameter Postgres resolves `||` to text || text, so the server reports $1
	// as text and pgx then refuses the Go int ("cannot find encode plan"). That
	// shape is fine there only because the caller passes strconv.FormatInt — a
	// string. The int-safe form is this one (analytics_handlers.go uses it too).
	err := a.DB.QueryRow(r.Context(), `SELECT
			(SELECT count(*) FROM platform_support_messages WHERE replied_at IS NULL),
			(SELECT count(*) FROM platform_configs c
				LEFT JOIN platform_connection_health h ON h.config_id = c.config_id
				WHERE c.is_active AND `+sqlChannelErrorPredicate+`),
			(SELECT count(*) FROM platform_outbox WHERE `+sqlOutboxFailedPredicate+`),
			(SELECT count(*) FROM platform_outbox WHERE `+sqlOutboxBacklogPredicate+`),
			(SELECT count(*) FROM tenant_billing
				WHERE paid_until IS NOT NULL AND paid_until < NOW() + make_interval(days => $1)),
			(SELECT count(*) FROM tenant_billing
				WHERE monthly_message_quota > 0 AND messages_used::float / monthly_message_quota > 0.8)`,
		paymentLapseDays).
		Scan(&supportOpen, &channelErrors, &outboxFailed, &outboxBacklog, &expiringPaid, &quotaPressure)
	if err != nil {
		a.Logger.Error("platform todo query failed", "error", err.Error())
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{
		"support_open":   supportOpen,
		"channel_errors": channelErrors,
		"outbox_failed":  outboxFailed,
		"outbox_backlog": outboxBacklog,
		"expiring_paid":  expiringPaid,
		"quota_pressure": quotaPressure,
		"lapse_days":     paymentLapseDays,
	}, nil
}

// platformSupport — the merchant support inbox.
func (a *App) platformSupport(w http.ResponseWriter, r *http.Request) (any, error) {
	status := r.URL.Query().Get("status")
	if status != "all" {
		status = "open"
	}
	var open, total int64
	if err := a.DB.QueryRow(r.Context(),
		"SELECT count(*) FILTER (WHERE replied_at IS NULL), count(*) FROM platform_support_messages").
		Scan(&open, &total); err != nil {
		return nil, ErrInternal("查询失败")
	}

	rows, err := a.DB.Query(r.Context(), `SELECT m.message_id, COALESCE(m.user_id,0), COALESCE(u.username,''),
			COALESCE(m.display_name,''), COALESCE(m.username,''), COALESCE(b.plan,''),
			m.body, m.replied_at, COALESCE(m.replied_by::text,''), m.created_at, COALESCE(m.telegram_user_id::text,'')
		FROM platform_support_messages m
		LEFT JOIN users u ON u.user_id = m.user_id
		LEFT JOIN tenant_billing b ON b.user_id = m.user_id
		WHERE ($1 = 'all' OR m.replied_at IS NULL)
		ORDER BY (m.replied_at IS NULL) DESC, m.created_at DESC
		LIMIT 200`, status)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id                                                             int64
			uid                                                            int32
			tenantUsername, displayName, tgUsername, plan, body, repliedBy string
			telegramID                                                     string
			repliedAt                                                      *time.Time
			createdAt                                                      time.Time
		)
		if rows.Scan(&id, &uid, &tenantUsername, &displayName, &tgUsername, &plan, &body, &repliedAt, &repliedBy, &createdAt, &telegramID) != nil {
			continue
		}
		out = append(out, map[string]any{
			"message_id": id, "user_id": uid, "tenant_username": tenantUsername,
			"display_name": displayName, "telegram_username": tgUsername, "plan": plan,
			"body": body, "replied_at": repliedAt, "replied_by": repliedBy,
			"created_at": createdAt, "telegram_user_id": telegramID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{"open": open, "total": total, "data": out}, nil
}

// platformRevenue — captured money, and who is about to lapse.
func (a *App) platformRevenue(w http.ResponseWriter, r *http.Request) (any, error) {
	var captured, pending int64
	var gross float64
	var paidTenants int64
	if err := a.DB.QueryRow(r.Context(), `SELECT
			count(*) FILTER (WHERE status = 'captured'),
			COALESCE(SUM(amount) FILTER (WHERE status = 'captured'),0)::float8,
			count(DISTINCT user_id) FILTER (WHERE status = 'captured'),
			count(*) FILTER (WHERE status <> 'captured')
		FROM payments WHERE provider = 'paypal'`).
		Scan(&captured, &gross, &paidTenants, &pending); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// Paid tenants with their lapse window. `plan <> 'free'` is the billing
	// plan, not the promise: a plan can be granted by hand (no paid_until), so
	// the row carries a state the console can render without doing its own date
	// math — the same classification the summary counts use, decided once here.
	expiring := make([]map[string]any, 0)
	rows, err := a.DB.Query(r.Context(), `SELECT b.user_id, u.username, b.plan, b.paid_until,
			b.messages_used, b.monthly_message_quota
		FROM tenant_billing b JOIN users u ON u.user_id = b.user_id
		WHERE b.plan <> 'free'
		ORDER BY b.paid_until NULLS LAST, b.user_id`)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	var expiringSoon, overdue int64
	now := time.Now()
	for rows.Next() {
		var (
			uid            int32
			username, plan string
			paidUntil      *time.Time
			used, quota    int64
		)
		if rows.Scan(&uid, &username, &plan, &paidUntil, &used, &quota) != nil {
			continue
		}
		state := "active"
		switch {
		case paidUntil == nil:
			// Hand-granted plan: no clock, nothing to chase.
			state = "no_clock"
		case paidUntil.Before(now):
			state = "overdue"
			overdue++
		case paidUntil.Before(now.AddDate(0, 0, paymentLapseDays)):
			state = "expiring"
			expiringSoon++
		}
		expiring = append(expiring, map[string]any{
			"user_id": uid, "username": username, "plan": plan,
			"paid_until": paidUntil, "messages_used": used, "message_quota": quota,
			"state": state,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}

	recent := make([]map[string]any, 0)
	prows, err := a.DB.Query(r.Context(), `SELECT p.payment_id, p.user_id, COALESCE(u.username,''),
			p.plan, COALESCE(p.amount,0)::float8, COALESCE(p.currency,''), p.status, p.created_at
		FROM payments p LEFT JOIN users u ON u.user_id = p.user_id
		ORDER BY p.created_at DESC LIMIT 20`)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	for prows.Next() {
		var (
			pid            int64
			uid            int32
			username, plan string
			amount         float64
			currency, st   string
			createdAt      time.Time
		)
		if prows.Scan(&pid, &uid, &username, &plan, &amount, &currency, &st, &createdAt) != nil {
			continue
		}
		recent = append(recent, map[string]any{
			"payment_id": pid, "user_id": uid, "username": username, "plan": plan,
			"amount": amount, "currency": currency, "status": st, "created_at": createdAt,
		})
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}

	// Recurring revenue from the configured price list — an estimate by
	// construction (the operator sees the same numbers the checkout charges), and
	// empty when a tier is not for sale.
	//
	// Only tenants with time left on the clock count: a hand-granted plan
	// (no paid_until) never paid and an already-lapsed one is not recurring
	// revenue in force. The first production run summed a granted tenant and
	// inflated the figure by a third — the tile is read as "money coming in",
	// not as "plan list price".
	prices := map[string]float64{}
	if v := a.priceOf("pro"); v > 0 {
		prices["pro"] = v
	}
	if v := a.priceOf("enterprise"); v > 0 {
		prices["enterprise"] = v
	}
	var mrr float64
	for _, row := range expiring {
		if state, _ := row["state"].(string); state != "active" && state != "expiring" {
			continue
		}
		plan, _ := row["plan"].(string)
		if p, ok := prices[plan]; ok {
			mrr += p
		}
	}

	return map[string]any{
		"captured_payments": captured,
		"gross_usd":         gross,
		"paid_tenants":      paidTenants,
		"pending_payments":  pending,
		"mrr_estimate_usd":  mrr,
		"prices":            prices,
		"expiring_soon":     expiringSoon,
		"overdue":           overdue,
		"lapse_days":        paymentLapseDays,
		"tenants":           expiring,
		"recent":            recent,
	}, nil
}

// priceOf reads one tier's configured price ("" or unparseable = not for sale).
func (a *App) priceOf(plan string) float64 {
	var raw string
	switch plan {
	case "pro":
		raw = a.Cfg.PayPal.PricePro
	case "enterprise":
		raw = a.Cfg.PayPal.PriceEnterprise
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return f
}

// platformChannels — every tenant's channel state plus its outbound backlog.
func (a *App) platformChannels(w http.ResponseWriter, r *http.Request) (any, error) {
	rows, err := a.DB.Query(r.Context(), `SELECT c.config_id, c.user_id, COALESCE(u.username,''),
			c.platform::text, c.is_active,
			COALESCE(h.status,'unknown'), COALESCE(h.detail,''), COALESCE(h.account_name,''), h.checked_at,
			-- Three scalar subqueries rather than one lateral aggregate: each shape is
			-- short and the SQL gate can validate them one by one. Collapse them into a
			-- single lateral aggregate if a deployment ever runs hundreds of channels.
			(SELECT count(*) FROM platform_outbox o WHERE o.config_id = c.config_id AND `+sqlOutboxBacklogPredicate+`),
			(SELECT count(*) FROM platform_outbox o WHERE o.config_id = c.config_id AND `+sqlOutboxFailedPredicate+`),
			COALESCE((SELECT o.last_error FROM platform_outbox o
				WHERE o.config_id = c.config_id AND o.last_error <> ''
				ORDER BY o.updated_at DESC LIMIT 1),''),
			(SELECT max(o.created_at) FROM platform_outbox o
				WHERE o.config_id = c.config_id AND o.last_error <> '')
		FROM platform_configs c
		JOIN users u ON u.user_id = c.user_id
		LEFT JOIN platform_connection_health h ON h.config_id = c.config_id
		WHERE c.is_active
		ORDER BY (COALESCE(h.status,'unknown') <> 'connected') DESC, c.config_id`)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	var errors, backlog, failed int64
	for rows.Next() {
		var (
			configID, uid        int32
			username, platform   string
			isActive             bool
			status, detail, acct string
			checkedAt            *time.Time
			pending, failedRows  int64
			lastError            string
			lastErrorAt          *time.Time
		)
		if rows.Scan(&configID, &uid, &username, &platform, &isActive, &status, &detail, &acct, &checkedAt,
			&pending, &failedRows, &lastError, &lastErrorAt) != nil {
			continue
		}
		if status != "connected" {
			errors++
		}
		backlog += pending
		failed += failedRows
		out = append(out, map[string]any{
			"config_id": configID, "user_id": uid, "username": username, "platform": platform,
			"is_active": isActive, "status": status, "detail": detail, "account_name": acct,
			"checked_at": checkedAt, "outbox_pending": pending, "outbox_failed": failedRows,
			"last_error": lastError, "last_error_at": lastErrorAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{
		"data": out, "errors": errors, "outbox_pending": backlog, "outbox_failed": failed,
	}, nil
}
