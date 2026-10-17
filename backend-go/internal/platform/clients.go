// Package platform — provider clients (Meta/WhatsApp/Instagram Graph API,
// Telegram Bot API, LINE Messaging API). Thin HTTP wrappers; the durable
// queue lives in pipeline.go.
package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/textutil"
)

// unwrapURLErr drops the *url.Error wrapper, whose message embeds the full
// request URL. Telegram Bot API paths carry the bot token and Meta URLs can
// carry page tokens, so a raw transport error would leak credentials into
// logs, platform_outbox.last_error and operator alert text. Keeping only the
// underlying cause (timeout, DNS, refused, TLS) stays informative without the
// credential.
func unwrapURLErr(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// Provider base URLs are variables rather than constants so tests can point the
// clients at an httptest server. Nothing writes them after init in production.
var (
	metaGraphBase = "https://graph.facebook.com"
	telegramBase  = "https://api.telegram.org"
	lineBase      = "https://api.line.me"
	zaloBase      = "https://openapi.zalo.me"
)

const (
	httpTimeout    = 15 * time.Second
	maxDownloadCap = 25 * 1024 * 1024
)

var httpClient = &http.Client{Timeout: httpTimeout}

// ============================================
// Meta / WhatsApp / Instagram
// ============================================

// MetaClient talks to the Graph API.
type MetaClient struct {
	AccessToken       string
	PageID            string
	InstagramBusiness string
	GraphVersion      string
}

// SendRequest is a provider-neutral outbound message.
type SendRequest struct {
	Platform           string
	RecipientID        string
	Text               string
	Kind               string // text | media | buttons | template
	MediaURL           string
	MediaType          string
	Buttons            [][2]string
	TemplateName       string
	TemplateLanguage   string
	TemplateBodyParams []string
	// Tag carries the Meta message tag (e.g. HUMAN_AGENT) that extends the
	// 24-hour window to 7 days for human-agent replies on Messenger/IG.
	Tag string
}

func NewMetaClient(accessToken, pageID, instagramBusiness, version string) *MetaClient {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "v24.0"
	}
	return &MetaClient{AccessToken: accessToken, PageID: pageID, InstagramBusiness: instagramBusiness, GraphVersion: strings.TrimPrefix(v, "/")}
}

func (m *MetaClient) base() string { return metaGraphBase + "/" + m.GraphVersion }

func (m *MetaClient) accountID(platform string) string {
	if platform == "instagram" && m.InstagramBusiness != "" {
		return m.InstagramBusiness
	}
	return m.PageID
}

// FetchProfile returns (name, avatarURL) for one Messenger or Instagram user.
//
// The Graph field list differs by object type — a Page's user has
// first_name/last_name, an Instagram account has username — and that is the
// provider's shape, so it stays here with the rest of the Graph API knowledge
// instead of leaking a platform branch into the orchestrator.
func (m *MetaClient) FetchProfile(ctx context.Context, platform, userID string) (string, string) {
	fields := "first_name,last_name,profile_pic"
	if platform == "instagram" {
		fields = "name,username,profile_pic"
	}
	v, err := m.get(ctx, "/"+userID, map[string][]string{"fields": {fields}})
	if err != nil {
		return "", ""
	}
	name, _ := v["name"].(string)
	if name == "" {
		first, _ := v["first_name"].(string)
		last, _ := v["last_name"].(string)
		name = strings.TrimSpace(first + " " + last)
	}
	pic, _ := v["profile_pic"].(string)
	return name, pic
}

func (m *MetaClient) get(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	u := m.base() + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Access token rides the Authorization header, never the URL query: a
	// transport-level *url.Error would otherwise render the token into the
	// error string that reaches last_error columns and operator alerts.
	req.Header.Set("Authorization", "OAuth "+m.AccessToken)
	return m.do(req, "meta get "+path)
}

func (m *MetaClient) post(ctx context.Context, path string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base()+path, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "OAuth "+m.AccessToken)
	return m.do(req, "meta post "+path)
}

func (m *MetaClient) do(req *http.Request, what string) (map[string]any, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, unwrapURLErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if resp.StatusCode >= 400 {
		msg := ""
		if e, ok := v["error"].(map[string]any); ok {
			if mm, ok := e["message"].(string); ok {
				msg = mm
			}
		}
		return nil, fmt.Errorf("%s failed (%d): %s", what, resp.StatusCode, msg)
	}
	return v, nil
}

