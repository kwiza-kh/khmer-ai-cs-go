// Package platform — Telegram notify bot: pushes tenant notifications
// (new customer messages, handoff requests) to an owner-configured bot chat.
package platform

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TelegramNotifyConfig is the decrypted per-tenant notify setup.
type TelegramNotifyConfig struct {
	BotToken       string
	ChatID         string
	ChatTitle      string
	NotifyMessages bool
	NotifyHandoff  bool
	// NotifyAnnouncements gates platform-wide broadcasts (055). Opt-OUT: a
	// broadcast reaches every linked merchant, so they must be able to refuse.
	NotifyAnnouncements bool
}

// notifyCache memoizes decrypted notify configs briefly — this is on the
// per-message path and repeated DB hits + AES decrypts add up.
type notifyCacheEntry struct {
	cfg     *TelegramNotifyConfig
	expires time.Time
}

var (
	notifyCacheMu sync.Mutex
	notifyCache   = map[int32]notifyCacheEntry{}
)

// InvalidateTelegramNotify drops the cache entry after a settings change.
func InvalidateTelegramNotify(userID int32) {
	notifyCacheMu.Lock()
	delete(notifyCache, userID)
	notifyCacheMu.Unlock()
}

// LoadTelegramNotify loads + decrypts the tenant's notify config; nil when
// unset or unusable (missing token/chat). Cached for 30 seconds.
func (p *Pipeline) LoadTelegramNotify(ctx context.Context, userID int32) *TelegramNotifyConfig {
	notifyCacheMu.Lock()
	if e, ok := notifyCache[userID]; ok && time.Now().Before(e.expires) {
		notifyCacheMu.Unlock()
		return e.cfg
	}
	notifyCacheMu.Unlock()
	cfg := p.loadTelegramNotifyUncached(ctx, userID)
	notifyCacheMu.Lock()
	if len(notifyCache) > 500 {
		notifyCache = map[int32]notifyCacheEntry{}
	}
	notifyCache[userID] = notifyCacheEntry{cfg: cfg, expires: time.Now().Add(30 * time.Second)}
	notifyCacheMu.Unlock()
	return cfg
}

func (p *Pipeline) loadTelegramNotifyUncached(ctx context.Context, userID int32) *TelegramNotifyConfig {
	var chatID, chatTitle *string
	var notifyMessages, notifyHandoff, notifyAnnouncements bool
	err := p.DB.QueryRow(ctx,
		"SELECT chat_id, chat_title, notify_messages, notify_handoff, notify_announcements "+
			"FROM telegram_notify_settings WHERE user_id = $1", userID).
		Scan(&chatID, &chatTitle, &notifyMessages, &notifyHandoff, &notifyAnnouncements)
	if err != nil || chatID == nil || *chatID == "" {
		return nil
	}
	// 056: every notification goes out through the platform bot. The
	// per-merchant token column is no longer read — it could not work anyway,
	// since the platform bot cannot address a chat the merchant started with a
	// different bot. A blank platform token means the feature is unavailable,
	// not that there is a fallback.
	token := strings.TrimSpace(p.Cfg.PlatformBot.Token)
	if token == "" {
		return nil
	}
	return &TelegramNotifyConfig{
		BotToken:            token,
		ChatID:              *chatID,
		ChatTitle:           deref(chatTitle),
		NotifyMessages:      notifyMessages,
		NotifyHandoff:       notifyHandoff,
		NotifyAnnouncements: notifyAnnouncements,
	}
}

// SessionLink — deep link that opens the session in the agent inbox
// (/inbox?session=...). Exported for the request-side notify path. Empty when
// PUBLIC_API_URL is unset.
func (p *Pipeline) SessionLink(sessionID string) string {
	return p.sessionLink(sessionID)
}

// sessionLink — deep link that opens the session in the agent inbox
// (/inbox?session=...). Empty when PUBLIC_API_URL is unset.
func (p *Pipeline) sessionLink(sessionID string) string {
	origin := strings.TrimSuffix(strings.TrimSpace(p.Cfg.Server.PublicAPIURL), "/")
	if origin == "" || sessionID == "" {
		return ""
	}
	return origin + "/inbox?session=" + sessionID
}

// SendTelegramNotify delivers one text best-effort (errors are logged, never
// returned — notifications must not break the message pipeline). Detaches
// from the request context so SSE disconnects don't drop the send.
func (p *Pipeline) SendTelegramNotify(ctx context.Context, userID int32, text string) {
	_ = p.SendTelegramNotifyChecked(ctx, userID, text)
}

