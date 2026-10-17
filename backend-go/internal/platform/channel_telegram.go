package platform

import (
	"context"
	"fmt"
	"strings"
)

// telegramChannel — Bot API. Moved verbatim out of Pipeline.deliverToProvider's
// `case "telegram"` branch so the orchestrator no longer branches on the
// platform name.
type telegramChannel struct {
	p   *Pipeline
	cfg *configCred
}

// Disconnect removes this deployment's webhook, so a later reconnect (possibly
// under another tenant) does not leave Telegram posting to a config that no longer
// owns the bot.
func (c *telegramChannel) Disconnect(ctx context.Context) (string, error) {
	if err := NewTelegramClient(c.cfg.BotToken).DeleteWebhook(ctx); err != nil {
		return "Disconnected; Telegram webhook removal failed", err
	}
	return "Disconnected; Telegram webhook removed", nil
}

func (c *telegramChannel) Caps() Capabilities { return CapabilitiesFor("telegram") }

func (c *telegramChannel) Send(ctx context.Context, msg ChannelMessage) (string, error) {
	client := NewTelegramClient(c.cfg.BotToken)
	if msg.Kind == "media" && msg.MediaURL != "" {
		switch msg.MediaType {
		case "audio":
			id, err := client.SendAudio(ctx, msg.RecipientID, msg.MediaURL, msg.Content)
			if err != nil {
				return "", err
			}
			return TelegramProviderMessageID(msg.RecipientID, id), nil
		case "video":
			return c.sendCaptionMedia(ctx, client, msg, "video")
		case "document", "file":
			return c.sendCaptionMedia(ctx, client, msg, "document")
		default: // image, and the empty type the composer can leave behind
			return c.sendCaptionMedia(ctx, client, msg, "photo")
		}
	}

	// AI replies get 👍/👎 inline buttons (customer-side CSAT collection).
	buttons, hasFeedbackButtons := feedbackButtons(msg.Delivery, msg.Buttons)
	chunks := SplitChannelText(msg.Content, c.Caps())
	lastID := ""
	replyTo := msg.ReplyTo
	for _, chunk := range chunks {
		id, err := c.sendChunk(ctx, client, msg.RecipientID, chunk, buttons, replyTo)
		if err != nil {
			return "", err
		}
		if id != "" {
			lastID = TelegramProviderMessageID(msg.RecipientID, id)
		}
		// Buttons and the quote ride on the first chunk only.
		buttons, replyTo = nil, 0
	}
	if hasFeedbackButtons {
		c.p.demotePreviousFeedbackKeyboard(ctx, client, msg.Delivery)
	}
	return lastID, nil
}

// sendChunk delivers one already-split chunk. On a MarkupHTML channel the
// Markdown source is converted to Telegram's HTML subset; if Telegram refuses
// the entities, the same chunk is retried as plain text — a formatting bug must
// cost the bold, never the answer.
func (c *telegramChannel) sendChunk(ctx context.Context, client *TelegramClient, chatID, chunk string, buttons [][2]string, replyTo int64) (string, error) {
	if c.Caps().Markup != MarkupHTML {
		return client.SendMessageFormatted(ctx, chatID, chunk, "", buttons, replyTo)
	}
	id, err := client.SendMessageFormatted(ctx, chatID, telegramHTML(chunk), "HTML", buttons, replyTo)
	if err == nil || !tgParseError(err) {
		return id, err
	}
	if c.p != nil && c.p.Logger != nil {
		c.p.Logger.Warn("telegram rejected HTML entities; sending plain text", "error", err.Error())
	}
	return client.SendMessageFormatted(ctx, chatID, plainTextForChat(chunk), "", buttons, replyTo)
}