// VerifyConnection confirms the token works and returns the account name.
func (m *MetaClient) VerifyConnection(ctx context.Context, platform string) (string, error) {
	var path string
	params := url.Values{}
	switch {
	case platform == "whatsapp":
		path = "/" + m.accountID(platform)
		params.Set("fields", "id,display_phone_number,verified_name")
	case platform == "instagram" && m.InstagramBusiness != "":
		// Verify the Instagram professional account itself — /me with a Page
		// token resolves to the Page, which proves nothing about IG access.
		path = "/" + m.InstagramBusiness
		params.Set("fields", "id,name,username")
	case platform == "meta":
		// A Messenger token's capabilities are messaging + webhook management, not
		// page reading: /me is for user tokens and /{page}?fields=name needs
		// pages_read_engagement, which this deployment's token does not carry
		// (Graph #100, 2026-10-17) — so both "obvious" checks reported a healthy
		// page as broken. subscribed_apps is the permission the channel actually
		// relies on, so it answers "is this token wired to this page".
		path = "/" + m.accountID(platform) + "/subscribed_apps"
	default:
		path = "/me"
		params.Set("fields", "id,name")
	}
	v, err := m.get(ctx, path, params)
	if err != nil {
		return "", err
	}
	for _, k := range []string{"name", "username", "verified_name", "display_phone_number"} {
		if s, ok := v[k].(string); ok && s != "" {
			return s, nil
		}
	}
	// subscribed_apps answers with {"data":[{"name":"…","subscribed_fields":[…]}]}.
	// An app subscribed to `messages` is exactly what the channel needs, so its
	// name is the account name the operator sees.
	if data, ok := v["data"].([]any); ok && len(data) > 0 {
		if first, ok := data[0].(map[string]any); ok {
			if name, ok := first["name"].(string); ok && name != "" {
				return name, nil
			}
		}
	}
	return "", nil
}

// GetUserName returns the display name for a PSID/user id.
func (m *MetaClient) GetUserName(ctx context.Context, userID, platform string) (string, error) {
	fields := "name"
	if platform == "instagram" {
		fields = "name,username"
	}
	v, err := m.get(ctx, "/"+userID, url.Values{"fields": []string{fields}})
	if err != nil {
		return "", err
	}
	if s, ok := v["name"].(string); ok {
		return s, nil
	}
	return "", nil
}

// DownloadMedia fetches a Meta media object by id. Returns (bytes, mime).
func (m *MetaClient) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	v, err := m.get(ctx, "/"+mediaID, nil)
	if err != nil {
		return nil, "", err
	}
	mediaURL, _ := v["url"].(string)
	mime, _ := v["mime_type"].(string)
	if mediaURL == "" {
		return nil, "", fmt.Errorf("media url missing")
	}
	data, dlMime, err := downloadMediaBytes(ctx, mediaURL)
	if mime == "" {
		mime = dlMime
	}
	return data, mime, err
}

// downloadMediaBytes is the indirection clients and channels use to fetch a
// provider-supplied media URL. Production points at the SSRF-guarded
// downloadBytes below; tests swap in a plain fetcher so a loopback httptest
// server can stand in for the provider's CDN.
var downloadMediaBytes = downloadBytes

