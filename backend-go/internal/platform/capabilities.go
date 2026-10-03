// Channel capabilities.
//
// Before this file every behavioural difference between channels was encoded as
// a `switch platform` in the send, policy and validation paths - 18 `case` sites
// spread over pipeline.go, policy.go and api/platform_handlers.go. Adding one
// channel meant finding all of them, and the reply-window rules lived in a
// package comment (policy.go) rather than in data.
//
// A capability is *data*: what a channel **is**, not what it **does**. Code that
// only has to decide something ("no window here", "how many runes before we must
// split", "which credentials are required", "is an image better than text here")
// reads this table. Code that has to perform something (download, send, typing)
// goes through the Channel interface in channel.go.
//
// Design borrowed from AstrBot's PlatformMetadata - `support_streaming_message` /
// `support_proactive_message` declared per adapter and consumed by the core
// (astrbot/core/astr_main_agent.py:1866) so the core never branches on a platform
// name. Architecture only: AstrBot is AGPL-3.0, no code was copied.
package platform

import "time"

// TextLimitUnit tells the splitter whether a channel's cap counts runes or
// bytes. WeChat/WeCom cap customer-service text at 2048 **bytes**, where a CJK
// rune is three of them - splitting by rune there silently overruns the cap.
type TextLimitUnit int

const (
	UnitRunes TextLimitUnit = iota
	UnitBytes
)

// MediaKinds is a bitmask of the message kinds a channel can carry.
type MediaKinds uint8

const (
	MediaText MediaKinds = 1 << iota
	MediaImage
	MediaAudio
	MediaVideo
	MediaFile

	MediaAll = MediaText | MediaImage | MediaAudio | MediaVideo | MediaFile
)

// Supports reports whether every kind in want is available on this channel.
func (m MediaKinds) Supports(want MediaKinds) bool { return m&want == want }

// CredentialField describes one credential a channel needs. The same list drives
// the connect form, the server-side validation and the error message, instead of
// the hand-written `if req.Platform == "instagram"` ladder that used to live in
// api/platform_handlers.go.
type CredentialField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
}

// Capabilities is the declarative description of one channel.
type Capabilities struct {
	Platform string
	// DisplayName is the human-facing name (policy messages, admin UI). It keeps
	// "The meta 24-hour reply window..." from reaching a merchant.
	DisplayName string

	// TextLimit / TextLimitUnit cap one outbound message.
	TextLimit     int
	TextLimitUnit TextLimitUnit

	// Media is the set of message kinds this channel can actually SEND. It is
	// what the channel implementations do, not what the provider's API allows in
	// principle: Telegram is text+audio here because telegramChannel.Send has no
	// image branch, and claiming otherwise would make text-to-image hand it a
	// picture it would deliver as text.
	Media MediaKinds

	// FlattensMarkdown is true for transports that deliver plain text to the
	// customer (every chat channel: Markdown is stripped in enqueueDelivery). The
	// website widget renders Markdown itself, so an image would be a downgrade
	// there - which is exactly the check text-to-image needs.
	FlattensMarkdown bool

	SupportsTyping  bool
	SupportsButtons bool
	// FeedbackButtons marks channels that can render the thumbs-up/down CSAT pair.
	FeedbackButtons bool

	// Windowless channels accept a reply without a preceding inbound message.
	// Channels with ReplyWindow > 0 must stay inside it (see policy.go).
	Windowless  bool
	ReplyWindow time.Duration
	// HumanExtension is the extra window a human agent gets (Meta's HUMAN_AGENT
	// tag). Zero when the channel has no such extension.
	HumanExtension time.Duration
	// TemplateExempt marks channels where an approved template escapes the reply
	// window (WhatsApp).
	TemplateExempt bool

	// ProactiveSend is true when the channel may be messaged with no inbound
	// context at all (campaigns). It is deliberately separate from Windowless:
	// Meta channels are not windowless, yet a template is still a valid send.
	ProactiveSend bool

	// MaxPerInbound caps how many messages we may send per inbound message
	// (WeChat customer service allows five per 48h). Zero means unlimited.
	MaxPerInbound int

	// Known is false for a platform string this build does not implement. Policy
	// code refuses to reason about unknown channels rather than guessing a
	// window - the previous `switch` returned "unsupported platform reply
	// policy" for everything it did not recognise, and that is preserved.
	Known bool

	Credentials []CredentialField
}

// Caps is the short name used by channel implementations.
type Caps = Capabilities

