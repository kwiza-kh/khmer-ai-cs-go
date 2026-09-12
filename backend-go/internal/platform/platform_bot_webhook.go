package platform

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ============================================
// Platform bot webhook
// ============================================

// telegramUpdate is the slice of the Bot API Update object this bot handles.
type telegramUpdate struct {
	Message *struct {
		MessageID int64 `json:"message_id"`
		From      *struct {
			ID        int64  `json:"id"`
			IsBot     bool   `json:"is_bot"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			Username  string `json:"username"`
		} `json:"from"`
		Chat *struct {
			ID    int64  `json:"id"`
			Type  string `json:"type"`
			Title string `json:"title"`
		} `json:"chat"`
		Text string `json:"text"`
		// Present when the operator long-presses one of our alerts and replies.
		// This is the whole reply mechanism for the support relay.
		ReplyTo *struct {
			MessageID int64 `json:"message_id"`
		} `json:"reply_to_message"`
	} `json:"message"`
}

// PlatformBotWebhook receives updates for the operator bot.
//
// Mounted publicly, so it authenticates the way Telegram intends: the
// secret_token registered with setWebhook comes back verbatim in
// X-Telegram-Bot-Api-Secret-Token on every delivery. Without that check the
// endpoint would accept forged commands from anyone who learned the URL — and
// these commands can read across every tenant.
func (p *Pipeline) PlatformBotWebhook(w http.ResponseWriter, r *http.Request) {
	if !p.PlatformBotEnabled() {
		w.WriteHeader(http.StatusOK)
		return
	}
	want := strings.TrimSpace(p.Cfg.PlatformBot.WebhookSecret)
	if want == "" {
		// No secret configured: refuse rather than run an unauthenticated
		// command surface. Better a silent bot than an open one.
		p.Logger.Warn("platform bot webhook called but no secret is configured")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	// Acknowledge before doing any work: Telegram retries on non-2xx, and a
	// slow command handler would otherwise produce duplicate deliveries.
	w.WriteHeader(http.StatusOK)

	var upd telegramUpdate
	if json.Unmarshal(body, &upd) != nil || upd.Message == nil || upd.Message.From == nil {
		return
	}
	// Detach: the HTTP response is already written, so the request context is
	// about to be cancelled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	p.dispatchPlatformUpdate(ctx, &upd)
}

// dispatchPlatformUpdate routes one message: account linking, operator
// commands, or the merchant support inbox.
func (p *Pipeline) dispatchPlatformUpdate(ctx context.Context, upd *telegramUpdate) {
	msg := upd.Message
	if msg.From.IsBot {
		return
	}
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	senderID := msg.From.ID
	text := strings.TrimSpace(msg.Text)

	display := strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
	handle := msg.From.Username

	// --- account linking: /start <token> ---
	if strings.HasPrefix(text, "/start") {
		arg := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
		if arg == "" {
			p.SendPlatformMessage(ctx, chatID,
				"👋 RelayChat bot.\n\nTo connect your RelayChat account, open Settings → Telegram notifications and tap *Connect Telegram*.",
				nil)
			return
		}
		userID, ok := p.RedeemLinkToken(ctx, arg, chatID, deref(&msg.Chat.Title))
		if !ok {
			p.SendPlatformMessage(ctx, chatID,
				"⚠️ That connect link is invalid or has expired. Generate a fresh one from Settings → Telegram notifications.", nil)
			return
		}
		p.SendPlatformMessage(ctx, chatID,
			"✅ Connected. RelayChat notifications for your account will arrive in this chat.", nil)
		p.Logger.Info("telegram account linked", "user_id", userID, "chat_id", chatID)
		return
	}

	// --- operator reply to a relayed support message ---
	// Checked BEFORE commands so an answer that happens to start with "/" is
	// still delivered as an answer rather than parsed as a command.
	if p.IsPlatformAdminChat(senderID) && msg.ReplyTo != nil {
		if p.relayAdminReply(ctx, msg.Chat.ID, msg.ReplyTo.MessageID, text) {
			return
		}
	}

	// --- operator commands (allow-list only) ---
	if p.IsPlatformAdminChat(senderID) {
		if p.handleAdminCommand(ctx, chatID, senderID, text) {
			return
		}
	}

	// --- merchant support inbox ---
	linkedUserID, linked := p.lookupLinkedUser(ctx, senderID, chatID)
	if strings.HasPrefix(text, "/") {
		// An unknown command from a non-admin: do not file it as a support
		// message, just explain what this bot does.
		p.SendPlatformMessage(ctx, chatID,
			"Commands: /start — connect your account.\nMessage me anything else and it reaches the RelayChat team.", nil)
		return
	}
	if text == "" {
		return
	}
	var supportID int64
	if err := p.DB.QueryRow(ctx,
		"INSERT INTO platform_support_messages (user_id, telegram_user_id, chat_id, username, display_name, body) "+
			"VALUES ($1,$2,$3,$4,$5,$6) RETURNING message_id",
		uidOrNil(linkedUserID, linked), senderID, chatID, handle, display, text).Scan(&supportID); err != nil {
		p.Logger.Warn("support message persist failed", "error", err.Error())
		return
	}
	p.SendPlatformMessage(ctx, chatID, "📨 已收到，消息已转达 RelayChat 团队。", nil)

	// Relay to the operator and remember WHICH of their messages carries this
	// conversation, so their reply can be routed back.
	//
	// Deliberately NOT PlatformAlert: that dedups for 15 minutes, which would
	// both hide a merchant's follow-up and lose the relay mapping for it.
	who := display
	if handle != "" {
		who += " (@" + handle + ")"
	}
	if linked {
		who += fmt.Sprintf(" · account #%d", linkedUserID)
	} else {
		who += " · not linked"
	}
	adminChat := p.platformAlertChat()
	if adminChat == "" || adminChat == chatID {
		// No relay target, or the sender IS the operator (a plain message from
		// them would otherwise notify them about themselves).
		return
	}
	copyID, err := p.SendPlatformMessageID(ctx, adminChat,
		fmt.Sprintf("💬 商家消息\n%s\n\n%s\n\n↩️ 直接回复本条消息即可回给该商家",
			who, truncateRunes(text, 800)), nil)
	if err == nil {
		p.storeSupportRelay(ctx, adminChat, copyID, msg.Chat.ID, supportID)
	}
}

// storeSupportRelay records which operator-facing message carries which
// merchant conversation, so a reply to it can be routed back.
func (p *Pipeline) storeSupportRelay(ctx context.Context, adminChat string, adminMessageID string, merchantChatID int64, supportID int64) {
	adminID, err := strconv.ParseInt(strings.TrimSpace(adminChat), 10, 64)
	if err != nil || adminID == 0 {
		return
	}
	mid, err := strconv.ParseInt(strings.TrimSpace(adminMessageID), 10, 64)
	if err != nil || mid == 0 {
		return
	}
	_, _ = p.DB.Exec(ctx,
		"INSERT INTO platform_support_relay (admin_chat_id, admin_message_id, merchant_chat_id, support_message_id) "+
			"VALUES ($1,$2,$3,$4) ON CONFLICT (admin_chat_id, admin_message_id) DO NOTHING",
		adminID, mid, merchantChatID, supportID)
}

// relayAdminReply routes an operator's reply back to the merchant it answers.
// Returns false when the replied-to message is not a relayed support message,
// so the caller falls through to command handling.
func (p *Pipeline) relayAdminReply(ctx context.Context, adminChatID, adminMessageID int64, text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	var merchantChat int64
	var supportID *int64
	err := p.DB.QueryRow(ctx,
		"SELECT merchant_chat_id, support_message_id FROM platform_support_relay "+
			"WHERE admin_chat_id = $1 AND admin_message_id = $2",
		adminChatID, adminMessageID).Scan(&merchantChat, &supportID)
	if err != nil {
		return false
	}
	adminChat := strconv.FormatInt(adminChatID, 10)
	if err := p.SendPlatformMessage(ctx, strconv.FormatInt(merchantChat, 10), "💬 RelayChat 团队：\n\n"+text, nil); err != nil {
		p.SendPlatformMessage(ctx, adminChat, "⚠️ 回复发送失败："+err.Error(), nil)
		return true
	}
	if supportID != nil {
		_, _ = p.DB.Exec(ctx,
			"UPDATE platform_support_messages SET replied_at = NOW(), replied_by = $1 WHERE message_id = $2",
			adminChatID, *supportID)
	}
	// One relay, one reply: consume the mapping so hitting reply twice does not
	// silently re-send the same answer.
	_, _ = p.DB.Exec(ctx,
		"DELETE FROM platform_support_relay WHERE admin_chat_id = $1 AND admin_message_id = $2",
		adminChatID, adminMessageID)
	p.SendPlatformMessage(ctx, adminChat, "✅ 已送达该商家。", nil)
	return true
}

// lookupLinkedUser resolves which RelayChat account owns this Telegram chat.
// Matched on the numeric Telegram id via the account's linked telegram_sub —
// never on @username, which is mutable and recyclable.
func (p *Pipeline) lookupLinkedUser(ctx context.Context, telegramUserID int64, chatID string) (int32, bool) {
	var userID int32
	err := p.DB.QueryRow(ctx,
		"SELECT user_id FROM users WHERE telegram_sub = $1 LIMIT 1",
		strconv.FormatInt(telegramUserID, 10)).Scan(&userID)
	if err == nil {
		return userID, true
	}
	// Fall back to the bound chat, which covers accounts linked before they
	// ever signed in with Telegram.
	err = p.DB.QueryRow(ctx,
		"SELECT user_id FROM telegram_notify_settings WHERE chat_id = $1 LIMIT 1", chatID).Scan(&userID)
	if err == nil {
		return userID, true
	}
	return 0, false
}

// handleAdminCommand runs the operator console. Returns true when the message
// was a command it handled (so it is not also filed as a support message).
//
// Two independent gates: the sender must be on the Telegram id allow-list, and
// if that Telegram identity is linked to a local account, that account must
// still be an active platform_admin. The second gate is what gives role changes
// teeth — demoting an operator revokes their console without needing to edit
// the allow-list and redeploy.
func (p *Pipeline) handleAdminCommand(ctx context.Context, chatID string, senderID int64, text string) bool {
	if !strings.HasPrefix(text, "/") {
		return false
	}
	cmd, arg, _ := strings.Cut(text, " ")
	cmd = strings.ToLower(strings.TrimSpace(cmd))

	switch cmd {
	case "/start":
		return false // handled earlier
	case "/status", "/tenants", "/tenant", "/digest", "/help", "/broadcast":
	default:
		return false
	}

	if ok, reason := p.adminConsoleAllowed(ctx, senderID); !ok {
		p.SendPlatformMessage(ctx, chatID, "⛔ "+reason, nil)
		p.auditPlatformCommand(ctx, senderID, cmd, "denied: "+reason)
		return true
	}
	p.auditPlatformCommand(ctx, senderID, cmd, strings.TrimSpace(arg))

	switch cmd {
	case "/help":
		p.SendPlatformMessage(ctx, chatID,
			"RelayChat operator console\n\n"+
				"/status — service health and queue depth\n"+
				"/tenants — merchant list with plan and usage\n"+
				"/tenant <id> — one merchant in detail\n"+
				"/digest — today's numbers\n"+
				"/broadcast <text> — announce to every merchant", nil)
	case "/broadcast":
		if strings.TrimSpace(arg) == "" {
			p.SendPlatformMessage(ctx, chatID, "Usage: /broadcast <text>", nil)
			return true
		}
		// Confirm receipt first: the send loop can take a while, and Telegram
		// gives the operator no other feedback that the command landed.
		p.SendPlatformMessage(ctx, chatID, "📣 Broadcasting…", nil)
		sent, failed := p.BroadcastToMerchants(ctx, arg)
		p.SendPlatformMessage(ctx, chatID,
			fmt.Sprintf("📣 已发送 %d 个商家，失败 %d 个。", sent, failed), nil)
	case "/status":
		p.SendPlatformMessage(ctx, chatID, p.adminStatusReport(ctx), nil)
	case "/tenants":
		p.SendPlatformMessage(ctx, chatID, p.adminTenantsReport(ctx), nil)
	case "/tenant":
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 32)
		if err != nil {
			p.SendPlatformMessage(ctx, chatID, "Usage: /tenant <id>", nil)
			return true
		}
		p.SendPlatformMessage(ctx, chatID, p.adminTenantReport(ctx, int32(id)), nil)
	case "/digest":
		p.SendPlatformMessage(ctx, chatID, p.adminDigestReport(ctx), nil)
	}
	return true
}

// adminConsoleAllowed applies the two gates described on handleAdminCommand.
func (p *Pipeline) adminConsoleAllowed(ctx context.Context, senderID int64) (bool, string) {
	if !p.IsPlatformAdminChat(senderID) {
		return false, "此账号不在运维白名单内 / not on the operator allow-list"
	}
	var role string
	var isActive bool
	err := p.DB.QueryRow(ctx,
		"SELECT role::text, is_active FROM users WHERE telegram_sub = $1 LIMIT 1",
		strconv.FormatInt(senderID, 10)).Scan(&role, &isActive)
	if err != nil {
		// Allow-listed but never signed in with Telegram, so there is no local
		// account to check. The allow-list is the authorisation here.
		return true, ""
	}
	if !isActive {
		return false, "绑定的账号已被禁用 / the linked account is disabled"
	}
	if role != "platform_admin" {
		return false, "绑定的账号已不是平台管理员 / the linked account is no longer a platform admin"
	}
	return true, ""
}

func uidOrNil(id int32, ok bool) any {
	if !ok || id == 0 {
		return nil
	}
	return id
}