// downloadBytes fetches a provider- or webhook-supplied media URL. The URL is
// attacker-influenced (a signed webhook payload names it), so it goes through
// the shared RAG egress guard: scheme allowlist, pre-dial public-address check,
// a dial-time control that binds the check to the resolved address, and a
// re-validated 3-hop redirect cap — the default client would otherwise follow
// up to 10 redirects into the host's own network with no check at all.
func downloadBytes(ctx context.Context, u string) ([]byte, string, error) {
	parsed, err := rag.ValidateFetchURL(u)
	if err != nil {
		return nil, "", fmt.Errorf("media download blocked: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := rag.PublicFetchClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("media download: %w", unwrapURLErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("media download status %d", resp.StatusCode)
	}
	mime := resp.Header.Get("Content-Type")
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadCap+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxDownloadCap {
		return nil, "", fmt.Errorf("media exceeds size limit")
	}
	return data, mime, nil
}

// SendMessage sends one (already-chunked) message. Returns provider id.
// ErrHumanTagNotApproved — Meta refused the HUMAN_AGENT tag because the app has
// not been approved for it. Nothing about this is transient: the fix is an App
// Review approval (or a reply inside the 24h window), so the sender classifies
// it as terminal instead of burning the retry budget on it. Seen in production
// on 2026-10-06: two Meta deliveries exhausted 5 attempts over hours with
// `(#100) Cannot tag messages with "HUMAN_AGENT" without prior approval`.
var ErrHumanTagNotApproved = errors.New("meta: HUMAN_AGENT tag is not approved for this app")

// humanTagNotApproved reports whether this Meta 400 is the "tag needs approval"
// refusal. Only meaningful when a tag was actually attached: the same status
// text from a tagless send is a different problem and must stay retryable.
func humanTagNotApproved(reqTag, errText string) bool {
	return reqTag != "" && strings.Contains(errText, "Cannot tag messages")
}

// classifyMetaSendError decides whether a failed Meta send is the tag refusal
// (terminal — see ErrHumanTagNotApproved) or something worth retrying, and wraps
// the former in the sentinel the pipeline classifies by.
func classifyMetaSendError(reqTag string, err error) error {
	if humanTagNotApproved(reqTag, err.Error()) {
		return fmt.Errorf("%w: %v", ErrHumanTagNotApproved, err)
	}
	return err
}

func (m *MetaClient) SendMessage(ctx context.Context, req *SendRequest) (string, error) {
	account := m.accountID(req.Platform)
	body := buildMetaMessageBody(req)
	v, err := m.post(ctx, "/"+account+"/messages", body)
	if err != nil {
		return "", classifyMetaSendError(req.Tag, err)
	}
	if id, ok := v["message_id"].(string); ok {
		return id, nil
	}
	if msgs, ok := v["messages"].([]any); ok && len(msgs) > 0 {
		if m0, ok := msgs[0].(map[string]any); ok {
			if id, ok := m0["id"].(string); ok {
				return id, nil
			}
		}
	}
	return "", nil
}

// SendSenderAction drives the transient typing indicator on Messenger/IG.
func (m *MetaClient) SendSenderAction(ctx context.Context, platform, recipientID, action string) error {
	_, err := m.post(ctx, "/"+m.accountID(platform)+"/messages", map[string]any{
		"recipient":     map[string]any{"id": recipientID},
		"sender_action": action,
	})
	return err
}

// ListWhatsAppTemplates fetches approved message templates from the WhatsApp
// Business account behind this config.
// SendPrivateReply answers a comment in a private message, which is how a
// commenter enters the DM thread. Meta allows one private reply per comment and
// only for a limited window after it was posted.
func (m *MetaClient) SendPrivateReply(ctx context.Context, platform, commentID, text string) (string, error) {
	body := map[string]any{
		"recipient": map[string]any{"comment_id": commentID},
		"message":   map[string]any{"text": text},
	}
	v, err := m.post(ctx, "/"+m.accountID(platform)+"/messages", body)
	if err != nil {
		return "", err
	}
	if id, ok := v["message_id"].(string); ok {
		return id, nil
	}
	return "", nil
}

// SetMessengerProfile installs the entry points a Messenger customer sees before
// typing anything: a greeting for a new thread and a persistent menu with the two
// actions every shop's customers eventually want. Returns a sentence for the
// connection-check detail.
//
// It is a no-op when the Page already has a menu: an operator (or an earlier
// integration) may have configured one, and silently replacing it would be the
// same class of surprise as overwriting a system prompt. Best-effort by design —
// a Page token without pages_manage_metadata refuses the write but is still a
// working messaging channel.
func (m *MetaClient) SetMessengerProfile(ctx context.Context) (string, error) {
	if m.PageID == "" {
		return "", nil
	}
	if v, err := m.get(ctx, "/"+m.PageID+"/messenger_profile", url.Values{"fields": []string{"persistent_menu"}}); err == nil {
		if data, ok := v["data"].([]any); ok && len(data) > 0 {
			if first, ok := data[0].(map[string]any); ok {
				if menu, ok := first["persistent_menu"].([]any); ok && len(menu) > 0 {
					return "existing Messenger menu kept", nil
				}
			}
		}
	}
	body := map[string]any{
		// Meta refuses a persistent menu without a Get Started button
		// ("You must set a Get started button if you also wish to use persistent
		// menu", #100 — hit in production on 2026-10-17). The tap arrives as a
		// GET_STARTED postback; the webhook turns it into a greeting message.
		"get_started": map[string]any{"payload": "GET_STARTED"},
		"greeting": []map[string]any{
			{"locale": "default", "text": "Hi! Tap a button below or just type your question. 🙋 Talk to a human any time."},
			{"locale": "km_KH", "text": "សួស្តី! ចុចប៊ូតុងខាងក្រោម ឬវាយសំណួររបស់អ្នក។ 🙋 ចង់និយាយជាមួយមនុស្ស សូមចុចប៊ូតុង។"},
		},
		"persistent_menu": []map[string]any{
			{
				"locale":                  "default",
				"composer_input_disabled": false,
				"call_to_actions": []map[string]any{
					{"type": "postback", "title": "🙋 Talk to a human", "payload": "HANDOFF"},
					{"type": "postback", "title": "💰 Prices & catalog", "payload": "PRICES"},
				},
			},
			{
				"locale":                  "km_KH",
				"composer_input_disabled": false,
				"call_to_actions": []map[string]any{
					{"type": "postback", "title": "🙋 និយាយជាមួយមនុស្ស", "payload": "HANDOFF"},
					{"type": "postback", "title": "💰 តម្លៃ និងទំនិញ", "payload": "PRICES"},
				},
			},
		},
	}
	if _, err := m.post(ctx, "/"+m.PageID+"/messenger_profile", body); err != nil {
		return "", err
	}
	return "Messenger greeting + menu installed", nil
}

func (m *MetaClient) ListWhatsAppTemplates(ctx context.Context, accountID string) ([]map[string]any, error) {
	if accountID == "" {
		return nil, fmt.Errorf("whatsapp account not configured")
	}
	v, err := m.get(ctx, "/"+accountID+"/message_templates", url.Values{"limit": []string{"100"}})
	if err != nil {
		return nil, err
	}
	data, _ := v["data"].([]any)
	out := make([]map[string]any, 0, len(data))
	for _, d := range data {
		if row, ok := d.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out, nil
}

// metaQuickReplies renders the (title, payload) pairs as Send API quick replies.
// Titles are capped at 20 characters by Meta; a longer one is truncated rather
// than rejected, because a long label must not cost the customer the choices.
func metaQuickReplies(buttons [][2]string) []map[string]any {
	out := make([]map[string]any, 0, len(buttons))
	for _, b := range buttons {
		title := []rune(b[0])
		if len(title) > 20 {
			title = title[:20]
		}
		out = append(out, map[string]any{"content_type": "text", "title": string(title), "payload": b[1]})
	}
	return out
}

func buildMetaMessageBody(req *SendRequest) map[string]any {
	body := buildMetaMessageBodyInner(req)
	if tag := metaMessageTag(req); tag != "" {
		body["tag"] = tag
	}
	return body
}

func buildMetaMessageBodyInner(req *SendRequest) map[string]any {
	// WhatsApp Cloud API uses a different envelope from Messenger/Instagram:
	// messaging_product + to + type + a type-specific object. Sending the
	// Messenger shape to /{phone-id}/messages is rejected with HTTP 400, which
	// previously made every non-template WhatsApp reply fail.
	if req.Platform == "whatsapp" {
		return buildWhatsAppBody(req)
	}
	switch req.Kind {
	case "media":
		mediaType := req.MediaType
		if mediaType == "" {
			mediaType = "image"
		}
		// The composer calls a PDF "document"; the Send API calls it "file".
		if mediaType == "document" {
			mediaType = "file"
		}
		// Messenger and Instagram take media as message.attachment{type,payload.url}
		// — "Sending audio, video, or a file from a URL will use the same format"
		// (Send API docs). The previous top-level `type`/`image:{link}` body is the
		// Zalo/LINE shape; Graph answers it with error 100 (Invalid parameter), and
		// nothing had ever exercised it (0 media rows in production, 2026-10-16).
		//
		// There is no caption field here (unlike WhatsApp's image.caption), which is
		// why the capability table gives Messenger/Instagram CaptionLimit 0 and
		// channel_meta.Send delivers the reply as its own message.
		message := map[string]any{
			"attachment": map[string]any{
				"type":    mediaType,
				"payload": map[string]any{"url": req.MediaURL},
			},
		}
		// The CSAT pair rides on the picture when the reply was sent as one, so a
		// media answer still collects a rating (Telegram attaches the same pair to
		// its photo message).
		if len(req.Buttons) > 0 {
			message["quick_replies"] = metaQuickReplies(req.Buttons)
		}
		return map[string]any{
			"recipient": map[string]any{"id": req.RecipientID},
			"message":   message,
		}
	case "buttons":
		return map[string]any{
			"recipient": map[string]any{"id": req.RecipientID},
			"message":   map[string]any{"text": req.Text, "quick_replies": metaQuickReplies(req.Buttons)},
		}
	case "template":
		var params []map[string]any
		for _, p := range req.TemplateBodyParams {
			params = append(params, map[string]any{"type": "text", "text": p})
		}
		return map[string]any{
			"messaging_product": "whatsapp",
			"to":                req.RecipientID,
			"type":              "template",
			"template": map[string]any{
				"name": req.TemplateName,
				"language": map[string]any{
					"code": textutil.FirstNonEmpty(req.TemplateLanguage, "en"),
				},
				"components": []map[string]any{{
					"type": "body", "parameters": params,
				}},
			},
		}
	default: // text
		return map[string]any{
			"recipient": map[string]any{"id": req.RecipientID},
			"message":   map[string]any{"text": req.Text},
		}
	}
}

// buildWhatsAppBody — WhatsApp Cloud API message envelope. Text/media/
// interactive all share messaging_product + to + type; templates add their
// own object. Long text is capped at 4096 by the caller (SplitPlatformText).
func buildWhatsAppBody(req *SendRequest) map[string]any {
	base := map[string]any{"messaging_product": "whatsapp", "to": req.RecipientID}
	switch req.Kind {
	case "template":
		var params []map[string]any
		for _, p := range req.TemplateBodyParams {
			params = append(params, map[string]any{"type": "text", "text": p})
		}
		base["type"] = "template"
		base["template"] = map[string]any{
			"name":     req.TemplateName,
			"language": map[string]any{"code": textutil.FirstNonEmpty(req.TemplateLanguage, "en")},
			"components": []map[string]any{{
				"type": "body", "parameters": params,
			}},
		}
		return base
	case "media":
		mediaType := req.MediaType
		if mediaType == "" {
			mediaType = "image"
		}
		media := map[string]any{"link": req.MediaURL}
		if req.Text != "" && mediaType != "audio" {
			media["caption"] = req.Text
		}
		base["type"] = mediaType
		base[mediaType] = media
		return base
	case "buttons":
		// WhatsApp interactive reply buttons (max 3, titles ≤ 20 chars).
		var btns []map[string]any
		for i, b := range req.Buttons {
			title := []rune(b[0])
			if len(title) > 20 {
				title = title[:20]
			}
			btns = append(btns, map[string]any{
				"type": "reply",
				"reply": map[string]any{
					"id":    textutil.FirstNonEmpty(b[1], string(title)),
					"title": string(title),
				},
			})
			if i == 2 {
				break
			}
		}
		base["type"] = "interactive"
		base["interactive"] = map[string]any{
			"type":   "button",
			"body":   map[string]any{"text": req.Text},
			"action": map[string]any{"buttons": btns},
		}
		return base
	default: // text
		base["type"] = "text"
		base["text"] = map[string]any{"body": req.Text, "preview_url": false}
		return base
	}
}

// metaMessageTag — the HUMAN_AGENT tag on Messenger/IG extends the 24h
// window to 7 days; empty for everything else (field must be omitted).
func metaMessageTag(req *SendRequest) string {
	if req.Tag != "" && (req.Platform == "meta" || req.Platform == "instagram") {
		return req.Tag
	}
	return ""
}

// ============================================
// Telegram
// ============================================

// TelegramClient talks to the Bot API.
type TelegramClient struct{ BotToken string }

func NewTelegramClient(botToken string) *TelegramClient { return &TelegramClient{BotToken: botToken} }

func (t *TelegramClient) call(ctx context.Context, method string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramBase+"/bot"+t.BotToken+"/"+method, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		// The bot token rides the URL path (Bot API design), so the raw
		// *url.Error would carry it into logs/alerts — keep only the cause.
		return nil, fmt.Errorf("telegram %s: %w", method, unwrapURLErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if resp.StatusCode >= 400 {
		desc, _ := v["description"].(string)
		return nil, fmt.Errorf("telegram %s failed (%d): %s", method, resp.StatusCode, desc)
	}
	return v, nil
}

// SendMessage sends a text message (optionally with inline keyboard).
func (t *TelegramClient) SendMessage(ctx context.Context, chatID, text string, buttons [][2]string) (string, error) {
	return t.SendMessageFormatted(ctx, chatID, text, "", buttons, 0)
}

// SendMessageFormatted sends text with an optional parse_mode ("HTML"). Callers
// that pass a markup mode must pre-convert the body: Telegram renders it and
// rejects the whole message when it cannot parse the entities.
func (t *TelegramClient) SendMessageFormatted(ctx context.Context, chatID, text, parseMode string, buttons [][2]string, replyTo int64) (string, error) {
	body := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		body["parse_mode"] = parseMode
	}
	if rp := telegramReplyParameters(replyTo); rp != nil {
		body["reply_parameters"] = rp
	}
	if len(buttons) > 0 {
		var rows [][]map[string]any
		for _, b := range buttons {
			rows = append(rows, []map[string]any{{"text": b[0], "callback_data": b[1]}})
		}
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	v, err := t.call(ctx, "sendMessage", body)
	if err != nil {
		return "", err
	}
	if result, ok := v["result"].(map[string]any); ok {
		if mid, ok := result["message_id"].(float64); ok {
			return fmt.Sprintf("%.0f", mid), nil
		}
	}
	return "", nil
}

// SendPhoto delivers a picture by URL (Telegram fetches it itself, so it must be
// an HTTPS address the provider can reach — an R2 public or presigned URL is),
// optionally with a caption and an inline keyboard.
func (t *TelegramClient) SendPhoto(ctx context.Context, chatID, photoURL, caption string, buttons [][2]string) (string, error) {
	return t.SendPhotoFormatted(ctx, chatID, photoURL, caption, "", buttons, 0)
}

// SendPhotoFormatted is SendPhoto with an optional parse_mode for the caption.
func (t *TelegramClient) SendPhotoFormatted(ctx context.Context, chatID, photoURL, caption, parseMode string, buttons [][2]string, replyTo int64) (string, error) {
	body := map[string]any{"chat_id": chatID, "photo": photoURL}
	if caption != "" {
		body["caption"] = caption
	}
	if parseMode != "" {
		body["parse_mode"] = parseMode
	}
	if rp := telegramReplyParameters(replyTo); rp != nil {
		body["reply_parameters"] = rp
	}
	if len(buttons) > 0 {
		var rows [][]map[string]any
		for _, b := range buttons {
			rows = append(rows, []map[string]any{{"text": b[0], "callback_data": b[1]}})
		}
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	v, err := t.call(ctx, "sendPhoto", body)
	if err != nil {
		return "", err
	}
	if result, ok := v["result"].(map[string]any); ok {
		if mid, ok := result["message_id"].(float64); ok {
			return fmt.Sprintf("%.0f", mid), nil
		}
	}
	return "", nil
}

// SendVideo delivers a video by URL, with the same caption rules as photos.
func (t *TelegramClient) SendVideo(ctx context.Context, chatID, videoURL, caption string, buttons [][2]string) (string, error) {
	return t.SendVideoFormatted(ctx, chatID, videoURL, caption, "", buttons, 0)
}

func (t *TelegramClient) SendVideoFormatted(ctx context.Context, chatID, videoURL, caption, parseMode string, buttons [][2]string, replyTo int64) (string, error) {
	body := map[string]any{"chat_id": chatID, "video": videoURL}
	if caption != "" {
		body["caption"] = caption
	}
	if parseMode != "" {
		body["parse_mode"] = parseMode
	}
	if rp := telegramReplyParameters(replyTo); rp != nil {
		body["reply_parameters"] = rp
	}
	if len(buttons) > 0 {
		var rows [][]map[string]any
		for _, b := range buttons {
			rows = append(rows, []map[string]any{{"text": b[0], "callback_data": b[1]}})
		}
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	v, err := t.call(ctx, "sendVideo", body)
	if err != nil {
		return "", err
	}
	if result, ok := v["result"].(map[string]any); ok {
		if mid, ok := result["message_id"].(float64); ok {
			return fmt.Sprintf("%.0f", mid), nil
		}
	}
	return "", nil
}

// SendDocument delivers a file by URL (a price list PDF, say). Telegram fetches
// the URL itself, so it must be reachable from the internet.
func (t *TelegramClient) SendDocument(ctx context.Context, chatID, documentURL, caption string, buttons [][2]string) (string, error) {
	return t.SendDocumentFormatted(ctx, chatID, documentURL, caption, "", buttons, 0)
}

func (t *TelegramClient) SendDocumentFormatted(ctx context.Context, chatID, documentURL, caption, parseMode string, buttons [][2]string, replyTo int64) (string, error) {
	body := map[string]any{"chat_id": chatID, "document": documentURL}
	if caption != "" {
		body["caption"] = caption
	}
	if parseMode != "" {
		body["parse_mode"] = parseMode
	}
	if rp := telegramReplyParameters(replyTo); rp != nil {
		body["reply_parameters"] = rp
	}
	if len(buttons) > 0 {
		var rows [][]map[string]any
		for _, b := range buttons {
			rows = append(rows, []map[string]any{{"text": b[0], "callback_data": b[1]}})
		}
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	v, err := t.call(ctx, "sendDocument", body)
	if err != nil {
		return "", err
	}
	if result, ok := v["result"].(map[string]any); ok {
		if mid, ok := result["message_id"].(float64); ok {
			return fmt.Sprintf("%.0f", mid), nil
		}
	}
	return "", nil
}

// telegramReplyParameters is the Bot API 7 reply block. allow_sending_without_reply
// keeps a stale quote from rejecting the whole message (the customer may have
// deleted the message being answered).
func telegramReplyParameters(replyTo int64) map[string]any {
	if replyTo <= 0 {
		return nil
	}
	return map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
}

// DownloadFile resolves file_id → path and fetches the bytes.
func (t *TelegramClient) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	v, err := t.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return nil, "", err
	}
	result, _ := v["result"].(map[string]any)
	filePath, _ := result["file_path"].(string)
	if filePath == "" {
		return nil, "", fmt.Errorf("telegram file path missing")
	}
	u := telegramBase + "/file/bot" + t.BotToken + "/" + filePath
	return downloadMediaBytes(ctx, u)
}

// SendChatAction shows the transient "bot is typing…" status (no message id).
func (t *TelegramClient) SendChatAction(ctx context.Context, chatID, action string) error {
	_, err := t.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action})
	return err
}

// SendAudio delivers a playable audio message from a URL. Returns message id.
func (t *TelegramClient) SendAudio(ctx context.Context, chatID, audioURL, caption string) (string, error) {
	body := map[string]any{"chat_id": chatID, "audio": audioURL}
	if caption != "" {
		body["caption"] = caption
	}
	v, err := t.call(ctx, "sendAudio", body)
	if err != nil {
		return "", err
	}
	if result, ok := v["result"].(map[string]any); ok {
		if mid, ok := result["message_id"].(float64); ok {
			return fmt.Sprintf("%.0f", mid), nil
		}
	}
	return "", nil
}

// AnswerCallbackQuery closes an inline-keyboard press (feedback buttons).
func (t *TelegramClient) AnswerCallback(ctx context.Context, callbackID, text string) error {
	_, err := t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text})
	return err
}

