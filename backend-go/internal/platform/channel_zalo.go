package platform

import "context"

// zaloChannel — Zalo Official Account (Vietnam).
type zaloChannel struct {
	p   *Pipeline
	cfg *configCred
}

func (c *zaloChannel) Caps() Capabilities { return CapabilitiesFor("zalo") }

func (c *zaloChannel) Send(ctx context.Context, msg ChannelMessage) (string, error) {
	client := NewZaloClient(c.cfg.AccessToken)
	if msg.Kind == "media" && msg.MediaType == "image" && msg.MediaURL != "" {
		return client.SendImage(ctx, msg.RecipientID, msg.MediaURL)
	}
	chunks := SplitChannelText(msg.Content, c.Caps())
	last := ""
	for _, chunk := range chunks {
		id, err := client.SendText(ctx, msg.RecipientID, chunk)
		if err != nil {
			return "", err
		}
		if id != "" {
			last = id
		}
	}
	return last, nil
}

// DownloadMedia — Zalo's OA API hands inbound media to us as a URL, so the only
// fetch available is the plain HTTP one. This mirrors what the old default
// branch in downloadInboundMedia did for zalo.
func (c *zaloChannel) DownloadMedia(ctx context.Context, _, sourceURL, _ string) ([]byte, string, error) {
	if sourceURL == "" {
		return nil, "", nil
	}
	return downloadMediaBytes(ctx, sourceURL)
}

// Typing — the Zalo OA client has no typing indicator.
func (c *zaloChannel) Typing(context.Context, string) error { return nil }

func (c *zaloChannel) Profile(ctx context.Context, userID string) (string, string, error) {
	return NewZaloClient(c.cfg.AccessToken).GetProfile(ctx, userID)
}
