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
	"errors"
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

	// ReplyTo is the provider message id this send quotes, when the channel
	// supports quoting and the payload named one (agent replies do; AI replies
	// answer the message that just arrived and do not need the quote).
	ReplyTo int64
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

	// Disconnect tears down the provider-side half of this config so that
	// deactivating it — or reconnecting the same account under another tenant —
	// does not leave the provider pointing at this deployment. It returns the
	// sentence the operator console should record ("" when there was nothing to
	// remove) plus any provider error.
	//
	// The caller disables the row whatever this returns: a provider that refuses to
	// co-operate must not leave a merchant unable to switch a channel off.
	Disconnect(ctx context.Context) (string, error)
}

// MessageEditor is implemented by channels that can change or remove a message
// they already delivered. The API layer drives the inbox's edit/delete actions
// through EditDeliveredMessage/DeleteDeliveredMessage, gated by
// Capabilities.CanEditMessage / CanDeleteMessage.
type MessageEditor interface {
	// EditMessage replaces the text of the message identified by providerRef.
	EditMessage(ctx context.Context, providerRef, content string) error
	// DeleteMessage removes it for everyone.
	DeleteMessage(ctx context.Context, providerRef string) error
}

// ErrMessageActionsUnsupported means the channel cannot do this at the provider
// (Meta's send API has neither edit nor delete). The API maps it to a 400 that
// names the channel rather than pretending the action succeeded.
var ErrMessageActionsUnsupported = errors.New("platform: message actions are not supported on this channel")

// EditDeliveredMessage / DeleteDeliveredMessage build the channel for one config
// and perform the action on the message the outbox recorded.
func EditDeliveredMessage(ctx context.Context, p *Pipeline, plat string, creds ChannelCredentials, providerRef, content string) error {
	ch, err := ChannelFor(p, plat, creds)
	if err != nil {
		return err
	}
	editor, ok := ch.(MessageEditor)
	if !ok || !ch.Caps().CanEditMessage {
		return ErrMessageActionsUnsupported
	}
	return editor.EditMessage(ctx, providerRef, content)
}

func DeleteDeliveredMessage(ctx context.Context, p *Pipeline, plat string, creds ChannelCredentials, providerRef string) error {
	ch, err := ChannelFor(p, plat, creds)
	if err != nil {
		return err
	}
	editor, ok := ch.(MessageEditor)
	if !ok || !ch.Caps().CanDeleteMessage {
		return ErrMessageActionsUnsupported
	}
	return editor.DeleteMessage(ctx, providerRef)
}

// ChannelFor builds the Channel for one config from already-decrypted credentials.
// The API layer cannot name configCred, so this is the seam it drives the lifecycle
// through (today: Disconnect). The platform→implementation mapping stays in
// NewChannel — this only fills the struct.
func ChannelFor(p *Pipeline, plat string, creds ChannelCredentials) (Channel, error) {
	return NewChannel(p, &configCred{
		Platform:          plat,
		AccessToken:       creds.AccessToken,
		PageID:            creds.PageID,
		InstagramBusiness: creds.InstagramBusinessID,
		BotToken:          creds.BotToken,
	})
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

// The three channels with nothing provider-side to tear down say so once, here,
// rather than making the caller guess from the platform name.
type noTeardown struct{}

func (noTeardown) Disconnect(context.Context) (string, error) { return "", nil }