// EditMessageReplyMarkup removes the inline keyboard from a sent message
// (used after a notify-bot button action so it cannot fire twice, and to
// demote the previous AI reply's 👍/👎 so only the newest one stays rateable).
// The keyboard must ride inside `reply_markup`: passing inline_keyboard at the
// top level is silently ignored by Telegram, which made this a no-op.
func (t *TelegramClient) EditMessageReplyMarkup(ctx context.Context, chatID string, messageID int64) error {
	_, err := t.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID, "message_id": messageID,
		"reply_markup": map[string]any{"inline_keyboard": []any{}},
	})
	return err
}

// GetProfile returns (displayName, photoFileID) via getChat. photoFileID is
// "" when the chat has no avatar. It must be mirrored into R2 before use —
// Telegram file URLs embed the bot token and must never reach a browser.
func (t *TelegramClient) GetProfile(ctx context.Context, chatID string) (string, string, error) {
	v, err := t.call(ctx, "getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return "", "", err
	}
	result, _ := v["result"].(map[string]any)
	first, _ := result["first_name"].(string)
	username, _ := result["username"].(string)
	name := first
	if name == "" {
		name = username
	}
	photo := ""
	if ph, ok := result["photo"].(map[string]any); ok {
		photo, _ = ph["big_file_id"].(string)
		if photo == "" {
			photo, _ = ph["small_file_id"].(string)
		}
	}
	return name, photo, nil
}

