// Package platform — durable inbound/outbound pipeline (port of work.rs).
// Webhooks persist inbound events; workers claim them, run the AI pipeline,
// and enqueue outbound deliveries.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/redisstore"
	"khmer-ai-cs-go/internal/replycache"
	"khmer-ai-cs-go/internal/security"
	"khmer-ai-cs-go/internal/storager2"
	"khmer-ai-cs-go/internal/textutil"
	"khmer-ai-cs-go/internal/typesafe"
	"khmer-ai-cs-go/internal/usage"
)

const (
	maxAttempts = 5
	workerCount = 4

	// staleLockMinutes is how long a claimed-but-unfinished event may hold its
	// lock before another worker is allowed to steal it. Both the inbound and
	// the outbound claim re-take a row whose lock is older than this.
	//
	// It has to exceed the worst-case processing time, because nothing refreshes
	// locked_at while an event is being handled (claim writes NOW(), the
	// completion path writes NULL — there is no heartbeat). The worst inbound
	// path is gemini.Chat → postWithRetry, which retries up to 3 times against a
	// 60s timeout, i.e. ≈3 minutes before the attempt can even be classified as
	// failed. The old value of 2 minutes sat *below* that, so a slow-but-alive
	// event got stolen mid-flight and processed twice: the customer received two
	// replies and the tenant was billed twice for one message.
	//
	// 5 minutes leaves ≈2 minutes of headroom. If a longer budget is ever given
	// to the Gemini calls (more retries, a higher per-request timeout), this
	// value MUST be raised with it — the two are one decision.
	//
	// The robust fix is a heartbeat: refresh locked_at periodically from inside
	// processInboundEvent and gate the steal on a much shorter staleness. That is
	// a deliberate follow-up, not something a constant can express.
	staleLockMinutes = 5

	inboundRateLimit = 15
)

// Two background lanes, because the work they carry has opposite failure modes.
//
// classifySem caps concurrent classification turns (each an LLM call plus
// several DB writes); unbounded goroutines could exhaust the pgx pool and pile
// 429 retries on Gemini during bursts. Losing a classification is survivable —
// the reply itself is already delivered, and a missed escalation just means
// the session stays with the AI.
//
// criticalSem carries work that must never be lost: message-quota accounting
// and owner notifications (new-customer and handoff alerts). These used to
// share the classifier's 8 slots, so a burst of LLM classifications silently
// discarded billing increments and escalation alerts — the one moment an
// owner most needs to be told a customer is waiting.
var (
	classifySem = make(chan struct{}, 8)
	criticalSem = make(chan struct{}, 64)
)

// criticalLaneOverflow counts how often the never-drop lane found no free slot
// and ran the work unbounded.
//
// Only observability was added here, deliberately: the unbounded run is the
// DESIGNED behaviour ("绝不丢弃"). Quota increments, owner notifications and
// handoff alerts lose their meaning if they are queued behind a bounded buffer
// and then dropped, so the overload is paid for in goroutines rather than in
// lost work. What was missing was any way to SEE it: a sustained spike would
// quietly run hundreds of goroutines against the pgx pool and the only trace
// was a log line per task, buried in the same stream as everything else.
//
// criticalLaneOverflowPageAt is the page threshold. Paging on every multiple
// (rather than once ever) keeps a spike that lasts hours from going silent
// after the first page, while PlatformAlert's own 15-minute key dedup collapses
// anything more frequent into one page per window.
const criticalLaneOverflowPageAt = 32

var criticalLaneOverflow atomic.Int64

// criticalLaneOverflowPage holds the operator page (func(overflow int64)).
//
// It is a package variable because spawnAsync is a free function: the lane is
// fed from package-level call sites as well as from Pipeline methods, so a
// single overloaded call has no *Pipeline to reach. SpawnWorkers installs it
// once at startup, before any traffic; when it is absent (unit tests, tooling)
// the overflow is still counted and logged.
var criticalLaneOverflowPage atomic.Value

// SpawnClassifier runs fn in the background on the best-effort lane. When the
// lane is saturated the work is DROPPED — only pass work that is safe to lose.
// Exported so the web-chat path shares the same guard and concurrency cap.
func SpawnClassifier(fn func()) { spawnAsync(classifySem, fn, true) }

// SpawnCritical runs fn in the background on the never-drop lane, for work
// whose loss would be user-visible: quota accounting and owner notifications.
func SpawnCritical(fn func()) { spawnAsync(criticalSem, fn, false) }

// spawnAsync runs fn in a guarded goroutine holding a slot in sem. Droppable
// lanes give up when saturated; non-droppable lanes run anyway and log the
// overload, because losing the work is worse than a brief goroutine spike.
func spawnAsync(sem chan struct{}, fn func(), droppable bool) {
	acquired := false
	select {
	case sem <- struct{}{}:
		acquired = true
	default:
		if droppable {
			return
		}
		overflow := criticalLaneOverflow.Add(1)
		slog.Default().Warn("critical background lane saturated; running unbounded",
			"overflow_total", overflow, "cap", cap(sem))
		// Page only on a threshold crossing: this branch is taken once per
		// spilled task. Re-entrancy is bounded by construction — the page
		// itself lands on this lane, so it consumes one more spill, and the
		// next crossing is a further criticalLaneOverflowPageAt spills away.
		if overflow%criticalLaneOverflowPageAt == 0 {
			if page, ok := criticalLaneOverflowPage.Load().(func(int64)); ok && page != nil {
				page(overflow)
			}
		}
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Default().Warn("background task panic recovered", "panic", r)
			}
			if acquired {
				<-sem
			}
		}()
		fn()
	}()
}

// Pipeline bundles pipeline dependencies.
type Pipeline struct {
	DB     *pgxpool.Pool
	Redis  *redisstore.Client
	Cfg    *config.Config
	Gemini *gemini.Service
	RAG    *rag.Service
	Sealer *security.Sealer
	Media  *storager2.Client
	Logger *slog.Logger
	// Jev is the TypeSafe judgment client for typed decisions (turn
	// classification, routing, guardrails). nil = disabled: every site
	// falls back to its previous logic.
	Jev *typesafe.Client
	// Cache is the semantic reply cache (nil = disabled: every turn takes the
	// full retrieval+generation path).
	Cache *replycache.Service
	// T2I renders long replies to an image (see t2i.go). nil = build the HTTP
	// renderer from the environment; tests inject a fake.
	T2I T2IRenderer
	// mediaUploader overrides Media for rendered images. Present so text-to-image
	// can be tested without a bucket.
	mediaUploader MediaUploader

	notify         chan struct{}
	notifyOutbound chan struct{}
	once           sync.Once
}

// InboundEvent is one queued customer message.
type InboundEvent struct {
	EventID         int64
	ConfigID        int32
	Platform        string
	PlatformUserID  string
	UserDisplayName string
	Content         string
	Media           map[string]any
}

// configCred is a decrypted platform config row.
type configCred struct {
	UserID            int32
	Platform          string
	AccessToken       string
	PageID            string
	InstagramBusiness string
	BotToken          string
}

// EnqueueInboundEvent persists one inbound message (idempotent, rate-limited).
func (p *Pipeline) EnqueueInboundEvent(ctx context.Context, configID int32, platform, externalID, platformUserID, displayName, content string, media map[string]any) error {
	if externalID == "" || platformUserID == "" || content == "" {
		return nil
	}
	// Per-customer inbound rate limit (anti-spam); fail-open.
	if ok, err := p.Redis.CheckRateLimit(ctx, fmt.Sprintf("inbound:%d:%s", configID, platformUserID), inboundRateLimit); err == nil && !ok {
		p.Logger.Warn("inbound rate limited; dropping message", "config_id", configID)
		return nil
	}
	now := time.Now()
	mediaJSON, _ := json.Marshal(media)
	var mediaParam any
	if media == nil {
		mediaParam = nil
	} else {
		mediaParam = mediaJSON
	}
	tag, err := p.DB.Exec(ctx,
		"INSERT INTO platform_inbound_events "+
			"(config_id, external_id, platform, platform_user_id, user_display_name, content, media_json, status, next_attempt_at, created_at) "+
			"VALUES ($1,$2,$3::platform_type,$4,$5,$6,$7,'pending',$8,$9) "+
			"ON CONFLICT (config_id, external_id) DO NOTHING",
		configID, externalID, platform, platformUserID, displayName, content, mediaParam, now, now)
	if err != nil {
		// Log here, not only at the call site: the inbound webhook handlers
		// used to swallow this error entirely, so a failed INSERT still
		// answered the provider with 200 and produced no line anywhere — the
		// customer's message was simply gone and nothing recorded that it had
		// ever arrived.
		p.Logger.Error("enqueue inbound failed",
			"config_id", configID, "platform", platform, "external_id", externalID, "error", err.Error())
		return fmt.Errorf("enqueue inbound: %w", err)
	}
	if tag.RowsAffected() > 0 {
		p.signal()
	}
	return nil
}

func (p *Pipeline) initNotify() {
	p.once.Do(func() {
		p.notify = make(chan struct{}, 64)
		p.notifyOutbound = make(chan struct{}, 64)
	})
}

