// Package api — Telegram notification settings.
//
// Since 056 there is exactly one path in: the merchant taps "Connect Telegram",
// which deep-links into the platform bot and binds their chat. The old
// bring-your-own-bot setup — paste a BotFather token, message the bot, poll
// getUpdates to discover the chat id — is gone. It needed five steps, one of
// which assumed the merchant knew what a bot token was, and it forced every
// merchant-side feature to be built twice. The second build never happened:
// the inline 接管/解决 buttons were only ever wired to the per-tenant bot.
//
// The API can therefore no longer set a chat. The chat is owned by the linking
// flow, and an endpoint that writes an arbitrary chat id is precisely what the
// removal was meant to end.
package api

import (
	"encoding/json"
	"net/http"

	"khmer-ai-cs-go/internal/platform"
)

type telegramNotifyPayload struct {
	NotifyMessages      *bool `json:"notify_messages"`
	NotifyHandoff       *bool `json:"notify_handoff"`
	NotifyAnnouncements *bool `json:"notify_announcements"`
}

// getTelegramNotify — current setup. No credentials exist to return any more,
// only the bound chat and the three toggles.
func (a *App) getTelegramNotify(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	// platform_bot_ready tells the page whether to offer the connect button at
	// all: without PLATFORM_TELEGRAM_BOT_TOKEN there is no bot to link to, and
	// the honest state is "unavailable" rather than a button that always fails.
	platformReady := a.Pipe != nil && a.Pipe.PlatformBotEnabled()
	out := map[string]any{
		"configured":           false,
		"chat_title":           "",
		"notify_messages":      true,
		"notify_handoff":       true,
		"notify_announcements": true,
		"platform_bot_ready":   platformReady,
	}
	if a.Pipe == nil {
		return out, nil
	}
	cfg := a.Pipe.LoadTelegramNotify(r.Context(), user.UserID)
	if cfg == nil {
		return out, nil
	}
	out["configured"] = true
	out["chat_title"] = cfg.ChatTitle
	out["notify_messages"] = cfg.NotifyMessages
	out["notify_handoff"] = cfg.NotifyHandoff
	out["notify_announcements"] = cfg.NotifyAnnouncements
	return out, nil
}

// putTelegramNotify — update the three toggles for a linked account.
func (a *App) putTelegramNotify(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req telegramNotifyPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	ctx := r.Context()
	if a.Pipe == nil || !a.Pipe.PlatformBotEnabled() {
		return nil, ErrBadRequest("平台 Telegram bot 未配置")
	}
	cfg := a.Pipe.LoadTelegramNotify(ctx, user.UserID)
	if cfg == nil {
		// Nothing to update: the toggles only mean something once a chat is
		// bound, and silently creating a row here would produce a setting that
		// looks configured but delivers nowhere.
		return nil, ErrBadRequest("请先连接 Telegram")
	}
	notifyMessages, notifyHandoff, notifyAnnouncements := cfg.NotifyMessages, cfg.NotifyHandoff, cfg.NotifyAnnouncements
	if req.NotifyMessages != nil {
		notifyMessages = *req.NotifyMessages
	}
	if req.NotifyHandoff != nil {
		notifyHandoff = *req.NotifyHandoff
	}
	if req.NotifyAnnouncements != nil {
		notifyAnnouncements = *req.NotifyAnnouncements
	}
	if _, err := a.DB.Exec(ctx,
		"UPDATE telegram_notify_settings SET notify_messages=$1, notify_handoff=$2, "+
			"notify_announcements=$3, updated_at=NOW() WHERE user_id=$4",
		notifyMessages, notifyHandoff, notifyAnnouncements, user.UserID); err != nil {
		return nil, ErrInternal("保存失败")
	}
	platform.InvalidateTelegramNotify(user.UserID)
	return map[string]any{"message": "已保存", "configured": true,
		"notify_messages": notifyMessages, "notify_handoff": notifyHandoff,
		"notify_announcements": notifyAnnouncements}, nil
}

// postTelegramNotifyLink — mint a one-time deep link the merchant taps to bind
// their Telegram chat to this account. This is the whole setup flow.
func (a *App) postTelegramNotifyLink(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if a.Pipe == nil || !a.Pipe.PlatformBotEnabled() {
		return nil, ErrBadRequest("平台 Telegram bot 未配置，无法使用一键连接")
	}
	token, err := a.Pipe.IssueLinkToken(r.Context(), user.UserID)
	if err != nil {
		return nil, ErrInternal("生成连接链接失败")
	}
	link := a.Pipe.PlatformBotLink(token)
	if link == "" {
		return nil, ErrBadRequest("平台 bot 用户名未配置 (PLATFORM_TELEGRAM_BOT_USERNAME)")
	}
	return map[string]any{"link": link, "expires_in_seconds": 600}, nil
}

// postTelegramNotifyTest — send a test message through the platform bot.
func (a *App) postTelegramNotifyTest(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if a.Pipe == nil {
		return nil, ErrBadRequest("平台 Telegram bot 未配置")
	}
	if err := a.Pipe.SendTelegramNotifyChecked(r.Context(), user.UserID, "✅ RelayChat 通知测试成功 — Telegram 通知已就绪 / Telegram notify test"); err != nil {
		return nil, &ApiError{http.StatusBadGateway, "测试发送失败: " + err.Error()}
	}
	return map[string]string{"message": "已发送"}, nil
}