// GetMe validates the bot token and returns (bot_id, username, first_name).
func (t *TelegramClient) GetMe(ctx context.Context) (int64, string, string, error) {
	v, err := t.call(ctx, "getMe", map[string]any{})
	if err != nil {
		return 0, "", "", err
	}
	result, _ := v["result"].(map[string]any)
	id, _ := result["id"].(float64)
	username, _ := result["username"].(string)
	firstName, _ := result["first_name"].(string)
	return int64(id), username, firstName, nil
}

// GetUpdates was removed with the bring-your-own-bot flow (056). Every bot the
// system now talks to is webhook-driven, and Telegram rejects getUpdates while
// a webhook is registered — keeping the method around would only invite the
// same mistake back.

// GetWebhookInfo returns the currently registered webhook URL.
func (t *TelegramClient) GetWebhookInfo(ctx context.Context) (string, error) {
	v, err := t.call(ctx, "getWebhookInfo", map[string]any{})
	if err != nil {
		return "", err
	}
	result, _ := v["result"].(map[string]any)
	url, _ := result["url"].(string)
	return url, nil
}

// SetWebhook registers the provider callback with a secret token.
func (t *TelegramClient) SetWebhook(ctx context.Context, url, secret string) error {
	body := map[string]any{"url": url}
	if secret != "" {
		body["secret_token"] = secret
	}
	if _, err := t.call(ctx, "setWebhook", body); err != nil {
		return err
	}
	return nil
}