func (p *Pipeline) signal() {
	p.initNotify()
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// SignalOutbound wakes the outbound workers immediately (a new delivery was
// enqueued — agent reply, campaign template, etc.). Without it deliveries sit
// in the outbox until the next 1s poll tick.
func (p *Pipeline) SignalOutbound() {
	p.initNotify()
	select {
	case p.notifyOutbound <- struct{}{}:
	default:
	}
}

// SpawnWorkers starts the inbound + outbound worker pools.
func (p *Pipeline) SpawnWorkers(ctx context.Context) {
	p.initNotify()
	// Install the critical-lane overflow page. spawnAsync is a free function
	// (that lane is also fed from package-level call sites), so it cannot reach
	// a *Pipeline by itself; SpawnWorkers is the one entry point every
	// deployment runs exactly once, before any traffic starts. Kept off the
	// caller's goroutine by SpawnCritical — paging does Redis and HTTP I/O, and
	// the overflow it reports is happening on a hot path.
	criticalLaneOverflowPage.Store(func(overflow int64) {
		SpawnCritical(func() {
			p.PlatformAlert(context.Background(), "critical-lane-overflow", "后台关键任务队列溢出",
				fmt.Sprintf("关键后台通道（配额计费、商家通知、转人工告警）已无空闲槽位 %d 次，任务改为无界执行。"+
					"不会丢任务，但进程 goroutine 与数据库连接正在被挤压，请检查出站投递是否变慢或数据库是否抖动。"+
					"阈值 %d 次告警一次。", overflow, criticalLaneOverflowPageAt))
		})
	})
	for i := 0; i < workerCount; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-p.notify:
				case <-time.After(2 * time.Second):
				}
				p.processInboundNext(ctx)
			}
		}()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-p.notifyOutbound:
				case <-time.After(1 * time.Second):
				}
				p.processOutboundNext(ctx)
			}
		}()
	}
}

// ============================================
// Inbound
// ============================================

// processInboundNext drains every claimable inbound event (the wake signal
// only has to be approximate; the loop empties the queue in one pass).
func (p *Pipeline) processInboundNext(ctx context.Context) {
	for {
		ev, err := p.claimInbound(ctx)
		if err != nil || ev == nil {
			return
		}
		if err := p.processInboundEvent(ctx, ev); err != nil {
			p.Logger.Warn("inbound event failed", "event_id", ev.EventID, "error", err.Error())
			p.retryInbound(ctx, ev.EventID)
		} else {
			_, _ = p.DB.Exec(ctx, "UPDATE platform_inbound_events SET status='completed', processed_at=$1, locked_at=NULL WHERE event_id=$2", time.Now(), ev.EventID)
		}
	}
}

func (p *Pipeline) claimInbound(ctx context.Context) (*InboundEvent, error) {
	now := time.Now()
	stale := now.Add(-staleLockMinutes * time.Minute)
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ev InboundEvent
	var mediaJSON []byte
	err = tx.QueryRow(ctx,
		"UPDATE platform_inbound_events SET status='processing', attempts = attempts + 1, locked_at = $1 "+
			"WHERE event_id = (SELECT event_id FROM platform_inbound_events "+
			"WHERE ((status = 'pending' AND next_attempt_at <= $2) OR (status = 'processing' AND locked_at < $3)) "+
			"AND NOT EXISTS (SELECT 1 FROM platform_inbound_events earlier WHERE earlier.config_id = platform_inbound_events.config_id "+
			"AND earlier.platform_user_id = platform_inbound_events.platform_user_id AND earlier.event_id < platform_inbound_events.event_id "+
			"AND earlier.status IN ('pending','processing')) "+
			"ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED) "+
			"RETURNING event_id, config_id, platform::text, platform_user_id, user_display_name, content, media_json",
		now, now, stale).Scan(&ev.EventID, &ev.ConfigID, &ev.Platform, &ev.PlatformUserID, &ev.UserDisplayName, &ev.Content, &mediaJSON)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			p.Logger.Warn("claim inbound failed", "error", err.Error())
		}
		return nil, nil
	}
	if len(mediaJSON) > 0 {
		_ = json.Unmarshal(mediaJSON, &ev.Media)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ev, nil
}

func (p *Pipeline) retryInbound(ctx context.Context, eventID int64) {
	var attempts int32
	_ = p.DB.QueryRow(ctx, "SELECT attempts FROM platform_inbound_events WHERE event_id = $1", eventID).Scan(&attempts)
	if attempts >= maxAttempts {
		_, _ = p.DB.Exec(ctx, "UPDATE platform_inbound_events SET status='failed', locked_at=NULL WHERE event_id=$1", eventID)
		return
	}
	delay := retryDelay(int(attempts))
	_, _ = p.DB.Exec(ctx, "UPDATE platform_inbound_events SET status='pending', next_attempt_at=$1, locked_at=NULL WHERE event_id=$2", time.Now().Add(delay), eventID)
}

func retryDelay(attempts int) time.Duration {
	secs := []int{5, 15, 60, 300, 900}
	idx := attempts
	if idx < 0 {
		idx = 0
	}
	if idx >= len(secs) {
		idx = len(secs) - 1
	}
	return time.Duration(secs[idx]) * time.Second
}

// loadConfig fetches + decrypts the config for an event.
func (p *Pipeline) loadConfig(ctx context.Context, configID int32) (*configCred, error) {
	var accessEnc, botEnc *string
	var cfg configCred
	var pageID, igBusiness *string
	err := p.DB.QueryRow(ctx,
		"SELECT user_id, platform::text, access_token, page_id, instagram_business_id, bot_token "+
			"FROM platform_configs WHERE config_id = $1 AND is_active = true", configID).
		Scan(&cfg.UserID, &cfg.Platform, &accessEnc, &pageID, &igBusiness, &botEnc)
	if err != nil {
		return nil, fmt.Errorf("platform config not found")
	}
	cfg.PageID = textutil.DerefString(pageID)
	cfg.InstagramBusiness = textutil.DerefString(igBusiness)
	if accessEnc != nil {
		cfg.AccessToken, _ = p.Sealer.Decrypt(*accessEnc)
	}
	if botEnc != nil {
		cfg.BotToken, _ = p.Sealer.Decrypt(*botEnc)
	}
	return &cfg, nil
}

// processInboundEvent runs one customer message end-to-end.
//
// The steps live in inbound_stages.go: one stage per decision, per-turn state in
// an inboundTurn, and a short-circuit that is an explicit `next=false` instead of
// a bare return buried between a database write and an LLM call.
func (p *Pipeline) processInboundEvent(ctx context.Context, ev *InboundEvent) error {
	t := &inboundTurn{Event: ev, Content: ev.Content, Urgency: UrgencyUnknown}
	return runInboundStages(ctx, p.inboundStages(), t)
}

// publishMessage fans out an inbox.message realtime event (best-effort).
func (p *Pipeline) publishMessage(ctx context.Context, userID int32, sessionID string, messageID int64, role string) {
	realtime.Publish(ctx, p.Redis, realtime.Event{
		Type:      realtime.EventMessage,
		UserID:    userID,
		SessionID: sessionID,
		MessageID: messageID,
		Role:      role,
	})
}

// publishSession fans out a session-changed realtime event (best-effort).
func (p *Pipeline) publishSession(ctx context.Context, userID int32, sessionID string) {
	realtime.Publish(ctx, p.Redis, realtime.Event{
		Type:      realtime.EventSession,
		UserID:    userID,
		SessionID: sessionID,
	})
}

func (p *Pipeline) ownerLanguage(ctx context.Context, userID int32) string {
	var lang *string
	_ = p.DB.QueryRow(ctx, "SELECT language FROM users WHERE user_id = $1", userID).Scan(&lang)
	return textutil.DerefString(lang)
}

// isOpenNow — business-hours check (no configured rows → open). Times are
// compared in Asia/Phnom_Penh regardless of the server's local zone, and a
// platform-specific row wins over the tenant-wide default.
func (p *Pipeline) isOpenNow(ctx context.Context, userID int32, platform string) bool {
	loc := phnomPenhLoc()
	now := time.Now().In(loc)
	weekday := int(now.Weekday()) // 0=Sunday
	hm := now.Format("15:04")
	var openTime, closeTime string
	err := p.DB.QueryRow(ctx,
		"SELECT open_time, close_time FROM business_hours WHERE user_id = $1 AND weekday = $2 AND is_active = true "+
			"AND (platform IS NULL OR platform = $3::platform_type) ORDER BY platform NULLS LAST LIMIT 1",
		userID, weekday, platform).Scan(&openTime, &closeTime)
	if err != nil {
		return true // no schedule configured → open
	}
	if openTime == "" || closeTime == "" {
		return false
	}
	return hm >= openTime && hm < closeTime
}

var ppLocOnce sync.Once
var ppLoc *time.Location

// PhnomPenhLoc exports the business-hours timezone (Indochina Time, UTC+7)
// for API-side checks.
func PhnomPenhLoc() *time.Location { return phnomPenhLoc() }

func phnomPenhLoc() *time.Location {
	ppLocOnce.Do(func() {
		loc, err := time.LoadLocation("Asia/Phnom_Penh")
		if err != nil {
			loc = time.FixedZone("+07", 7*3600)
		}
		ppLoc = loc
	})
	return ppLoc
}

