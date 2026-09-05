// Package platform — webhook HTTP handlers (Meta/WhatsApp/Telegram/LINE).
// Verify signatures, then enqueue inbound events into the durable pipeline.
package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/security"
)

// Webhooks bundles webhook dependencies.
type Webhooks struct {
	DB     *pgxpool.Pool
	Pipe   *Pipeline
	Sealer *security.Sealer
	MetaVerifyToken string
}

// configRow is the lookup shape for webhook routing.
type webhookConfig struct {
	ConfigID        int32
	UserID          int32
	Platform        string
	WebhookSecret   string
	BotTokenHash    string
}

// recordHealth upserts connection health (a signed webhook event is live proof
// of connectivity). Best-effort.
func (wh *Webhooks) recordHealth(ctx context.Context, configID int32, status, accountName, detail string) {
	now := time.Now()
	_, _ = wh.DB.Exec(ctx,
		"INSERT INTO platform_connection_health (config_id, status, account_name, detail, checked_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6) "+
			"ON CONFLICT (config_id) DO UPDATE SET status=EXCLUDED.status, "+
			"account_name=CASE WHEN EXCLUDED.account_name = '' THEN platform_connection_health.account_name ELSE EXCLUDED.account_name END, "+
			"detail=EXCLUDED.detail, checked_at=EXCLUDED.checked_at, updated_at=EXCLUDED.updated_at",
		configID, status, accountName, detail, now, now)
}

