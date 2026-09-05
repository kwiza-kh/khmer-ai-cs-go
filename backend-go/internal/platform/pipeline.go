// Package platform — durable inbound/outbound pipeline (port of work.rs).
// Webhooks persist inbound events; workers claim them, run the AI pipeline,
// and enqueue outbound deliveries.
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/redisstore"
	"khmer-ai-cs-go/internal/security"
	"khmer-ai-cs-go/internal/storager2"
	"khmer-ai-cs-go/internal/usage"
)

const (
	maxAttempts       = 5
	workerCount       = 4
	staleLockMinutes  = 2
	inboundRateLimit  = 15
)

// Pipeline bundles pipeline dependencies.
type Pipeline struct {
	DB     *pgxpool.Pool
	Redis  *redisstore.Client
	Cfg    *config.Config
	Gemini *gemini.Service
	RAG    *rag.Service
	Sealer *security.Sealer
	Media  *storager2.Client
	Logger *slog.Logger

	notify         chan struct{}
	notifyOutbound chan struct{}
	once           sync.Once
}

// InboundEvent is one queued customer message.
type InboundEvent struct {
	EventID         int64
	ConfigID        int32
	Platform        string
	PlatformUserID  string
	UserDisplayName string
	Content         string
	Media           map[string]any
}

// configCred is a decrypted platform config row.
type configCred struct {
	UserID            int32
	Platform          string
	AccessToken       string
	PageID            string
	InstagramBusiness string
	BotToken          string
}

// EnqueueInboundEvent persists one inbound message (idempotent, rate-limited).
func (p *Pipeline) EnqueueInboundEvent(ctx context.Context, configID int32, platform, externalID, platformUserID, displayName, content string, media map[string]any) error {
	if externalID == "" || platformUserID == "" || content == "" {
		return nil
	}
	// Per-customer inbound rate limit (anti-spam); fail-open.
	if ok, err := p.Redis.CheckRateLimit(ctx, fmt.Sprintf("inbound:%d:%s", configID, platformUserID), inboundRateLimit); err == nil && !ok {
		p.Logger.Warn("inbound rate limited; dropping message", "config_id", configID)
		return nil
	}
	now := time.Now()
	mediaJSON, _ := json.Marshal(media)
	var mediaParam any
	if media == nil {
		mediaParam = nil
	} else {
		mediaParam = mediaJSON
	}
	tag, err := p.DB.Exec(ctx,
		"INSERT INTO platform_inbound_events "+
			"(config_id, external_id, platform, platform_user_id, user_display_name, content, media_json, status, next_attempt_at, created_at) "+
			"VALUES ($1,$2,$3::platform_type,$4,$5,$6,$7,'pending',$8,$9) "+
			"ON CONFLICT (config_id, external_id) DO NOTHING",
		configID, externalID, platform, platformUserID, displayName, content, mediaParam, now, now)
	if err != nil {
		return fmt.Errorf("enqueue inbound: %w", err)
	}
	if tag.RowsAffected() > 0 {
		p.signal()
	}
	return nil
}

func (p *Pipeline) initNotify() {
	p.once.Do(func() {
		p.notify = make(chan struct{}, 64)
		p.notifyOutbound = make(chan struct{}, 64)
	})
}

func (p *Pipeline) signal() {
	p.initNotify()
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// SignalOutbound wakes the outbound workers immediately (a new delivery was
// enqueued — agent reply, campaign template, etc.). Without it deliveries sit
// in the outbox until the next 1s poll tick.
func (p *Pipeline) SignalOutbound() {
	p.initNotify()
	select {
	case p.notifyOutbound <- struct{}{}:
	default:
	}
}

// SpawnWorkers starts the inbound + outbound worker pools.
func (p *Pipeline) SpawnWorkers(ctx context.Context) {
	p.initNotify()
	for i := 0; i < workerCount; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-p.notify:
				case <-time.After(2 * time.Second):
				}
				p.processInboundNext(ctx)
			}
		}()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-p.notifyOutbound:
				case <-time.After(1 * time.Second):
				}
				p.processOutboundNext(ctx)
			}
		}()
	}
}

// ============================================
// Inbound
// ============================================

// processInboundNext drains every claimable inbound event (the wake signal
// only has to be approximate; the loop empties the queue in one pass).
func (p *Pipeline) processInboundNext(ctx context.Context) {
	for {
		ev, err := p.claimInbound(ctx)
		if err != nil || ev == nil {
			return
		}
		if err := p.processInboundEvent(ctx, ev); err != nil {
			p.Logger.Warn("inbound event failed", "event_id", ev.EventID, "error", err.Error())
			p.retryInbound(ctx, ev.EventID)
		} else {
			_, _ = p.DB.Exec(ctx, "UPDATE platform_inbound_events SET status='completed', processed_at=$1, locked_at=NULL WHERE event_id=$2", time.Now(), ev.EventID)
		}
	}
}