// loadHistory returns the session's AI-visible turns in chronological order.
// excludeMessageID drops the triggering customer message (it was persisted
// before this call and is appended to the request separately — without the
// exclusion the model sees it twice). TTS echo rows (model/audio restate an
// existing model turn) and cancelled deliveries (the customer never saw them)
// are excluded so they neither duplicate turns nor feed false context.
func (p *Pipeline) loadHistory(ctx context.Context, sessionID string, excludeMessageID int64) []gemini.HistoryItem {
	rows, err := p.DB.Query(ctx,
		"SELECT role, content FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent') "+
			"AND message_id <> $2 AND NOT (role = 'model' AND message_type = 'audio') AND cancelled_at IS NULL "+
			"ORDER BY message_id DESC LIMIT 20", sessionID, excludeMessageID)
	if err != nil {
		// Answering with zero context is better than not answering, but a
		// silent amnesia made DB hiccups look like AI memory bugs.
		p.Logger.Error("load history failed; AI will answer without context", "session_id", sessionID, "error", err.Error())
		p.PlatformAlert(ctx, "history-load", "会话历史加载失败",
			"AI 本轮将在无上下文状态下回复。session="+sessionID+" err="+textutil.TruncateRunes(err.Error(), 200))
		return nil
	}
	defer rows.Close()
	var rev []gemini.HistoryItem
	for rows.Next() {
		var h gemini.HistoryItem
		if rows.Scan(&h.Role, &h.Content) == nil {
			rev = append(rev, h)
		}
	}
	// A read that broke off midway is treated exactly like the query failing
	// above: the rows that arrived are real but the conversation they describe
	// is silently truncated, and the model has no way to know that the turn it
	// is missing ever happened — the failure mode looks like an AI memory bug
	// again. Same alert, same key, so the two paths dedup together.
	if err := rows.Err(); err != nil {
		p.Logger.Error("load history failed; AI will answer without context", "session_id", sessionID, "error", err.Error())
		p.PlatformAlert(ctx, "history-load", "会话历史加载失败",
			"AI 本轮将在无上下文状态下回复。session="+sessionID+" err="+textutil.TruncateRunes(err.Error(), 200))
		return nil
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return gemini.TrimHistoryBudget(rev)
}

// customerAvatar returns the stored avatar URL for a customer ("" = none).
func (p *Pipeline) customerAvatar(ctx context.Context, cfg *configCred, platformUserID string) string {
	var avatar string
	_ = p.DB.QueryRow(ctx,
		"SELECT avatar_url FROM customer_profiles WHERE user_id=$1 AND platform=$2 AND platform_user_id=$3",
		cfg.UserID, cfg.Platform, platformUserID).Scan(&avatar)
	return avatar
}

// storeTelegramAvatar mirrors the chat photo into R2 and returns a
// browser-safe URL. Telegram file URLs embed the bot token, so the photo is
// downloaded server-side and re-hosted. Best-effort: "" on any failure
// (R2 disabled, download error, upload error).
func (p *Pipeline) storeTelegramAvatar(ctx context.Context, cfg *configCred, platformUserID, photoFileID string) string {
	if !p.Media.Enabled() || photoFileID == "" {
		return ""
	}
	data, _, err := NewTelegramClient(cfg.BotToken).DownloadFile(ctx, photoFileID)
	if err != nil || len(data) == 0 {
		return ""
	}
	key := fmt.Sprintf("avatars/telegram/%d/%s.jpg", cfg.UserID, platformUserID)
	putCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if uerr := p.Media.PutObject(putCtx, key, data, "image/jpeg"); uerr != nil {
		p.Logger.Warn("telegram avatar upload failed", "error", uerr.Error())
		return ""
	}
	return p.Media.PublicOrPresigned(key, 7*24*time.Hour)
}

// prepareMedia downloads media, transcribes voice, describes images, stores
// the file in R2, and folds the extracted text into the message content.
// Returns (content, storageKey, platformMedia).
func (p *Pipeline) prepareMedia(ctx context.Context, ev *InboundEvent, cfg *configCred, content string) (string, string, map[string]any) {
	// Worker contexts carry no HTTP request, so tag the tenant here for the
	// transcription / image-description spend below.
	ctx = usage.WithUser(ctx, cfg.UserID)
	kind, _ := ev.Media["kind"].(string)
	providerID, _ := ev.Media["provider_media_id"].(string)
	sourceURL, _ := ev.Media["source_url"].(string)
	filename, _ := ev.Media["filename"].(string)
	declaredMime, _ := ev.Media["mime_type"].(string)
	if providerID == "" && sourceURL == "" {
		return content, "", nil
	}

	var data []byte
	var mime string
	var err error
	if ch, cerr := NewChannel(p, cfg); cerr == nil {
		data, mime, err = ch.DownloadMedia(ctx, providerID, sourceURL, declaredMime)
	} else if sourceURL != "" {
		// Unknown channel: the only fetch we can still attempt is the URL the
		// provider handed us.
		data, mime, err = downloadBytes(ctx, sourceURL)
	}
	if err != nil || len(data) == 0 {
		p.Logger.Warn("media download failed", "error", fmt.Sprint(err))
		return content, "", nil
	}
	// Telegram file downloads often return a generic Content-Type
	// (application/octet-stream); prefer the mime declared by the webhook
	// (e.g. voice = audio/ogg) so Gemini can decode the audio.
	if mime == "" || mime == "application/octet-stream" {
		mime = declaredMime
	}
	isImage := kind == "photo" || kind == "image" || strings.HasPrefix(mime, "image/")

	// Voice/audio → transcribe into the message content.
	// Image    → Gemini vision description so the AI can actually answer.
	extracted := ""
	if !isImage && (kind == "audio" || kind == "voice") {
		// Pass no language hint: the merchant's UI language says nothing about
		// what the customer spoke. Pinning it made Gemini render Khmer speech in
		// the wrong script (e.g. Amharic) — let the model detect it, and the
		// script guard in TranscribeAudio retries as Khmer if it mis-decodes.
		transcript, terr := transcribeAudio(ctx, p, data, mime, "")
		switch {
		case terr != nil:
			p.Logger.Warn("voice transcription failed", "event_id", ev.EventID, "error", fmt.Sprint(terr))
		case transcript != "":
			content = transcript
			extracted = transcript
		default:
			p.Logger.Warn("voice transcription returned empty transcript", "event_id", ev.EventID)
		}
	} else if isImage {
		desc, derr := p.Gemini.DescribeImage(ctx, data, mime)
		if derr == nil && strings.TrimSpace(desc) != "" {
			content = desc
			extracted = desc
		} else {
			p.Logger.Warn("image description failed", "error", fmt.Sprint(derr))
		}
	}

	// Store for replay (best-effort; requires R2 config).
	storageKey := ""
	if p.Media.Enabled() {
		storageKey = fmt.Sprintf("platform-media/%d/%d/%s", cfg.UserID, ev.EventID, safeFilename(filename, kind, ev.EventID))
		uploadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		if uerr := p.Media.PutObject(uploadCtx, storageKey, data, mime); uerr != nil {
			p.Logger.Warn("r2 upload failed", "error", uerr.Error())
			storageKey = ""
		}
		cancel()
	}
	pm := map[string]any{"kind": kind, "filename": filename, "mime_type": mime}
	if storageKey != "" {
		pm["processing_status"] = "stored"
	} else {
		// No stored object (R2 off or upload failed) — mark it explicitly so
		// the inbox stops requesting a media URL that will always 404.
		pm["processing_status"] = "unavailable"
	}
	if extracted != "" {
		pm["extracted_text"] = extracted
	}
	return content, storageKey, pm
}

func safeFilename(filename, kind string, eventID int64) string {
	if filename == "" {
		return fmt.Sprintf("%s-%d.bin", kind, eventID)
	}
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(filename)
}

// transcribeAudio calls Gemini multimodal speech-to-text.
func transcribeAudio(ctx context.Context, p *Pipeline, audio []byte, mime, lang string) (string, error) {
	return p.Gemini.TranscribeAudio(ctx, audio, mime, lang)
}

// enqueueHandoffAck sends the canned "agent will respond" acknowledgement.
func (p *Pipeline) enqueueHandoffAck(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID string) {
	lang := p.ownerLanguage(ctx, cfg.UserID)
	if lang == "" {
		if det := gemini.DetectLanguage(ev.Content); det != "" {
			lang = det
		} else {
			lang = "km"
		}
	}
	reply := handoffAcknowledgement(lang)
	var msgID int64
	if err := p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'system','text',$2,$3) RETURNING message_id",
		sessionID, reply, time.Now()).Scan(&msgID); err != nil {
		return
	}
	p.publishMessage(ctx, cfg.UserID, sessionID, msgID, "system")
	_ = p.enqueueDelivery(ctx, ev, cfg, sessionID, msgID, reply, nil)
}

func handoffAcknowledgement(language string) string {
	switch language {
	case "en":
		return "Thank you for your message. A human agent has been notified and will respond shortly. I'll hand this conversation over to them."
	case "zh":
		return "感谢您的消息。已为您转接人工客服，客服人员将尽快回复您。我已把这次对话转交人工处理。"
	default:
		return "សូមអរគុណសម្រាប់សាររបស់អ្នក។ " + gemini.KhmerHandoffSentence
	}
}

// ============================================
// Auto-handoff engine
// ============================================

// humanRequestKeywords — phrases where the customer explicitly asks for a
// human (en / km / zh). Matched case-insensitively on the raw message. Short
// English words are matched on word boundaries separately (see the matcher) to
// avoid false positives like "management" containing "manage".
var humanRequestKeywords = []string{
	// English multi-word
	"real person", "real human", "human agent", "live agent", "human support",
	"talk to agent", "talk to a human", "talk to someone", "speak to a person",
	"speak to a human", "speak to an agent", "connect me to", "transfer me",
	"customer agent", "staff member", "a representative", "speak to staff",
	// Khmer (common explicit requests)
	"មនុស្សពិត", "និយាយជាមួយមនុស្ស", "ភ្នាក់ងារមនុស្ស", "ទាក់ទងមនុស្ស",
	"សុំភ្នាក់ងារ", "និយាយជាមួយភ្នាក់ងារ", "ចង់និយាយជាមួយ", "បម្រើមនុស្ស",
	"ភ្នាក់ងារជំនួយ", "មនុស្សបម្រើ", "សុំមនុស្ស",
	"សុំបុគ្គលិក", "និយាយជាមួយបុគ្គលិក",
	// Chinese
	"人工", "真人", "转人工", "找客服", "人工客服", "转接客服", "我要客服",
	"联系人工", "人工服务", "找个人", "接人工",
}

