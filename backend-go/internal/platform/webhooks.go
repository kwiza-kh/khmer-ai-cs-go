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
	DB              *pgxpool.Pool
	Pipe            *Pipeline
	Sealer          *security.Sealer
	MetaVerifyToken string
}

// configRow is the lookup shape for webhook routing.
type webhookConfig struct {
	ConfigID        int32
	UserID          int32
	Platform        string
	WebhookSecret   string
	BotTokenHash    string
	ChannelIdentity string
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
		// An unset verify token would accept "subscribe" + empty token —
		// anyone could pass the challenge check.
		if wh.MetaVerifyToken != "" && mode == "subscribe" && token == wh.MetaVerifyToken {
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
			cfg := wh.resolveMetaConfig(r.Context(), entryID)
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

	cfg := wh.resolveTelegramConfig(r.Context(), r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
	if cfg == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	// Verify the Telegram bot API secret token (set via setWebhook). A
	// missing/undecryptable secret means we cannot authenticate the caller —
	// reject rather than accept unsigned events.
	if !VerifyTelegramSecret(cfg.WebhookSecret, r.Header.Get("X-Telegram-Bot-Api-Secret-Token")) {
		w.WriteHeader(http.StatusUnauthorized)
		return
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

	// LINE puts the bot's own userId in the top-level "destination" — the only
	// per-tenant routing key in the payload.
	var envelope struct {
		Destination string `json:"destination"`
	}
	_ = json.Unmarshal(body, &envelope)

	cfg, adoptable := wh.resolveLineConfig(r.Context(), envelope.Destination)
	if cfg == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !LineVerifySignature(cfg.WebhookSecret, signature, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Signature verified: the sender holds this channel's secret, so it is
	// safe to latch the routing identity onto the config.
	if adoptable {
		wh.bindChannelIdentity(r.Context(), cfg.ConfigID, envelope.Destination)
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
		// LINE's webhook self-test fires a signed "Hello, world!" event from
		// the reserved all-zero user id — never treat it as a customer.
		if userID == "U00000000000000000000000000000000" {
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

// ZaloWebhook handles Zalo OA callback events (X-ZEvent-Signature signed).
// Event shapes consumed: user_send_text, user_send_image (others ignored).
func (wh *Webhooks) ZaloWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	signature := r.Header.Get("X-ZEvent-Signature")

	// Zalo identifies the receiving Official Account with a top-level "oa_id".
	var envelope struct {
		OAID string `json:"oa_id"`
	}
	_ = json.Unmarshal(body, &envelope)

	cfg, adoptable := wh.resolveZaloConfig(r.Context(), envelope.OAID)
	if cfg == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if !ZaloVerifySignature(cfg.WebhookSecret, signature, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Signature verified: safe to latch the routing identity.
	if adoptable {
		wh.bindChannelIdentity(r.Context(), cfg.ConfigID, envelope.OAID)
	}
	wh.recordHealth(r.Context(), cfg.ConfigID, "connected", "", "Receiving signed Zalo webhook events")
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	eventName, _ := payload["event_name"].(string)
	sender, _ := payload["sender"].(map[string]any)
	userID := ""
	if sender != nil {
		userID, _ = sender["id"].(string)
	}
	message, _ := payload["message"].(map[string]any)
	if userID == "" || message == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	text := ""
	var media map[string]any
	switch eventName {
	case "user_send_text":
		text, _ = message["text"].(string)
	case "user_send_image":
		if atts, ok := message["attachments"].([]any); ok {
			for _, a := range atts {
				att, _ := a.(map[string]any)
				attType, _ := att["type"].(string)
				p, _ := att["payload"].(map[string]any)
				u, _ := p["url"].(string)
				if attType == "image" && u != "" {
					media = map[string]any{"kind": "photo", "source_url": u, "mime_type": "image/jpeg"}
					text = "[Customer sent an image]"
					break
				}
			}
		}
	default:
		// Stickers / files / location events are ignored for now.
	}
	if text == "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	msgID, _ := message["msg_id"].(string)
	externalID := msgID
	if externalID == "" {
		externalID = userID
	}
	_ = wh.Pipe.EnqueueInboundEvent(r.Context(), cfg.ConfigID, "zalo", externalID, userID, "", text, media)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// WhatsAppWebhook handles GET verification + POST messages.
func (wh *Webhooks) WhatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if wh.MetaVerifyToken != "" && q.Get("hub.mode") == "subscribe" && q.Get("hub.verify_token") == wh.MetaVerifyToken {
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
	handled := false
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		changes, _ := entry["changes"].([]any)
		for _, c := range changes {
			change, _ := c.(map[string]any)
			value, _ := change["value"].(map[string]any)
			if value == nil {
				continue
			}
			// Route by the phone number the event belongs to (stored as
			// page_id). webhook_secret is per-tenant (BYO Meta app): the
			// merchant supplies their own app secret at activation, so the
			// HMAC binds the event to this tenant's secret. A deployment that
			// instead distributes ONE app secret to every merchant loses that
			// property — per-tenant secrets are required for cross-tenant
			// isolation.
			phoneNumberID := ""
			if meta, ok := value["metadata"].(map[string]any); ok {
				phoneNumberID, _ = meta["phone_number_id"].(string)
			}
			cfg := wh.resolveWhatsAppConfig(r.Context(), phoneNumberID)
			if cfg == nil || cfg.WebhookSecret == "" {
				continue
			}
			// Signature gate: WhatsApp Cloud API signs payloads with the app
			// secret via X-Hub-Signature-256. Without this check anyone could
			// forge inbound messages and make the pipeline reply through the
			// tenant's WhatsApp credentials.
			if !verifyMetaSignature(cfg.WebhookSecret, r.Header.Get("X-Hub-Signature-256"), body) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			handled = true
			wh.recordHealth(r.Context(), cfg.ConfigID, "connected", "", "Receiving signed WhatsApp webhook events")

			// Delivery receipts (sent/delivered/read/failed) → update outbox.
			if statuses, ok := value["statuses"].([]any); ok {
				for _, st := range statuses {
					if s, ok := st.(map[string]any); ok {
						wh.applyWhatsAppStatus(r.Context(), cfg.ConfigID, s)
					}
				}
			}

			messages, _ := value["messages"].([]any)
			for _, m := range messages {
				msg, _ := m.(map[string]any)
				from, _ := msg["from"].(string)
				msgID, _ := msg["id"].(string)
				msgType, _ := msg["type"].(string)
				if from == "" || msgID == "" {
					continue
				}
				text := ""
				var media map[string]any
				switch msgType {
				case "text":
					if bodyObj, ok := msg["text"].(map[string]any); ok {
						text, _ = bodyObj["body"].(string)
					}
				case "button":
					// Quick-reply / template button press carries the title.
					if b, ok := msg["button"].(map[string]any); ok {
						text, _ = b["text"].(string)
					}
				case "interactive":
					if ia, ok := msg["interactive"].(map[string]any); ok {
						if br, ok := ia["button_reply"].(map[string]any); ok {
							text, _ = br["title"].(string)
						} else if lr, ok := ia["list_reply"].(map[string]any); ok {
							text, _ = lr["title"].(string)
						}
					}
				case "location":
					if loc, ok := msg["location"].(map[string]any); ok {
						name, _ := loc["name"].(string)
						addr, _ := loc["address"].(string)
						text = "[Customer shared a location"
						if name != "" {
							text += ": " + name
						}
						if addr != "" {
							text += " " + addr
						}
						text += "]"
					}
				default:
					// image / video / document / audio / voice / sticker —
					// forward the media id so the pipeline can download it.
					if payloadObj, ok := msg[msgType].(map[string]any); ok {
						fileID, _ := payloadObj["id"].(string)
						mime, _ := payloadObj["mime_type"].(string)
						caption, _ := payloadObj["caption"].(string)
						kind := msgType
						if msgType == "voice" {
							kind = "audio"
						}
						media = map[string]any{"kind": kind, "provider_media_id": fileID, "mime_type": mime}
						text = caption
						if text == "" {
							text = "[Customer sent " + msgType + " media]"
						}
					}
				}
				if text == "" && media == nil {
					continue
				}
				_ = wh.Pipe.EnqueueInboundEvent(r.Context(), cfg.ConfigID, "whatsapp", msgID, from, "", text, media)
			}
		}
	}
	if !handled {
		// No configured tenant owns this phone number — acknowledge so Meta
		// does not retry forever, but do not process the event.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"received"}`))
}

// applyWhatsAppStatus records a WhatsApp delivery receipt on the outbox row.
func (wh *Webhooks) applyWhatsAppStatus(ctx context.Context, configID int32, status map[string]any) {
	providerID, _ := status["id"].(string)
	state, _ := status["status"].(string)
	if providerID == "" || state == "" {
		return
	}
	switch state {
	case "sent":
		_, _ = wh.DB.Exec(ctx,
			"UPDATE platform_outbox SET provider_message_id = COALESCE(NULLIF(provider_message_id,''), $1), status = 'sent' "+
				"WHERE config_id = $2 AND provider_message_id = $1 AND status NOT IN ('sent','delivered','read')",
			providerID, configID)
	case "delivered", "read":
		_, _ = wh.DB.Exec(ctx,
			"UPDATE platform_outbox SET status = $1 WHERE config_id = $2 AND provider_message_id = $3",
			state, configID, providerID)
	case "failed":
		errMsg := ""
		if errs, ok := status["errors"].([]any); ok && len(errs) > 0 {
			if e0, ok := errs[0].(map[string]any); ok {
				errMsg, _ = e0["title"].(string)
			}
		}
		_, _ = wh.DB.Exec(ctx,
			"UPDATE platform_outbox SET status = 'failed', last_error = $1 WHERE config_id = $2 AND provider_message_id = $3",
			"provider rejected: "+errMsg, configID, providerID)
	}
}

// ============================================
// Config resolution
// ============================================

// resolveMetaConfig routes a Meta webhook entry to its config. Messenger
// events carry the Page ID as the entry id, Instagram messaging events carry
// the Instagram professional account ID — match each against its own column.
// No exact match means the entry does not belong to any configured channel:
// routing it to an arbitrary fallback config would deliver the event to the
// wrong tenant (whose secret check then fails) — drop it instead.
func (wh *Webhooks) resolveMetaConfig(ctx context.Context, entryID string) *webhookConfig {
	if cfg := wh.metaConfigWhere(ctx,
		"platform = 'meta'::platform_type AND page_id = $1", entryID); cfg != nil {
		return cfg
	}
	return wh.metaConfigWhere(ctx,
		"platform = 'instagram'::platform_type AND instagram_business_id = $1", entryID)
}

func (wh *Webhooks) metaConfigWhere(ctx context.Context, where, id string) *webhookConfig {
	query := "SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE " + where + " AND is_active = true ORDER BY config_id LIMIT 1"
	var cfg webhookConfig
	var secretEnc *string
	var err error
	if id != "" {
		err = wh.DB.QueryRow(ctx, query, id).Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	} else {
		err = wh.DB.QueryRow(ctx, query).Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	}
	if err != nil {
		return nil
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

// resolveTelegramConfig routes a Telegram update to its tenant. The bot API
// sends the per-webhook secret in X-Telegram-Bot-Api-Secret-Token; its
// sha256 matches webhook_secret_hash, which is how the owning config is found
// when several tenants each run a bot. Falling back to "first active" would
// 401 the other tenants' events forever.
func (wh *Webhooks) resolveTelegramConfig(ctx context.Context, providedSecret string) *webhookConfig {
	var cfg webhookConfig
	var secretEnc *string
	if providedSecret != "" {
		hash := security.Sha256Hex(providedSecret)
		if err := wh.DB.QueryRow(ctx,
			"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs "+
				"WHERE platform = 'telegram'::platform_type AND is_active = true AND webhook_secret_hash = $1 LIMIT 1",
			hash).Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc); err == nil {
			if secretEnc != nil {
				cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
			}
			return &cfg
		}
	}
	// Legacy configs saved before the hash column was populated.
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs WHERE platform = 'telegram'::platform_type AND is_active = true ORDER BY config_id LIMIT 1").
		Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
	if err != nil {
		return nil
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

// resolveChannelByIdentity looks up the active config owning a provider-side
// routing identity (column added in migration 051).
func (wh *Webhooks) resolveChannelByIdentity(ctx context.Context, platform, identity string) *webhookConfig {
	if identity == "" {
		return nil
	}
	var cfg webhookConfig
	var secretEnc *string
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret, channel_identity "+
			"FROM platform_configs WHERE platform = $1::platform_type AND is_active = true AND channel_identity = $2",
		platform, identity).
		Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc, &cfg.ChannelIdentity)
	if err != nil {
		return nil
	}
	if secretEnc != nil {
		cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
	}
	return &cfg
}

// resolveChannelForAdoption finds the one active config of a platform that has
// not learned its routing identity yet — channels connected before migration
// 051. It returns nil when the choice is ambiguous: with two or more
// candidates there is no safe guess, and guessing is precisely the bug this
// replaces. The caller must verify the webhook signature before binding.
func (wh *Webhooks) resolveChannelForAdoption(ctx context.Context, platform string) *webhookConfig {
	rows, err := wh.DB.Query(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret, channel_identity "+
			"FROM platform_configs WHERE platform = $1::platform_type AND is_active = true AND channel_identity = ''",
		platform)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var found *webhookConfig
	n := 0
	for rows.Next() {
		var cfg webhookConfig
		var secretEnc *string
		if err := rows.Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc, &cfg.ChannelIdentity); err != nil {
			continue
		}
		if secretEnc != nil {
			cfg.WebhookSecret, _ = wh.Sealer.Decrypt(*secretEnc)
		}
		n++
		found = &cfg
	}
	if n != 1 {
		return nil
	}
	return found
}

// bindChannelIdentity records a config's routing identity, so subsequent
// webhooks route directly. Only ever called after the signature has been
// verified: binding from an unauthenticated payload would let anyone claim a
// tenant's channel by sending one forged request ahead of the real first
// webhook.
func (wh *Webhooks) bindChannelIdentity(ctx context.Context, configID int32, identity string) {
	if identity == "" {
		return
	}
	_, _ = wh.DB.Exec(ctx,
		"UPDATE platform_configs SET channel_identity = $1 WHERE config_id = $2 AND channel_identity = ''",
		identity, configID)
}

// resolveLineConfig routes a LINE webhook by the payload's top-level
// "destination" (the bot's own userId). adoptable reports that the config has
// no identity yet, so the caller may bind it once the signature checks out.
func (wh *Webhooks) resolveLineConfig(ctx context.Context, destination string) (cfg *webhookConfig, adoptable bool) {
	if c := wh.resolveChannelByIdentity(ctx, "line", destination); c != nil {
		return c, false
	}
	c := wh.resolveChannelForAdoption(ctx, "line")
	if c == nil {
		return nil, false
	}
	return c, destination != ""
}

// resolveZaloConfig routes a Zalo webhook by the payload's "oa_id".
func (wh *Webhooks) resolveZaloConfig(ctx context.Context, oaID string) (cfg *webhookConfig, adoptable bool) {
	if c := wh.resolveChannelByIdentity(ctx, "zalo", oaID); c != nil {
		return c, false
	}
	c := wh.resolveChannelForAdoption(ctx, "zalo")
	if c == nil {
		return nil, false
	}
	return c, oaID != ""
}

// resolveWhatsAppConfig routes a WhatsApp webhook to its tenant by the
// phone_number_id carried in value.metadata (stored in the page_id column).
// Falling back to "any active config" would deliver the event to the wrong
// tenant — and with per-tenant secrets (BYO Meta app) the signature only
// binds the event to the routed tenant, so routing must be exact. No match
// means drop.
func (wh *Webhooks) resolveWhatsAppConfig(ctx context.Context, phoneNumberID string) *webhookConfig {
	if phoneNumberID == "" {
		return nil
	}
	var cfg webhookConfig
	var secretEnc *string
	err := wh.DB.QueryRow(ctx,
		"SELECT config_id, user_id, platform::text, webhook_secret FROM platform_configs "+
			"WHERE platform = 'whatsapp'::platform_type AND is_active = true AND page_id = $1 LIMIT 1",
		phoneNumberID).Scan(&cfg.ConfigID, &cfg.UserID, &cfg.Platform, &secretEnc)
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

// VerifyTelegramSecret — constant-time comparison of the Telegram webhook
// secret header. An empty configured secret cannot authenticate anyone, so it
// rejects (fail-closed).
func VerifyTelegramSecret(secret, provided string) bool {
	if secret == "" {
		return false
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
