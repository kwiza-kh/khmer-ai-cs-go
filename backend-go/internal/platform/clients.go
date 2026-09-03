// Package platform — provider clients (Meta/WhatsApp/Instagram Graph API,
// Telegram Bot API, LINE Messaging API). Thin HTTP wrappers; the durable
// queue lives in pipeline.go.
package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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
	if platform == "whatsapp" {
		path = fmt.Sprintf("/%s?fields=id,display_phone_number,verified_name", m.accountID(platform))
	} else {
		fields := "id,name"
		if platform == "instagram" {
			fields = "id,name,username"
		}
		path = "/me?fields=" + fields
	}
	v, err := m.get(ctx, path, nil)
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

func buildMetaMessageBody(req *SendRequest) map[string]any {
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

// GetProfile returns the chat first/last/username for display.
func (t *TelegramClient) GetProfile(ctx context.Context, chatID string) (string, string, error) {
	v, err := t.call(ctx, "getChat", map[string]any{"chat_id": chatID})
	if err != nil {
		return "", "", err
	}
	result, _ := v["result"].(map[string]any)
	first, _ := result["first_name"].(string)
	username, _ := result["username"].(string)
	return first, username, nil
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