// humanRequestWordMarkers — single tokens matched on ASCII word boundaries so
// a bare "agent"/"human" in an English sentence still escalates, but we do not
// fire on substrings inside unrelated words.
var humanRequestWordMarkers = []string{
	"human", "agent", "representative", "staff",
}

// handoffNegationPhrases — when one of these appears before the matched
// keyword, the customer is declining a transfer ("不用转人工了"), not asking
// for one. Position-aware: a negation that FOLLOWS the keyword still escalates
// ("I need a human, don't send me a bot").
var handoffNegationPhrases = []string{
	// Chinese
	"不用", "不需要", "不要", "无需", "不想", "别转", "不用了", "算了", "取消",
	// English
	"no need", "don't need", "dont need", "do not need", "don't bother",
	"never mind", "cancel the transfer", "stop transferring",
	// Khmer
	"មិនត្រូវការ", "មិនចង់", "លែងត្រូវការ",
}

func humanRequestKeyword(content string) (string, bool) {
	lowered := strings.ToLower(content)
	for _, kw := range humanRequestKeywords {
		if idx := strings.Index(lowered, kw); idx >= 0 {
			if negatedBefore(lowered, idx) {
				return "", false
			}
			return kw, true
		}
	}
	for _, w := range humanRequestWordMarkers {
		if start, ok := containsWordAt(lowered, w); ok {
			if negatedBefore(lowered, start) {
				return "", false
			}
			return w, true
		}
	}
	return "", false
}

// negatedBefore reports whether a cancellation phrase appears before the
// matched keyword in the same message. Short (≤2 rune) negations like "不用"
// must sit within 3 runes of the keyword ("不用转人工了" yes, "取消订单，转人工"
// no); longer phrases like "no need" count anywhere before the keyword.
func negatedBefore(lowered string, keywordIdx int) bool {
	kwRunes := len([]rune(lowered[:keywordIdx]))
	for _, neg := range handoffNegationPhrases {
		idx := strings.Index(lowered, neg)
		if idx < 0 {
			continue
		}
		end := len([]rune(lowered[:idx])) + len([]rune(neg))
		if end <= kwRunes && (kwRunes-end <= 3 || len([]rune(neg)) > 2) {
			return true
		}
	}
	return false
}

// containsWordAt reports the start index of `word` in `s` when surrounded by
// non-ASCII letters (so "agent" matches "talk to an agent", not "reagent").
func containsWordAt(s, word string) (int, bool) {
	for i := 0; i+len(word) <= len(s); {
		idx := strings.Index(s[i:], word)
		if idx < 0 {
			return 0, false
		}
		start := i + idx
		end := start + len(word)
		leftOK := start == 0 || !isASCIILetter(s[start-1])
		rightOK := end >= len(s) || !isASCIILetter(s[end])
		if leftOK && rightOK {
			return start, true
		}
		i = end
	}
	return 0, false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// HumanRequestKeyword is the exported matcher used by the web-chat path so the
// keyword rule has a single source of truth across pipeline + HTTP.
func HumanRequestKeyword(content string) (string, bool) { return humanRequestKeyword(content) }

// handoffClaimPhrases — decisive transfer commitments the AI writes when it
// tells the customer a human is taking over. Offers/questions ("需要我为您
// 转接吗?") are deliberately NOT here — only completed or committed actions,
// so the customer's consent is respected.
var handoffClaimPhrases = []string{
	// Chinese (completed / committed)
	"已为您转接", "已转接", "正在为您转接", "正在转接", "马上为您转接", "这就为您转接",
	"已为您联系人工", "已将您转接", "转接了人工", "已为您安排人工",
	"已把这次对话转交", "已转交人工", "正在转交", "已为您转交",
	"已通知", "为您联系了", "已为您安排专员", "专员将尽快", "人工客服将尽快",
	"客服人员将尽快", "客服会尽快与您联系", "已收到您转接",
	// English
	"connecting you", "i have transferred", "i've transferred", "transferring you",
	"i'll connect you", "let me connect you", "handing you over", "i'll hand you over",
	"i've escalated", "escalating this", "i have notified",
	// Khmer (បាន/កំពុង = completed/ongoing handover). The first phrase is the
	// subject clause of gemini.KhmerHandoffSentence; the last is its second
	// clause, which is what a full sentence actually matches on.
	"បានប្រគល់ការសន្ទនា", "កំពុងប្រគល់ការសន្ទនា", "នឹងប្រគល់ការសន្ទនានេះទៅឱ្យ",
	"បុគ្គលិករបស់យើងត្រូវបានជូនដំណឹង",
}

// ReplyClaimsHandoff reports whether an AI reply announced a handoff to the
// customer. When it did, the system must create the request — otherwise the
// customer waits for an agent who was never notified.
func ReplyClaimsHandoff(reply string) bool {
	lowered := strings.ToLower(reply)
	for _, p := range handoffClaimPhrases {
		if strings.Contains(lowered, p) {
			return true
		}
	}
	return false
}

// HandoffAcknowledgement is the exported canned "agent will respond" text.
func HandoffAcknowledgement(language string) string { return handoffAcknowledgement(language) }

// JunkAcknowledgement — platform channels stay truly silent on junk, but the
// web widget cannot leave a visitor's message bubble hanging, so it answers
// junk with a thanks-only line: no promise of a human, no AI engagement, and
// none of the generation cost. Khmer is first-draft copy (needs a native pass,
// like every bot string here).
func JunkAcknowledgement(language string) string {
	switch language {
	case "en":
		return "🙏 Thanks — your message has been received."
	case "zh":
		return "🙏 感谢您的留言！"
	default:
		return "🙏 អរគុណសម្រាប់សាររបស់អ្នក!"
	}
}

// SmallTalkReply — the fixed answer for chit-chat (greetings, thanks, "ok").
// Jev routes small_talk only when it is confident the message carries no
// request, and those turns are the most repeated ones the system sees:
// answering them from this table costs nothing instead of a full generation.
// The line invites the real question rather than conversing.
// JEV_CHITCHAT_CANNED=0 sends them to the model again.
func SmallTalkReply(language string) string {
	switch language {
	case "en":
		return "Thanks for reaching out! Let me know if you have any questions."
	case "zh":
		return "谢谢您的消息！有任何问题随时告诉我。"
	default:
		return "សូមអរគុណ! ប្រសិនបើមានសំណួរ សូមប្រាប់ខ្ញុំ។"
	}
}

// smallTalkCanned — whether chit-chat is answered from the template above.
func smallTalkCanned() bool {
	if v := strings.TrimSpace(os.Getenv("JEV_CHITCHAT_CANNED")); v != "" {
		if on, err := strconv.ParseBool(v); err == nil {
			return on
		}
	}
	return true
}

// SmallTalkCanned is the exported switch, so the widget path answers chit-chat
// the same way the platform channels do.
func SmallTalkCanned() bool { return smallTalkCanned() }

// ReleaseHandoffKeyword is the exported customer-cancellation matcher so the
// web-chat path shares the pipeline's release rule.
func ReleaseHandoffKeyword(content string) (string, bool) { return releaseHandoffKeyword(content) }

// escalateToHuman moves a session to the handoff queue: creates the request
// row (unless one is already open), flips the status, and — when an inbound
// event is given — acks the customer. Every auto trigger funnels through here.
// urgency is the router's verdict for this turn ("" when unavailable); it can
// raise the queue priority, never lower it.
// Escalation failures never fail the inbound event (the AI already replied or
// the queue dedupes), so the error is logged, not returned.
func (p *Pipeline) escalateToHuman(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID, trigger, reason, urgency string) {
	if err := p.createHandoffRequest(ctx, cfg.UserID, sessionID, trigger, reason, urgency); err != nil {
		p.Logger.Warn("auto handoff request failed", "session_id", sessionID, "error", err.Error())
		return
	}
	if ev != nil {
		p.enqueueHandoffAck(ctx, ev, cfg, sessionID)
	}
}

// createHandoffRequest inserts a queue row + session status + notification.
// The partial unique index keeps exactly one open request per session, so
// repeat triggers are silently deduplicated.
// urgency (the router's verdict for this turn, "" when unknown) can raise the
// queue priority — an urgent message jumps the line even when its trigger
// would normally be "normal".
func (p *Pipeline) createHandoffRequest(ctx context.Context, userID int32, sessionID, trigger, reason, urgency string) error {
	priority := handoffPriority(trigger, urgency)
	_, err := p.DB.Exec(ctx,
		"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
			"VALUES ($1,$2,'pending',$3,$4::human_handoff_trigger,$5,NOW()) ON CONFLICT DO NOTHING",
		sessionID, userID, priority, trigger, reason)
	if err != nil {
		return err
	}
	_, _ = p.DB.Exec(ctx,
		"UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at, NOW()) WHERE session_id=$1 AND status='active'", sessionID)
	p.notifyUser(ctx, userID, "handoff", "New human-handoff request", trigger+": "+textutil.Ellipsize(reason, 120), sessionID)
	p.publishSession(ctx, userID, sessionID)
	return nil
}

// handoffPriority merges the static trigger table with the router's urgency:
// urgency can raise the priority to "high", never lower it below the trigger's
// default. The schema only knows "normal" | "high" (human_handoff_requests).
func handoffPriority(trigger, urgency string) string {
	priority := highPriorityTriggers[trigger]
	if priority == "" {
		priority = "normal"
	}
	if urgency == UrgencyUrgent {
		priority = "high"
	}
	return priority
}

// HandoffPriority is handoffPriority for callers outside the platform package
// (the web widget shares the handoff queue and must compute the same priority
// instead of hardcoding one — the two paths drifted before).
func HandoffPriority(trigger, urgency string) string { return handoffPriority(trigger, urgency) }

var highPriorityTriggers = map[string]string{
	"customer_request":  "high",
	"negative_feedback": "high",
	"ai_decision":       "normal",
	"no_knowledge_base": "normal",
	"manual":            "normal",
}

// releaseHandoffKeywords — customer phrases that cancel a pending handoff.
// Only checked while the session is in handoff (never on active sessions), so
// they cannot clash with the escalation keywords.
var releaseHandoffKeywords = []string{
	// Chinese
	"不需要人工", "不用人工", "不需要客服", "不用客服", "取消转人工", "取消人工",
	"不用转人工", "没人接", "不需要了", "不用了",
	// English
	"don't need human", "dont need human", "no human needed", "cancel human",
	"don't need agent", "dont need agent", "no agent needed", "never mind",
	// Khmer
	"មិនត្រូវការមនុស្ស", "មិនត្រូវការភ្នាក់ងារ", "លែងត្រូវការមនុស្ស",
}

func releaseHandoffKeyword(content string) (string, bool) {
	lowered := strings.ToLower(content)
	for _, kw := range releaseHandoffKeywords {
		if strings.Contains(lowered, kw) {
			return kw, true
		}
	}
	return "", false
}

// maybeReleaseHandoff hands a handoff session back to the AI. Two paths:
//  1. no open (pending/assigned) request left — every request was resolved, so
//     the stale status should not silence the AI forever;
//  2. the customer explicitly cancels while the request is still pending
//     (once an agent has claimed it, the human keeps the conversation).
//
// Returns true when the session was released.
func (p *Pipeline) maybeReleaseHandoff(ctx context.Context, cfg *configCred, sessionID, content string) bool {
	var open int
	_ = p.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM human_handoff_requests WHERE session_id=$1 AND status IN ('pending','assigned')",
		sessionID).Scan(&open)
	if open == 0 {
		_, _ = p.DB.Exec(ctx, "UPDATE sessions SET status='active' WHERE session_id=$1 AND status='handoff'", sessionID)
		p.publishSession(ctx, cfg.UserID, sessionID)
		p.Logger.Info("handoff released: no open request; session back to AI", "session_id", sessionID)
		return true
	}
	if _, ok := releaseHandoffKeyword(content); !ok {
		return false
	}
	tag, err := p.DB.Exec(ctx,
		"UPDATE human_handoff_requests SET status='resolved', resolved_at=NOW(), resolution_note='customer cancelled handoff' "+
			"WHERE session_id=$1 AND status='pending'", sessionID)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	_, _ = p.DB.Exec(ctx, "UPDATE sessions SET status='active' WHERE session_id=$1 AND status='handoff'", sessionID)
	p.publishSession(ctx, cfg.UserID, sessionID)
	p.Logger.Info("customer cancelled handoff; session back to AI", "session_id", sessionID)
	return true
}