// DeleteWebhook removes the registered webhook.
func (t *TelegramClient) DeleteWebhook(ctx context.Context) error {
	_, err := t.call(ctx, "deleteWebhook", map[string]any{})
	return err
}

// TelegramProviderMessageID builds the receipt id "chatID:messageID".
func TelegramProviderMessageID(chatID, messageID string) string { return chatID + ":" + messageID }

// ============================================
// LINE
// ============================================

// LineClient talks to the LINE Messaging API.
type LineClient struct{ ChannelAccessToken string }

func NewLineClient(token string) *LineClient { return &LineClient{ChannelAccessToken: token} }

func (l *LineClient) get(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lineBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+l.ChannelAccessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("line get %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("line %s failed (%d)", path, resp.StatusCode)
	}
	return v, nil
}

// PushText sends a text message. Returns the request id.
func (l *LineClient) PushText(ctx context.Context, to, text string) (string, error) {
	body := map[string]any{"to": to, "messages": []map[string]any{{"type": "text", "text": text}}}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, lineBase+"/v2/bot/message/push", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+l.ChannelAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("line push: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("line push failed (%d)", resp.StatusCode)
	}
	return resp.Header.Get("X-Line-Request-Id"), nil
}

// SendTypingIndicator shows the "typing…" status in a LINE chat (expires
// after ~10 seconds — exactly what the AI compose window needs).
func (l *LineClient) SendTypingIndicator(ctx context.Context, chatID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, lineBase+"/v2/bot/chat/"+url.PathEscape(chatID)+"/typing", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+l.ChannelAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("line typing: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("line typing failed (%d)", resp.StatusCode)
	}
	return nil
}