func (p *Pipeline) claimInbound(ctx context.Context) (*InboundEvent, error) {
	now := time.Now()
	stale := now.Add(-staleLockMinutes * time.Minute)
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ev InboundEvent
	var mediaJSON []byte
	err = tx.QueryRow(ctx,
		"UPDATE platform_inbound_events SET status='processing', attempts = attempts + 1, locked_at = $1 "+
			"WHERE event_id = (SELECT event_id FROM platform_inbound_events "+
			"WHERE ((status = 'pending' AND next_attempt_at <= $2) OR (status = 'processing' AND locked_at < $3)) "+
			"AND NOT EXISTS (SELECT 1 FROM platform_inbound_events earlier WHERE earlier.config_id = platform_inbound_events.config_id "+
			"AND earlier.platform_user_id = platform_inbound_events.platform_user_id AND earlier.event_id < platform_inbound_events.event_id "+
			"AND earlier.status IN ('pending','processing')) "+
			"ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED) "+
			"RETURNING event_id, config_id, platform::text, platform_user_id, user_display_name, content, media_json",
		now, now, stale).Scan(&ev.EventID, &ev.ConfigID, &ev.Platform, &ev.PlatformUserID, &ev.UserDisplayName, &ev.Content, &mediaJSON)
	if err != nil {
		return nil, nil
	}
	if len(mediaJSON) > 0 {
		_ = json.Unmarshal(mediaJSON, &ev.Media)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ev, nil
}

func (p *Pipeline) retryInbound(ctx context.Context, eventID int64) {
	var attempts int32
	_ = p.DB.QueryRow(ctx, "SELECT attempts FROM platform_inbound_events WHERE event_id = $1", eventID).Scan(&attempts)
	if attempts >= maxAttempts {
		_, _ = p.DB.Exec(ctx, "UPDATE platform_inbound_events SET status='failed', locked_at=NULL WHERE event_id=$1", eventID)
		return
	}
	delay := retryDelay(int(attempts))
	_, _ = p.DB.Exec(ctx, "UPDATE platform_inbound_events SET status='pending', next_attempt_at=$1, locked_at=NULL WHERE event_id=$2", time.Now().Add(delay), eventID)
}

func retryDelay(attempts int) time.Duration {
	secs := []int{5, 15, 60, 300, 900}
	idx := attempts
	if idx < 0 {
		idx = 0
	}
	if idx >= len(secs) {
		idx = len(secs) - 1
	}
	return time.Duration(secs[idx]) * time.Second
}

// loadConfig fetches + decrypts the config for an event.
func (p *Pipeline) loadConfig(ctx context.Context, configID int32) (*configCred, error) {
	var accessEnc, botEnc *string
	var cfg configCred
	var pageID, igBusiness *string
	err := p.DB.QueryRow(ctx,
		"SELECT user_id, platform::text, access_token, page_id, instagram_business_id, bot_token "+
			"FROM platform_configs WHERE config_id = $1 AND is_active = true", configID).
		Scan(&cfg.UserID, &cfg.Platform, &accessEnc, &pageID, &igBusiness, &botEnc)
	if err != nil {
		return nil, fmt.Errorf("platform config not found")
	}
	cfg.PageID = deref(pageID)
	cfg.InstagramBusiness = deref(igBusiness)
	if accessEnc != nil {
		cfg.AccessToken, _ = p.Sealer.Decrypt(*accessEnc)
	}
	if botEnc != nil {
		cfg.BotToken, _ = p.Sealer.Decrypt(*botEnc)
	}
	return &cfg, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// processInboundEvent runs one customer message end-to-end.
func (p *Pipeline) processInboundEvent(ctx context.Context, ev *InboundEvent) error {
	cfg, err := p.loadConfig(ctx, ev.ConfigID)
	if err != nil {
		return err
	}
	content := ev.Content

	// Resolve display name + avatar (best-effort, never blocks).
	avatar := ""
	if ev.UserDisplayName == "" && (cfg.Platform == "meta" || cfg.Platform == "instagram") {
		name, av := p.fetchMessengerProfile(ctx, cfg, ev.PlatformUserID)
		ev.UserDisplayName = name
		avatar = av
	} else if ev.UserDisplayName == "" && cfg.Platform == "line" {
		if name, pic, err := NewLineClient(cfg.AccessToken).GetProfile(ctx, ev.PlatformUserID); err == nil {
			ev.UserDisplayName = name
			avatar = pic
		}
	} else if cfg.Platform == "telegram" {
		if name, photoID, perr := NewTelegramClient(cfg.BotToken).GetProfile(ctx, ev.PlatformUserID); perr == nil {
			if ev.UserDisplayName == "" {
				ev.UserDisplayName = name
			}
			if photoID != "" && p.customerAvatar(ctx, cfg, ev.PlatformUserID) == "" {
				avatar = p.storeTelegramAvatar(ctx, cfg, ev.PlatformUserID, photoID)
			}
		}
	}

	// Prepare media: voice → transcribe, images → describe (vision), store files.
	mediaURL := ""
	var platformMedia map[string]any
	if ev.Media != nil {
		content, mediaURL, platformMedia = p.prepareMedia(ctx, ev, cfg, content)
	}

	// Ensure session (create/resume) and persist the user message.
	sessionID, _, isNew, sessionStatus, err := p.ensureSession(ctx, ev, cfg, content, avatar, mediaURL, platformMedia)
	if err != nil {
		return err
	}

	// Customer replied on a resolved/closed session → reopen (always get a reply).
	if isNew && (sessionStatus == "resolved" || sessionStatus == "closed") {
		_, _ = p.DB.Exec(ctx, "UPDATE sessions SET status='active', resolved_at=NULL, closed_at=NULL WHERE session_id=$1", sessionID)
		sessionStatus = "active"
		p.Logger.Info("customer replied on a finished session; reopened", "session_id", sessionID)
	}

	// Escalation gate: non-active sessions get the canned ack, not an AI reply.
	if sessionStatus != "active" {
		p.enqueueHandoffAck(ctx, ev, cfg, sessionID)
		return nil
	}

	// Auto-handoff trigger 1 — the customer explicitly asked for a human. The
	// AI stays silent; the conversation lands in the handoff queue.
	if matched, ok := humanRequestKeyword(content); ok {
		p.Logger.Info("auto handoff: customer requested a human", "session_id", sessionID, "keyword", matched)
		p.escalateToHuman(ctx, ev, cfg, sessionID, "customer_request",
			"Customer asked for a human agent (matched: "+matched+")")
		return nil
	}

	// Typing indicator while the AI is composing (best-effort, per platform).
	p.sendTyping(ctx, cfg, ev.PlatformUserID)

	// AI reply (grounded in the knowledge base).
	ownerLang := p.ownerLanguage(ctx, cfg.UserID)
	replyLang := ownerLang
	if replyLang == "" {
		if det := gemini.DetectLanguage(content); det != "" {
			replyLang = det
		} else {
			replyLang = "km"
		}
	}

	history := p.loadHistory(ctx, sessionID)
	groundCtx := p.RAG.Ground(ctx, cfg.UserID, &sessionID, content, replyLang, history, 0)
	message := content
	if groundCtx.HasMatch {
		message = rag.AugmentMessage(content, &groundCtx)
	}
	result, err := p.Gemini.Chat(ctx, message, history, replyLang)
	if err != nil {
		return fmt.Errorf("AI 响应失败: %w", err)
	}
	usage.Record(ctx, p.DB, cfg.UserID, &sessionID, p.Gemini.ModelName(), result.PromptTokens, result.OutputTokens, result.CachedTokens)
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}

	// After-hours preamble.
	if !p.isOpenNow(ctx, cfg.UserID, cfg.Platform) {
		reply = "យើងកំពុងបិទសេវាកម្មនៅពេលនេះ។ ភ្នាក់ងារនឹងឆ្លើយតបនៅពេលម៉ោងធ្វើការ។\n\n" + reply
	}

	// Persist model reply + enqueue delivery (👍/👎 buttons ride on Telegram).
	var modelMessageID int64
	err = p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, tokens_used, model_name, used_mock, sources_json, created_at) "+
			"VALUES ($1,'model','text',$2,$3,$4,$5,$6,$7) RETURNING message_id",
		sessionID, reply, result.PromptTokens+result.OutputTokens, p.Gemini.ModelName(), result.UsedMock,
		sourcesJSON(groundCtx), time.Now()).
		Scan(&modelMessageID)
	if err != nil {
		return fmt.Errorf("persist model reply: %w", err)
	}
	_, _ = p.DB.Exec(ctx, "UPDATE sessions SET model_message_count = model_message_count + 1, first_response_at = COALESCE(first_response_at, $1) WHERE session_id = $2", time.Now(), sessionID)
	p.publishMessage(ctx, cfg.UserID, sessionID, modelMessageID, "model")
	feedbackPayload := map[string]any{"feedback": true}
	if err := p.enqueueDelivery(ctx, ev, cfg, sessionID, modelMessageID, reply, feedbackPayload); err != nil {
		return err
	}

	// Voice reply (opt-in): when the customer sent a voice note and TTS is
	// active, deliver the same answer as playable audio too.
	if mediaKind, _ := platformMedia["kind"].(string); p.Cfg.TTSActive() && (mediaKind == "voice" || mediaKind == "audio") {
		p.enqueueVoiceReply(ctx, ev, cfg, sessionID, reply)
	}

	// Auto-handoff triggers 2+3 — classify the turn in the background (never
	// blocks the customer) and escalate on negative sentiment / no-answer.
	p.classifyTurnAsync(cfg.UserID, sessionID, content, reply, groundCtx.HasMatch)
	return nil
}

