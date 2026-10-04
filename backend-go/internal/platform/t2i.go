// Text-to-image for long replies.
//
// Chat channels flatten Markdown and cap message length, so a long AI answer is
// the worst case for both: the layout is lost and the answer arrives as several
// chunks. Rendering it to one image keeps the layout, and on a channel that
// counts MESSAGES rather than characters (WeChat customer service allows five per
// 48h) it spends exactly one message instead of several.
//
// Borrowed from AstrBot's T2I subsystem (astrbot/core/utils/t2i, wired in
// pipeline/result_decorate/stage.py: a word threshold gates the render, the
// `remote` strategy posts to a configured endpoint, and any single result can opt
// out with use_t2i(False)).
//
// Only the REMOTE half is implemented here, on purpose. AstrBot renders locally
// with HTML templates and a bundled 2.4MB shiki runtime; this platform serves
// Khmer, Chinese and English with no browser and no font stack in the image, and
// a pure-Go bitmap font covers none of those scripts. So local rendering would
// produce boxes for the only languages that matter here. With T2I_ENDPOINT unset
// the feature is off and replies go out as text exactly as before.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/textutil"
)

// T2IThresholdDefault is the minimum reply length that is worth an image,
// mirroring AstrBot's t2i_word_threshold default. It counts characters, not
// words: Khmer and Chinese do not delimit words with spaces.
const T2IThresholdDefault = 150

// t2iMaxImageBytes caps what a render endpoint may hand back. A hostile or
// misconfigured endpoint must not be able to fill memory (or the object store)
// through one reply.
const t2iMaxImageBytes = 8 * 1024 * 1024

// t2iUploadURLTTL is the lifetime of the URL handed to the provider. Providers
// fetch media within seconds of the send; a day is generous and keeps a leaked
// URL from living forever.
const t2iUploadURLTTL = 24 * time.Hour

// T2IConfig is the whole decision surface for rendering a reply as an image.
type T2IConfig struct {
	Enabled   bool
	Threshold int
	Endpoint  string
	Token     string
	Timeout   time.Duration
}

// T2IConfigFromEnv reads the configuration. Setting T2I_ENDPOINT is enough to
// turn the feature on; T2I_ENABLED=false is the explicit off switch. Read per
// call so a test (or a .env edit plus restart) takes effect without cached state.
func T2IConfigFromEnv() T2IConfig {
	endpoint := strings.TrimSpace(os.Getenv("T2I_ENDPOINT"))
	cfg := T2IConfig{
		Enabled:   endpoint != "",
		Threshold: T2IThresholdDefault,
		Endpoint:  endpoint,
		Token:     strings.TrimSpace(os.Getenv("T2I_TOKEN")),
		Timeout:   15 * time.Second,
	}
	if v := strings.TrimSpace(os.Getenv("T2I_ENABLED")); v != "" {
		cfg.Enabled = isTruthy(v) && endpoint != ""
	}
	if ms, err := strconv.Atoi(strings.TrimSpace(os.Getenv("T2I_WORD_THRESHOLD"))); err == nil && ms > 0 {
		cfg.Threshold = ms
	}
	if ms, err := strconv.Atoi(strings.TrimSpace(os.Getenv("T2I_TIMEOUT_MS"))); err == nil && ms > 0 {
		cfg.Timeout = time.Duration(ms) * time.Millisecond
	}
	return cfg
}

// isTruthy accepts the spellings an operator is likely to write in .env-go.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ShouldRender decides whether one reply becomes an image.
//
// Three things must hold: the feature is configured, the CHANNEL can carry an
// image, and the reply is long enough. The channel check is what keeps this from
// firing on Telegram (no image send path) or the website widget (which renders
// Markdown itself, so an image would be a downgrade).
func (c T2IConfig) ShouldRender(text string, caps Capabilities) bool {
	if !c.Enabled || c.Endpoint == "" {
		return false
	}
	if !caps.Media.Supports(MediaImage) || !caps.FlattensMarkdown {
		return false
	}
	return len([]rune(text)) >= c.Threshold
}

// T2IRenderer turns text into image bytes. An interface so the pipeline can be
// tested without a rendering service.
type T2IRenderer interface {
	Render(ctx context.Context, text string) (data []byte, contentType string, err error)
}

// MediaUploader is the slice of the object store T2I needs, so a render can be
// tested without a bucket. *storager2.Client satisfies it.
type MediaUploader interface {
	PutObject(ctx context.Context, key string, data []byte, contentType string) error
	PublicOrPresigned(key string, ttl time.Duration) string
}