// SendTelegramNotifyChecked — same send but returns the error (settings test
// button needs pass/fail feedback).
func (p *Pipeline) SendTelegramNotifyChecked(ctx context.Context, userID int32, text string) error {
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil {
		return ErrNotConfigured
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	if _, err := NewTelegramClient(cfg.BotToken).SendMessage(sendCtx, cfg.ChatID, text, nil); err != nil {
		p.Logger.Warn("telegram notify send failed", "user_id", userID, "error", err.Error())
		return err
	}
	return nil
}

// ErrNotConfigured — sentinel for the settings test flow.
var ErrNotConfigured = errNotConfigured{}

type errNotConfigured struct{}

func (errNotConfigured) Error() string { return "telegram notify is not configured" }

// ThrottleTelegramMessage — at most one Telegram ping per session per 30s so
// a chatty customer doesn't flood the owner's phone (fail-open: on Redis
// errors we prefer notifying over silencing).
func ThrottleTelegramMessage(redis redisWindowLimiter, sessionID string) bool {
	if sessionID == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ok, err := redis.IncrWindow(ctx, "tg-notify-throttle:"+sessionID, 1, 30*time.Second)
	return err != nil || ok
}

// redisWindowLimiter — the slice of redisstore.Client this file needs.
type redisWindowLimiter interface {
	IncrWindow(ctx context.Context, key string, max int64, ttl time.Duration) (bool, error)
}

// NotifyNewCustomerMessage — best-effort Telegram ping for one inbound
// customer message (throttled per session). Includes a deep link that opens
// the conversation in the agent inbox.
func (p *Pipeline) NotifyNewCustomerMessage(ctx context.Context, userID int32, sessionID, platformName, displayName, content string) {
	// Throttle first: a throttled message should not pay for the config
	// lookup and AES decryption.
	if !ThrottleTelegramMessage(p.Redis, sessionID) {
		return
	}
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil || !cfg.NotifyMessages {
		return
	}
	name := displayName
	if name == "" {
		name = "customer"
	}
	text := "💬 [" + strings.ToUpper(platformName[:1]) + platformName[1:] + "] " + name + "\n" + truncateStr(content, 300)
	if link := p.sessionLink(sessionID); link != "" {
		text += "\n🔗 " + link
	}
	p.SendTelegramNotify(ctx, userID, text)
}

// bumpMessagesUsed — advance the tenant billing counter so the usage dashboards
// reflect reality (enforcement is a deliberate product decision, kept off).
func (p *Pipeline) bumpMessagesUsed(ctx context.Context, userID int32) {
	_, _ = p.DB.Exec(ctx,
		"UPDATE tenant_billing SET messages_used = messages_used + 1, updated_at = NOW() WHERE user_id = $1", userID)
}

// NotifyHandoffRequest — Telegram ping for one handoff request, with inline
// buttons so the owner can take over or resolve right from the phone.
//
// The button presses come back through the platform bot's webhook (see
// handlePlatformCallback) rather than a getUpdates poll: since 056 this is the
// only bot that sends them, and the operator bot is webhook-driven.
//
// The HTTP send runs in the background with its own timeout: this is called
// from request handlers (widget/handoff/feedback), and a slow Telegram API
// must not add up to 15s of latency to the caller.
func (p *Pipeline) NotifyHandoffRequest(ctx context.Context, userID int32, sessionID, reason string) {
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil || !cfg.NotifyHandoff {
		return
	}
	lang := p.merchantLang(ctx, userID, "")
	buttons := [][2]string{
		{merchantText(lang, "btn_takeover"), "ho:takeover:" + sessionID},
		{merchantText(lang, "btn_resolve"), "ho:resolve:" + sessionID},
	}
	text := merchantText(lang, "handoff_header") + "\n" + reason
	botToken, chatID := cfg.BotToken, cfg.ChatID
	SpawnCritical(func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := NewTelegramClient(botToken).SendMessage(sendCtx, chatID, text, buttons); err != nil {
			p.Logger.Warn("telegram notify send failed", "user_id", userID, "error", err.Error())
		}
	})
}

