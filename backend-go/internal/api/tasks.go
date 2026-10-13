package api

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/scheduler"
)

// StartBackgroundTasks launches the periodic jobs that keep the platform
// alive: SLA breach scanning, knowledge URL freshness, and billing cycle
// rollover. These run for the lifetime of the process.
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
	go a.loop(ctx, 60*time.Second, a.scanSLABreaches)
	go a.loop(ctx, 1*time.Hour, a.resetBillingCycles)
	// Lapsed paid plans fall back to free. Entitlement is a timestamp on
	// tenant_billing (set by a PayPal capture), deliberately separate from the
	// 30-day usage cycle: a renewal extends paid_until, an expiry revokes the
	// plan, and a plan set by hand (paid_until NULL) is never touched here.
	go a.loop(ctx, 10*time.Minute, a.expirePaidPlans)
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

	// DB-backed scheduled jobs (migration 066): the jobs an operator or a tenant
	// created, as opposed to the fixed loops above. That is what makes a cadence
	// changeable — or a per-tenant job possible at all — without a deploy.
	//
	// The loops above deliberately keep running unchanged. Moving platform
	// maintenance into the table would make the SLA scan depend on a row
	// existing, and a missing row would stop it silently.
	//
	// Zero job types are seeded by any migration, so the map above exists for
	// rows an operator adds by hand. sla_scan is deliberately absent: the SLA
	// scan is global (it walks every tenant's policies; there is nothing
	// per-tenant to schedule) and the 60s loop already runs it, so a row would
	// only make the same scan run twice per tick.
	a.Scheduler = scheduler.NewManager(scheduler.NewPostgresStore(a.DB), scheduleLocation())
	for jobType, fn := range map[string]func(context.Context){
		"platform_health": a.checkPlatformHealth,
		"retention":       a.pruneStaleSupportRelays,
		"billing_reset":   a.resetBillingCycles,
		"billing_expiry":  a.expirePaidPlans,
		"digest":          a.sendDueDigests,
	} {
		fn := fn
		a.Scheduler.Register(jobType, func(ctx context.Context, _ scheduler.Job) error {
			// These handlers report failure by logging and by what they write;
			// none of them returns an error, so a run is recorded as ok.
			fn(ctx)
			return nil
		})
	}
	go a.loop(ctx, time.Minute, func(ctx context.Context) { a.Scheduler.RunDue(ctx) })
}

// scheduleLocation is the wall clock a daily job means. A digest is "08:00 where
// the merchant is", so the deployment names the zone with SCHEDULER_TZ; an unset
// or unloadable value falls back to UTC rather than failing to boot.
func scheduleLocation() *time.Location {
	if name := strings.TrimSpace(os.Getenv("SCHEDULER_TZ")); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
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

	a.sendOperatorDigest(ctx, day)
}