// publishMessage fans out an inbox.message realtime event (best-effort).
func (p *Pipeline) publishMessage(ctx context.Context, userID int32, sessionID string, messageID int64, role string) {
	realtime.Publish(ctx, p.Redis, realtime.Event{
		Type:      realtime.EventMessage,
		UserID:    userID,
		SessionID: sessionID,
		MessageID: messageID,
		Role:      role,
	})
}

func (p *Pipeline) ownerLanguage(ctx context.Context, userID int32) string {
	var lang *string
	_ = p.DB.QueryRow(ctx, "SELECT language FROM users WHERE user_id = $1", userID).Scan(&lang)
	return deref(lang)
}

// isOpenNow — business-hours check (no configured rows → open).
func (p *Pipeline) isOpenNow(ctx context.Context, userID int32, platform string) bool {
	now := time.Now()
	weekday := int(now.Weekday()) // 0=Sunday
	hm := now.Format("15:04")
	var openTime, closeTime string
	err := p.DB.QueryRow(ctx,
		"SELECT open_time, close_time FROM business_hours WHERE user_id = $1 AND weekday = $2 AND is_active = true LIMIT 1",
		userID, weekday).Scan(&openTime, &closeTime)
	if err != nil {
		return true // no schedule configured → open
	}
	if openTime == "" || closeTime == "" {
		return false
	}
	return hm >= openTime && hm < closeTime
}

