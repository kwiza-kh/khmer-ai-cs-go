package platform

import "context"

// metaChannel covers the three Graph-API channels: Messenger ("meta"),
// Instagram messaging and the WhatsApp Cloud API. They share a client but not a
// behaviour, so the differences are keyed off cfg.Platform in exactly the spots
// the old switch was.
type metaChannel struct {
	p   *Pipeline
	cfg *configCred

	noTeardown
}

func (c *metaChannel) Caps() Capabilities { return CapabilitiesFor(c.cfg.Platform) }

func (c *metaChannel) client() *MetaClient {
	// Cfg is nil in unit tests; NewMetaClient falls back to its own default
	// Graph version, which is what production passes in anyway.
	version := ""
	if c.p != nil && c.p.Cfg != nil {
		version = c.p.Cfg.Meta.GraphAPIVersion
	}
	return NewMetaClient(c.cfg.AccessToken, c.cfg.PageID, c.cfg.InstagramBusiness, version)
}

func (c *metaChannel) Send(ctx context.Context, msg ChannelMessage) (string, error) {
	client := c.client()
	// CSAT: Messenger/Instagram render the 👍/👎 pair as quick replies — the same
	// pairs Telegram draws as an inline keyboard. WhatsApp has no such affordance
	// (its buttons are interactive templates), so it stays plain.
	buttons, _ := feedbackButtons(msg.Delivery, msg.Buttons)
	if c.cfg.Platform == "whatsapp" {
		buttons = nil
	}
	req := &SendRequest{
		Platform:           c.cfg.Platform,
		RecipientID:        msg.RecipientID,
		Kind:               msg.Kind,
		MediaURL:           msg.MediaURL,
		MediaType:          msg.MediaType,
		Buttons:            buttons,
		TemplateName:       msg.TemplateName,
		TemplateLanguage:   msg.TemplateLanguage,
		TemplateBodyParams: msg.TemplateBodyParams,
		Tag:                msg.Tag,
	}
	if msg.Kind == "text" || msg.Kind == "buttons" {
		req.Text = msg.Content
	}
	// An image carries the reply as its caption when that fits (WhatsApp,
	// Messenger and Instagram all render one). A reply longer than the caption
	// limit goes out as its own message after the picture: an over-long caption
	// makes the provider reject the whole send, and dropping the answer to keep
	// the image is the wrong trade — the picture exists to support the words.
	if msg.Kind == "media" && msg.MediaURL != "" {
		if !captionFits(msg.Content, c.Caps()) {
			bare := *req
			bare.Kind, bare.Text, bare.Buttons = "media", "", nil
			id, err := client.SendMessage(ctx, &bare)
			if err != nil {
				return "", err
			}
			// The picture is on its way: the rest of this function must send plain
			// text, or every chunk would carry the same image again.
			last := id
			mid, err := c.sendTextChunks(ctx, client, msg, req, buttons)
			if err != nil {
				return "", err
			}
			if mid != "" {
				last = mid
			}
			return last, nil
		}
		// The caption carries the reply; the rating pair rides on the same message.
		req.Text = msg.Content
		return client.SendMessage(ctx, req)
	}
	if msg.Kind != "text" && msg.Kind != "buttons" {
		// template / media without a URL — one send, nothing to split.
		return client.SendMessage(ctx, req)
	}
	return c.sendTextChunks(ctx, client, msg, req, buttons)
}

// sendTextChunks splits the reply and attaches any buttons to the LAST chunk, so
// the choices sit under the complete answer instead of under its first third.
func (c *metaChannel) sendTextChunks(ctx context.Context, client *MetaClient, msg ChannelMessage, req *SendRequest, buttons [][2]string) (string, error) {
	chunks := SplitChannelText(msg.Content, c.Caps())
	last := ""
	for i, chunk := range chunks {
		out := *req
		out.Kind, out.Text, out.Buttons = "text", chunk, nil
		if i == len(chunks)-1 && len(buttons) > 0 {
			out.Kind, out.Buttons = "buttons", buttons
		}
		id, err := client.SendMessage(ctx, &out)
		if err != nil {
			return "", err
		}
		if id != "" {
			last = id
		}
	}
	return last, nil
}

func (c *metaChannel) DownloadMedia(ctx context.Context, providerID, sourceURL, _ string) ([]byte, string, error) {
	if c.cfg.Platform == "whatsapp" && providerID != "" {
		return c.client().DownloadMedia(ctx, providerID)
	}
	if sourceURL != "" {
		return downloadMediaBytes(ctx, sourceURL)
	}
	return nil, "", nil
}

func (c *metaChannel) Typing(ctx context.Context, recipientID string) error {
	if recipientID == "" {
		return nil
	}
	switch c.cfg.Platform {
	case "meta":
		// Reading the customer's message is what puts "seen" on their side before
		// the answer arrives. Messenger has the action; Instagram's API does not
		// document mark_seen, so only the typing hint is raised there.
		_ = c.client().SendSenderAction(ctx, c.cfg.Platform, recipientID, "mark_seen")
		return c.client().SendSenderAction(ctx, c.cfg.Platform, recipientID, "typing_on")
	case "instagram":
		return c.client().SendSenderAction(ctx, c.cfg.Platform, recipientID, "typing_on")
	default:
		// WhatsApp Cloud API has no sender-action endpoint.
		return nil
	}
}

func (c *metaChannel) Profile(ctx context.Context, userID string) (string, string, error) {
	if c.cfg.Platform == "whatsapp" {
		return "", "", nil
	}
	client := NewMetaClient(c.cfg.AccessToken, c.cfg.PageID, c.cfg.InstagramBusiness, c.p.Cfg.Meta.GraphAPIVersion)
	name, avatar := client.FetchProfile(ctx, c.cfg.Platform, userID)
	return name, avatar, nil
}