// ackHandoffOnce sends the canned "agent will respond" ack only for the first
// customer message after an escalation; further messages stay silent (they
// still land in the inbox for the human agent).
func (p *Pipeline) ackHandoffOnce(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID string) {
	var acked bool
	_ = p.DB.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM chat_messages WHERE session_id=$1 AND role='system' "+
			"AND created_at > COALESCE((SELECT escalated_at FROM sessions WHERE session_id=$2), to_timestamp(0)))",
		sessionID, sessionID).Scan(&acked)
	if acked {
		return
	}
	p.enqueueHandoffAck(ctx, ev, cfg, sessionID)
}

// EscalateHuman moves a session to the handoff queue without the canned ack —
// the exported entry point for the web-chat path, which shares the queue.
// No router urgency here: the web-chat path escalates from its own rules.
func (p *Pipeline) EscalateHuman(ctx context.Context, userID int32, sessionID, trigger, reason string) {
	p.escalateToHuman(ctx, nil, &configCred{UserID: userID}, sessionID, trigger, reason, "")
}

// HasReadyDocs — whether the tenant has at least one indexed knowledge doc.
func (p *Pipeline) HasReadyDocs(ctx context.Context, userID int32) bool {
	return p.hasReadyDocs(ctx, userID)
}

// isChitChat — whether the model's "small_talk" label may be trusted to excuse a
// knowledge-base miss.
//
// Same rule as the routing veto (see routeDecision): a question is not chit-chat.
// The label is what suppresses the no_knowledge_base handoff, so a question
// mislabelled small_talk would otherwise reach the customer as an ungrounded guess
// instead of a human — "你是谁" was labelled small_talk on 2026-10-04.
func isChitChat(intent, content string) bool {
	return intent == "small_talk" && !LooksLikeQuestion(content)
}

// TurnTrigger — the shared escalation decision for one classified turn. This
// is the single source of truth for the platform pipeline AND the web-chat
// path (they previously drifted). Returns the handoff trigger + reason, or ""
// when no human is needed.
func TurnTrigger(v gemini.TurnVerdict, hasMatch, hasDocs bool, content string) (string, string) {
	// Intents a human must own outright, regardless of tone.
	switch v.Intent {
	case "complaint", "refund", "legal":
		return "negative_feedback", "Intent requires a human (" + v.Intent + ")"
	case "customization", "bulk_order":
		return "ai_decision", "Intent requires sales involvement (" + v.Intent + ")"
	}
	switch {
	case v.Sentiment == "negative":
		return "negative_feedback", "Customer sentiment turned negative (" + v.Intent + ")"
	case v.Escalate:
		return "ai_decision", "AI classifier recommends human review (" + v.Intent + ")"
	case !hasMatch && v.Confidence < 0.35 && hasDocs && !isChitChat(v.Intent, content):
		return "no_knowledge_base", "Answer not grounded in the knowledge base (intent: " + v.Intent + ")"
	}
	return "", ""
}

// TurnTriggerFor is the escalation decision for a Jev-sourced verdict. The
// intent/sentiment rules were written for the fast model's label semantics
// and over-fire on Jev's distributions (calibrated 2026-09-21 on 370 real
// turns: 20-26 false handoffs per 370 without confirmation, 3 with), so here
// they require the model's own escalate probability to corroborate them, and
// the solo-noul valve sits at a high bar. The no-knowledge-base rule stays
// unconditional: it rests on retrieval facts, not labels.
func TurnTriggerFor(v gemini.TurnVerdict, rawNoul float64, hasMatch, hasDocs bool, content string) (string, string) {
	confirm := config.EnvFloat("JEV_RULE_CONFIRM_MIN", 0.70)
	// JEV_RULE_SOLO_MIN is deliberately NOT JEV_TURN_ESCALATE_MIN. The two bars
	// mean different things and have always carried different defaults (0.90
	// here, 0.60 in judgeTurnJev) — yet both read the same variable. With no
	// JEV_* set in production, which is the case, each site silently used its
	// own default and tuning one would have moved the other.
	solo := config.EnvFloat("JEV_RULE_SOLO_MIN", 0.90)
	ruleIntent := false
	switch v.Intent {
	case "complaint", "refund", "legal", "customization", "bulk_order":
		ruleIntent = true
	}
	switch {
	case (ruleIntent || v.Sentiment == "negative") && rawNoul >= confirm:
		return "negative_feedback", "Jev intent/sentiment rule confirmed by escalate probability (" +
			v.Intent + ", p=" + strconv.FormatFloat(rawNoul, 'f', 2, 64) + ")"
	case rawNoul >= solo:
		return "ai_decision", "Jev escalate probability above the solo bar (" +
			strconv.FormatFloat(rawNoul, 'f', 2, 64) + ", intent: " + v.Intent + ")"
	case !hasMatch && v.Confidence < 0.35 && hasDocs && !isChitChat(v.Intent, content):
		return "no_knowledge_base", "Answer not grounded in the knowledge base (intent: " + v.Intent + ")"
	}
	return "", ""
}

// notifyUser inserts a notification row and fans an inbox.notification event
// out to the tenant's WebSocket connections (the bell polls too, so this is
// an acceleration, not a guarantee). Handoff-type notifications also ping the
// tenant's Telegram notify bot when configured.
func (p *Pipeline) notifyUser(ctx context.Context, userID int32, kind, title, body, sessionID string) {
	_, _ = p.DB.Exec(ctx,
		"INSERT INTO notifications (user_id, kind, title, body, session_id, created_at) VALUES ($1,$2,$3,$4,$5,NOW())",
		userID, kind, title, body, nullSession(sessionID))
	realtime.Publish(ctx, p.Redis, realtime.Event{
		Type: realtime.EventNotification, UserID: userID, SessionID: sessionID,
	})
	if kind == "handoff" {
		text := title + " — " + textutil.Ellipsize(body, 200)
		if link := p.sessionLink(sessionID); link != "" {
			text += "\n🔗 " + link
		}
		p.NotifyHandoffRequest(ctx, userID, sessionID, text)
	}
}

