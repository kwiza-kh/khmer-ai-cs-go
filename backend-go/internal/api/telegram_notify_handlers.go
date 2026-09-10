// Package api — Telegram notify-bot settings (owner setup + test + chat
// discovery). The bot token is stored Sealer-encrypted like platform creds.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/platform"
)

type telegramNotifyPayload struct {
	BotToken       string `json:"bot_token"`
	ChatID         string `json:"chat_id"`
	ChatTitle      string `json:"chat_title"`
	NotifyMessages *bool  `json:"notify_messages"`
	NotifyHandoff  *bool  `json:"notify_handoff"`
}

// getTelegramNotify — current setup (token never returned, only metadata).
func (a *App) getTelegramNotify(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	cfg := a.Pipe.LoadTelegramNotify(r.Context(), user.UserID)
	out := map[string]any{
		"configured":      false,
		"chat_id":         "",
		"chat_title":      "",
		"notify_messages": true,
		"notify_handoff":  true,
	}
	if cfg == nil {
		return out, nil
	}
	out["configured"] = true
	out["chat_id"] = cfg.ChatID
	out["chat_title"] = cfg.ChatTitle
	out["notify_messages"] = cfg.NotifyMessages
	out["notify_handoff"] = cfg.NotifyHandoff
	return out, nil
}

// putTelegramNotify — save the setup. Empty bot_token keeps the stored one.
func (a *App) putTelegramNotify(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req telegramNotifyPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	ctx := r.Context()

	var existingTokenEnc *string
	var existingChatID, existingChatTitle string
	var notifyMessages, notifyHandoff = true, true
	var hasRow bool
	err := a.DB.QueryRow(ctx,
		"SELECT bot_token_enc, chat_id, chat_title, notify_messages, notify_handoff FROM telegram_notify_settings WHERE user_id = $1",
		user.UserID).Scan(&existingTokenEnc, &existingChatID, &existingChatTitle, &notifyMessages, &notifyHandoff)
	hasRow = err == nil

	tokenEnc := ""
	if hasRow && existingTokenEnc != nil {
		tokenEnc = *existingTokenEnc
	}
	chatID := existingChatID
	chatTitle := existingChatTitle
	if req.BotToken != "" {
		token := strings.TrimSpace(req.BotToken)
		enc, encErr := a.Sealer.Encrypt(token)
		if encErr != nil {
			return nil, ErrInternal("加密失败")
		}
		tokenEnc = enc
		// Token changed — verify it and resolve the bot username as feedback.
		if _, username, _, meErr := platform.NewTelegramClient(token).GetMe(ctx); meErr == nil && username != "" {
			chatTitle = "@" + username
		}
	}
	if req.ChatID != "" {
		chatID = strings.TrimSpace(req.ChatID)
	}
	if req.ChatTitle != "" {
		chatTitle = strings.TrimSpace(req.ChatTitle)
	}
	if req.NotifyMessages != nil {
		notifyMessages = *req.NotifyMessages
	}
	if req.NotifyHandoff != nil {
		notifyHandoff = *req.NotifyHandoff
	}
	if tokenEnc == "" || chatID == "" {
		return nil, ErrBadRequest("需要 bot_token 和 chat_id")
	}

	if hasRow {
		_, _ = a.DB.Exec(ctx,
			"UPDATE telegram_notify_settings SET bot_token_enc=$1, chat_id=$2, chat_title=$3, notify_messages=$4, notify_handoff=$5, updated_at=NOW() WHERE user_id=$6",
			tokenEnc, chatID, chatTitle, notifyMessages, notifyHandoff, user.UserID)
	} else {
		_, _ = a.DB.Exec(ctx,
			"INSERT INTO telegram_notify_settings (user_id, bot_token_enc, chat_id, chat_title, notify_messages, notify_handoff) VALUES ($1,$2,$3,$4,$5,$6)",
			user.UserID, tokenEnc, chatID, chatTitle, notifyMessages, notifyHandoff)
	}
	platform.InvalidateTelegramNotify(user.UserID)
	return map[string]any{"message": "已保存", "configured": true, "chat_id": chatID, "chat_title": chatTitle,
		"notify_messages": notifyMessages, "notify_handoff": notifyHandoff}, nil
}

// postTelegramNotifyUpdates — pull recent bot chats via getUpdates so the
// owner can pick the destination chat. Accepts an unsaved token.
func (a *App) postTelegramNotifyUpdates(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		BotToken string `json:"bot_token"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req)
	token := strings.TrimSpace(req.BotToken)
	if token == "" {
		cfg := a.Pipe.LoadTelegramNotify(r.Context(), user.UserID)
		if cfg == nil {
			return nil, ErrBadRequest("请先填写 bot token")
		}
		token = cfg.BotToken
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	updates, err := platform.NewTelegramClient(token).GetUpdates(ctx, 0)
	if err != nil {
		return nil, &ApiError{http.StatusBadGateway, "获取会话失败: " + err.Error()}
	}
	type chatItem struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	seen := map[string]bool{}
	chats := make([]chatItem, 0)
	for _, u := range updates {
		msg, _ := u["message"].(map[string]any)
		if msg == nil {
			continue
		}
		chat, _ := msg["chat"].(map[string]any)
		if chat == nil {
			continue
		}
		id, _ := chat["id"].(string)
		if f, ok := chat["id"].(float64); ok {
			id = trimFloat(f)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		title := ""
		if fn, ok := chat["first_name"].(string); ok {
			title = fn
		}
		if un, ok := chat["username"].(string); ok && un != "" {
			title += " (@" + un + ")"
		}
		if t, ok := chat["title"].(string); ok && t != "" {
			title = t
		}
		chats = append(chats, chatItem{ID: id, Title: title})
	}
	return map[string]any{"chats": chats}, nil
}

// postTelegramNotifyTest — send a test message through the saved/new config.
func (a *App) postTelegramNotifyTest(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := a.Pipe.SendTelegramNotifyChecked(r.Context(), user.UserID, "✅ Khmer AI 通知测试成功 — Telegram 通知已就绪 / Telegram notify test"); err != nil {
		return nil, &ApiError{http.StatusBadGateway, "测试发送失败: " + err.Error()}
	}
	return map[string]string{"message": "已发送"}, nil
}

func trimFloat(f float64) string {
	// Telegram chat ids exceed float64's integer precision in scientific
	// notation via json.Marshal — format plainly instead.
	return strconv.FormatFloat(f, 'f', -1, 64)
}
