package platform

import (
	"context"
	"errors"
)

// Connection self-checks ("test this config" in the operator console).
//
// This lives here, next to NewChannel, for the same reason that switch does: what
// it takes to put a given provider on the air is channel knowledge, not
// HTTP-handler knowledge. It used to be an 84-line `switch c.Platform` inside
// internal/api, where each case built its own client, decrypted its own copy of
// the credentials (Meta's client was constructed twice, once per `case`), and
// chose its own status code. AGENTS.md calls that shape out by name — "a decision
// belongs in data, not in a switch" — and capabilities.go already records one
// earlier ladder removed from that same file.
//
// What stays in the API layer is what is API-layer work: reading the row,
// decrypting the credentials, recording connection_health, persisting the routing
// identity, and choosing a status code from the stage below.

// ChannelCredentials is the decrypted credential bundle for one platform config,
// shared by the two provider-side lifecycle calls: the connection self-check
// (VerifyConnection) and the teardown (ChannelFor → Channel.Disconnect). Absent
// credentials are the empty string — the sealer answers "" for an empty input
// rather than erroring.
type ChannelCredentials struct {
	AccessToken               string
	BotToken                  string
	WebhookSecret             string
	PageID                    string
	InstagramBusinessID       string
	WhatsAppBusinessAccountID string
}

// VerifyParams are the server-side facts a self-check needs that are not
// per-channel credentials.
type VerifyParams struct {
	// GraphAPIVersion is the Meta Graph API version to call.
	GraphAPIVersion string
	// WebhookURL builds this deployment's public webhook URL for a platform. It
	// fails when PUBLIC_API_URL is unset or is not a public https origin.
	WebhookURL func(platform string) (string, error)
}

// VerifyOutcome is what a successful self-check learned.
type VerifyOutcome struct {
	// AccountName is what connection_health stores as account_name (the bot's
	// @handle, the page name, the OA name).
	AccountName string
	// Detail is the sentence the operator console shows as the reason.
	Detail string
	// RoutingIdentity is the provider-side id inbound events are keyed on — LINE's
	// bot userId, Zalo's OA id — or "" for channels that do not need one.
	RoutingIdentity string
	// Extra carries provider-specific fields the API adds to its response body.
	Extra map[string]any
}

// VerifyStage says which part of a self-check failed.
//
// The API layer maps stages to status codes and to whether the failure is worth
// recording against the config; this package deliberately does not know what a
// 502 is.
type VerifyStage int

const (
	// VerifyServerMisconfigured — the credential may be fine, but this host cannot
	// complete the check (no usable PUBLIC_API_URL). Nothing was asked of the
	// provider, so this must not be recorded as a connection failure.
	VerifyServerMisconfigured VerifyStage = iota + 1
	// VerifyWebhookRegistrationFailed — the credential verified, but the provider
	// would not accept the webhook. A partial success: the operator sees the
	// reason, and the config is not marked connected.
	VerifyWebhookRegistrationFailed
	// VerifyUnsupportedPlatform — the config names a platform this build serves no
	// outbound channel for.
	VerifyUnsupportedPlatform
)

// VerifyError is a self-check failure that is not "the provider rejected the
// credential" — the caller has to treat it differently, so it has to be typed.
type VerifyError struct {
	Stage VerifyStage
	Msg   string
}

func (e *VerifyError) Error() string { return e.Msg }

// verifier is what a self-check needs from one channel implementation.
type verifier func(ctx context.Context, plat string, creds ChannelCredentials, p VerifyParams) (VerifyOutcome, error)

// verifyHandler returns the verifier for a platform, or nil for a name this build
// serves no outbound channel for. This is the only place that maps a platform name
// to its verifier — deliberately the same shape and the same list as NewChannel's
// switch, so "can send" and "can be verified" cannot drift apart.
func verifyHandler(plat string) verifier {
	switch plat {
	case "telegram":
		return verifyTelegram
	case "meta", "instagram", "whatsapp":
		return verifyMetaFamily
	case "line":
		return verifyLine
	case "zalo":
		return verifyZalo
	}
	return nil
}

// VerifyConnection runs the provider-side half of "test this connection".
//
// Any error that is not a *VerifyError is a provider-side failure and should be
// recorded against the config as such.
func VerifyConnection(ctx context.Context, plat string, creds ChannelCredentials, p VerifyParams) (VerifyOutcome, error) {
	verify := verifyHandler(plat)
	if verify == nil {
		return VerifyOutcome{}, &VerifyError{
			Stage: VerifyUnsupportedPlatform,
			Msg:   "unsupported platform: " + plat,
		}
	}
	return verify(ctx, plat, creds, p)
}