func (p *Pipeline) loadHistory(ctx context.Context, sessionID string) []gemini.HistoryItem {
	rows, err := p.DB.Query(ctx,
		"SELECT role, content FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent') ORDER BY message_id DESC LIMIT 20", sessionID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var rev []gemini.HistoryItem
	for rows.Next() {
		var h gemini.HistoryItem
		if rows.Scan(&h.Role, &h.Content) == nil {
			rev = append(rev, h)
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// fetchMessengerProfile returns (name, avatarURL) via the Graph API.
func (p *Pipeline) fetchMessengerProfile(ctx context.Context, cfg *configCred, platformUserID string) (string, string) {
	client := NewMetaClient(cfg.AccessToken, cfg.PageID, cfg.InstagramBusiness, p.Cfg.Meta.GraphAPIVersion)
	fields := "name"
	if cfg.Platform == "instagram" {
		fields = "name,username,profile_pic"
	} else {
		fields = "first_name,last_name,profile_pic"
	}
	v, err := client.get(ctx, "/"+platformUserID, map[string][]string{"fields": {fields}})
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

// customerAvatar returns the stored avatar URL for a customer ("" = none).
func (p *Pipeline) customerAvatar(ctx context.Context, cfg *configCred, platformUserID string) string {
	var avatar string
	_ = p.DB.QueryRow(ctx,
		"SELECT avatar_url FROM customer_profiles WHERE user_id=$1 AND platform=$2 AND platform_user_id=$3",
		cfg.UserID, cfg.Platform, platformUserID).Scan(&avatar)
	return avatar
}

// storeTelegramAvatar mirrors the chat photo into R2 and returns a
// browser-safe URL. Telegram file URLs embed the bot token, so the photo is
// downloaded server-side and re-hosted. Best-effort: "" on any failure
// (R2 disabled, download error, upload error).
func (p *Pipeline) storeTelegramAvatar(ctx context.Context, cfg *configCred, platformUserID, photoFileID string) string {
	if !p.Media.Enabled() || photoFileID == "" {
		return ""
	}
	data, _, err := NewTelegramClient(cfg.BotToken).DownloadFile(ctx, photoFileID)
	if err != nil || len(data) == 0 {
		return ""
	}
	key := fmt.Sprintf("avatars/telegram/%d/%s.jpg", cfg.UserID, platformUserID)
	putCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if uerr := p.Media.PutObject(putCtx, key, data, "image/jpeg"); uerr != nil {
		p.Logger.Warn("telegram avatar upload failed", "error", uerr.Error())
		return ""
	}
	return p.Media.PublicOrPresigned(key, 7*24*time.Hour)
}

// prepareMedia downloads media, transcribes voice, describes images, stores
// the file in R2, and folds the extracted text into the message content.
// Returns (content, storageKey, platformMedia).
func (p *Pipeline) prepareMedia(ctx context.Context, ev *InboundEvent, cfg *configCred, content string) (string, string, map[string]any) {
	kind, _ := ev.Media["kind"].(string)
	providerID, _ := ev.Media["provider_media_id"].(string)
	sourceURL, _ := ev.Media["source_url"].(string)
	filename, _ := ev.Media["filename"].(string)
	declaredMime, _ := ev.Media["mime_type"].(string)
	if providerID == "" && sourceURL == "" {
		return content, "", nil
	}

	var data []byte
	var mime string
	var err error
	switch cfg.Platform {
	case "telegram":
		data, mime, err = NewTelegramClient(cfg.BotToken).DownloadFile(ctx, providerID)
	case "line":
		data, mime, err = NewLineClient(cfg.AccessToken).DownloadContent(ctx, providerID)
	default: // meta / instagram / whatsapp
		if cfg.Platform == "whatsapp" && providerID != "" {
			data, mime, err = NewMetaClient(cfg.AccessToken, cfg.PageID, cfg.InstagramBusiness, p.Cfg.Meta.GraphAPIVersion).DownloadMedia(ctx, providerID)
		} else if sourceURL != "" {
			data, mime, err = downloadBytes(ctx, sourceURL)
		} else {
			return content, "", nil
		}
	}
	if err != nil || len(data) == 0 {
		p.Logger.Warn("media download failed", "error", fmt.Sprint(err))
		return content, "", nil
	}
	// Telegram file downloads often return a generic Content-Type
	// (application/octet-stream); prefer the mime declared by the webhook
	// (e.g. voice = audio/ogg) so Gemini can decode the audio.
	if mime == "" || mime == "application/octet-stream" {
		mime = declaredMime
	}
	isImage := kind == "photo" || kind == "image" || strings.HasPrefix(mime, "image/")

	// Voice/audio → transcribe into the message content.
	// Image    → Gemini vision description so the AI can actually answer.
	extracted := ""
	if !isImage && (kind == "audio" || kind == "voice") {
		transcript, terr := transcribeAudio(ctx, p, data, mime, p.ownerLanguage(ctx, cfg.UserID))
		switch {
		case terr != nil:
			p.Logger.Warn("voice transcription failed", "event_id", ev.EventID, "error", fmt.Sprint(terr))
		case transcript != "":
			content = transcript
			extracted = transcript
		default:
			p.Logger.Warn("voice transcription returned empty transcript", "event_id", ev.EventID)
		}
	} else if isImage {
		desc, derr := p.Gemini.DescribeImage(ctx, data, mime)
		if derr == nil && strings.TrimSpace(desc) != "" {
			content = desc
			extracted = desc
		} else {
			p.Logger.Warn("image description failed", "error", fmt.Sprint(derr))
		}
	}

	// Store for replay (best-effort; requires R2 config).
	storageKey := ""
	if p.Media.Enabled() {
		storageKey = fmt.Sprintf("platform-media/%d/%d/%s", cfg.UserID, ev.EventID, safeFilename(filename, kind, ev.EventID))
		uploadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		if uerr := p.Media.PutObject(uploadCtx, storageKey, data, mime); uerr != nil {
			p.Logger.Warn("r2 upload failed", "error", uerr.Error())
			storageKey = ""
		}
		cancel()
	}
	pm := map[string]any{"kind": kind, "filename": filename, "mime_type": mime}
	if storageKey != "" {
		pm["processing_status"] = "stored"
	}
	if extracted != "" {
		pm["extracted_text"] = extracted
	}
	return content, storageKey, pm
}

func safeFilename(filename, kind string, eventID int64) string {
	if filename == "" {
		return fmt.Sprintf("%s-%d.bin", kind, eventID)
	}
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(filename)
}

// transcribeAudio calls Gemini multimodal speech-to-text.
func transcribeAudio(ctx context.Context, p *Pipeline, audio []byte, mime, lang string) (string, error) {
	return p.Gemini.TranscribeAudio(ctx, audio, mime, lang)
}

// enqueueHandoffAck sends the canned "agent will respond" acknowledgement.
func (p *Pipeline) enqueueHandoffAck(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID string) {
	lang := p.ownerLanguage(ctx, cfg.UserID)
	if lang == "" {
		if det := gemini.DetectLanguage(ev.Content); det != "" {
			lang = det
		} else {
			lang = "km"
		}
	}
	reply := handoffAcknowledgement(lang)
	var msgID int64
	if err := p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, created_at) VALUES ($1,'system','text',$2,$3) RETURNING message_id",
		sessionID, reply, time.Now()).Scan(&msgID); err != nil {
		return
	}
	p.publishMessage(ctx, cfg.UserID, sessionID, msgID, "system")
	_ = p.enqueueDelivery(ctx, ev, cfg, sessionID, msgID, reply, nil)
}

func handoffAcknowledgement(language string) string {
	switch language {
	case "en":
		return "Thank you for your message. A human agent has been notified and will respond shortly. I'll hand this conversation over to them."
	case "zh":
		return "感谢您的消息。已为您转接人工客服，客服人员将尽快回复您。我已把这次对话转交人工处理。"
	default:
		return "សូមអរគុណសម្រាប់សាររបស់អ្នក។ ភ្នាក់ងារមនុស្សត្រូវបានជូនដំណឹង ហើយនឹងឆ្លើយតបក្នុងពេលឆាប់ៗនេះ។ ខ្ញុំនឹងប្រគល់ការសន្ទនានេះទៅឱ្យពួកគេ។"
	}
}

// ============================================
// Auto-handoff engine
// ============================================

// humanRequestKeywords — phrases where the customer explicitly asks for a
// human (en / km / zh). Matched case-insensitively on the raw message. Short
// English words are matched on word boundaries separately (see the matcher) to
// avoid false positives like "management" containing "manage".
var humanRequestKeywords = []string{
	// English multi-word
	"real person", "real human", "human agent", "live agent", "human support",
	"talk to agent", "talk to a human", "talk to someone", "speak to a person",
	"speak to a human", "speak to an agent", "connect me to", "transfer me",
	"customer agent", "staff member", "a representative", "speak to staff",
	// Khmer (common explicit requests)
	"មនុស្សពិត", "និយាយជាមួយមនុស្ស", "ភ្នាក់ងារមនុស្ស", "ទាក់ទងមនុស្ស",
	"សុំភ្នាក់ងារ", "និយាយជាមួយភ្នាក់ងារ", "ចង់និយាយជាមួយ", "បម្រើមនុស្ស",
	"ភ្នាក់ងារជំនួយ", "មនុស្សបម្រើ", "សុំមនុស្ស",
	// Chinese
	"人工", "真人", "转人工", "找客服", "人工客服", "转接客服", "我要客服",
	"联系人工", "人工服务", "找个人", "接人工",
}

// humanRequestWordMarkers — single tokens matched on ASCII word boundaries so
// a bare "agent"/"human" in an English sentence still escalates, but we do not
// fire on substrings inside unrelated words.
var humanRequestWordMarkers = []string{
	"human", "agent", "representative", "staff",
}

func humanRequestKeyword(content string) (string, bool) {
	lowered := strings.ToLower(content)
	for _, kw := range humanRequestKeywords {
		if strings.Contains(lowered, kw) {
			return kw, true
		}
	}
	for _, w := range humanRequestWordMarkers {
		if containsWord(lowered, w) {
			return w, true
		}
	}
	return "", false
}

// containsWord reports whether `word` appears in `s` surrounded by non-ASCII
// letters (so "agent" matches "talk to an agent", not "reagent").
func containsWord(s, word string) bool {
	for i := 0; i+len(word) <= len(s); {
		idx := strings.Index(s[i:], word)
		if idx < 0 {
			return false
		}
		start := i + idx
		end := start + len(word)
		leftOK := start == 0 || !isASCIILetter(s[start-1])
		rightOK := end >= len(s) || !isASCIILetter(s[end])
		if leftOK && rightOK {
			return true
		}
		i = end
	}
	return false
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// HumanRequestKeyword is the exported matcher used by the web-chat path so the
// keyword rule has a single source of truth across pipeline + HTTP.
func HumanRequestKeyword(content string) (string, bool) { return humanRequestKeyword(content) }

// HandoffAcknowledgement is the exported canned "agent will respond" text.
func HandoffAcknowledgement(language string) string { return handoffAcknowledgement(language) }

// escalateToHuman moves a session to the handoff queue: creates the request
// row (unless one is already open), flips the status, and — when an inbound
// event is given — acks the customer. Every auto trigger funnels through here.
// Escalation failures never fail the inbound event (the AI already replied or
// the queue dedupes), so the error is logged, not returned.
func (p *Pipeline) escalateToHuman(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID, trigger, reason string) {
	if err := p.createHandoffRequest(ctx, cfg.UserID, sessionID, trigger, reason); err != nil {
		p.Logger.Warn("auto handoff request failed", "session_id", sessionID, "error", err.Error())
		return
	}
	if ev != nil {
		p.enqueueHandoffAck(ctx, ev, cfg, sessionID)
	}
}

// createHandoffRequest inserts a queue row + session status + notification.
// The partial unique index keeps exactly one open request per session, so
// repeat triggers are silently deduplicated.
func (p *Pipeline) createHandoffRequest(ctx context.Context, userID int32, sessionID, trigger, reason string) error {
	priority := highPriorityTriggers[trigger]
	if priority == "" {
		priority = "normal"
	}
	_, err := p.DB.Exec(ctx,
		"INSERT INTO human_handoff_requests (session_id, user_id, status, priority, trigger, reason, created_at) "+
			"VALUES ($1,$2,'pending',$3,$4::human_handoff_trigger,$5,NOW()) ON CONFLICT DO NOTHING",
		sessionID, userID, priority, trigger, reason)
	if err != nil {
		return err
	}
	_, _ = p.DB.Exec(ctx,
		"UPDATE sessions SET status='handoff', escalated_at=COALESCE(escalated_at, NOW()) WHERE session_id=$1 AND status='active'", sessionID)
	p.notifyUser(ctx, userID, "handoff", "New human-handoff request", trigger+": "+truncateStr(reason, 120), sessionID)
	realtime.Publish(ctx, p.Redis, realtime.Event{
		Type: realtime.EventSession, UserID: userID, SessionID: sessionID,
	})
	return nil
}

var highPriorityTriggers = map[string]string{
	"customer_request":  "high",
	"negative_feedback": "high",
	"ai_decision":       "normal",
	"no_knowledge_base": "normal",
	"manual":            "normal",
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// notifyUser inserts a notification row and fans an inbox.notification event
// out to the tenant's WebSocket connections (the bell polls too, so this is
// an acceleration, not a guarantee).
func (p *Pipeline) notifyUser(ctx context.Context, userID int32, kind, title, body, sessionID string) {
	_, _ = p.DB.Exec(ctx,
		"INSERT INTO notifications (user_id, kind, title, body, session_id, created_at) VALUES ($1,$2,$3,$4,$5,NOW())",
		userID, kind, title, body, nullSession(sessionID))
	realtime.Publish(ctx, p.Redis, realtime.Event{
		Type: realtime.EventNotification, UserID: userID, SessionID: sessionID,
	})
}

func nullSession(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// classifyTurnAsync runs the fast-model turn analysis off the request path:
// persists sentiment/intent/confidence on the session and escalates when the
// AI should not have owned this message.
func (p *Pipeline) classifyTurnAsync(userID int32, sessionID, customerMsg, reply string, hasMatch bool) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 20*time.Second)
		defer cancel()

		prompt := "You audit one customer-service turn. Reply with ONLY a JSON object:\n" +
			`{"sentiment":"positive|neutral|negative","intent":"one of: question, complaint, refund, order_status, price, booking, small_talk, other","confidence":0.0-1.0,"escalate":true|false}` + "\n" +
			"escalate=true when the customer is angry, repeatedly unsatisfied, asks for something only a human can do (refunds beyond policy, complaints, legal threats), or the assistant answer clearly does not resolve the question.\n\n" +
			"[KB grounded]=" + fmt.Sprintf("%t", hasMatch) + "\n" +
			"Customer: " + truncateStr(customerMsg, 600) + "\n" +
			"Assistant: " + truncateStr(reply, 600)

		out, ok := p.Gemini.GenerateFast(ctx, prompt, 10*time.Second)
		if !ok {
			return
		}
		var cls struct {
			Sentiment  string  `json:"sentiment"`
			Intent     string  `json:"intent"`
			Confidence float64 `json:"confidence"`
			Escalate   bool    `json:"escalate"`
		}
		trimmed := strings.TrimSpace(out)
		if i := strings.Index(trimmed, "{"); i >= 0 {
			if j := strings.LastIndex(trimmed, "}"); j > i {
				trimmed = trimmed[i : j+1]
			}
		}
		if json.Unmarshal([]byte(trimmed), &cls) != nil {
			return
		}
		sentiment := cls.Sentiment
		switch sentiment {
		case "positive", "negative":
		default:
			sentiment = "neutral"
		}
		intent := cls.Intent
		if intent == "" || len(intent) > 64 {
			intent = "other"
		}
		conf := cls.Confidence
		if conf < 0 || conf > 1 {
			conf = 0.5
		}
		_, _ = p.DB.Exec(ctx,
			"UPDATE sessions SET sentiment=$1, sentiment_at=NOW(), intent=$2, confidence=$3 WHERE session_id=$4 AND status='active'",
			sentiment, intent, conf, sessionID)

		// Still owned by the AI? only escalate active sessions.
		var status string
		_ = p.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id=$1", sessionID).Scan(&status)
		if status != "active" {
			return
		}

		// Trigger 2 — negative sentiment (angry customer).
		if sentiment == "negative" {
			p.escalateToHuman(ctx, nil, &configCred{UserID: userID}, sessionID, "negative_feedback",
				"Customer sentiment turned negative ("+intent+")")
			return
		}
		// Trigger 3 — the AI answered without any knowledge-base grounding and
		// the classifier is not confident: the KB cannot cover this.
		if !hasMatch && !cls.Escalate && conf < 0.35 && p.hasReadyDocs(ctx, userID) && intent != "small_talk" {
			p.escalateToHuman(ctx, nil, &configCred{UserID: userID}, sessionID, "no_knowledge_base",
				"Answer not grounded in the knowledge base (intent: "+intent+")")
			return
		}
		if cls.Escalate {
			p.escalateToHuman(ctx, nil, &configCred{UserID: userID}, sessionID, "ai_decision",
				"AI classifier recommends human review ("+intent+")")
		}
	}()
}

// hasReadyDocs — whether the tenant has at least one indexed knowledge doc.
func (p *Pipeline) hasReadyDocs(ctx context.Context, userID int32) bool {
	var exists bool
	_ = p.DB.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM knowledge_documents WHERE uploaded_by = $1 AND index_status = 'ready')", userID).Scan(&exists)
	return exists
}

// sendTyping shows the "typing…" hint while the AI composes (best effort).
func (p *Pipeline) sendTyping(ctx context.Context, cfg *configCred, recipientID string) {
	if recipientID == "" {
		return
	}
	switch cfg.Platform {
	case "telegram":
		_ = NewTelegramClient(cfg.BotToken).SendChatAction(ctx, recipientID, "typing")
	case "line":
		_ = NewLineClient(cfg.AccessToken).SendTypingIndicator(ctx, recipientID)
	case "meta", "instagram":
		_ = NewMetaClient(cfg.AccessToken, cfg.PageID, cfg.InstagramBusiness, p.Cfg.Meta.GraphAPIVersion).
			SendSenderAction(ctx, cfg.Platform, recipientID, "typing_on")
	}
}

// enqueueVoiceReply synthesizes the model reply as audio and queues it as a
// follow-up media delivery. Fully best-effort — any failure is just "no audio".
func (p *Pipeline) enqueueVoiceReply(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID, reply string) {
	text := truncateStr(stripMarkdown(reply), 400)
	if text == "" {
		return
	}
	synthCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	wav, err := p.Gemini.SynthesizeSpeech(synthCtx, text)
	cancel()
	if err != nil || len(wav) == 0 {
		p.Logger.Warn("tts synthesis failed", "error", fmt.Sprint(err))
		return
	}
	if len(sessionID) < 8 {
		return
	}
	key := fmt.Sprintf("tts/%d/%s/%d.wav", cfg.UserID, sessionID[:8], time.Now().UnixNano())
	upCtx, cancel2 := context.WithTimeout(ctx, 45*time.Second)
	if err := p.Media.PutObject(upCtx, key, wav, "audio/wav"); err != nil {
		cancel2()
		return
	}
	cancel2()
	audioURL := p.Media.PublicOrPresigned(key, time.Hour)
	if audioURL == "" {
		return
	}
	var msgID int64
	err = p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, media_url, created_at) VALUES ($1,'model','audio',$2,$3,$4) RETURNING message_id",
		sessionID, text, key, time.Now()).Scan(&msgID)
	if err != nil {
		return
	}
	_, _ = p.DB.Exec(ctx, "UPDATE sessions SET model_message_count = model_message_count + 1 WHERE session_id = $1", sessionID)
	p.publishMessage(ctx, cfg.UserID, sessionID, msgID, "model")
	payload := map[string]any{"kind": "media", "media_url": audioURL, "media_type": "audio"}
	_ = p.enqueueDelivery(ctx, ev, cfg, sessionID, msgID, text, payload)
}