func nullSession(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// classifyTurnAsync runs the fast-model turn analysis off the request path:
// persists sentiment/intent/confidence on the session and escalates when the
// AI should not have owned this message.
func (p *Pipeline) classifyTurnAsync(userID int32, sessionID, customerMsg, reply string, hasMatch bool) {
	SpawnClassifier(func() { p.classifyTurn(userID, sessionID, customerMsg, reply, hasMatch) })
}

func (p *Pipeline) classifyTurn(userID int32, sessionID, customerMsg, reply string, hasMatch bool) {
	{
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 20*time.Second)
		defer cancel()

		verdict, topic, raw, fromJev, ok := p.judgeTurn(ctx, customerMsg, reply, hasMatch)
		if !ok {
			return
		}
		p.PersistTurnVerdict(ctx, sessionID, verdict, topic)

		// Still owned by the AI? only escalate active sessions.
		var status string
		_ = p.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id=$1", sessionID).Scan(&status)
		if status != "active" {
			return
		}
		var trigger, reason string
		if fromJev {
			trigger, reason = TurnTriggerFor(verdict, raw, hasMatch, p.hasReadyDocs(ctx, userID), customerMsg)
		} else {
			trigger, reason = TurnTrigger(verdict, hasMatch, p.hasReadyDocs(ctx, userID), customerMsg)
		}
		if trigger != "" {
			p.escalateToHuman(ctx, nil, &configCred{UserID: userID}, sessionID, trigger, reason, "")
		}
	}
}

// PersistTurnVerdict writes the classifier's verdict onto the session. The
// topic lands in the existing sessions.tags column as a `topic:*` entry (the
// inbox TagsBar renders and edits it like any other tag); manual tags are
// preserved and an older topic tag is replaced. Shared by the platform
// pipeline and the web-chat classifier so the two cannot drift.
func (p *Pipeline) PersistTurnVerdict(ctx context.Context, sessionID string, verdict gemini.TurnVerdict, topic string) {
	topicTag := ""
	if topic != "" && topic != "other" {
		topicTag = "topic:" + topic
	}
	_, _ = p.DB.Exec(ctx,
		"UPDATE sessions SET sentiment=$1, sentiment_at=NOW(), intent=$2, confidence=$3, "+
			"tags = CASE WHEN $5::text = '' THEN tags ELSE "+
			"(SELECT COALESCE(array_agg(x), '{}'::text[]) FROM unnest(tags) x WHERE x NOT LIKE 'topic:%') || ARRAY[$5::text] END "+
			"WHERE session_id=$4 AND status='active'",
		verdict.Sentiment, verdict.Intent, verdict.Confidence, sessionID, topicTag)
}

// JudgeTurnFor is the Jev-first turn judgment for callers outside the
// pipeline (the web-chat classifier). fromJev tells the caller which
// escalation policy applies: TurnTriggerFor for Jev verdicts, TurnTrigger for
// fast-model fallbacks.
func (p *Pipeline) JudgeTurnFor(ctx context.Context, customerMsg, reply string, hasMatch bool) (gemini.TurnVerdict, string, float64, bool, bool) {
	return p.judgeTurn(ctx, customerMsg, reply, hasMatch)
}

// hasReadyDocs — whether the tenant has at least one indexed knowledge doc.
func (p *Pipeline) hasReadyDocs(ctx context.Context, userID int32) bool {
	var exists bool
	_ = p.DB.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM knowledge_documents WHERE uploaded_by = $1 AND index_status = 'ready')", userID).Scan(&exists)
	return exists
}

// turnIntentValues mirrors the intent vocabulary of JudgeTurn so persisted
// values stay comparable across the Jev and fast-model paths.
var turnIntentValues = map[string]bool{
	"question": true, "complaint": true, "refund": true, "order_status": true,
	"price": true, "booking": true, "customization": true, "bulk_order": true,
	"delivery": true, "payment": true, "legal": true, "small_talk": true, "other": true,
}

var turnSentimentValues = map[string]bool{"positive": true, "neutral": true, "negative": true}

// judgeTurn decides one turn's verdict plus a topic tag. Jev (typed
// judgments in a single parallel batch) is primary; the fast-model JSON
// audit is the fallback when Jev is disabled, unavailable, or returns an
// out-of-vocabulary answer (it yields no topic).
func (p *Pipeline) judgeTurn(ctx context.Context, customerMsg, reply string, hasMatch bool) (gemini.TurnVerdict, string, float64, bool, bool) {
	if v, topic, raw, ok := p.judgeTurnJev(ctx, customerMsg, reply, hasMatch); ok {
		return v, topic, raw, true, true
	}
	v, ok := p.Gemini.JudgeTurn(ctx, customerMsg, reply, hasMatch)
	return v, "", 0, false, ok
}

// turnTopicValues — the topic vocabulary persisted as `topic:*` tags.
var turnTopicValues = map[string]bool{
	"product": true, "price": true, "delivery": true, "complaint": true, "order": true, "other": true,
}

// turnBudget bounds the turn judgment. Two callers share it: the website
// widget reply path (JudgeTurnFor, on the request context) and the background
// classifier (classifyTurnAsync, detached with a 20s outer budget). Neither
// capped the call below the HTTP client's 10s timeout, so a stalled Jev held a
// widget turn — and pinned a classifier slot — for the full ten seconds before
// the fast-model fallback could run. guard/route each carry their own reply-path
// budget; this call has none of its own, hence its own knob. The default sits
// above the production server's measured 0.8-2.1s spread to api.typesafe.ai.
var turnBudget = config.EnvMillis("JEV_TURN_BUDGET_MS", 4*time.Second)

// judgeTurnJev asks Jev the same four decisions JudgeTurn's prompt encodes,
// as typed questions. Escalation is a Noul thresholded in code (calibrated on
// real handoff outcomes, see JEV_TURN_ESCALATE_MIN — the verdict flag's own
// knob, distinct from TurnTriggerFor's JEV_RULE_SOLO_MIN); the confidence
// stored on the session is the intent distribution's concentration.
func (p *Pipeline) judgeTurnJev(ctx context.Context, customerMsg, reply string, hasMatch bool) (gemini.TurnVerdict, string, float64, bool) {
	if !p.Jev.Enabled() {
		return gemini.TurnVerdict{}, "", 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, turnBudget)
	defer cancel()

	state := map[string]any{
		"customer_message": textutil.Ellipsize(customerMsg, 600),
		"assistant_reply":  textutil.Ellipsize(reply, 600),
		"kb_grounded":      hasMatch,
	}
	resp, err := p.Jev.Judge(ctx, state, map[string]typesafe.Question{
		"sentiment": typesafe.Choice(
			"Overall emotional tone of `customer_message` in this customer-service turn.",
			map[string]string{
				"positive": "Satisfied, grateful, or friendly",
				"neutral":  "Matter-of-fact, no strong emotion",
				"negative": "Angry, rude, frustrated, or repeating a complaint",
			}),
		"intent": typesafe.Choice(
			"Primary intent of `customer_message`.",
			map[string]string{
				"question":      "Asks about a product or service",
				"complaint":     "Complains about a past problem",
				"refund":        "Demands a refund, return, or money back",
				"order_status":  "Asks where an order is",
				"price":         "Asks about prices or negotiates",
				"booking":       "Wants to book or reserve",
				"customization": "Needs custom sizing or special specifications",
				"bulk_order":    "Wholesale or bulk purchase",
				"delivery":      "Delivery arrangement or delivery problem",
				"payment":       "Invoice, contract, or payment terms",
				"legal":         "Legal threat or dispute",
				"small_talk":    "Greeting or chit-chat",
				"other":         "Anything else",
			}),
		"escalate": typesafe.Noul(
			"Should a human agent take over this conversation now? Yes when ANY applies: " +
				"the customer is angry or repeats a complaint; refund/return demands even if a policy was quoted; " +
				"delivery problems (late, damaged, wrong items, address change after ordering); custom sizing or " +
				"special specifications not in the catalog; bulk/wholesale orders or price negotiation; contract, " +
				"invoice, payment terms, or legal threats; the answer clearly did not resolve the question or the " +
				"customer says it is wrong; the customer explicitly asks for a human. No only for well-answered " +
				"product questions, greetings, and small talk."),
		"topic": typesafe.Choice(
			"Main subject of `customer_message`.",
			map[string]string{
				"product":   "Product features, availability, or specifications",
				"price":     "Prices, quotes, discounts, or payment amounts",
				"delivery":  "Shipping, delivery time, or logistics",
				"complaint": "A problem with a past purchase or service",
				"order":     "Placing, changing, or tracking an order",
				"other":     "Anything else",
			}),
	})
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("jev turn judgment failed; falling back to fast model", "error", err.Error())
		}
		return gemini.TurnVerdict{}, "", 0, false
	}
	sentiment, _, okS := resp.ChoiceValue("sentiment")
	intent, intentConf, okI := resp.ChoiceValue("intent")
	escalateP, okE := resp.NoulValue("escalate")
	if !okS || !okI || !okE || !turnSentimentValues[sentiment] || !turnIntentValues[intent] {
		if p.Logger != nil {
			p.Logger.Warn("jev turn judgment out of vocabulary; falling back to fast model",
				"sentiment", sentiment, "intent", intent)
		}
		return gemini.TurnVerdict{}, "", 0, false
	}
	// A missing or unknown topic only drops the tag, never the verdict.
	topic, _, _ := resp.ChoiceValue("topic")
	if !turnTopicValues[topic] {
		topic = ""
	}
	return gemini.TurnVerdict{
		Sentiment:  sentiment,
		Intent:     intent,
		Confidence: intentConf,
		// The verdict's own escalate flag. A different bar from
		// TurnTriggerFor's JEV_RULE_SOLO_MIN, hence a different knob.
		Escalate: escalateP >= config.EnvFloat("JEV_TURN_ESCALATE_MIN", 0.60),
	}, topic, escalateP, true
}

// JudgeTurnJev exposes the raw Jev turn judgment (verdict, topic tag, raw
// escalate probability) for offline calibration tooling (cmd/jeveval).
func (p *Pipeline) JudgeTurnJev(ctx context.Context, customerMsg, reply string, hasMatch bool) (gemini.TurnVerdict, string, float64, bool) {
	return p.judgeTurnJev(ctx, customerMsg, reply, hasMatch)
}