// MetaWebhook handles GET (hub.challenge verification) + POST (events).
func (wh *Webhooks) MetaWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		mode := q.Get("hub.mode")
		token := q.Get("hub.verify_token")
		challenge := q.Get("hub.challenge")
		if mode == "subscribe" && token == wh.MetaVerifyToken {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(challenge))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	signature := r.Header.Get("X-Hub-Signature-256")

	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	entries, _ := payload["entry"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		entryID, _ := entry["id"].(string)
		messaging, _ := entry["messaging"].([]any)
		for _, m := range messaging {
			msg, _ := m.(map[string]any)
			cfg := wh.resolveConfig(r.Context(), "meta", entryID)
			if cfg == nil {
				continue
			}
			if !verifyMetaSignature(cfg.WebhookSecret, signature, body) {
				continue
			}
			wh.recordHealth(r.Context(), cfg.ConfigID, "connected", "", "Receiving signed Meta webhook events")
			wh.handleMetaMessaging(r.Context(), cfg, msg)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"received"}`))
}

func verifyMetaSignature(secret, signature string, body []byte) bool {
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// handleMetaMessaging extracts one Messenger/Instagram message into the queue.
func (wh *Webhooks) handleMetaMessaging(ctx context.Context, cfg *webhookConfig, msg map[string]any) {
	// Skip echoes (our own replies coming back).
	if message, ok := msg["message"].(map[string]any); ok {
		if isEcho, _ := message["is_echo"].(bool); isEcho {
			return
		}
	}
	sender := ""
	if senderObj, ok := msg["sender"].(map[string]any); ok {
		sender, _ = senderObj["id"].(string)
	}
	message, _ := msg["message"].(map[string]any)
	if message == nil || sender == "" {
		return
	}
	text, _ := message["text"].(string)
	mid, _ := message["mid"].(string)
	if mid == "" {
		mid = sender
	}

	media := extractMetaMedia(message)
	if text == "" && media != nil {
		if kind, _ := media["kind"].(string); kind != "" {
			text = "[Customer sent " + kind + " media]"
		}
	}
	if text == "" {
		return
	}
	_ = wh.Pipe.EnqueueInboundEvent(ctx, cfg.ConfigID, cfg.Platform, mid, sender, "", text, media)
}

func extractMetaMedia(message map[string]any) map[string]any {
	attachments, _ := message["attachments"].([]any)
	if len(attachments) == 0 {
		return nil
	}
	first, _ := attachments[0].(map[string]any)
	if first == nil {
		return nil
	}
	kind, _ := first["type"].(string)
	sourceURL := ""
	if payloadObj, ok := first["payload"].(map[string]any); ok {
		sourceURL, _ = payloadObj["url"].(string)
	}
	if kind == "" && sourceURL == "" {
		return nil
	}
	return map[string]any{"kind": kind, "source_url": sourceURL}
}

// TelegramWebhook handles POST updates.
func (wh *Webhooks) TelegramWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	var update map[string]any
	_ = json.Unmarshal(body, &update)

	cfg := wh.resolveTelegramConfig(r.Context())
	if cfg == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	// Verify the Telegram bot API secret token (set via setWebhook). Reject
	// unsigned events when a secret is configured.
	if cfg.WebhookSecret != "" {
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if got != cfg.WebhookSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	wh.recordHealth(r.Context(), cfg.ConfigID, "connected", "", "Receiving Telegram webhook events")

	// Inline keyboard presses (👍/👎 on AI replies) before message handling.
	if callback, ok := update["callback_query"].(map[string]any); ok {
		wh.handleTelegramCallback(r.Context(), cfg, callback)
		w.WriteHeader(http.StatusOK)
		return
	}

	message, _ := update["message"].(map[string]any)
	if message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	chat, _ := message["chat"].(map[string]any)
	chatID := ""
	if id, ok := chat["id"].(float64); ok {
		chatID = jsonNumberString(id)
	} else if idStr, ok := chat["id"].(string); ok {
		chatID = idStr
	}
	if chatID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	text, _ := message["text"].(string)
	updateID := ""
	if uid, ok := update["update_id"].(float64); ok {
		updateID = jsonNumberString(uid)
	}
	if updateID == "" {
		updateID = chatID
	}

	media := extractTelegramMedia(message)
	if text == "" && media != nil {
		if kind, _ := media["kind"].(string); kind != "" {
			text = "[Customer sent " + kind + " media]"
		}
	}
	if text == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	display := ""
	if from, ok := message["from"].(map[string]any); ok {
		if fn, ok := from["first_name"].(string); ok {
			display = fn
		}
		if un, ok := from["username"].(string); ok && display == "" {
			display = un
		}
	}
	_ = wh.Pipe.EnqueueInboundEvent(r.Context(), cfg.ConfigID, "telegram", updateID, chatID, display, text, media)
	w.WriteHeader(http.StatusOK)
}

// handleTelegramCallback processes an inline-keyboard press ("fb:<msgID>:<±1>")
// on an AI reply: stores the rating on the message, escalates 👎 to the
// handoff queue, and answers the callback so Telegram clears the button state.
func (wh *Webhooks) handleTelegramCallback(ctx context.Context, cfg *webhookConfig, callback map[string]any) {
	cbID, _ := callback["id"].(string)
	data, _ := callback["data"].(string)
	chatID := ""
	if from, ok := callback["from"].(map[string]any); ok {
		if id, ok := from["id"].(float64); ok {
			chatID = jsonNumberString(id)
		}
	}
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "fb" {
		return
	}
	msgID, err1 := strconv.ParseInt(parts[1], 10, 64)
	rating, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || (rating != 1 && rating != -1) || chatID == "" {
		return
	}

	// Tenant + chat guard: the message must be a model reply on this tenant's
	// Telegram session for this exact chat.
	var sessionID string
	var ownerID int32
	err := wh.DB.QueryRow(ctx,
		"SELECT cm.session_id, s.user_id FROM chat_messages cm JOIN sessions s ON s.session_id = cm.session_id "+
			"WHERE cm.message_id = $1 AND cm.role = 'model' AND s.user_id = $2 AND s.platform = 'telegram'::platform_type AND s.platform_user_id = $3",
		msgID, cfg.UserID, chatID).Scan(&sessionID, &ownerID)
	if err != nil {
		return
	}
	_, _ = wh.DB.Exec(ctx,
		"UPDATE chat_messages SET feedback_rating = $1, feedback_at = NOW() WHERE message_id = $2", rating, msgID)

	if rating == -1 {
		// 👎 = "the answer was not good enough" → negative_feedback handoff.
		_ = wh.Pipe.createHandoffRequest(ctx, ownerID, sessionID, "negative_feedback",
			"Customer rated the AI reply 👎")
	}
	realtime.Publish(ctx, wh.Pipe.Redis, realtime.Event{
		Type: realtime.EventSession, UserID: ownerID, SessionID: sessionID,
	})

	// Close the button spinner with a thank-you toast.
	botToken := wh.telegramBotToken(ctx, cfg.ConfigID)
	if cbID != "" && botToken != "" {
		thanks := map[string]string{
			"1":  "សូមអរគុណ! 🙏",
			"-1": "អរគុណ! យើងនឹងប្រគល់ឱ្យភ្នាក់ងារមនុស្ស។",
		}[strconv.Itoa(rating)]
		_ = NewTelegramClient(botToken).AnswerCallback(ctx, cbID, thanks)
	}
}

// telegramBotToken decrypts the bot token for answering callbacks.
func (wh *Webhooks) telegramBotToken(ctx context.Context, configID int32) string {
	var enc *string
	if err := wh.DB.QueryRow(ctx, "SELECT bot_token FROM platform_configs WHERE config_id = $1", configID).Scan(&enc); err != nil || enc == nil {
		return ""
	}
	token, err := wh.Sealer.Decrypt(*enc)
	if err != nil {
		return ""
	}
	return token
}

func extractTelegramMedia(message map[string]any) map[string]any {
	for _, key := range []struct {
		field, kind string
	}{{"voice", "voice"}, {"audio", "audio"}, {"video", "video"}, {"document", "document"}, {"photo", "photo"}} {
		if obj, ok := message[key.field].(map[string]any); ok {
			fileID, _ := obj["file_id"].(string)
			mime, _ := obj["mime_type"].(string)
			return map[string]any{"kind": key.kind, "provider_media_id": fileID, "mime_type": mime}
		}
		if key.field == "photo" {
			if arr, ok := message["photo"].([]any); ok && len(arr) > 0 {
				last, _ := arr[len(arr)-1].(map[string]any)
				if last != nil {
					fileID, _ := last["file_id"].(string)
					return map[string]any{"kind": "photo", "provider_media_id": fileID, "mime_type": "image/jpeg"}
				}
			}
		}
	}
	return nil
}

// LineWebhook handles POST events.
func (wh *Webhooks) LineWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	signature := r.Header.Get("X-Line-Signature")

	cfg := wh.resolveLineConfig(r.Context())
	if cfg == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !LineVerifySignature(cfg.WebhookSecret, signature, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	wh.recordHealth(r.Context(), cfg.ConfigID, "connected", "", "Receiving signed LINE webhook events")
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	events, _ := payload["events"].([]any)
	for _, e := range events {
		ev, _ := e.(map[string]any)
		evType, _ := ev["type"].(string)
		source, _ := ev["source"].(map[string]any)
		userID := ""
		if source != nil {
			userID, _ = source["userId"].(string)
		}
		message, _ := ev["message"].(map[string]any)
		msgType := ""
		if message != nil {
			msgType, _ = message["type"].(string)
		}
		if evType != "message" || userID == "" {
			continue
		}
		text := ""
		var media map[string]any
		if msgType == "text" {
			text, _ = message["text"].(string)
		} else if msgType == "audio" || msgType == "voice" {
			msgID, _ := message["id"].(string)
			media = map[string]any{"kind": "audio", "provider_media_id": msgID}
			text = "[Customer sent audio media]"
		}
		if text == "" {
			continue
		}
		msgID, _ := message["id"].(string)
		externalID := msgID
		if externalID == "" {
			externalID = userID
		}
		_ = wh.Pipe.EnqueueInboundEvent(r.Context(), cfg.ConfigID, "line", externalID, userID, "", text, media)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// WhatsAppWebhook handles GET verification + POST messages.
func (wh *Webhooks) WhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if q.Get("hub.mode") == "subscribe" && q.Get("hub.verify_token") == wh.MetaVerifyToken {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(q.Get("hub.challenge")))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	entries, _ := payload["entry"].([]any)
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		changes, _ := entry["changes"].([]any)
		for _, c := range changes {
			change, _ := c.(map[string]any)
			value, _ := change["value"].(map[string]any)
			messages, _ := value["messages"].([]any)
			for _, m := range messages {
				msg, _ := m.(map[string]any)
				from, _ := msg["from"].(string)
				msgID, _ := msg["id"].(string)
				msgType, _ := msg["type"].(string)
				if from == "" || msgID == "" {
					continue
				}
				cfg := wh.resolveWhatsAppConfig(r.Context())
				if cfg == nil {
					continue
				}
				wh.recordHealth(r.Context(), cfg.ConfigID, "connected", "", "Receiving signed WhatsApp webhook events")
				text := ""
				var media map[string]any
				if msgType == "text" {
					if bodyObj, ok := msg["text"].(map[string]any); ok {
						text, _ = bodyObj["body"].(string)
					}
				} else if msgType == "audio" || msgType == "voice" {
					if audioObj, ok := msg["audio"].(map[string]any); ok {
						fileID, _ := audioObj["id"].(string)
						mime, _ := audioObj["mime_type"].(string)
						media = map[string]any{"kind": "audio", "provider_media_id": fileID, "mime_type": mime}
						text = "[Customer sent audio media]"
					}
				}
				if text == "" {
					continue
				}
				_ = wh.Pipe.EnqueueInboundEvent(r.Context(), cfg.ConfigID, "whatsapp", msgID, from, "", text, media)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"received"}`))
}

