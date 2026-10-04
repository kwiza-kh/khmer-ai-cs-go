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
	req := &SendRequest{
		Platform:           c.cfg.Platform,
		RecipientID:        msg.RecipientID,
		Kind:               msg.Kind,
		MediaURL:           msg.MediaURL,
		MediaType:          msg.MediaType,
		Buttons:            msg.Buttons,
		TemplateName:       msg.TemplateName,
		TemplateLanguage:   msg.TemplateLanguage,
		TemplateBodyParams: msg.TemplateBodyParams,
		Tag:                msg.Tag,
	}
	if msg.Kind == "text" || msg.Kind == "buttons" {
		req.Text = msg.Content
	}
	chunks := SplitChannelText(msg.Content, c.Caps())
	last := ""
	if msg.Kind != "text" || len(chunks) == 0 {
		id, err := client.SendMessage(ctx, req)
		if err != nil {
			return "", err
		}
		return id, nil
	}
	for _, chunk := range chunks {
		req.Text = chunk
		id, err := client.SendMessage(ctx, req)
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
	case "meta", "instagram":
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
