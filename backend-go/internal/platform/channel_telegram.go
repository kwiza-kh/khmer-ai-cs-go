package platform

import (
	"context"
	"fmt"
)

// telegramChannel — Bot API. Moved verbatim out of Pipeline.deliverToProvider's
// `case "telegram"` branch so the orchestrator no longer branches on the
// platform name.
type telegramChannel struct {
	p   *Pipeline
	cfg *configCred
}

func (c *telegramChannel) Caps() Capabilities { return CapabilitiesFor("telegram") }

func (c *telegramChannel) Send(ctx context.Context, msg ChannelMessage) (string, error) {
	client := NewTelegramClient(c.cfg.BotToken)
	if msg.Kind == "media" && msg.MediaType == "audio" && msg.MediaURL != "" {
		id, err := client.SendAudio(ctx, msg.RecipientID, msg.MediaURL, msg.Content)
		if err != nil {
			return "", err
		}
		return TelegramProviderMessageID(msg.RecipientID, id), nil
	}

	// AI replies get 👍/👎 inline buttons (customer-side CSAT collection).
	buttons, hasFeedbackButtons := feedbackButtons(msg.Delivery, msg.Buttons)
	chunks := SplitChannelText(msg.Content, c.Caps())
	lastID := ""
	for _, chunk := range chunks {
		id, err := client.SendMessage(ctx, msg.RecipientID, chunk, buttons)
		if err != nil {
			return "", err
		}
		if id != "" {
			lastID = TelegramProviderMessageID(msg.RecipientID, id)
		}
		// Buttons ride on the first chunk only.
		buttons = nil
	}
	if hasFeedbackButtons {
		c.p.demotePreviousFeedbackKeyboard(ctx, client, msg.Delivery)
	}
	return lastID, nil
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
