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
	}

	// Prepare media: voice → transcribe, images → collect.
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
	reply := result.Reply
	if !result.UsedMock {
		reply = gemini.StripSourceMarkers(reply)
	}

	// After-hours preamble.
	if !p.isOpenNow(ctx, cfg.UserID, cfg.Platform) {
		reply = "យើងកំពុងបិទសេវាកម្មនៅពេលនេះ។ ភ្នាក់ងារនឹងឆ្លើយតបនៅពេលម៉ោងធ្វើការ។\n\n" + reply
	}

	// Persist model reply + enqueue delivery.
	var modelMessageID int64
	err = p.DB.QueryRow(ctx,
		"INSERT INTO chat_messages (session_id, role, message_type, content, tokens_used, model_name, used_mock, created_at) "+
			"VALUES ($1,'model','text',$2,$3,$4,$5,$6) RETURNING message_id",
		sessionID, reply, result.PromptTokens+result.OutputTokens, p.Gemini.ModelName(), result.UsedMock, time.Now()).
		Scan(&modelMessageID)
	if err != nil {
		return fmt.Errorf("persist model reply: %w", err)
	}
	_, _ = p.DB.Exec(ctx, "UPDATE sessions SET model_message_count = model_message_count + 1, first_response_at = COALESCE(first_response_at, $1) WHERE session_id = $2", time.Now(), sessionID)
	p.publishMessage(ctx, cfg.UserID, sessionID, modelMessageID, "model")
	return p.enqueueDelivery(ctx, ev, cfg, sessionID, modelMessageID, reply, nil)
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

// prepareMedia downloads media, transcribes voice, stores the file, and folds
// the transcript into the message content. Returns (content, storageKey, platformMedia).
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
	if mime == "" {
		mime = declaredMime
	}

	// Voice → transcribe into the message content.
	if kind == "audio" || kind == "voice" {
		transcript, terr := transcribeAudio(ctx, p, data, mime)
		if terr == nil && transcript != "" {
			content = transcript
		}
	}

	// Store for replay (best-effort; requires R2 config).
	storageKey := ""
	if p.Cfg.R2Enabled() && p.RAG != nil {
		storageKey = fmt.Sprintf("platform-media/%d/%d/%s", cfg.UserID, ev.EventID, safeFilename(filename, kind, ev.EventID))
	}
	pm := map[string]any{"kind": kind, "filename": filename, "mime_type": mime}
	return content, storageKey, pm
}

func safeFilename(filename, kind string, eventID int64) string {
	if filename == "" {
		return fmt.Sprintf("%s-%d.bin", kind, eventID)
	}
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(filename)
}

// transcribeAudio calls Gemini multimodal speech-to-text.
func transcribeAudio(ctx context.Context, p *Pipeline, audio []byte, mime string) (string, error) {
	return p.Gemini.TranscribeAudio(ctx, audio, mime)
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