// capabilitiesTable is the single place a channel's static behaviour is
// declared. Adding a channel is one row here plus one Channel implementation -
// no other file needs a new `case`.
var capabilitiesTable = map[string]Capabilities{
	"telegram": {
		Platform:         "telegram",
		DisplayName:      "Telegram",
		TextLimit:        4096,
		TextLimitUnit:    UnitRunes,
		Media:            MediaText | MediaAudio,
		FlattensMarkdown: true,
		SupportsTyping:   true,
		SupportsButtons:  true,
		FeedbackButtons:  true,
		Windowless:       true,
		ProactiveSend:    true,
		Known:            true,
		Credentials: []CredentialField{
			{Key: "bot_token", Label: "Bot token", Required: true, Secret: true},
		},
	},
	"line": {
		Platform:         "line",
		DisplayName:      "LINE",
		TextLimit:        5000,
		TextLimitUnit:    UnitRunes,
		Media:            MediaText,
		FlattensMarkdown: true,
		SupportsTyping:   true,
		Windowless:       true,
		ProactiveSend:    true,
		Known:            true,
		Credentials: []CredentialField{
			{Key: "access_token", Label: "Channel access token", Required: true, Secret: true},
		},
	},
	"zalo": {
		Platform:         "zalo",
		DisplayName:      "Zalo",
		TextLimit:        2000,
		TextLimitUnit:    UnitRunes,
		Media:            MediaText | MediaImage,
		FlattensMarkdown: true,
		Windowless:       true,
		ProactiveSend:    true,
		Known:            true,
		Credentials: []CredentialField{
			{Key: "access_token", Label: "OA access token", Required: true, Secret: true},
		},
	},
	"whatsapp": {
		Platform:         "whatsapp",
		DisplayName:      "WhatsApp",
		TextLimit:        1024,
		TextLimitUnit:    UnitRunes,
		Media:            MediaText | MediaImage | MediaAudio | MediaFile,
		FlattensMarkdown: true,
		ReplyWindow:      CustomerCareWindowHours * time.Hour,
		TemplateExempt:   true,
		Known:            true,
		Credentials: []CredentialField{
			{Key: "access_token", Label: "System user access token", Required: true, Secret: true},
			{Key: "whatsapp_business_account_id", Label: "WhatsApp business account id", Required: true},
		},
	},
	"meta": {
		Platform:         "meta",
		DisplayName:      "Messenger",
		TextLimit:        2000,
		TextLimitUnit:    UnitRunes,
		Media:            MediaText | MediaImage | MediaAudio | MediaFile,
		FlattensMarkdown: true,
		SupportsTyping:   true,
		SupportsButtons:  true,
		ReplyWindow:      CustomerCareWindowHours * time.Hour,
		HumanExtension:   HumanAgentWindowDays * 24 * time.Hour,
		Known:            true,
		Credentials: []CredentialField{
			{Key: "access_token", Label: "Page access token", Required: true, Secret: true},
			{Key: "page_id", Label: "Facebook page id", Required: true},
		},
	},
	"instagram": {
		Platform:         "instagram",
		DisplayName:      "Instagram",
		TextLimit:        1000,
		TextLimitUnit:    UnitRunes,
		Media:            MediaText | MediaImage,
		FlattensMarkdown: true,
		SupportsTyping:   true,
		ReplyWindow:      CustomerCareWindowHours * time.Hour,
		HumanExtension:   HumanAgentWindowDays * 24 * time.Hour,
		Known:            true,
		Credentials: []CredentialField{
			{Key: "access_token", Label: "Page access token", Required: true, Secret: true},
			{Key: "instagram_business_id", Label: "Instagram business account id", Required: true},
		},
	},
	// The website widget is served over its own SSE path, not deliverToProvider;
	// it is listed so /platforms and the capability tests see it, but it keeps
	// Windowless=false so the reply-window policy still refuses it exactly as
	// before (see the "unsupported platform reply policy" branch). It also
	// renders Markdown, so text-to-image must not fire on it.
	"web": {
		Platform:      "web",
		DisplayName:   "Web widget",
		TextLimit:     2000,
		TextLimitUnit: UnitRunes,
		Media:         MediaText | MediaImage,
		Known:         true,
	},
}

// defaultCapabilities is what an unknown platform gets. Known=false is what
// makes policy code refuse rather than invent a window.
func defaultCapabilities(platform string) Capabilities {
	return Capabilities{
		Platform:      platform,
		DisplayName:   platform,
		TextLimit:     2000,
		TextLimitUnit: UnitRunes,
		Media:         MediaText | MediaImage,
		Known:         false,
	}
}

// CapabilitiesFor returns the declared capabilities for a platform string.
func CapabilitiesFor(platform string) Capabilities {
	if c, ok := capabilitiesTable[platform]; ok {
		return c
	}
	return defaultCapabilities(platform)
}

// KnownPlatforms lists every platform this build declares capabilities for,
// sorted. Used by tests and by the admin surface.
func KnownPlatforms() []string {
	out := make([]string, 0, len(capabilitiesTable))
	for name := range capabilitiesTable {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// sortStrings keeps KnownPlatforms deterministic without pulling sort into the
// hot path of every caller.
func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

// SplitChannelText splits an outbound message so that every chunk fits the
// channel's cap in the channel's own unit. It replaces the bare
// SplitPlatformText(text, PlatformTextLimit(platform)) pattern, which always
// counted runes.
func SplitChannelText(text string, caps Capabilities) []string {
	if caps.TextLimit <= 0 {
		return []string{text}
	}
	switch caps.TextLimitUnit {
	case UnitBytes:
		return splitTextBytes(text, caps.TextLimit)
	default:
		return SplitPlatformText(text, caps.TextLimit)
	}
}

// splitTextBytes splits on rune boundaries but measures UTF-8 bytes, so a chunk
// never exceeds limit bytes and never cuts a rune in half. It prefers to break
// at the last space or newline inside the window, mirroring SplitPlatformText.
func splitTextBytes(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	runes := []rune(text)
	start := 0
	for start < len(runes) {
		size := 0
		end := start
		for end < len(runes) {
			n := len(string(runes[end])) // UTF-8 length of this rune
			if size+n > limit {
				break
			}
			size += n
			end++
		}
		if end == start {
			// A single rune larger than the whole budget: take it anyway so the
			// splitter always makes progress instead of looping forever.
			end = start + 1
		}
		if end < len(runes) {
			breakAt := -1
			for i := end - 1; i > start; i-- {
				if runes[i] == ' ' || runes[i] == '\n' {
					breakAt = i
					break
				}
			}
			if breakAt > start {
				end = breakAt + 1
			}
		}
		out = append(out, string(runes[start:end]))
		start = end
	}
	return out
}