// sendTyping shows the "typing…" hint while the AI composes (best effort).
func (p *Pipeline) sendTyping(ctx context.Context, cfg *configCred, recipientID string) {
	if recipientID == "" {
		return
	}
	// Whether a channel has a typing indicator, and how to raise it, is the
	// channel's business — the orchestrator only decides that it wants one.
	ch, err := NewChannel(p, cfg)
	if err != nil {
		return
	}
	_ = ch.Typing(ctx, recipientID)
}

// enqueueVoiceReply synthesizes the model reply as audio and queues it as a
// follow-up media delivery. Fully best-effort — any failure is just "no audio".
func (p *Pipeline) enqueueVoiceReply(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID, reply string) {
	// Bill the TTS synthesis to the owning tenant (no HTTP request here).
	ctx = usage.WithUser(ctx, cfg.UserID)
	text := textutil.Ellipsize(stripMarkdown(reply), 400)
	if text == "" {
		return
	}
	synthCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	wav, err := p.Gemini.SynthesizeSpeech(synthCtx, text)
	cancel()
	if err != nil || len(wav) == 0 {
		p.Logger.Warn("tts synthesis failed", "error", fmt.Sprint(err))
		return
	}
	if len(sessionID) < 8 {
		return
	}
	key := fmt.Sprintf("tts/%d/%s/%d.wav", cfg.UserID, sessionID[:8], time.Now().UnixNano())
	upCtx, cancel2 := context.WithTimeout(ctx, 45*time.Second)
	if err := p.Media.PutObject(upCtx, key, wav, "audio/wav"); err != nil {
		cancel2()
		return
	}
	cancel2()
	// The voice reply is tenant-private conversation content delivered through
	// the provider, so it must carry a real expiry: PresignedOnly keeps the
	// 1-hour grant, where PublicOrPresigned would discard it and hand out a
	// permanent credential-free URL whenever R2_PUBLIC_URL is configured.
	audioURL := p.Media.PresignedOnly(key, time.Hour)
	if audioURL == "" {
		return
	}
	var msgID int64
	err = p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, media_url, created_at) VALUES ($1,'model','audio',$2,$3,$4) RETURNING message_id",
		sessionID, text, key, time.Now()).Scan(&msgID)
	if err != nil {
		return
	}
	_, _ = p.DB.Exec(ctx, "UPDATE sessions SET model_message_count = model_message_count + 1 WHERE session_id = $1", sessionID)
	p.publishMessage(ctx, cfg.UserID, sessionID, msgID, "model")
	payload := map[string]any{"kind": "media", "media_url": audioURL, "media_type": "audio"}
	_ = p.enqueueDelivery(ctx, ev, cfg, sessionID, msgID, text, payload)
}

