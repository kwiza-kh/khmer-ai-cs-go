package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// todoCounts — the six numbers behind both the console's "what needs me today"
// card and the operator's daily digest. One query, one meaning: a digest that
// disagreed with the card the operator opens would be worse than no digest.
type todoCounts struct {
	SupportOpen   int64
	ChannelErrors int64
	OutboxFailed  int64
	OutboxBacklog int64
	ExpiringPaid  int64
	QuotaPressure int64
}

func (a *App) todoCounts(ctx context.Context) (todoCounts, error) {
	var c todoCounts
	// One round trip: the six numbers are read together so they describe the
	// same instant.
	//
	// The lapse window uses make_interval(days => $1) rather than the
	// ($1 || ' days')::interval shape that rag/service.go uses: with an `unknown`
	// parameter Postgres resolves `||` to text || text, so the server reports $1
	// as text and pgx then refuses the Go int ("cannot find encode plan"). That
	// shape is fine there only because the caller passes strconv.FormatInt — a
	// string. The int-safe form is this one (analytics_handlers.go uses it too).
	err := a.DB.QueryRow(ctx, `SELECT
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
		Scan(&c.SupportOpen, &c.ChannelErrors, &c.OutboxFailed, &c.OutboxBacklog, &c.ExpiringPaid, &c.QuotaPressure)
	return c, err
}

// needsAttention reports whether any counter would make an operator act. A digest
// that fires every day with six zeroes trains the operator to ignore it — and the
// queued-sends count alone is not worth a page (it drains on its own).
func (c todoCounts) needsAttention() bool {
	return c.SupportOpen+c.ChannelErrors+c.OutboxFailed+c.ExpiringPaid+c.QuotaPressure > 0
}

// formatDigest renders the operator's daily summary. Pure, so the thresholds and
// the wording are testable without a database or a Telegram chat.
func formatDigest(c todoCounts) (title, detail string, send bool) {
	if !c.needsAttention() {
		return "", "", false
	}
	lines := make([]string, 0, 5)
	if c.SupportOpen > 0 {
		lines = append(lines, fmt.Sprintf("• 商家消息待回复：%d", c.SupportOpen))
	}
	if c.ChannelErrors > 0 {
		lines = append(lines, fmt.Sprintf("• 渠道异常：%d", c.ChannelErrors))
	}
	if c.OutboxFailed > 0 {
		lines = append(lines, fmt.Sprintf("• 发送失败（重试已耗尽）：%d", c.OutboxFailed))
	}
	if c.ExpiringPaid > 0 {
		lines = append(lines, fmt.Sprintf("• 付费即将/已过期（%d 天内）：%d", paymentLapseDays, c.ExpiringPaid))
	}
	if c.QuotaPressure > 0 {
		lines = append(lines, fmt.Sprintf("• 额度已用 >80%%：%d", c.QuotaPressure))
	}
	detail = strings.Join(lines, "\n") +
		"\n\n打开平台台（总览 → 今天要处理）看明细；渠道页可一键重试失败的发送。"
	return "平台今日待处理", detail, true
}

// platformTodo — the "what needs me today" summary card.
func (a *App) platformTodo(w http.ResponseWriter, r *http.Request) (any, error) {
	counts, err := a.todoCounts(r.Context())
	if err != nil {
		a.Logger.Error("platform todo query failed", "error", err.Error())
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{
		"support_open":   counts.SupportOpen,
		"channel_errors": counts.ChannelErrors,
		"outbox_failed":  counts.OutboxFailed,
		"outbox_backlog": counts.OutboxBacklog,
		"expiring_paid":  counts.ExpiringPaid,
		"quota_pressure": counts.QuotaPressure,
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

// execer is the slice of pgxpool.Pool / pgx.Tx these flips need, so a test can
// drive them inside a transaction that never commits.
// execer is the one method the retry lane needs from the pool: a seam that lets the
// retry accounting be tested without issuing writes against a live database.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ============================================
// Tenant health + cross-tenant knowledge gaps
// ============================================

// gapWindowDays bounds the unanswered-question window used by both the tenant
// health card and the cross-tenant report.
const gapWindowDays = 30

// tenantHealth answers "what is the state of this tenant" in one object: its
// channels (with backlog), its knowledge base (with index state), its seats, and
// whether anything moved recently. Every number is read, none is inferred.
//
// The detail pane used to show session rollups only: the operator could see that
// a tenant existed, but not that its channel was broken, its documents failed to
// index, or nobody had logged in for a month.
func (a *App) tenantHealth(ctx context.Context, userID int32) map[string]any {
	health := map[string]any{}

	// Channels + backlog. A config without a health row reports 'unknown', the
	// same way the fleet-wide channels view does.
	channels := make([]map[string]any, 0)
	if crows, err := a.DB.Query(ctx, `SELECT c.config_id, c.platform::text, COALESCE(h.status,'unknown'),
			COALESCE(h.account_name,''), h.checked_at,
			(SELECT count(*) FROM platform_outbox o WHERE o.config_id = c.config_id AND `+sqlOutboxBacklogPredicate+`),
			(SELECT count(*) FROM platform_outbox o WHERE o.config_id = c.config_id AND `+sqlOutboxFailedPredicate+`)
		FROM platform_configs c
		LEFT JOIN platform_connection_health h ON h.config_id = c.config_id
		WHERE c.user_id = $1 AND c.is_active
		ORDER BY c.config_id`, userID); err == nil {
		for crows.Next() {
			var (
				configID        int32
				platform, state string
				accountName     string
				checkedAt       *time.Time
				pending, failed int64
			)
			if crows.Scan(&configID, &platform, &state, &accountName, &checkedAt, &pending, &failed) == nil {
				channels = append(channels, map[string]any{
					"config_id": configID, "platform": platform, "status": state,
					"account_name": accountName, "checked_at": checkedAt,
					"outbox_pending": pending, "outbox_failed": failed,
				})
			}
		}
		crows.Close()
	}
	health["channels"] = channels

	// Knowledge base: how much, and in what index state. The state breakdown comes
	// back grouped and uninterpreted — the vocabulary lives in the ingest path, so
	// a new state must appear here without a second code change.
	var docs, chunks int64
	var lastUpload *time.Time
	_ = a.DB.QueryRow(ctx, `SELECT count(*), COALESCE(SUM(chunk_count),0), MAX(created_at)
		FROM knowledge_documents WHERE uploaded_by = $1`, userID).Scan(&docs, &chunks, &lastUpload)
	indexStates := map[string]int64{}
	if srows, err := a.DB.Query(ctx, `SELECT COALESCE(index_status,'unknown'), count(*)
		FROM knowledge_documents WHERE uploaded_by = $1 GROUP BY 1`, userID); err == nil {
		for srows.Next() {
			var state string
			var n int64
			if srows.Scan(&state, &n) == nil {
				indexStates[state] = n
			}
		}
		srows.Close()
	}
	health["knowledge"] = map[string]any{
		"documents": docs, "chunks": chunks, "last_upload_at": lastUpload, "index_states": indexStates,
	}

	// Seats: the owner is implicit, agent_teams rows are the invited members.
	var seats, invites int64
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM agent_teams WHERE owner_user_id = $1 AND is_active", userID).Scan(&seats)
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM team_invites WHERE owner_user_id = $1", userID).Scan(&invites)
	health["seats"] = map[string]any{"active": seats, "invites": invites}

	// Activity: is this tenant alive? Counts for the last week plus the newest of
	// each, because "0 sessions in 7 days" and "never had a session" are
	// different conversations.
	var sessions7, messages7 int64
	var lastSession, lastMessage *time.Time
	_ = a.DB.QueryRow(ctx, `SELECT count(*) FROM sessions
		WHERE user_id = $1 AND is_test = FALSE AND created_at > NOW() - INTERVAL '7 days'`, userID).Scan(&sessions7)
	_ = a.DB.QueryRow(ctx, `SELECT count(*) FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id
		WHERE s.user_id = $1 AND s.is_test = FALSE AND cm.created_at > NOW() - INTERVAL '7 days'`, userID).Scan(&messages7)
	_ = a.DB.QueryRow(ctx, "SELECT MAX(created_at) FROM sessions WHERE user_id = $1 AND is_test = FALSE", userID).Scan(&lastSession)
	_ = a.DB.QueryRow(ctx, `SELECT MAX(cm.created_at) FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id
		WHERE s.user_id = $1 AND s.is_test = FALSE`, userID).Scan(&lastMessage)
	health["activity"] = map[string]any{
		"sessions_7d": sessions7, "messages_7d": messages7,
		"last_session_at": lastSession, "last_message_at": lastMessage,
	}

	// Unanswered questions in the window — the same predicate the cross-tenant
	// report uses, so the card's number matches the list it links to.
	var gaps int64
	_ = a.DB.QueryRow(ctx, `SELECT count(*) FROM rag_query_logs
		WHERE user_id = $1 AND NOT used_in_reply AND created_at > NOW() - make_interval(days => $2)`, userID, gapWindowDays).Scan(&gaps)
	health["gaps"] = gaps

	return health
}

// platformKnowledgeGaps — per tenant, the questions the knowledge base could not
// answer. This is the operator's advisory material: "add these two documents and
// most of this traffic is covered" is a conversation the console can start.
func (a *App) platformKnowledgeGaps(_ http.ResponseWriter, r *http.Request) (any, error) {
	days := daysParam(r, gapWindowDays)
	perTenant := parseIntOr(r.URL.Query().Get("per_tenant"), 3)
	if perTenant < 1 {
		perTenant = 1
	}
	if perTenant > 20 {
		perTenant = 20
	}

	// One query, ranked per tenant: a query per tenant is the N+1 shape this
	// console already had to collapse once.
	rows, err := a.DB.Query(r.Context(), `WITH gaps AS (
			SELECT l.user_id, l.query, COUNT(*) AS hits, MAX(l.created_at) AS last_seen
			FROM rag_query_logs l
			WHERE NOT l.used_in_reply AND l.created_at > NOW() - make_interval(days => $1)
			GROUP BY l.user_id, lower(l.query), l.query
		), ranked AS (
			SELECT g.*, ROW_NUMBER() OVER (PARTITION BY g.user_id ORDER BY g.hits DESC, g.last_seen DESC) AS rn,
			       COUNT(*) OVER (PARTITION BY g.user_id) AS total
			FROM gaps g
		)
		SELECT r.user_id, u.username, COALESCE(b.plan,'free'), r.total, r.query, r.hits, r.last_seen
		FROM ranked r
		JOIN users u ON u.user_id = r.user_id
		LEFT JOIN tenant_billing b ON b.user_id = r.user_id
		WHERE r.rn <= $2
		ORDER BY r.total DESC, r.hits DESC`, days, perTenant)
	if err != nil {
		a.Logger.Error("knowledge gaps query failed", "error", err.Error())
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()

	type tenantGaps struct {
		row       map[string]any
		questions []map[string]any
		total     int64
	}
	grouped := make([]tenantGaps, 0)
	byUser := map[int32]int{}
	var totalGaps int64
	for rows.Next() {
		var (
			uid                   int32
			username, plan, query string
			total, hits           int64
			lastSeen              time.Time
		)
		if rows.Scan(&uid, &username, &plan, &total, &query, &hits, &lastSeen) != nil {
			continue
		}
		idx, ok := byUser[uid]
		if !ok {
			grouped = append(grouped, tenantGaps{
				row:   map[string]any{"user_id": uid, "username": username, "plan": plan},
				total: total,
			})
			idx = len(grouped) - 1
			byUser[uid] = idx
			totalGaps += total
		}
		grouped[idx].questions = append(grouped[idx].questions, map[string]any{
			"query": query, "hits": hits, "last_seen": lastSeen,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrInternal("查询失败")
	}

	tenants := make([]map[string]any, 0, len(grouped))
	for _, g := range grouped {
		g.row["gap_total"] = g.total
		g.row["questions"] = g.questions
		tenants = append(tenants, g.row)
	}
	return map[string]any{
		"days": days, "per_tenant": perTenant,
		"tenants": tenants, "tenants_with_gaps": len(tenants), "total_gaps": totalGaps,
	}, nil
}

// retryFailedDeliveries puts every failed outbound delivery of one channel back
// in the queue, with a clean attempt budget.
//
// A failed row is otherwise dead forever: the retry endpoint is tenant-scoped, so
// the operator who just fixed the cause (Meta approved the tag, a credential was
// rotated) cannot act on someone else's tenant. Seen in production on
// 2026-10-06: two Meta sends stuck on the HUMAN_AGENT tag, visible in this
// console and fixable nowhere.
func retryFailedDeliveries(ctx context.Context, db execer, configID int32) (int64, error) {
	tag, err := db.Exec(ctx,
		"UPDATE platform_outbox SET status='pending', attempts=0, next_attempt_at=NOW(), locked_at=NULL, sent_at=NULL, last_error='', updated_at=NOW() "+
			"WHERE config_id = $1 AND status = 'failed'", configID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// platformRetryChannel — requeue this channel's failed sends (platformAdminOnly,
// and therefore audited: it writes to a tenant's queue).
func (a *App) platformRetryChannel(_ http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	n, err := retryFailedDeliveries(r.Context(), a.DB, configID)
	if err != nil {
		a.Logger.Error("retry failed deliveries failed", "config_id", configID, "error", err.Error())
		return nil, ErrInternal("重试失败")
	}
	return map[string]any{"requeued": n}, nil
}