// GetProfile returns (displayName, pictureURL).
func (l *LineClient) GetProfile(ctx context.Context, userID string) (string, string, error) {
	v, err := l.get(ctx, "/v2/bot/profile/"+url.PathEscape(userID))
	if err != nil {
		return "", "", err
	}
	name, _ := v["displayName"].(string)
	pic, _ := v["pictureUrl"].(string)
	return name, pic, nil
}

// GetBotInfo returns (displayName, basicId, userId) for the bot.
func (l *LineClient) GetBotInfo(ctx context.Context) (string, string, string, error) {
	v, err := l.get(ctx, "/v2/bot/info")
	if err != nil {
		return "", "", "", err
	}
	name, _ := v["displayName"].(string)
	basic, _ := v["basicId"].(string)
	uid, _ := v["userId"].(string)
	return name, basic, uid, nil
}

// DownloadContent fetches a message's media bytes (LINE → audio/mpeg).
func (l *LineClient) DownloadContent(ctx context.Context, messageID string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lineBase+"/v2/bot/message/"+url.PathEscape(messageID)+"/content", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+l.ChannelAccessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("line content: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("line content failed (%d)", resp.StatusCode)
	}
	mime := resp.Header.Get("Content-Type")
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadCap+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxDownloadCap {
		return nil, "", fmt.Errorf("media exceeds size limit")
	}
	return data, mime, nil
}

