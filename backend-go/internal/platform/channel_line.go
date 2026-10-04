package platform

import (
	"context"
	"fmt"
)

// lineChannel — LINE Messaging API.
type lineChannel struct {
	p   *Pipeline
	cfg *configCred

	noTeardown
}

func (c *lineChannel) Caps() Capabilities { return CapabilitiesFor("line") }

func (c *lineChannel) Send(ctx context.Context, msg ChannelMessage) (string, error) {
	if msg.Kind != "text" {
		return "", fmt.Errorf("LINE currently supports text messages only")
	}
	client := NewLineClient(c.cfg.AccessToken)
	chunks := SplitChannelText(msg.Content, c.Caps())
	last := ""
	for _, chunk := range chunks {
		id, err := client.PushText(ctx, msg.RecipientID, chunk)
		if err != nil {
			return "", err
		}
		last = id
	}
	return last, nil
}

func (c *lineChannel) DownloadMedia(ctx context.Context, providerID, _ string, _ string) ([]byte, string, error) {
	if providerID == "" {
		return nil, "", nil
	}
	return NewLineClient(c.cfg.AccessToken).DownloadContent(ctx, providerID)
}

func (c *lineChannel) Typing(ctx context.Context, recipientID string) error {
	if recipientID == "" {
		return nil
	}
	return NewLineClient(c.cfg.AccessToken).SendTypingIndicator(ctx, recipientID)
}

func (c *lineChannel) Profile(ctx context.Context, userID string) (string, string, error) {
	return NewLineClient(c.cfg.AccessToken).GetProfile(ctx, userID)
}