// HTTPRenderer posts the reply to a rendering service.
//
// The contract is deliberately tiny — POST {"text": "..."} and answer with image
// bytes — because the service is deployment-specific (AstrBot's own remote
// strategy is the same shape). The Content-Type decides the stored object type;
// image/png is assumed when the service does not say.
type HTTPRenderer struct {
	Endpoint string
	Token    string
	Timeout  time.Duration
	// HTTP is a seam for tests; nil builds a client with Timeout.
	HTTP *http.Client
}

func (r *HTTPRenderer) Render(ctx context.Context, text string) ([]byte, string, error) {
	if strings.TrimSpace(r.Endpoint) == "" {
		return nil, "", errors.New("t2i: no render endpoint configured")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]any{"text": text, "format": "png"})
	if err != nil {
		return nil, "", fmt.Errorf("t2i: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("t2i: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}

	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("t2i: render request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, t2iMaxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("t2i: read render response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("t2i: render endpoint returned HTTP %d: %s", resp.StatusCode, textutil.TruncateRunes(string(body), 200))
	}
	if len(body) == 0 {
		return nil, "", errors.New("t2i: render endpoint returned no image")
	}
	if len(body) > t2iMaxImageBytes {
		return nil, "", fmt.Errorf("t2i: rendered image exceeds %d bytes", t2iMaxImageBytes)
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "image/png"
	}
	return body, contentType, nil
}

// RenderReplyToImage renders one reply and stores it, returning a URL the channel
// can fetch. Every failure is returned, never swallowed here: the caller decides
// whether to fall back to text (it does — an image is an optimisation, never a
// reason to drop an answer).
func RenderReplyToImage(ctx context.Context, cfg T2IConfig, renderer T2IRenderer, up MediaUploader, key, text string) (string, error) {
	if renderer == nil {
		renderer = &HTTPRenderer{Endpoint: cfg.Endpoint, Token: cfg.Token, Timeout: cfg.Timeout}
	}
	if up == nil {
		return "", errors.New("t2i: no media uploader")
	}
	data, contentType, err := renderer.Render(ctx, text)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("t2i: renderer returned no bytes")
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "image/png"
	}
	if err := up.PutObject(ctx, key, data, contentType); err != nil {
		return "", fmt.Errorf("t2i: upload rendered image: %w", err)
	}
	return up.PublicOrPresigned(key, t2iUploadURLTTL), nil
}

// t2iRenderer returns the renderer to use: an injected one (tests), else the HTTP
// renderer built from the environment, else nil when the feature is off.
func (p *Pipeline) t2iRenderer(cfg T2IConfig) T2IRenderer {
	if p.T2I != nil {
		return p.T2I
	}
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil
	}
	return &HTTPRenderer{Endpoint: cfg.Endpoint, Token: cfg.Token, Timeout: cfg.Timeout}
}

// uploader returns the object store used for rendered images.
func (p *Pipeline) uploader() MediaUploader {
	if p.mediaUploader != nil {
		return p.mediaUploader
	}
	if p.Media == nil {
		return nil
	}
	return p.Media
}

// maybeRenderReply turns a long plain-text reply into an image delivery when the
// feature is configured and the channel can carry one. It never fails the
// delivery: on any error the original content and payload come back unchanged, so
// a broken render service degrades to today's text behaviour.
func (p *Pipeline) maybeRenderReply(ctx context.Context, cfg *configCred, content string, payload map[string]any) (string, map[string]any) {
	// Only plain replies. A template, a voice note or an already-attached image
	// carries its own kind, and replacing it with a picture of its caption would
	// change what the customer receives.
	if kind, _ := payload["kind"].(string); kind != "" && kind != "text" {
		return content, payload
	}
	t2i := T2IConfigFromEnv()
	if !t2i.ShouldRender(content, CapabilitiesFor(cfg.Platform)) {
		return content, payload
	}
	renderer := p.t2iRenderer(t2i)
	up := p.uploader()
	if renderer == nil || up == nil {
		return content, payload
	}
	key := fmt.Sprintf("t2i/%d/%d.png", cfg.UserID, time.Now().UnixNano())
	url, err := RenderReplyToImage(ctx, t2i, renderer, up, key, content)
	if err != nil {
		p.Logger.Warn("text-to-image failed; sending text instead",
			"config_id", cfg.UserID, "platform", cfg.Platform, "error", err.Error())
		return content, payload
	}
	// The image carries the whole answer, so an empty caption avoids re-sending
	// the same text underneath it (and dodges the caption length caps).
	return "", map[string]any{"kind": "media", "media_type": "image", "media_url": url}
}