// merchantLang resolves which language to speak to a merchant in.
//
// users.language is the merchant's explicit AI-reply-language preference; an
// empty value means "auto-detect per message" and carries no signal, so it
// falls through to the caller's hint — normally the Telegram client's own
// language_code, which is the only thing known before an account is linked.
func (p *Pipeline) merchantLang(ctx context.Context, userID int32, fallback string) string {
	var lang string
	if err := p.DB.QueryRow(ctx, "SELECT language FROM users WHERE user_id = $1", userID).Scan(&lang); err == nil {
		if strings.TrimSpace(lang) != "" {
			return botLang(lang)
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return botLang(fallback)
	}
	return "en"
}

// UnlinkMerchant removes the binding between a Telegram chat and its account.
// Clears chat_id so delivery stops and the settings page reports "not
// connected" — leaving it behind would let the page claim "connected" while
// every send silently failed.
func (p *Pipeline) UnlinkMerchant(ctx context.Context, userID int32) bool {
	tag, err := p.DB.Exec(ctx,
		"UPDATE telegram_notify_settings SET chat_id = '', chat_title = '', updated_at = NOW() "+
			"WHERE user_id = $1 AND chat_id <> ''", userID)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	InvalidateTelegramNotify(userID)
	return true
}

// applyNotifyAction executes one notify-bot button action. Returns false when
// the request was not in an actionable state.
func (p *Pipeline) applyNotifyAction(ctx context.Context, userID int32, action, sessionID string) bool {
	switch action {
	case "takeover":
		tag, err := p.DB.Exec(ctx,
			"UPDATE human_handoff_requests SET status='assigned', assigned_at=NOW() "+
				"WHERE session_id=$1 AND user_id=$2 AND status='pending'", sessionID, userID)
		if err != nil || tag.RowsAffected() == 0 {
			return false
		}
		_, _ = p.DB.Exec(ctx,
			"UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at, NOW()) WHERE session_id=$1 AND status='active'", sessionID)
		p.publishSession(ctx, userID, sessionID)
		return true
	case "resolve":
		tag, err := p.DB.Exec(ctx,
			"UPDATE human_handoff_requests SET status='resolved', resolved_at=NOW(), resolution_note='resolved from Telegram' "+
				"WHERE session_id=$1 AND user_id=$2 AND status IN ('pending','assigned')", sessionID, userID)
		if err != nil || tag.RowsAffected() == 0 {
			return false
		}
		_, _ = p.DB.Exec(ctx,
			"UPDATE sessions SET status='resolved', resolved_at=NOW() WHERE session_id=$1 AND status IN ('active','handoff')", sessionID)
		p.publishSession(ctx, userID, sessionID)
		return true
	}
	return false
}

// SendDailyDigest — the 8:00 owner report: last-24h conversation stats and
// the most-asked customer questions.
func (p *Pipeline) SendDailyDigest(ctx context.Context, userID int32) {
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil {
		return
	}
	var newSessions, handoffs, aiResolved int64
	_ = p.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND created_at >= NOW() - INTERVAL '24 hours'", userID).Scan(&newSessions)
	_ = p.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM human_handoff_requests WHERE user_id=$1 AND created_at >= NOW() - INTERVAL '24 hours'", userID).Scan(&handoffs)
	_ = p.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND resolved_at >= NOW() - INTERVAL '24 hours' AND escalated_at IS NULL", userID).Scan(&aiResolved)

	rows, err := p.DB.Query(ctx,
		"SELECT LEFT(cm.content, 80), COUNT(*) FROM chat_messages cm "+
			"JOIN sessions s ON s.session_id = cm.session_id "+
			"WHERE s.user_id=$1 AND cm.role='user' AND cm.created_at >= NOW() - INTERVAL '24 hours' "+
			"GROUP BY 1 ORDER BY 2 DESC LIMIT 3", userID)
	top := make([]string, 0, 3)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var q string
			var n int64
			if rows.Scan(&q, &n) == nil {
				top = append(top, fmt.Sprintf("%d. %s (×%d)", len(top)+1, strings.ReplaceAll(q, "\n", " "), n))
			}
		}
	}

	lang := p.merchantLang(ctx, userID, "")
	var b strings.Builder
	b.WriteString(merchantText(lang, "digest_title") + "\n")
	b.WriteString(fmt.Sprintf("• %s: %d\n", merchantText(lang, "digest_new"), newSessions))
	b.WriteString(fmt.Sprintf("• %s: %d\n", merchantText(lang, "digest_handoff"), handoffs))
	b.WriteString(fmt.Sprintf("• %s: %d\n", merchantText(lang, "digest_ai"), aiResolved))
	if len(top) > 0 {
		b.WriteString(merchantText(lang, "digest_top") + "\n" + strings.Join(top, "\n"))
	}
	p.SendTelegramNotify(ctx, userID, b.String())
}
