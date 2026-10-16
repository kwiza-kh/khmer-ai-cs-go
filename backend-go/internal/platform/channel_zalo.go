package platform

import (
	"context"
	"strings"
)

// zaloChannel — Zalo Official Account (Vietnam).
type zaloChannel struct {
	p   *Pipeline
	cfg *configCred

	noTeardown
}

func (c *zaloChannel) Caps() Capabilities { return CapabilitiesFor("zalo") }

func (c *zaloChannel) Send(ctx context.Context, msg ChannelMessage) (string, error) {
	client := NewZaloClient(c.cfg.AccessToken)
	if msg.Kind == "media" && msg.MediaType == "image" && msg.MediaURL != "" {
		id, err := client.SendImage(ctx, msg.RecipientID, msg.MediaURL)
		if err != nil {
			return "", err
		}
		// Zalo's send-image call takes a URL and nothing else, so the answer is
		// not a caption here — it follows as its own message. Returning the image
		// alone used to be fine only because the picture WAS the whole reply
		// (text-to-image); a matched product photo has words to go with it, and
		// dropping them would be the customer-visible failure.
		if strings.TrimSpace(msg.Content) != "" {
			for _, chunk := range SplitChannelText(msg.Content, c.Caps()) {
				mid, err := client.SendText(ctx, msg.RecipientID, chunk)
				if err != nil {
					return "", err
				}
				if mid != "" {
					id = mid
				}
			}
		}
		return id, nil
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
