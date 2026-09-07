// Package platform — Telegram notify bot: pushes tenant notifications
// (new customer messages, handoff requests) to an owner-configured bot chat.
package platform

import (
	"context"
	"strings"
	"time"
)

// TelegramNotifyConfig is the decrypted per-tenant notify setup.
type TelegramNotifyConfig struct {
	BotToken       string
	ChatID         string
	ChatTitle      string
	NotifyMessages bool
	NotifyHandoff  bool
}

// LoadTelegramNotify loads + decrypts the tenant's notify config; nil when
// unset or unusable (missing token/chat).
func (p *Pipeline) LoadTelegramNotify(ctx context.Context, userID int32) *TelegramNotifyConfig {
	var tokenEnc, chatID, chatTitle *string
	var notifyMessages, notifyHandoff bool
	err := p.DB.QueryRow(ctx,
		"SELECT bot_token_enc, chat_id, chat_title, notify_messages, notify_handoff "+
			"FROM telegram_notify_settings WHERE user_id = $1", userID).
		Scan(&tokenEnc, &chatID, &chatTitle, &notifyMessages, &notifyHandoff)
	if err != nil || tokenEnc == nil || *tokenEnc == "" || chatID == nil || *chatID == "" {
		return nil
	}
	token, decErr := p.Sealer.Decrypt(*tokenEnc)
	if decErr != nil || token == "" {
		return nil
	}
	return &TelegramNotifyConfig{
		BotToken:       token,
		ChatID:         *chatID,
		ChatTitle:      deref(chatTitle),
		NotifyMessages: notifyMessages,
		NotifyHandoff:  notifyHandoff,
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
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil || !cfg.NotifyMessages {
		return
	}
	if !ThrottleTelegramMessage(p.Redis, sessionID) {
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

// NotifyHandoffRequest — Telegram ping for one handoff request.
func (p *Pipeline) NotifyHandoffRequest(ctx context.Context, userID int32, reason string) {
	cfg := p.LoadTelegramNotify(ctx, userID)
	if cfg == nil || !cfg.NotifyHandoff {
		return
	}
	p.SendTelegramNotify(ctx, userID, "🔔 "+reason)
}