func verifyTelegram(ctx context.Context, _ string, creds ChannelCredentials, p VerifyParams) (VerifyOutcome, error) {
	client := NewTelegramClient(creds.BotToken)
	botID, username, firstName, err := client.GetMe(ctx)
	if err != nil {
		return VerifyOutcome{}, err
	}
	if botID == 0 {
		return VerifyOutcome{}, errors.New("invalid bot token")
	}
	webhookURL, err := p.WebhookURL("telegram")
	if err != nil {
		return VerifyOutcome{}, &VerifyError{Stage: VerifyServerMisconfigured, Msg: err.Error()}
	}
	// Re-register only when the provider's idea of the webhook differs from ours:
	// SetWebhook is a provider write, and re-issuing it on every check would make
	// an idempotent test look like a change.
	infoURL, _ := client.GetWebhookInfo(ctx)
	if infoURL != webhookURL {
		if err := client.SetWebhook(ctx, webhookURL, creds.WebhookSecret); err != nil {
			return VerifyOutcome{}, &VerifyError{
				Stage: VerifyWebhookRegistrationFailed,
				Msg:   "register Telegram webhook: " + err.Error(),
			}
		}
	}
	account := firstName
	if username != "" {
		account = "@" + username
	}
	return VerifyOutcome{
		AccountName: account,
		Detail:      "Webhook registered via Telegram Bot API",
		Extra:       map[string]any{"bot_username": username, "bot_first_name": firstName},
	}, nil
}

// verifyMetaFamily — one Graph API call for meta, instagram and whatsapp, which
// differ only in the sentence the console shows. plat is passed through so the
// Graph call is made for the platform the config actually names.
func verifyMetaFamily(ctx context.Context, plat string, creds ChannelCredentials, p VerifyParams) (VerifyOutcome, error) {
	detail := "Connection verified via Meta Graph API"
	if plat == "whatsapp" {
		detail = "Connection verified via WhatsApp Cloud API"
	}
	client := NewMetaClient(creds.AccessToken, creds.PageID, creds.InstagramBusinessID, p.GraphAPIVersion)
	name, err := client.VerifyConnection(ctx, plat)
	if err != nil {
		return VerifyOutcome{}, err
	}
	return VerifyOutcome{AccountName: name, Detail: detail}, nil
}

func verifyLine(ctx context.Context, _ string, creds ChannelCredentials, p VerifyParams) (VerifyOutcome, error) {
	client := NewLineClient(creds.AccessToken)
	name, _, botUserID, err := client.GetBotInfo(ctx)
	if err != nil {
		return VerifyOutcome{}, err
	}
	// The bot's userId is returned as RoutingIdentity so the caller can store it:
	// LINE stamps it on every webhook as "destination", and it is the only key
	// that routes an inbound event to this tenant rather than to whichever LINE
	// config came first.
	//
	// Auto-register + self-test the webhook: the merchant never has to touch the
	// LINE Developers console for webhook configuration.
	detail := "Connection verified via LINE Messaging API"
	webhookURL, werr := p.WebhookURL("line")
	if werr == nil {
		if rerr := client.SetWebhookEndpoint(ctx, webhookURL); rerr != nil {
			detail = "Connection verified; webhook auto-registration failed: " + rerr.Error()
		} else if ok, msg, terr := client.TestWebhookEndpoint(ctx); terr != nil {
			detail = "Connection verified; webhook registered (self-test error: " + terr.Error() + ")"
		} else if ok {
			detail = "Connection verified; webhook registered and self-test passed via LINE API"
		} else {
			detail = "Connection verified; webhook registered (LINE self-test pending: " + msg + ")"
		}
	}
	return VerifyOutcome{
		AccountName:     name,
		Detail:          detail,
		RoutingIdentity: botUserID,
		// Always present, even when the URL could not be built: the console renders
		// the field, and an absent key would read as a missing feature rather than
		// as a misconfigured host.
		Extra: map[string]any{"webhook_url": webhookURL},
	}, nil
}

func verifyZalo(ctx context.Context, _ string, creds ChannelCredentials, _ VerifyParams) (VerifyOutcome, error) {
	client := NewZaloClient(creds.AccessToken)
	name, oaID, err := client.VerifyOA(ctx)
	if err != nil {
		return VerifyOutcome{}, err
	}
	// oa_id is the routing identity carried on every Zalo webhook.
	return VerifyOutcome{
		AccountName:     name,
		Detail:          "Connection verified via Zalo OA API",
		RoutingIdentity: oaID,
	}, nil
}