// VerifySignature — LINE X-Line-Signature HMAC-SHA256 base64 check.
func LineVerifySignature(secret, signature string, body []byte) bool {
	// An empty configured secret cannot authenticate anyone — computing an
	// HMAC with an empty key would let anyone forge a matching signature.
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// WebhookAPIError marks a failure of the webhook-management API (2xx status
// but LINE reported an error inside the body).
type WebhookAPIError struct{ Msg string }

func (l *LineClient) webhookCall(ctx context.Context, method, path string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, lineBase+path, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+l.ChannelAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("line webhook api: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if resp.StatusCode >= 400 {
		msg, _ := v["message"].(string)
		return nil, fmt.Errorf("line webhook api failed (%d): %s", resp.StatusCode, msg)
	}
	return v, nil
}

// SetWebhookEndpoint registers the Messaging API webhook URL programmatically
// (saves the merchant a trip to the LINE Developers console).
func (l *LineClient) SetWebhookEndpoint(ctx context.Context, endpoint string) error {
	if _, err := l.webhookCall(ctx, http.MethodPut, "/v2/bot/channel/webhook/endpoint", map[string]any{"endpoint": endpoint}); err != nil {
		return err
	}
	return nil
}

// TestWebhookEndpoint asks LINE to fire a test event at the webhook and
// returns (success, detailMessage).
func (l *LineClient) TestWebhookEndpoint(ctx context.Context) (bool, string, error) {
	v, err := l.webhookCall(ctx, http.MethodPost, "/v2/bot/channel/webhook/test", map[string]any{})
	if err != nil {
		return false, "", err
	}
	success, _ := v["success"].(bool)
	msg, _ := v["message"].(string)
	return success, msg, nil
}

// ============================================
// Zalo (Vietnam — Zalo Official Account)
// ============================================

// ZaloClient talks to the Zalo OA OpenAPI (customer-care messaging).
type ZaloClient struct{ AccessToken string }

func NewZaloClient(accessToken string) *ZaloClient { return &ZaloClient{AccessToken: accessToken} }

// do executes one Zalo API call. Zalo reports application-level failures
// inside HTTP 200 bodies as {"error": <code>, "message": "..."} — both shapes
// are surfaced as errors here.
func (z *ZaloClient) do(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var rdr io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = strings.NewReader(string(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, zaloBase+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("access_token", z.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zalo %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if resp.StatusCode >= 400 {
		msg, _ := v["message"].(string)
		return nil, fmt.Errorf("zalo %s failed (%d): %s", path, resp.StatusCode, msg)
	}
	if code, ok := v["error"].(float64); ok && code != 0 {
		msg, _ := v["message"].(string)
		return nil, fmt.Errorf("zalo %s error %d: %s", path, int(code), msg)
	}
	return v, nil
}

// SendText sends a customer-care text message (v3.0 CS API). Returns msg_id.
func (z *ZaloClient) SendText(ctx context.Context, userID, text string) (string, error) {
	v, err := z.do(ctx, http.MethodPost, "/v3.0/oa/message/cs", map[string]any{
		"recipient": map[string]any{"user_id": userID},
		"message":   map[string]any{"text": text},
	})
	if err != nil {
		return "", err
	}
	if data, ok := v["data"].(map[string]any); ok {
		if id, ok := data["msg_id"].(string); ok {
			return id, nil
		}
	}
	return "", nil
}

// SendImage sends an image attachment from a public URL (v3.0 CS API).
func (z *ZaloClient) SendImage(ctx context.Context, userID, imageURL string) (string, error) {
	v, err := z.do(ctx, http.MethodPost, "/v3.0/oa/message/cs", map[string]any{
		"recipient": map[string]any{"user_id": userID},
		"message":   map[string]any{"type": "image", "payload": map[string]any{"url": imageURL}},
	})
	if err != nil {
		return "", err
	}
	if data, ok := v["data"].(map[string]any); ok {
		if id, ok := data["msg_id"].(string); ok {
			return id, nil
		}
	}
	return "", nil
}

// GetProfile returns (displayName, avatarURL) for a Zalo user id.
func (z *ZaloClient) GetProfile(ctx context.Context, userID string) (string, string, error) {
	data, _ := json.Marshal(map[string]any{"user_id": userID})
	v, err := z.do(ctx, http.MethodGet, "/v2.0/oa/getprofile?data="+url.QueryEscape(string(data)), nil)
	if err != nil {
		return "", "", err
	}
	inner, _ := v["data"].(map[string]any)
	name, _ := inner["display_name"].(string)
	avatar := ""
	if avatars, ok := inner["avatars"].(map[string]any); ok {
		avatar, _ = avatars["large"].(string)
		if avatar == "" {
			avatar, _ = avatars["medium"].(string)
		}
	}
	return name, avatar, nil
}

// VerifyOA validates the OA access token and returns the OA display name.
// VerifyOA returns the Official Account's display name and its oa_id. The id
// is the routing identity Zalo stamps on every webhook, so the caller stores
// it to route inbound events to the right tenant.
func (z *ZaloClient) VerifyOA(ctx context.Context) (name, oaID string, err error) {
	v, err := z.do(ctx, http.MethodGet, "/v2.0/oa/getoa", nil)
	if err != nil {
		return "", "", err
	}
	inner, _ := v["data"].(map[string]any)
	name, _ = inner["name"].(string)
	// Zalo returns oa_id as a string, but a numeric-typed JSON field would
	// decode as float64.
	switch id := inner["oa_id"].(type) {
	case string:
		oaID = id
	case float64:
		oaID = strconv.FormatInt(int64(id), 10)
	}
	return name, oaID, nil
}

// ZaloVerifySignature — X-ZEvent-Signature HMAC-SHA256 hex check. An empty
// configured secret cannot authenticate anyone, so it rejects (fail-closed).
func ZaloVerifySignature(secret, signature string, body []byte) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