// stripMarkdown — rough markdown → plain text for TTS.
func stripMarkdown(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#*-•>"))
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

// sourcesJSON renders the grounding sources for persistence (NULL when no
// grounding happened).
func sourcesJSON(groundCtx rag.GroundingContext) any {
	if !groundCtx.HasMatch || len(groundCtx.Sources) == 0 {
		return nil
	}
	data, err := json.Marshal(groundCtx.Sources)
	if err != nil {
		return nil
	}
	return string(data)
}

// ============================================
// Outbound
// ============================================

// enqueueDelivery queues one outbound message (idempotent by chat_message_id).
func (p *Pipeline) enqueueDelivery(ctx context.Context, ev *InboundEvent, cfg *configCred, sessionID string, chatMessageID int64, content string, payload map[string]any) error {
	now := time.Now()
	var payloadJSON []byte
	if payload != nil {
		payloadJSON, _ = json.Marshal(payload)
	}
	tag, err := p.DB.Exec(ctx,
		"INSERT INTO platform_outbox "+
			"(config_id, session_id, chat_message_id, platform, recipient_id, content, payload, status, next_attempt_at, created_at, updated_at) "+
			"VALUES ($1,$2,$3,$4::platform_type,$5,$6,$7,'pending',$8,$9,$9) "+
			"ON CONFLICT (chat_message_id) DO NOTHING",
		ev.ConfigID, sessionID, chatMessageID, cfg.Platform, ev.PlatformUserID, content, payloadJSON, now, now)
	if err != nil {
		return fmt.Errorf("enqueue delivery: %w", err)
	}
	if tag.RowsAffected() > 0 {
		p.SignalOutbound()
	}
	return nil
}

// outboundDelivery is a claimed delivery row.
type outboundDelivery struct {
	DeliveryID    int64
	ConfigID      int32
	SessionID     string
	Platform      string
	RecipientID   string
	Content       string
	Payload       map[string]any
	Status        string
	Attempts      int32
	LastMessageID int64
}

// processOutboundNext drains every claimable delivery in one pass.
func (p *Pipeline) processOutboundNext(ctx context.Context) {
	for {
		d, err := p.claimOutbound(ctx)
		if err != nil || d == nil {
			return
		}
		p.deliver(ctx, d)
	}
}

func (p *Pipeline) claimOutbound(ctx context.Context) (*outboundDelivery, error) {
	now := time.Now()
	stale := now.Add(-staleLockMinutes * time.Minute)
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var d outboundDelivery
	var payloadJSON []byte
	err = tx.QueryRow(ctx,
		"UPDATE platform_outbox SET status='processing', attempts = attempts + 1, locked_at=$1 "+
			"WHERE delivery_id = (SELECT delivery_id FROM platform_outbox "+
			"WHERE (status = 'pending' AND next_attempt_at <= $2) OR (status = 'processing' AND locked_at < $3) "+
			"ORDER BY created_at ASC LIMIT 1 FOR UPDATE SKIP LOCKED) "+
			"RETURNING delivery_id, config_id, session_id, platform::text, recipient_id, content, payload, status, attempts, chat_message_id",
		now, now, stale).Scan(&d.DeliveryID, &d.ConfigID, &d.SessionID, &d.Platform, &d.RecipientID, &d.Content, &payloadJSON, &d.Status, &d.Attempts, &d.LastMessageID)
	if err != nil {
		return nil, nil
	}
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &d.Payload)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &d, nil
}

