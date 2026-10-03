// Channel is the behaviour half of a platform integration: how to send, fetch
// media, show a typing hint and read a customer profile. What a channel *is*
// (limits, windows, media kinds) lives in capabilities.go.
//
// Splitting the two is what removes the `switch platform` sites from
// pipeline.go: the orchestrator asks a Channel to do something and reads
// Capabilities to decide whether it may. A new channel is one implementation
// file plus one row in capabilitiesTable.
package platform

import (
	"context"
	"fmt"
)

// ChannelMessage is the provider-neutral outbound message handed to a Channel.
// It carries the already-parsed payload so every implementation does not repeat
// the `Payload["kind"]` / buttons / template decoding.
type ChannelMessage struct {
	// Delivery is the outbound row this send belongs to. Telegram's feedback
	// keyboard demotion needs LastMessageID and Payload, and the delivery id
	// travels through logging, so the original is kept.
	Delivery *outboundDelivery

	RecipientID string
	// Kind is text | buttons | media | template (payload "kind").
	Kind      string
	Content   string
	MediaURL  string
	MediaType string
	Buttons   [][2]string

	TemplateName       string
	TemplateLanguage   string
	TemplateBodyParams []string

	// Tag is the provider tag to attach (Meta HUMAN_AGENT when the 7-day
	// extension applies).
	Tag string
}

// Channel performs the provider-specific half of delivery for one config row.
type Channel interface {
	// Caps returns the declarative capabilities for this channel.
	Caps() Capabilities

	// Send delivers one message and returns the provider's message id.
	Send(ctx context.Context, msg ChannelMessage) (string, error)

	// DownloadMedia fetches inbound media. providerID may be empty when the
	// provider handed us a direct URL (sourceURL). declaredMIME is the mime the
	// webhook announced; implementations may prefer it over the one the
	// download returns.
	DownloadMedia(ctx context.Context, providerID, sourceURL, declaredMIME string) ([]byte, string, error)

	// Typing shows the transient "typing…" hint. Channels without one no-op.
	Typing(ctx context.Context, recipientID string) error

	// Profile returns (display name, avatar URL). Best effort: an error means
	// "keep whatever we already stored", not "fail the message".
	Profile(ctx context.Context, userID string) (string, string, error)
}

// NewChannel builds the Channel for a config row. This is the only place that
// maps a platform name to an implementation.
func NewChannel(p *Pipeline, cfg *configCred) (Channel, error) {
	if cfg == nil {
		return nil, fmt.Errorf("channel: nil config")
	}
	switch cfg.Platform {
	case "telegram":
		return &telegramChannel{p: p, cfg: cfg}, nil
	case "line":
		return &lineChannel{p: p, cfg: cfg}, nil
	case "zalo":
		return &zaloChannel{p: p, cfg: cfg}, nil
	case "meta", "instagram", "whatsapp":
		return &metaChannel{p: p, cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("channel: unsupported platform %q", cfg.Platform)
	}
}
