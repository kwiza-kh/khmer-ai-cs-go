// Package platform — Telegram notify bot: pushes tenant notifications
// (new customer messages, handoff requests) to an owner-configured bot chat.
package platform

import (
	"context"
	"fmt"
	"strconv"
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
	var tokenEnc, chatID, chatTitle *string
	var notifyMessages, notifyHandoff, notifyAnnouncements bool
	err := p.DB.QueryRow(ctx,
		"SELECT bot_token_enc, chat_id, chat_title, notify_messages, notify_handoff, notify_announcements "+
			"FROM telegram_notify_settings WHERE user_id = $1", userID).
		Scan(&tokenEnc, &chatID, &chatTitle, &notifyMessages, &notifyHandoff, &notifyAnnouncements)
	if err != nil || chatID == nil || *chatID == "" {
		return nil
	}
	// A NULL token means the merchant linked through the platform bot (054);
	// the credential comes from the operator config instead of their own row.
	// A non-NULL one keeps the original per-merchant bot working untouched.
	token := ""
	if tokenEnc != nil && *tokenEnc != "" {
		dec, decErr := p.Sealer.Decrypt(*tokenEnc)
		if decErr != nil || dec == "" {
			return nil
		}
		token = dec
	} else {
		token = strings.TrimSpace(p.Cfg.PlatformBot.Token)
		if token == "" {
			return nil
		}
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
// The HTTP send runs in the background with its own timeout: this is called
// from request handlers (widget/handoff/feedback), and a slow Telegram API
// must not add up to 15s of latency to the caller.
func (p *Pipeline) NotifyHandoffRequest(ctx context.Context, userID int32, sessionID, reason string) {
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil || !cfg.NotifyHandoff {
		return
	}
	buttons := [][2]string{
		{"🧑‍💼 接管", "ho:takeover:" + sessionID},
		{"✅ 解决", "ho:resolve:" + sessionID},
	}
	botToken, chatID := cfg.BotToken, cfg.ChatID
	SpawnCritical(func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := NewTelegramClient(botToken).SendMessage(sendCtx, chatID, "🔔 "+reason, buttons); err != nil {
			p.Logger.Warn("telegram notify send failed", "user_id", userID, "error", err.Error())
		}
	})
}

// ProcessNotifyCallbacks — poll the notify bot's getUpdates and handle the
// inline-button presses (接管 / 解决). Runs on a background ticker; the notify
// bot has no webhook, so getUpdates is the delivery path. Per-tenant offset
// lives in Redis.
func (p *Pipeline) ProcessNotifyCallbacks(ctx context.Context, userID int32) {
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil {
		return
	}
	offsetKey := "tg-notify-offset:" + strconv.FormatInt(int64(userID), 10)
	offset := int64(0)
	if v, err := p.Redis.GetString(ctx, offsetKey); err == nil {
		offset, _ = strconv.ParseInt(v, 10, 64)
	}
	updates, err := NewTelegramClient(cfg.BotToken).GetUpdates(ctx, offset)
	if err != nil {
		return
	}
	maxUpdate := offset - 1
	for _, u := range updates {
		idF, _ := u["update_id"].(float64)
		updateID := int64(idF)
		if updateID >= maxUpdate {
			maxUpdate = updateID
		}
		cb, _ := u["callback_query"].(map[string]any)
		if cb == nil {
			continue
		}
		cbID, _ := cb["id"].(string)
		data, _ := cb["data"].(string)
		msg, _ := cb["message"].(map[string]any)
		chat, _ := msg["chat"].(map[string]any)
		chatID, _ := chat["id"].(string)
		if f, ok := chat["id"].(float64); ok {
			chatID = strconv.FormatFloat(f, 'f', -1, 64)
		}
		msgIDF, _ := msg["message_id"].(float64)
		// Only the owner's configured chat may act on these buttons.
		if chatID != cfg.ChatID {
			NewTelegramClient(cfg.BotToken).AnswerCallback(ctx, cbID, "无权操作")
			continue
		}
		parts := strings.Split(data, ":")
		if len(parts) != 3 || parts[0] != "ho" || (parts[1] != "takeover" && parts[1] != "resolve") {
			continue
		}
		sid := parts[2]
		action := parts[1]
		done := p.applyNotifyAction(ctx, userID, action, sid)
		if done {
			NewTelegramClient(cfg.BotToken).AnswerCallback(ctx, cbID,
				map[string]string{"takeover": "已接管 — 会话已分配给你", "resolve": "已解决 ✅"}[action])
			// Remove the buttons so the action cannot repeat.
			if msgIDF > 0 {
				NewTelegramClient(cfg.BotToken).EditMessageReplyMarkup(ctx, cfg.ChatID, int64(msgIDF))
			}
		} else {
			NewTelegramClient(cfg.BotToken).AnswerCallback(ctx, cbID, "请求不存在或已被处理")
		}
	}
	if maxUpdate >= offset {
		_ = p.Redis.SetString(ctx, offsetKey, strconv.FormatInt(maxUpdate+1, 10), 7*24*time.Hour)
	}
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

	var b strings.Builder
	b.WriteString("📊 过去 24 小时经营摘要\n")
	b.WriteString(fmt.Sprintf("• 新会话：%d\n", newSessions))
	b.WriteString(fmt.Sprintf("• 转人工：%d\n", handoffs))
	b.WriteString(fmt.Sprintf("• AI 独立解决：%d\n", aiResolved))
	if len(top) > 0 {
		b.WriteString("客户最常问：\n" + strings.Join(top, "\n"))
	}
	p.SendTelegramNotify(ctx, userID, b.String())
}