func (p *Pipeline) deliver(ctx context.Context, d *outboundDelivery) {
	// Cancel queued model replies when the session left 'active'.
	var role string
	_ = p.DB.QueryRow(ctx, "SELECT role FROM chat_messages WHERE message_id = $1", d.LastMessageID).Scan(&role)
	var sessionStatus string
	_ = p.DB.QueryRow(ctx, "SELECT status::text FROM sessions WHERE session_id = $1", d.SessionID).Scan(&sessionStatus)
	if sessionStatus == "" {
		sessionStatus = "active"
	}
	if role == "model" && (sessionStatus == "handoff" || sessionStatus == "resolved" || sessionStatus == "closed") {
		reason := "Automatic reply cancelled because the conversation is no longer active"
		switch sessionStatus {
		case "handoff":
			reason = "Automatic reply cancelled because an agent took over the conversation"
		case "resolved":
			reason = "Automatic reply cancelled because the conversation was resolved"
		case "closed":
			reason = "Automatic reply cancelled because the conversation was closed"
		}
		_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='cancelled', locked_at=NULL, last_error=$1, updated_at=NOW() WHERE delivery_id=$2", reason, d.DeliveryID)
		return
	}
	if d.Status == "cancelled" {
		return
	}

	providerID, err := p.deliverToProvider(ctx, d)
	if err == nil {
		if providerID == "" {
			_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='sent', sent_at=$1, locked_at=NULL, last_error='' WHERE delivery_id=$2", time.Now(), d.DeliveryID)
		} else {
			_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='sent', sent_at=$1, locked_at=NULL, last_error='', provider_message_id=$2, provider_status='sent' WHERE delivery_id=$3", time.Now(), providerID, d.DeliveryID)
		}
		return
	}
	p.Logger.Warn("delivery failed", "delivery_id", d.DeliveryID, "error", err.Error())
	msg := truncate(err.Error(), 1000)
	if _, ok := err.(*PolicyError); ok || d.Attempts >= maxAttempts {
		_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='failed', next_attempt_at=$1, locked_at=NULL, last_error=$2, updated_at=NOW() WHERE delivery_id=$3", time.Now(), msg, d.DeliveryID)
	} else {
		_, _ = p.DB.Exec(ctx, "UPDATE platform_outbox SET status='pending', next_attempt_at=$1, locked_at=NULL, last_error=$2, updated_at=NOW() WHERE delivery_id=$3", time.Now().Add(retryDelay(int(d.Attempts))), msg, d.DeliveryID)
	}
}