// sendCaptionMedia delivers a photo, video or document — a matched knowledge
// document's own product photo, a text-to-image render, or an attachment an agent
// queued from the inbox — with the reply as its caption.
//
// Telegram caps a caption at 1024 characters. A longer reply is NOT truncated:
// losing the answer to keep the picture would be the worse trade, so the media
// goes out bare and the text follows as ordinary messages (which also keeps the
// 👍/👎 buttons on a text bubble, where a customer can actually read them beside
// the words they rate). The same fallback covers a caption whose HTML form
// Telegram refuses or finds too long.
func (c *telegramChannel) sendCaptionMedia(ctx context.Context, client *TelegramClient, msg ChannelMessage, kind string) (string, error) {
	buttons, hasFeedbackButtons := feedbackButtons(msg.Delivery, msg.Buttons)
	markup := c.Caps().Markup == MarkupHTML
	send := func(caption, mode string, buttons [][2]string) (string, error) {
		switch kind {
		case "video":
			return client.SendVideoFormatted(ctx, msg.RecipientID, msg.MediaURL, caption, mode, buttons, msg.ReplyTo)
		case "document":
			return client.SendDocumentFormatted(ctx, msg.RecipientID, msg.MediaURL, caption, mode, buttons, msg.ReplyTo)
		default:
			return client.SendPhotoFormatted(ctx, msg.RecipientID, msg.MediaURL, caption, mode, buttons, msg.ReplyTo)
		}
	}
	if captionFits(msg.Content, c.Caps()) {
		caption, mode := msg.Content, ""
		if markup {
			caption, mode = telegramHTML(caption), "HTML"
		}
		id, err := send(caption, mode, buttons)
		switch {
		case err == nil:
			if hasFeedbackButtons {
				c.p.demotePreviousFeedbackKeyboard(ctx, client, msg.Delivery)
			}
			return TelegramProviderMessageID(msg.RecipientID, id), nil
		case markup && tgParseError(err):
			if id, plainErr := send(plainTextForChat(msg.Content), "", buttons); plainErr == nil {
				if hasFeedbackButtons {
					c.p.demotePreviousFeedbackKeyboard(ctx, client, msg.Delivery)
				}
				return TelegramProviderMessageID(msg.RecipientID, id), nil
			}
			// Fall through to the bare-media path below.
		case tgCaptionTooLong(err):
			// Fall through to the bare-media path below.
		default:
			return "", err
		}
	}
	id, err := send("", "", nil)
	if err != nil {
		return "", err
	}
	lastID := TelegramProviderMessageID(msg.RecipientID, id)
	for _, chunk := range SplitChannelText(msg.Content, c.Caps()) {
		mid, err := c.sendChunk(ctx, client, msg.RecipientID, chunk, buttons, 0)
		if err != nil {
			return "", err
		}
		if mid != "" {
			lastID = TelegramProviderMessageID(msg.RecipientID, mid)
		}
		buttons = nil
	}
	if hasFeedbackButtons {
		c.p.demotePreviousFeedbackKeyboard(ctx, client, msg.Delivery)
	}
	return lastID, nil
}

// splitTelegramProviderRef splits the outbox's "chatID:messageID" reference.
// Chat ids are often negative, so the split is on the LAST colon.
func splitTelegramProviderRef(ref string) (chatID, messageID string, ok bool) {
	i := strings.LastIndex(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// EditMessage rewrites a delivered text message. Markdown becomes the HTML subset
// exactly like a fresh send, and a formatting refusal falls back to plain text.
func (c *telegramChannel) EditMessage(ctx context.Context, providerRef, content string) error {
	chatID, messageID, ok := splitTelegramProviderRef(providerRef)
	if !ok {
		return fmt.Errorf("telegram: bad provider message ref %q", providerRef)
	}
	client := NewTelegramClient(c.cfg.BotToken)
	if c.Caps().Markup == MarkupHTML {
		if err := client.EditMessageText(ctx, chatID, messageID, telegramHTML(content), "HTML"); err == nil || !tgParseError(err) {
			return err
		}
	}
	return client.EditMessageText(ctx, chatID, messageID, plainTextForChat(content), "")
}

func (c *telegramChannel) DeleteMessage(ctx context.Context, providerRef string) error {
	chatID, messageID, ok := splitTelegramProviderRef(providerRef)
	if !ok {
		return fmt.Errorf("telegram: bad provider message ref %q", providerRef)
	}
	return NewTelegramClient(c.cfg.BotToken).DeleteMessage(ctx, chatID, messageID)
}

func (c *telegramChannel) DownloadMedia(ctx context.Context, providerID, _ string, _ string) ([]byte, string, error) {
	if providerID == "" {
		return nil, "", nil
	}
	return NewTelegramClient(c.cfg.BotToken).DownloadFile(ctx, providerID)
}

func (c *telegramChannel) Typing(ctx context.Context, recipientID string) error {
	if recipientID == "" {
		return nil
	}
	return NewTelegramClient(c.cfg.BotToken).SendChatAction(ctx, recipientID, "typing")
}

func (c *telegramChannel) Profile(ctx context.Context, userID string) (string, string, error) {
	return NewTelegramClient(c.cfg.BotToken).GetProfile(ctx, userID)
}

// feedbackButtons returns the 👍/👎 pair to attach to an AI reply and whether it
// was attached. The delivery payload must ask for feedback, the message must not
// already carry buttons, and there must be a message to attach the vote to.
// Extracted from Send so the CSAT wiring is testable without a database.
func feedbackButtons(d *outboundDelivery, existing [][2]string) ([][2]string, bool) {
	if d == nil || len(existing) > 0 || d.LastMessageID <= 0 {
		return existing, false
	}
	fb, _ := d.Payload["feedback"].(bool)
	if !fb {
		return existing, false
	}
	return [][2]string{
		{"👍", fmt.Sprintf("fb:%d:1", d.LastMessageID)},
		{"👎", fmt.Sprintf("fb:%d:-1", d.LastMessageID)},
	}, true
}