// sendOperatorDigest pushes the console's "what needs me today" card to the
// operator's alert chat, once a day.
//
// Until this existed the card only helped an operator who happened to open the
// console: a merchant message waiting for a reply, a channel in error, a payment
// about to lapse and a tenant at 90% of its quota all sat there silently.
//
// It shares the tenant digests' slot (08:00 Phnom Penh) and their Redis guard, so
// a deploy that restarts the process inside that hour neither sends it twice nor
// swallows the day's. The counts come from the same query the card uses, so the
// push cannot disagree with what the operator sees after opening it.
func (a *App) sendOperatorDigest(ctx context.Context, day string) {
	counts, err := a.todoCounts(ctx)
	if err != nil {
		// Auxiliary path: a failed read must not consume the day's guard, so a
		// later tick inside the same hour can still send it.
		a.Logger.Warn("operator digest counts failed", "error", err.Error())
		return
	}
	if !counts.needsAttention() {
		return
	}
	flag := "platform-digest:" + day
	ok, err := a.Redis.IncrWindow(ctx, flag, 1, 26*time.Hour)
	if err != nil || !ok {
		return
	}
	title, detail, send := formatDigest(counts)
	if !send {
		return
	}
	a.Pipe.PlatformAlert(ctx, "platform-digest-"+day, title, detail)
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

type slaPol struct {
	userID, slaID     int32
	priority          string
	firstSecs         int
	resolutionSecs    *int
	businessHoursOnly bool
}

// slaPriorityRank orders the values the enum actually allows (030:19):
// high > normal > all. Comparing the names directly only ever distinguished
// "high" from everything else, so 'normal' vs 'all' fell through to the clock
// comparison — and then to sla_id scan order (review, 2026-10-12).
func slaPriorityRank(priority string) int {
	switch priority {
	case "high":
		return 3
	case "normal":
		return 2
	default: // "all"
		return 1
	}
}

// preferSLA reports whether cand should represent the tenant for one clock
// instead of cur: the higher-priority policy wins, then the tighter clock, then
// the lower sla_id so the choice is stable across ticks.
func preferSLA(cand, cur slaPol, clock func(slaPol) int) bool {
	if c, u := slaPriorityRank(cand.priority), slaPriorityRank(cur.priority); c != u {
		return c > u
	}
	if c, u := clock(cand), clock(cur); c != u {
		return c < u
	}
	return cand.slaID < cur.slaID
}

func firstResponseClock(p slaPol) int { return p.firstSecs }

func resolutionClock(p slaPol) int {
	if p.resolutionSecs == nil {
		return 0
	}
	return *p.resolutionSecs
}

// scanSLABreaches records first_response and resolution breaches for active
// sessions against the tenant's active SLA policies. Idempotent via UNIQUE
// (session_id, breach_type).
func (a *App) scanSLABreaches(ctx context.Context) {
	rows, err := a.DB.Query(ctx,
		"SELECT p.user_id, p.sla_id, p.priority, p.first_response_secs, p.resolution_secs, p.business_hours_only FROM sla_policies p "+
			"JOIN users u ON u.user_id = p.user_id AND u.is_active WHERE p.is_active = true "+
			"ORDER BY p.user_id, p.sla_id")
	if err != nil {
		return
	}
	// sla_breaches is UNIQUE (session_id, breach_type), so per session only ONE
	// policy can own each breach type: a second active policy's INSERT ... SELECT
	// conflicted on the first policy's row, returned nothing, and therefore never
	// notified — silently, with sla_id recording whichever policy the planner
	// happened to return first. Dedupe per (tenant, breach type) instead of per
	// tenant: a tenant may legitimately run a first-response policy and a separate
	// resolution policy, and the two rows can never collide.
	first := map[int32]slaPol{}
	resolution := map[int32]slaPol{}
	order := []int32{}
	seenUser := map[int32]bool{}
	for rows.Next() {
		var p slaPol
		if rows.Scan(&p.userID, &p.slaID, &p.priority, &p.firstSecs, &p.resolutionSecs, &p.businessHoursOnly) != nil {
			continue
		}
		if !seenUser[p.userID] {
			seenUser[p.userID] = true
			order = append(order, p.userID)
		}
		if p.firstSecs > 0 {
			if cur, ok := first[p.userID]; !ok || preferSLA(p, cur, firstResponseClock) {
				first[p.userID] = p
			}
		}
		if resolutionClock(p) > 0 {
			if cur, ok := resolution[p.userID]; !ok || preferSLA(p, cur, resolutionClock) {
				resolution[p.userID] = p
			}
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

	for _, userID := range order {
		if p, ok := first[userID]; ok {
			// A business-hours-only policy holds its clock outside the window. The
			// window itself is not stored per session, so the clock cannot literally be
			// paused; the effect at the customer-visible end is the same, because the
			// still-unanswered session trips the same check at the first in-hours
			// scan. Without this the flag was read out of sla_policies and never used,
			// so a tenant who turned it on was still woken at 3am about a queue nobody
			// could work.
			if p.businessHoursOnly && !a.withinBusinessHours(ctx, userID) {
				continue
			}
			// One INSERT ... SELECT per policy instead of one INSERT per overdue
			// session: the old shape cost a round trip per session, per policy,
			// every 60 seconds for a scan that is one statement's worth of work.
			// RETURNING keeps the per-session notification, and only rows that
			// are actually new come back (ON CONFLICT DO NOTHING returns nothing
			// for a breach already recorded).
			//
			// make_interval(secs => $3), not ($3 || ' seconds')::interval:
			// Postgres resolves the parameter of a `||` expression as text (OID
			// 25), and pgx's extended protocol cannot encode a Go int into a text
			// parameter — the query failed to send on every single scan
			// ("unable to encode 300 into text format for text"). psql PREPARE
			// and sqlcheck both pass, because neither exercises parameter
			// encoding; only a real bound execution does. The `if err == nil`
			// swallowing below used to hide it, so the whole SLA engine was a
			// silent no-op.
			a.recordSLABreaches(ctx, userID, `INSERT INTO sla_breaches (user_id, session_id, sla_id, breach_type)
				SELECT $1, s.session_id, $2, 'first_response' FROM sessions s
				WHERE s.user_id = $1 AND s.status IN ('active','handoff')
				  AND s.first_response_at IS NULL AND s.created_at < NOW() - make_interval(secs => $3)
				  AND NOT EXISTS (SELECT 1 FROM sla_breaches b
				                  WHERE b.session_id = s.session_id AND b.breach_type = 'first_response')
				ON CONFLICT (session_id, breach_type) DO NOTHING
				RETURNING session_id`,
				"first_response", "SLA 违约：首次响应超时", "A session breached the first-response SLA", p.slaID, p.firstSecs)
		}
		p, ok := resolution[userID]
		if !ok || p.businessHoursOnly && !a.withinBusinessHours(ctx, userID) {
			continue
		}
		// A 60-second scan must not re-read what it already recorded: without the
		// NOT EXISTS above (and the record-only path that never notifies), every
		// session that already breached is fetched and discarded again, every tick,
		// forever.
		// Same make_interval reasoning as the first-response scan above.
		// The empty title/body mean "record only": this branch records the
		// breach but sends no notification, while the first-response branch
		// does. Asymmetric on purpose or not is an open question; see
		// docs/DEVELOPMENT.md.
		a.recordSLABreaches(ctx, userID, `INSERT INTO sla_breaches (user_id, session_id, sla_id, breach_type)
			SELECT $1, s.session_id, $2, 'resolution' FROM sessions s
			WHERE s.user_id = $1 AND s.status IN ('active','handoff')
			  AND s.resolved_at IS NULL AND s.created_at < NOW() - make_interval(secs => $3)
			  AND NOT EXISTS (SELECT 1 FROM sla_breaches b
			                  WHERE b.session_id = s.session_id AND b.breach_type = 'resolution')
			ON CONFLICT (session_id, breach_type) DO NOTHING
			RETURNING session_id`,
			"resolution", "", "", p.slaID, resolutionClock(p))
	}
}

// withinBusinessHours reports whether the tenant is inside a configured
// business-hours window right now. No rows configured means open, the same
// default the pipeline's isOpenNow uses (a tenant who never set hours is not
// silently exempted from SLA). Times are compared in Asia/Phnom_Penh, not the
// server's zone.
//
// SLA policies are not platform-scoped while business_hours rows are, so a
// tenant-wide answer does not always exist. Precedence: a tenant-wide row
// (platform IS NULL) governs every platform and wins; otherwise, if the tenant
// has exactly one distinct per-platform *window*, that window is unambiguous and
// is used; otherwise the answer would be a coin toss between platforms (the old
// query had no tiebreaker and took whichever tied row Postgres returned first),
// so this reports open — failing toward paging the owner rather than silently
// holding a clock on a guess. The honest fix is to let an SLA policy carry a
// platform; see docs/DEVELOPMENT.md.
func (a *App) withinBusinessHours(ctx context.Context, userID int32) bool {
	now := time.Now().In(platform.PhnomPenhLoc())
	rows, err := a.DB.Query(ctx,
		"SELECT platform::text, open_time, close_time FROM business_hours "+
			"WHERE user_id = $1 AND weekday = $2 AND is_active = true",
		userID, int(now.Weekday()))
	if err != nil {
		return true
	}
	defer rows.Close()
	var tenantWide *[2]string
	// Keyed by window, not by platform name: "web 09:00-18:00 + telegram
	// 09:00-18:00" is one window, and the old name-keyed map called it ambiguous
	// and reported open around the clock (review, 2026-10-12).
	windows := map[[2]string]struct{}{}
	for rows.Next() {
		var plat *string
		// Nullable columns: a row without times has no window to match. Treating it
		// as "absent" keeps the fail-open direction (never silently hold a clock),
		// where a failed Scan used to drop the row for the same effect by accident.
		var openTime, closeTime *string
		if rows.Scan(&plat, &openTime, &closeTime) != nil {
			continue
		}
		if openTime == nil || closeTime == nil {
			continue
		}
		w := [2]string{*openTime, *closeTime}
		if plat == nil {
			tenantWide = &w
			continue
		}
		windows[w] = struct{}{}
	}
	if rows.Err() != nil {
		return true
	}
	hm := now.Format("15:04")
	switch {
	case tenantWide != nil:
		return platform.WithinWindow(tenantWide[0], tenantWide[1], hm)
	case len(windows) == 1:
		for w := range windows {
			return platform.WithinWindow(w[0], w[1], hm)
		}
		return true
	default:
		return true
	}
}

// recordSLABreaches runs one policy's set-based breach insert and notifies the
// owner for every session that was newly recorded. An empty notifyTitle means
// record only. The tenant stays a bound parameter ($1 = sessions.user_id), so
// the statement still names the tenant column next to the SELECT that scopes it.
func (a *App) recordSLABreaches(ctx context.Context, userID int32, query, breachType, notifyTitle, notifyBody string, slaID int32, secs int) {
	rows, err := a.DB.Query(ctx, query, userID, slaID, secs)
	if err != nil {
		a.Logger.Warn("sla "+breachType+" scan failed", "user_id", userID, "error", err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		if rows.Scan(&sid) != nil {
			continue
		}
		if notifyTitle != "" {
			a.notifyUser(ctx, userID, "sla", notifyTitle, notifyBody, sid)
		}
	}
	if err := rows.Err(); err != nil {
		// Breaches missed here are the expensive kind: an SLA clock nobody gets
		// alerted about. The scan retries next tick, but the partial pass must
		// not look clean.
		a.Logger.Warn("sla "+breachType+" scan incomplete", "user_id", userID, "error", err.Error())
	}
}

// resetBillingCycles rolls over lapsed billing cycles (reset usage counters).
func (a *App) resetBillingCycles(ctx context.Context) {
	_, _ = a.DB.Exec(ctx,
		"UPDATE tenant_billing SET messages_used=0, docs_used=0, cycle_start=NOW(), cycle_end=NOW()+INTERVAL '30 days' WHERE cycle_end <= NOW()")
}