// deliverToProvider sends via the correct provider client. Returns provider id.
func (p *Pipeline) deliverToProvider(ctx context.Context, d *outboundDelivery) (string, error) {
	isTemplate := false
	if d.Payload != nil {
		if k, _ := d.Payload["kind"].(string); k == "template" {
			isTemplate = true
		}
	}
	if _, err := EnsureReplyWindow(ctx, p.DB, d.Platform, d.ConfigID, d.SessionID, isTemplate, time.Now()); err != nil {
		if _, ok := err.(*PolicyError); ok {
			return "", err
		}
		return "", err
	}
	cfg, err := p.loadConfig(ctx, d.ConfigID)
	if err != nil {
		return "", err
	}
	kind := "text"
	var mediaURL, mediaType string
	var buttons [][2]string
	if d.Payload != nil {
		if k, ok := d.Payload["kind"].(string); ok && k != "" {
			kind = k
		}
		mediaURL, _ = d.Payload["media_url"].(string)
		mediaType, _ = d.Payload["media_type"].(string)
		if b, ok := d.Payload["buttons"].([]any); ok {
			for _, item := range b {
				if m, ok := item.(map[string]any); ok {
					title, _ := m["title"].(string)
					payload, _ := m["payload"].(string)
					buttons = append(buttons, [2]string{title, payload})
				}
			}
		}
	}

	switch d.Platform {
	case "telegram":
		client := NewTelegramClient(cfg.BotToken)
		if kind == "media" && mediaType == "audio" && mediaURL != "" {
			id, err := client.SendAudio(ctx, d.RecipientID, mediaURL, d.Content)
			if err != nil {
				return "", err
			}
			return TelegramProviderMessageID(d.RecipientID, id), nil
		}
		// AI replies get 👍/👎 inline buttons (customer-side CSAT collection).
		if fb, _ := d.Payload["feedback"].(bool); fb && len(buttons) == 0 && d.LastMessageID > 0 {
			buttons = [][2]string{
				{"👍", fmt.Sprintf("fb:%d:1", d.LastMessageID)},
				{"👎", fmt.Sprintf("fb:%d:-1", d.LastMessageID)},
			}
		}
		chunks := SplitPlatformText(d.Content, PlatformTextLimit("telegram"))
		lastID := ""
		for _, chunk := range chunks {
			id, err := client.SendMessage(ctx, d.RecipientID, chunk, buttons)
			if err != nil {
				return "", err
			}
			if id != "" {
				lastID = TelegramProviderMessageID(d.RecipientID, id)
			}
			// Buttons ride on the first chunk only.
			buttons = nil
		}
		return lastID, nil
	case "line":
		if kind != "text" {
			return "", fmt.Errorf("LINE currently supports text messages only")
		}
		client := NewLineClient(cfg.AccessToken)
		chunks := SplitPlatformText(d.Content, PlatformTextLimit("line"))
		last := ""
		for _, chunk := range chunks {
			id, err := client.PushText(ctx, d.RecipientID, chunk)
			if err != nil {
				return "", err
			}
			last = id
		}
		return last, nil
	default: // meta / instagram / whatsapp
		client := NewMetaClient(cfg.AccessToken, cfg.PageID, cfg.InstagramBusiness, p.Cfg.Meta.GraphAPIVersion)
		req := &SendRequest{Platform: d.Platform, RecipientID: d.RecipientID, Kind: kind, MediaURL: mediaURL, MediaType: mediaType, Buttons: buttons}
		if d.Payload != nil {
			req.TemplateName, _ = d.Payload["template_name"].(string)
			req.TemplateLanguage, _ = d.Payload["template_language"].(string)
			if params, ok := d.Payload["template_body_params"].([]any); ok {
				for _, p := range params {
					if s, ok := p.(string); ok {
						req.TemplateBodyParams = append(req.TemplateBodyParams, s)
					}
				}
			}
		}
		if kind == "text" || kind == "buttons" {
			req.Text = d.Content
		} else if kind == "media" && mediaType != "audio" {
			// caption handled in body builder
		}
		chunks := SplitPlatformText(d.Content, PlatformTextLimit(d.Platform))
		last := ""
		if kind != "text" || len(chunks) == 0 {
			id, err := client.SendMessage(ctx, req)
			if err != nil {
				return "", err
			}
			return id, nil
		}
		for _, chunk := range chunks {
			req.Text = chunk
			id, err := client.SendMessage(ctx, req)
			if err != nil {
				return "", err
			}
			if id != "" {
				last = id
			}
		}
		return last, nil
	}
}

// PlatformTextLimit — per-platform outbound message length cap.
func PlatformTextLimit(platform string) int {
	switch platform {
	case "telegram":
		return 4096
	case "line":
		return 5000
	case "whatsapp":
		return 1024
	case "instagram":
		return 1000
	default:
		return 2000
	}
}

// SplitPlatformText splits on rune boundaries, preferring a space near the cap.
func SplitPlatformText(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := start + limit
		if end > len(runes) {
			end = len(runes)
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

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