// stripMarkdown — rough markdown → plain text for TTS.
func stripMarkdown(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#*-•>"))
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

// Inline emphasis the model still emits despite the prompt asking for plain
// text. Only DOUBLED markers and code spans are unwrapped: single `_` / `*`
// are left alone so identifiers like KWF_RO_100 and prices like *2 for 1*
// survive byte-for-byte.
var (
	chatBoldRe  = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	chatUnderRe = regexp.MustCompile(`__([^_\n]+)__`)
	chatCodeRe  = regexp.MustCompile("`([^`\n]+)`")
	chatHeadRe  = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]*`)
)

// plainTextForChat converts a model reply into text a chat bubble renders
// faithfully. No messenger transport here sets parse_mode (Telegram included),
// so a reply containing "**EPS PANEL**" reaches the customer as literal
// asterisks. The system prompt asks for plain text; this makes it true
// regardless of what the model actually emits. Paragraph and list-line
// structure is preserved — only the markup is removed.
func plainTextForChat(s string) string {
	s = chatBoldRe.ReplaceAllString(s, "$1")
	s = chatUnderRe.ReplaceAllString(s, "$1")
	s = chatCodeRe.ReplaceAllString(s, "$1")
	s = chatHeadRe.ReplaceAllString(s, "")
	return s
}

// sourcesJSON renders the grounding sources for persistence (NULL when no
// grounding happened).
func sourcesJSON(groundCtx rag.GroundingContext) any {
	if !groundCtx.HasMatch || len(groundCtx.Sources) == 0 {
		return nil
	}
	data, err := json.Marshal(groundCtx.Sources)
	if err != nil {
		return nil
	}
	return string(data)
}

// ============================================
// Outbound
// ============================================

// enqueueDelivery queues one outbound message (idempotent by chat_message_id).
// The stored content is what the customer's app will render, so inline
// Markdown is flattened here — the chat_messages row keeps the original for
// the agent console, which does render it.
func (p *Pipeline) enqueueDelivery(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID string, chatMessageID int64, content string, payload map[string]any) error {
	now := time.Now()
	content = plainTextForChat(content)
	// A long plain-text reply can go out as a single image instead of several
	// chunks: the layout survives the flattening and, on a channel that counts
	// messages rather than characters (WeChat customer service), it costs one
	// message instead of five. Any failure leaves the text untouched — an image
	// is an optimisation, never a reason to drop an answer.
	content, payload = p.maybeRenderReply(ctx, cfg, content, payload)
	var payloadJSON []byte
	if payload != nil {
		payloadJSON, _ = json.Marshal(payload)
	}
	tag, err := p.DB.Exec(ctx,
		"INSERT INTO platform_outbox "+
			"(config_id, session_id, chat_message_id, platform, recipient_id, content, payload, status, next_attempt_at, created_at, updated_at) "+
			"VALUES ($1,$2,$3,$4::platform_type,$5,$6,$7,'pending',$8,$9,$9) "+
			"ON CONFLICT (chat_message_id) DO NOTHING",
		ev.ConfigID, sessionID, chatMessageID, cfg.Platform, ev.PlatformUserID, content, payloadJSON, now, now)
	if err != nil {
		return fmt.Errorf("enqueue delivery: %w", err)
	}
	if tag.RowsAffected() > 0 {
		p.SignalOutbound()
	}
	return nil
}

// outboundDelivery is a claimed delivery row.
type outboundDelivery struct {
	DeliveryID    int64
	ConfigID      int32
	SessionID     string
	Platform      string
	RecipientID   string
	Content       string
	Payload       map[string]any
	Status        string
	Attempts      int32
	LastMessageID int64
}

// processOutboundNext drains every claimable delivery in one pass.
func (p *Pipeline) processOutboundNext(ctx context.Context) {
	for {
		d, err := p.claimOutbound(ctx)
		if err != nil || d == nil {
			return
		}
		p.deliver(ctx, d)
	}
}

func (p *Pipeline) claimOutbound(ctx context.Context) (*outboundDelivery, error) {
	now := time.Now()
	stale := now.Add(-staleLockMinutes * time.Minute)
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var d outboundDelivery
	var payloadJSON []byte
	// Same head-of-line guard the inbound claim uses (claimInbound): only take
	// a delivery when no earlier one for the same session is still in flight.
	// Without it the four outbound workers could pick two queued replies to one
	// customer concurrently and send them out of order — the inbound side has
	// always serialised per customer, so replies were being ordered correctly
	// only until a burst put two in the queue at once.
	err = tx.QueryRow(ctx,
		"UPDATE platform_outbox SET status='processing', attempts = attempts + 1, locked_at=$1 "+
			"WHERE delivery_id = (SELECT delivery_id FROM platform_outbox "+
			"WHERE ((status = 'pending' AND next_attempt_at <= $2) OR (status = 'processing' AND locked_at < $3)) "+
			"AND NOT EXISTS (SELECT 1 FROM platform_outbox earlier "+
			"WHERE earlier.session_id = platform_outbox.session_id "+
			"AND earlier.delivery_id < platform_outbox.delivery_id "+
			"AND earlier.status IN ('pending','processing')) "+
			"ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED) "+
			"RETURNING delivery_id, config_id, session_id, platform::text, recipient_id, content, payload, status, attempts, chat_message_id",
		now, now, stale).Scan(&d.DeliveryID, &d.ConfigID, &d.SessionID, &d.Platform, &d.RecipientID, &d.Content, &payloadJSON, &d.Status, &d.Attempts, &d.LastMessageID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			p.Logger.Warn("claim outbound failed", "error", err.Error())
		}
		return nil, nil
	}
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &d.Payload)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &d, nil
}

func (p *Pipeline) deliver(ctx context.Context, d *outboundDelivery) {
	// Cancel queued model replies when the session left 'active'.
	var role string
	_ = p.DB.QueryRow(ctx, "SELECT role FROM chat_messages WHERE message_id = $1", d.LastMessageID).Scan(&role)
	var sessionStatus string
	_ = p.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id = $1", d.SessionID).Scan(&sessionStatus)
	if sessionStatus == "" {
		sessionStatus = "active"
	}
	if role == "model" && (sessionStatus == "handoff" || sessionStatus == "resolved" || sessionStatus == "closed") {
		reason := "Automatic reply cancelled because the conversation is no longer active"
		switch sessionStatus {
		case "handoff":
			reason = "Automatic reply cancelled because an agent took over the conversation"
		case "resolved":
			reason = "Automatic reply cancelled because the conversation was resolved"
		case "closed":
			reason = "Automatic reply cancelled because the conversation was closed"
		}
		_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='cancelled', locked_at=NULL, last_error=$1, updated_at=NOW() WHERE delivery_id=$2", reason, d.DeliveryID)
		// Keep the row for the transcript but hide it from future AI context:
		// the customer never received this reply, so the model must not
		// believe they did.
		_, _ = p.DB.Exec(ctx, "UPDATE chat_messages SET cancelled_at = NOW() WHERE message_id = $1 AND cancelled_at IS NULL", d.LastMessageID)
		return
	}
	if d.Status == "cancelled" {
		return
	}

	providerID, err := p.deliverToProvider(ctx, d, role == "agent")
	if err == nil {
		if providerID == "" {
			_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='sent', sent_at=$1, locked_at=NULL, last_error='' WHERE delivery_id=$2", time.Now(), d.DeliveryID)
		} else {
			_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='sent', sent_at=$1, locked_at=NULL, last_error='', provider_message_id=$2, provider_status='sent' WHERE delivery_id=$3", time.Now(), providerID, d.DeliveryID)
			// The row now carries the id a receipt needs, so any receipt that
			// arrived while we were still sending can finally be applied.
			p.flushDeliveryReceipts(ctx, d.ConfigID, providerID)
		}
		return
	}
	p.Logger.Warn("delivery failed", "delivery_id", d.DeliveryID, "error", err.Error())
	msg := textutil.TruncateRunes(err.Error(), 1000)
	if _, ok := err.(*PolicyError); ok || d.Attempts >= maxAttempts {
		_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='failed', next_attempt_at=$1, locked_at=NULL, last_error=$2, updated_at=NOW() WHERE delivery_id=$3", time.Now(), msg, d.DeliveryID)
		p.alertOutboundFailures(ctx, d.Platform, msg)
	} else {
		_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='pending', next_attempt_at=$1, locked_at=NULL, last_error=$2, updated_at=NOW() WHERE delivery_id=$3", time.Now().Add(retryDelay(int(d.Attempts))), msg, d.DeliveryID)
	}
}

// flushDeliveryReceipts replays the provider receipts that arrived before the
// outbox row carried provider_message_id — the second half of migration 014's
// contract, whose first half is retainDeliveryReceipt in webhooks.go.
//
// It runs in the worker, immediately after the id is written, and not in the
// webhook: the receipt can only be correlated once the id exists, and the
// worker is the one that creates that correlation.
//
// Receipts are applied oldest-first so the furthest provider state wins the
// last write (a "read" that happened before a redelivered "delivered" must not
// be overwritten), and each row is marked applied right after its own write —
// so a crash mid-replay resumes with the rest still pending rather than
// re-applying everything.
func (p *Pipeline) flushDeliveryReceipts(ctx context.Context, configID int32, providerID string) {
	rows, err := p.DB.Query(ctx,
		"SELECT receipt_id, status, occurred_at, COALESCE(failure_detail,'') FROM platform_delivery_receipts "+
			"WHERE config_id = $1 AND provider_message_id = $2 AND applied_at IS NULL "+
			"ORDER BY occurred_at, receipt_id",
		configID, providerID)
	if err != nil {
		p.Logger.Warn("pending delivery receipts not readable", "config_id", configID, "provider_message_id", providerID, "error", err.Error())
		return
	}
	defer rows.Close()
	type pendingReceipt struct {
		receiptID     int64
		state         string
		occurredAt    time.Time
		failureDetail string
	}
	var pending []pendingReceipt
	for rows.Next() {
		var r pendingReceipt
		if rows.Scan(&r.receiptID, &r.state, &r.occurredAt, &r.failureDetail) == nil {
			pending = append(pending, r)
		}
	}
	// A partially read list must not be replayed as if it were complete: the
	// rows that never arrived would stay unapplied behind receipts already
	// marked applied, and a provider redelivery would be the only way back.
	if err := rows.Err(); err != nil {
		p.Logger.Warn("pending delivery receipts read failed", "config_id", configID, "provider_message_id", providerID, "error", err.Error())
		return
	}
	for _, r := range pending {
		applied, err := applyProviderReceipt(ctx, p.DB, configID, providerID, r.state, r.failureDetail)
		if err != nil {
			p.Logger.Warn("delivery receipt replay failed", "config_id", configID, "provider_message_id", providerID, "receipt_id", r.receiptID, "error", err.Error())
			continue
		}
		if !applied {
			// No outbox row carries this id any more, so there is nothing to
			// apply. Leave the row pending rather than burning the record.
			continue
		}
		if _, err := p.DB.Exec(ctx, "UPDATE platform_delivery_receipts SET applied_at = NOW() WHERE receipt_id = $1", r.receiptID); err != nil {
			p.Logger.Warn("delivery receipt not marked applied", "receipt_id", r.receiptID, "error", err.Error())
		}
	}
}

// parseTelegramMessageID extracts the numeric message id from a stored
// provider_message_id ("<chat_id>:<message_id>", see TelegramProviderMessageID).
func parseTelegramMessageID(providerID string) (int64, bool) {
	idx := strings.LastIndex(providerID, ":")
	if idx < 0 || idx == len(providerID)-1 {
		return 0, false
	}
	id, err := strconv.ParseInt(providerID[idx+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// demotePreviousFeedbackKeyboard strips the 👍/👎 keyboard from the customer's
// previous AI reply, so exactly one message — the newest — stays rateable.
// Telegram stores an inline keyboard on the message itself and keeps it for
// the life of that message, so without this every answer in the conversation
// keeps offering 好评/差评 (and a customer can rate an old answer by mistake).
// Best-effort: the new reply is already delivered, so a failure is logged and
// never returned.
func (p *Pipeline) demotePreviousFeedbackKeyboard(ctx context.Context, client *TelegramClient, d *outboundDelivery) {
	var prevID string
	err := p.DB.QueryRow(ctx,
		"SELECT provider_message_id FROM platform_outbox "+
			"WHERE session_id = $1 AND platform = 'telegram' AND delivery_id <> $2 "+
			"AND status = 'sent' AND provider_message_id IS NOT NULL AND provider_message_id <> '' "+
			"AND payload->>'feedback' = 'true' "+
			"ORDER BY delivery_id DESC LIMIT 1",
		d.SessionID, d.DeliveryID).Scan(&prevID)
	if err != nil || prevID == "" {
		return // first rateable reply of the conversation — nothing to demote
	}
	msgID, ok := parseTelegramMessageID(prevID)
	if !ok {
		return
	}
	if err := client.EditMessageReplyMarkup(ctx, d.RecipientID, msgID); err != nil && p.Logger != nil {
		p.Logger.Warn("demoting previous feedback keyboard failed",
			"session_id", d.SessionID, "provider_message_id", prevID, "error", err.Error())
	}
}

// deliverToProvider sends via the correct provider client. Returns provider id.
// isHuman marks agent-authored messages: only those may use the Meta
// HUMAN_AGENT tag to reply within the 7-day extension window.
func (p *Pipeline) deliverToProvider(ctx context.Context, d *outboundDelivery, isHuman bool) (string, error) {
	isTemplate := false
	if d.Payload != nil {
		if k, _ := d.Payload["kind"].(string); k == "template" {
			isTemplate = true
		}
	}
	humanTag := ""
	_, extended, err := EnsureReplyWindow(ctx, p.DB, d.Platform, d.ConfigID, d.SessionID, isTemplate, isHuman, time.Now())
	if err != nil {
		if _, ok := err.(*PolicyError); ok {
			return "", err
		}
		return "", err
	}
	if extended {
		humanTag = HumanAgentTag
	}
	cfg, err := p.loadConfig(ctx, d.ConfigID)
	if err != nil {
		return "", err
	}
	kind := "text"
	var mediaURL, mediaType string
	var buttons [][2]string
	if d.Payload != nil {
		if k, ok := d.Payload["kind"].(string); ok && k != "" {
			kind = k
		}
		mediaURL, _ = d.Payload["media_url"].(string)
		mediaType, _ = d.Payload["media_type"].(string)
		if b, ok := d.Payload["buttons"].([]any); ok {
			for _, item := range b {
				if m, ok := item.(map[string]any); ok {
					title, _ := m["title"].(string)
					payload, _ := m["payload"].(string)
					buttons = append(buttons, [2]string{title, payload})
				}
			}
		}
	}

	ch, err := NewChannel(p, cfg)
	if err != nil {
		return "", err
	}
	msg := ChannelMessage{
		Delivery:    d,
		RecipientID: d.RecipientID,
		Kind:        kind,
		Content:     d.Content,
		MediaURL:    mediaURL,
		MediaType:   mediaType,
		Buttons:     buttons,
		Tag:         humanTag,
	}
	if d.Payload != nil {
		msg.TemplateName, _ = d.Payload["template_name"].(string)
		msg.TemplateLanguage, _ = d.Payload["template_language"].(string)
		if params, ok := d.Payload["template_body_params"].([]any); ok {
			for _, param := range params {
				if s, ok := param.(string); ok {
					msg.TemplateBodyParams = append(msg.TemplateBodyParams, s)
				}
			}
		}
	}
	return ch.Send(ctx, msg)
}

// PlatformTextLimit — per-platform outbound message length cap.
func PlatformTextLimit(platform string) int {
	// The number lives in the capability table (capabilities.go). Channels whose
	// cap counts bytes must split through SplitChannelText, not this value.
	return CapabilitiesFor(platform).TextLimit
}

// SplitPlatformText splits on rune boundaries, preferring a space near the cap.
func SplitPlatformText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := start + limit
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			breakAt := -1
			for i := end - 1; i > start; i-- {
				if runes[i] == ' ' || runes[i] == '\n' {
					breakAt = i
					break
				}
			}
			if breakAt > start {
				end = breakAt + 1
			}
		}
		out = append(out, string(runes[start:end]))
		start = end
	}
	return out
}
