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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	metaGraphBase  = "https://graph.facebook.com"
	telegramBase   = "https://api.telegram.org"
	lineBase       = "https://api.line.me"
	zaloBase       = "https://openapi.zalo.me"
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
	Platform             string
	RecipientID          string
	Text                 string
	Kind                 string // text | media | buttons | template
	MediaURL             string
	MediaType            string
	Buttons              [][2]string
	TemplateName         string
	TemplateLanguage     string
	TemplateBodyParams   []string
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

func (m *MetaClient) get(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	u := m.base() + path
	q := url.Values{}
	q.Set("access_token", m.AccessToken)
	for k, vs := range params {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	return m.do(req, "meta get "+path)
}

func (m *MetaClient) post(ctx context.Context, path string, body any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u := m.base() + path + "?access_token=" + url.QueryEscape(m.AccessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return m.do(req, "meta post "+path)
}

func (m *MetaClient) do(req *http.Request, what string) (map[string]any, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
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
	if platform == "whatsapp" {
		path = "/" + m.accountID(platform)
		params.Set("fields", "id,display_phone_number,verified_name")
	} else if platform == "instagram" && m.InstagramBusiness != "" {
		// Verify the Instagram professional account itself — /me with a Page
		// token resolves to the Page, which proves nothing about IG access.
		path = "/" + m.InstagramBusiness
		params.Set("fields", "id,name,username")
	} else {
		fields := "id,name"
		if platform == "instagram" {
			fields = "id,name,username"
		}
		path = "/me"
		params.Set("fields", fields)
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
	data, dlMime, err := downloadBytes(ctx, mediaURL)
	if mime == "" {
		mime = dlMime
	}
	return data, mime, err
}

func downloadBytes(ctx context.Context, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("media download: %w", err)
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
func (m *MetaClient) SendMessage(ctx context.Context, req *SendRequest) (string, error) {
	account := m.accountID(req.Platform)
	body := buildMetaMessageBody(req)
	v, err := m.post(ctx, "/"+account+"/messages", body)
	if err != nil {
		return "", err
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

func buildMetaMessageBody(req *SendRequest) map[string]any {
	body := buildMetaMessageBodyInner(req)
	if tag := metaMessageTag(req); tag != "" {
		body["tag"] = tag
	}
	return body
}

func buildMetaMessageBodyInner(req *SendRequest) map[string]any {
	switch req.Kind {
	case "media":
		mediaType := req.MediaType
		if mediaType == "" {
			mediaType = "image"
		}
		media := map[string]any{"link": req.MediaURL}
		if req.Text != "" && mediaType != "audio" {
			media["caption"] = req.Text
		}
		return map[string]any{"recipient": map[string]any{"id": req.RecipientID}, "type": mediaType, mediaType: media}
	case "buttons":
		var elems []map[string]any
		for _, b := range req.Buttons {
			elems = append(elems, map[string]any{"content_type": "text", "title": b[0], "payload": b[1]})
		}
		return map[string]any{
			"recipient": map[string]any{"id": req.RecipientID},
			"message":   map[string]any{"text": req.Text, "quick_replies": elems},
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
					"code": firstNonEmpty(req.TemplateLanguage, "en"),
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

// metaMessageTag — the HUMAN_AGENT tag on Messenger/IG extends the 24h
// window to 7 days; empty for everything else (field must be omitted).
func metaMessageTag(req *SendRequest) string {
	if req.Tag != "" && (req.Platform == "meta" || req.Platform == "instagram") {
		return req.Tag
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
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
		return nil, fmt.Errorf("telegram %s: %w", method, err)
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
	body := map[string]any{"chat_id": chatID, "text": text}
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
	return downloadBytes(ctx, u)
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
// (used after a notify-bot button action so it cannot fire twice).
func (t *TelegramClient) EditMessageReplyMarkup(ctx context.Context, chatID string, messageID int64) error {
	_, err := t.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID, "message_id": messageID, "inline_keyboard": []any{},
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

// GetUpdates — fetch recent bot chats (used by the notify-setup flow so the
// owner can pick the chat to receive notifications in).
func (t *TelegramClient) GetUpdates(ctx context.Context, offset int64) ([]map[string]any, error) {
	v, err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 0, "limit": 20})
	if err != nil {
		return nil, err
	}
	updates, _ := v["result"].([]any)
	out := make([]map[string]any, 0, len(updates))
	for _, u := range updates {
		if m, ok := u.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

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
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// WebhookAPIError marks a failure of the webhook-management API (2xx status
// but LINE reported an error inside the body).
type WebhookAPIError struct{ Msg string }

func (e *WebhookAPIError) Error() string { return e.Msg }

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

// GetWebhookEndpointInfo returns the currently configured webhook URL.
func (l *LineClient) GetWebhookEndpointInfo(ctx context.Context) (string, error) {
	v, err := l.webhookCall(ctx, http.MethodGet, "/v2/bot/channel/webhook/info", map[string]any{})
	if err != nil {
		return "", err
	}
	u, _ := v["endpoint"].(string)
	return u, nil
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
func (z *ZaloClient) VerifyOA(ctx context.Context) (string, error) {
	v, err := z.do(ctx, http.MethodGet, "/v2.0/oa/getoa", nil)
	if err != nil {
		return "", err
	}
	inner, _ := v["data"].(map[string]any)
	name, _ := inner["name"].(string)
	return name, nil
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