// ============================================
// Config resolution
// ============================================

func (wh *Webhooks) resolveConfig(ctx context.Context, platform, pageID string) *webhookConfig {
	var cfg webhookConfig
	var secretEnc *string
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE platform = $1::platform_type AND page_id = $2 AND is_active = true LIMIT 1",
		platform, pageID).Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	if err != nil {
		// Fall back: any active config of this platform.
		err = wh.DB.QueryRow(ctx,
			"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE platform = $1::platform_type AND is_active = true LIMIT 1",
			platform).Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
		if err != nil {
			return nil
		}
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

func (wh *Webhooks) resolveTelegramConfig(ctx context.Context) *webhookConfig {
	var cfg webhookConfig
	var secretEnc *string
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE platform = 'telegram'::platform_type AND is_active = true LIMIT 1").
		Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	if err != nil {
		return nil
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

func (wh *Webhooks) resolveLineConfig(ctx context.Context) *webhookConfig {
	var cfg webhookConfig
	var secretEnc *string
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE platform = 'line'::platform_type AND is_active = true LIMIT 1").
		Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	if err != nil {
		return nil
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

func (wh *Webhooks) resolveWhatsAppConfig(ctx context.Context) *webhookConfig {
	var cfg webhookConfig
	var secretEnc *string
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE platform = 'whatsapp'::platform_type AND is_active = true LIMIT 1").
		Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	if err != nil {
		return nil
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

func jsonNumberString(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(jsonNumber(f), ".0"), ".0")
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// VerifyTelegramSecret — SHA-256 HMAC check for X-Telegram-Bot-Api-Secret-Token.
func VerifyTelegramSecret(secret, provided string) bool {
	if secret == "" {
		return true
	}
	return hmac.Equal([]byte(secret), []byte(provided))
}

// LineHMACSHA1 — LINE webhook signature helper (base64 HMAC-SHA256 is standard;
// kept for parity with older LINE HMAC-SHA1 flows).
func LineHMACSHA1(secret string, body []byte) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
