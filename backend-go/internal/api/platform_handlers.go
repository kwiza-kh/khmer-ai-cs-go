package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/security"
)

// ============================================
// Platform config CRUD + verify + health
// ============================================

type platformConfigRequest struct {
	ConfigID                  *int32  `json:"config_id"`
	Platform                  string  `json:"platform"`
	AccessToken               *string `json:"access_token"`
	PageID                    *string `json:"page_id"`
	InstagramBusinessID       *string `json:"instagram_business_id"`
	WhatsAppBusinessAccountID *string `json:"whatsapp_business_account_id"`
	BotToken                  *string `json:"bot_token"`
	WebhookSecret             *string `json:"webhook_secret"`
	IsActive                  *bool   `json:"is_active"`
}

// platformConfigRow scans a platform_configs row.
type platformConfigRow struct {
	ConfigID                  int32
	UserID                    int32
	Platform                  string
	AccessToken               string
	PageID                    string
	InstagramBusinessID       *string
	WhatsAppBusinessAccountID *string
	BotToken                  *string
	BotTokenHash              *string
	WebhookSecret             *string
	WebhookSecretHash         *string
	IsActive                  bool
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

const platformConfigCols = "config_id, user_id, platform::text AS platform, access_token, page_id, " +
	"instagram_business_id, whatsapp_business_account_id, bot_token, bot_token_hash, " +
	"webhook_secret, webhook_secret_hash, is_active, created_at, updated_at"

// listPlatformConfigs — the caller's platform configs with health + counts.
func (a *App) listPlatformConfigs(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT "+platformConfigCols+" FROM platform_configs WHERE user_id = $1 ORDER BY platform ASC, config_id ASC",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	var cfgs []platformConfigRow
	for rows.Next() {
		var c platformConfigRow
		if err := rows.Scan(&c.ConfigID, &c.UserID, &c.Platform, &c.AccessToken, &c.PageID,
			&c.InstagramBusinessID, &c.WhatsAppBusinessAccountID, &c.BotToken, &c.BotTokenHash,
			&c.WebhookSecret, &c.WebhookSecretHash, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err != nil {
			a.Logger.Warn("platform config row skipped", "config_id", c.ConfigID, "error", err.Error())
			continue
		}
		cfgs = append(cfgs, c)
	}
	// The response IS this list: a truncated read would show the merchant fewer
	// channels than they have — and a missing channel is precisely what makes
	// someone reconnect the same account and trip the cross-tenant conflict
	// check. Fail the request instead of serving a partial answer.
	if err := rows.Err(); err != nil {
		a.Logger.Warn("platform config list read failed", "user_id", user.UserID, "error", err.Error())
		return nil, ErrInternal("查询失败")
	}
	out := make([]map[string]any, 0)
	for i := range cfgs {
		out = append(out, a.attachPlatformStatus(r.Context(), &cfgs[i]))
	}
	return out, nil
}

// attachPlatformStatus adds health + outbox/inbound counts (credentials never serialized).
func (a *App) attachPlatformStatus(ctx context.Context, c *platformConfigRow) map[string]any {
	base := map[string]any{
		"config_id":                    c.ConfigID,
		"user_id":                      c.UserID,
		"platform":                     c.Platform,
		"page_id":                      c.PageID,
		"instagram_business_id":        c.InstagramBusinessID,
		"whatsapp_business_account_id": c.WhatsAppBusinessAccountID,
		"is_active":                    c.IsActive,
		"created_at":                   c.CreatedAt,
		"updated_at":                   c.UpdatedAt,
	}
	// Health.
	var status, detail string
	var accountName *string
	var checkedAt time.Time
	err := a.DB.QueryRow(ctx,
		"SELECT status, account_name, detail, checked_at FROM platform_connection_health WHERE config_id = $1",
		c.ConfigID).Scan(&status, &accountName, &detail, &checkedAt)
	if err == nil {
		base["health"] = map[string]any{"config_id": c.ConfigID, "status": status, "account_name": accountName, "detail": detail, "checked_at": checkedAt}
	}
	// Outbox counts.
	var pending, failed, cancelled, accepted, delivered, read, totalOut int64
	err = a.DB.QueryRow(ctx,
		"SELECT SUM(CASE WHEN status IN ('pending','processing') THEN 1 ELSE 0 END), "+
			"SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END), "+
			"SUM(CASE WHEN status='sent' THEN 1 ELSE 0 END), SUM(CASE WHEN provider_status IN ('delivered','read') THEN 1 ELSE 0 END), "+
			"SUM(CASE WHEN provider_status='read' THEN 1 ELSE 0 END), COUNT(*) FROM platform_outbox WHERE config_id = $1",
		c.ConfigID).Scan(&pending, &failed, &cancelled, &accepted, &delivered, &read, &totalOut)
	if err == nil {
		base["pending_deliveries"] = pending
		base["failed_deliveries"] = failed
		base["cancelled_deliveries"] = cancelled
		base["accepted_deliveries"] = accepted
		base["delivered_deliveries"] = delivered
		base["read_deliveries"] = read
	}
	// Inbound counts.
	var inPending, inFailed int64
	err = a.DB.QueryRow(ctx,
		"SELECT SUM(CASE WHEN status IN ('pending','processing') THEN 1 ELSE 0 END), SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END) FROM platform_inbound_events WHERE config_id = $1",
		c.ConfigID).Scan(&inPending, &inFailed)
	if err == nil {
		base["pending_inbound_events"] = inPending
		base["failed_inbound_events"] = inFailed
	}
	var lastInbound *time.Time
	var lastRaw *time.Time
	_ = a.DB.QueryRow(ctx, "SELECT MAX(last_inbound_at) FROM platform_user_sessions WHERE config_id = $1", c.ConfigID).Scan(&lastRaw)
	lastInbound = lastRaw
	if lastInbound != nil {
		base["last_inbound_at"] = lastInbound
	}
	return base
}

// upsertPlatformConfig — create or update a platform config.
func (a *App) upsertPlatformConfig(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req platformConfigRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Platform == "" {
		return nil, ErrBadRequest("platform is required")
	}
	// Which platform strings exist is declared in the capability table, not in a
	// list copied here (it used to be a switch that had to be edited for every
	// new channel). "web" is a real channel but it is not connectable through
	// this endpoint, so it is excluded explicitly.
	if !platform.CapabilitiesFor(req.Platform).Known || req.Platform == "web" {
		return nil, ErrBadRequest("invalid platform")
	}

	// Load existing (by config_id) or dedup by identity.
	var existing *platformConfigRow
	if req.ConfigID != nil && *req.ConfigID > 0 {
		row := a.DB.QueryRow(r.Context(),
			"SELECT "+platformConfigCols+" FROM platform_configs WHERE config_id = $1 AND user_id = $2",
			*req.ConfigID, user.UserID)
		var c platformConfigRow
		if err := row.Scan(&c.ConfigID, &c.UserID, &c.Platform, &c.AccessToken, &c.PageID,
			&c.InstagramBusinessID, &c.WhatsAppBusinessAccountID, &c.BotToken, &c.BotTokenHash,
			&c.WebhookSecret, &c.WebhookSecretHash, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err == nil {
			existing = &c
		}
	} else {
		identity := derefStr(req.PageID)
		if req.Platform == "instagram" {
			identity = derefStr(req.InstagramBusinessID)
		}
		if identity != "" {
			row := a.DB.QueryRow(r.Context(),
				"SELECT "+platformConfigCols+" FROM platform_configs WHERE user_id = $1 AND platform = $2::platform_type AND page_id = $3",
				user.UserID, req.Platform, identity)
			var c platformConfigRow
			if err := row.Scan(&c.ConfigID, &c.UserID, &c.Platform, &c.AccessToken, &c.PageID,
				&c.InstagramBusinessID, &c.WhatsAppBusinessAccountID, &c.BotToken, &c.BotTokenHash,
				&c.WebhookSecret, &c.WebhookSecretHash, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err == nil {
				existing = &c
			}
		}
	}

	// Merge: blank fields fall back to existing decrypted values.
	var decAccess, decBot, decSecret string
	if existing != nil {
		decAccess, _ = a.Sealer.Decrypt(existing.AccessToken)
		decBot, _ = a.Sealer.Decrypt(derefStr(existing.BotToken))
		decSecret, _ = a.Sealer.Decrypt(derefStr(existing.WebhookSecret))
	}
	access := orDefault(req.AccessToken, decAccess)
	bot := orDefault(req.BotToken, decBot)
	secret := orDefault(req.WebhookSecret, decSecret)
	pageID := derefStr(req.PageID)
	if pageID == "" && existing != nil {
		pageID = existing.PageID
	}
	igID := req.InstagramBusinessID
	if (igID == nil || *igID == "") && existing != nil {
		igID = existing.InstagramBusinessID
	}
	wabaID := ""
	if req.WhatsAppBusinessAccountID != nil && *req.WhatsAppBusinessAccountID != "" {
		wabaID = *req.WhatsAppBusinessAccountID
	} else if existing != nil && existing.WhatsAppBusinessAccountID != nil {
		wabaID = *existing.WhatsAppBusinessAccountID
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	} else if existing != nil {
		isActive = existing.IsActive
	}

	// Validation when activating.
	if isActive {
		if secret == "" {
			return nil, ErrBadRequest("启用平台 Webhook 时必须设置签名密钥")
		}
		if req.Platform != "telegram" && req.Platform != "zalo" && pageID == "" && derefStr(igID) == "" {
			return nil, ErrBadRequest("该平台需要配置平台集成 ID (page_id 或 instagram_business_id)")
		}
		if req.Platform == "telegram" && bot == "" {
			return nil, ErrBadRequest("Telegram 必须设置 bot_token")
		}
		if req.Platform != "telegram" && access == "" {
			return nil, ErrBadRequest("该平台需要配置 access_token")
		}
		// Cross-tenant conflict. A failed lookup must fail the save, not
		// silently pass (the dedupe depends on it).
		var conflict int64
		if req.Platform == "instagram" {
			if err := a.DB.QueryRow(r.Context(),
				"SELECT COUNT(*) FROM platform_configs WHERE platform='instagram' AND is_active=true AND user_id <> $1 AND instagram_business_id = $2",
				user.UserID, derefStr(igID)).Scan(&conflict); err != nil {
				return nil, ErrInternal("冲突检查失败")
			}
		} else {
			if err := a.DB.QueryRow(r.Context(),
				"SELECT COUNT(*) FROM platform_configs WHERE platform = $1::platform_type AND is_active=true AND user_id <> $2 AND page_id = $3",
				req.Platform, user.UserID, pageID).Scan(&conflict); err != nil {
				return nil, ErrInternal("冲突检查失败")
			}
		}
		if conflict > 0 {
			return nil, ErrConflict("该平台账号已连接到其他客户")
		}
		if req.Platform == "telegram" && bot != "" {
			botHash := security.Sha256Hex(bot)
			var c int64
			if err := a.DB.QueryRow(r.Context(),
				"SELECT COUNT(*) FROM platform_configs WHERE platform='telegram' AND is_active=true AND bot_token_hash = $1", botHash).Scan(&c); err != nil {
				return nil, ErrInternal("冲突检查失败")
			}
			if c > 0 {
				return nil, ErrConflict("This Telegram bot is already connected to another customer")
			}
		}
	}

	encAccess, err := a.Sealer.Encrypt(access)
	if err != nil {
		return nil, ErrInternal("加密失败")
	}
	encBot, _ := a.Sealer.Encrypt(bot)
	encSecret, _ := a.Sealer.Encrypt(secret)
	secretHash := security.Sha256Hex(secret)
	botHash := security.Sha256Hex(bot)

	var configID int32
	if existing != nil {
		_, err := a.DB.Exec(r.Context(),
			"UPDATE platform_configs SET access_token=$1, page_id=$2, instagram_business_id=$3, whatsapp_business_account_id=$4, bot_token=$5, bot_token_hash=$6, webhook_secret=$7, webhook_secret_hash=$8, is_active=$9, updated_at=NOW() WHERE config_id=$10",
			encAccess, pageID, igID, wabaID, encBot, botHash, encSecret, secretHash, isActive, existing.ConfigID)
		if err != nil {
			return nil, ErrInternal("更新失败")
		}
		configID = existing.ConfigID
	} else {
		if err := a.DB.QueryRow(r.Context(),
			"INSERT INTO platform_configs (user_id, platform, access_token, page_id, instagram_business_id, whatsapp_business_account_id, bot_token, bot_token_hash, webhook_secret, webhook_secret_hash, is_active) VALUES ($1,$2::platform_type,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING config_id",
			user.UserID, req.Platform, encAccess, pageID, igID, wabaID, encBot, botHash, encSecret, secretHash, isActive).Scan(&configID); err != nil {
			return nil, ErrInternal("创建失败")
		}
	}

	_ = a.recordHealth(r.Context(), configID, "unknown", "", "Credentials changed. Run a connection check.")

	// Return the saved config with status.
	var c platformConfigRow
	row := a.DB.QueryRow(r.Context(), "SELECT "+platformConfigCols+" FROM platform_configs WHERE config_id = $1 AND user_id = $2", configID, user.UserID)
	if err := row.Scan(&c.ConfigID, &c.UserID, &c.Platform, &c.AccessToken, &c.PageID,
		&c.InstagramBusinessID, &c.WhatsAppBusinessAccountID, &c.BotToken, &c.BotTokenHash,
		&c.WebhookSecret, &c.WebhookSecretHash, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, ErrInternal("查询失败")
	}
	return a.attachPlatformStatus(r.Context(), &c), nil
}

func orDefault(s *string, fallback string) string {
	if s != nil && *s != "" {
		return *s
	}
	return fallback
}

// recordHealth upserts platform connection health.
func (a *App) recordHealth(ctx context.Context, configID int32, status, accountName, detail string) error {
	now := time.Now()
	_, err := a.DB.Exec(ctx,
		"INSERT INTO platform_connection_health (config_id, status, account_name, detail, checked_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6) "+
			"ON CONFLICT (config_id) DO UPDATE SET status=EXCLUDED.status, "+
			"account_name=CASE WHEN EXCLUDED.account_name = '' THEN platform_connection_health.account_name ELSE EXCLUDED.account_name END, "+
			"detail=EXCLUDED.detail, checked_at=EXCLUDED.checked_at, updated_at=EXCLUDED.updated_at",
		configID, status, accountName, detail, now, now)
	return err
}

// telegramWebhookURL builds the provider webhook URL from PUBLIC_API_URL.
func (a *App) telegramWebhookURL() (string, error) {
	return a.providerWebhookURL("telegram")
}

// providerWebhookURL builds the public webhook endpoint for a platform.
func (a *App) providerWebhookURL(platform string) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(a.Cfg.Server.PublicAPIURL), "/")
	if raw == "" {
		return "", fmt.Errorf("PUBLIC_API_URL must be set before %s can register its webhook", platform)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("PUBLIC_API_URL must be a public https URL")
	}
	return raw + "/api/v1/webhook/" + platform, nil
}

// connectionCheckFailed records the failed self-check, then answers 502.
//
// platform_connection_health.status='error' is what the operator console counts
// to warn "渠道连接异常" (platform_bot_reports.go), and until this existed
// NOTHING in the codebase ever wrote that value: every check failure returned
// 502 straight from memory without touching the table, and the only other
// statuses ever written were 'connected' and 'unknown'. The warning therefore
// could only ever read zero — dead code guarding against exactly the silent
// breakage it had been written to catch. Recording first also means the
// operator's /platforms view keeps the reason after the merchant closes the tab.
//
// The health write is best-effort on purpose: it must not replace the real
// error (the provider's message is what the merchant needs to fix the token),
// and a DB hiccup here must not turn a 502 into a 500.
func (a *App) connectionCheckFailed(ctx context.Context, configID int32, err error) *ApiError {
	detail := "connection check failed: " + err.Error()
	_ = a.recordHealth(ctx, configID, "error", "", detail)
	return &ApiError{http.StatusBadGateway, detail}
}

// verifyPlatformConfig — verify a platform connection.
func (a *App) verifyPlatformConfig(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	user, _ := UserFrom(r)
	if configID <= 0 {
		return nil, ErrBadRequest("invalid platform configuration")
	}
	var c platformConfigRow
	row := a.DB.QueryRow(r.Context(), "SELECT "+platformConfigCols+" FROM platform_configs WHERE config_id = $1 AND user_id = $2", configID, user.UserID)
	if err := row.Scan(&c.ConfigID, &c.UserID, &c.Platform, &c.AccessToken, &c.PageID,
		&c.InstagramBusinessID, &c.WhatsAppBusinessAccountID, &c.BotToken, &c.BotTokenHash,
		&c.WebhookSecret, &c.WebhookSecretHash, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, ErrNotFound("platform configuration was not found")
	}
	if !c.IsActive {
		return nil, ErrConflict("activate this platform before testing the connection")
	}
	botToken, _ := a.Sealer.Decrypt(derefStr(c.BotToken))
	webhookSecret, _ := a.Sealer.Decrypt(derefStr(c.WebhookSecret))

	switch c.Platform {
	case "telegram":
		client := platform.NewTelegramClient(botToken)
		botID, username, firstName, err := client.GetMe(r.Context())
		if err != nil {
			return nil, a.connectionCheckFailed(r.Context(), configID, err)
		}
		if botID == 0 {
			return nil, a.connectionCheckFailed(r.Context(), configID, errors.New("invalid bot token"))
		}
		webhookURL, err := a.telegramWebhookURL()
		if err != nil {
			return nil, ErrInternal(err.Error())
		}
		infoURL, _ := client.GetWebhookInfo(r.Context())
		if infoURL != webhookURL {
			if err := client.SetWebhook(r.Context(), webhookURL, webhookSecret); err != nil {
				return nil, &ApiError{http.StatusBadGateway, "register Telegram webhook: " + err.Error()}
			}
		}
		account := firstName
		if username != "" {
			account = "@" + username
		}
		_ = a.recordHealth(r.Context(), configID, "connected", account, "Webhook registered via Telegram Bot API")
		return map[string]any{"config_id": configID, "health": map[string]any{"config_id": configID, "status": "connected", "account_name": account}, "bot_username": username, "bot_first_name": firstName}, nil
	case "meta", "instagram":
		access, _ := a.Sealer.Decrypt(c.AccessToken)
		client := platform.NewMetaClient(access, c.PageID, derefStr(c.InstagramBusinessID), a.Cfg.Meta.GraphAPIVersion)
		name, err := client.VerifyConnection(r.Context(), c.Platform)
		if err != nil {
			return nil, a.connectionCheckFailed(r.Context(), configID, err)
		}
		_ = a.recordHealth(r.Context(), configID, "connected", name, "Connection verified via Meta Graph API")
		return map[string]any{"config_id": configID, "health": map[string]any{"config_id": configID, "status": "connected", "account_name": name}}, nil
	case "whatsapp":
		access, _ := a.Sealer.Decrypt(c.AccessToken)
		client := platform.NewMetaClient(access, c.PageID, derefStr(c.InstagramBusinessID), a.Cfg.Meta.GraphAPIVersion)
		name, err := client.VerifyConnection(r.Context(), "whatsapp")
		if err != nil {
			return nil, a.connectionCheckFailed(r.Context(), configID, err)
		}
		_ = a.recordHealth(r.Context(), configID, "connected", name, "Connection verified via WhatsApp Cloud API")
		return map[string]any{"config_id": configID, "health": map[string]any{"config_id": configID, "status": "connected", "account_name": name}}, nil
	case "line":
		access, _ := a.Sealer.Decrypt(c.AccessToken)
		client := platform.NewLineClient(access)
		name, _, botUserID, err := client.GetBotInfo(r.Context())
		if err != nil {
			return nil, a.connectionCheckFailed(r.Context(), configID, err)
		}
		// Store the bot's userId: LINE stamps it on every webhook as
		// "destination", and it is the only key that routes an inbound event
		// to this tenant rather than to whichever LINE config came first.
		_ = a.setChannelIdentity(r.Context(), configID, botUserID)
		// Auto-register + self-test the webhook: the merchant never has to
		// touch the LINE Developers console for webhook configuration.
		detail := "Connection verified via LINE Messaging API"
		if webhookURL, werr := a.providerWebhookURL("line"); werr == nil {
			if rerr := client.SetWebhookEndpoint(r.Context(), webhookURL); rerr != nil {
				detail = "Connection verified; webhook auto-registration failed: " + rerr.Error()
			} else if ok, msg, terr := client.TestWebhookEndpoint(r.Context()); terr != nil {
				detail = "Connection verified; webhook registered (self-test error: " + terr.Error() + ")"
			} else if ok {
				detail = "Connection verified; webhook registered and self-test passed via LINE API"
			} else {
				detail = "Connection verified; webhook registered (LINE self-test pending: " + msg + ")"
			}
		}
		_ = a.recordHealth(r.Context(), configID, "connected", name, detail)
		return map[string]any{"config_id": configID, "health": map[string]any{"config_id": configID, "status": "connected", "account_name": name}, "webhook_url": func() string { u, _ := a.providerWebhookURL("line"); return u }()}, nil
	case "zalo":
		access, _ := a.Sealer.Decrypt(c.AccessToken)
		client := platform.NewZaloClient(access)
		name, oaID, err := client.VerifyOA(r.Context())
		if err != nil {
			return nil, a.connectionCheckFailed(r.Context(), configID, err)
		}
		// oa_id is the routing identity carried on every Zalo webhook.
		_ = a.setChannelIdentity(r.Context(), configID, oaID)
		_ = a.recordHealth(r.Context(), configID, "connected", name, "Connection verified via Zalo OA API")
		return map[string]any{"config_id": configID, "health": map[string]any{"config_id": configID, "status": "connected", "account_name": name}}, nil
	default:
		return nil, ErrBadRequest("unsupported platform: " + c.Platform)
	}
}

// setChannelIdentity records the provider-side routing identity learned from
// the provider API at connect time (LINE bot userId / Zalo OA id), so inbound
// webhooks can be matched to this tenant instead of to the first active config
// of that platform. Best-effort: verification still succeeds if the write
// fails, and the webhook resolver will adopt a lone unbound config instead.
func (a *App) setChannelIdentity(ctx context.Context, configID int32, identity string) error {
	if identity == "" {
		return nil
	}
	_, err := a.DB.Exec(ctx,
		"UPDATE platform_configs SET channel_identity = $1 WHERE config_id = $2",
		identity, configID)
	return err
}

// deactivatePlatformConfig — deactivate a config (Telegram webhook removed first).
func (a *App) deactivatePlatformConfig(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	user, _ := UserFrom(r)
	var c platformConfigRow
	row := a.DB.QueryRow(r.Context(), "SELECT "+platformConfigCols+" FROM platform_configs WHERE config_id = $1 AND user_id = $2", configID, user.UserID)
	if err := row.Scan(&c.ConfigID, &c.UserID, &c.Platform, &c.AccessToken, &c.PageID,
		&c.InstagramBusinessID, &c.WhatsAppBusinessAccountID, &c.BotToken, &c.BotTokenHash,
		&c.WebhookSecret, &c.WebhookSecretHash, &c.IsActive, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, ErrNotFound("platform configuration was not found")
	}
	if c.Platform == "telegram" {
		botToken, _ := a.Sealer.Decrypt(derefStr(c.BotToken))
		client := platform.NewTelegramClient(botToken)
		_ = client.DeleteWebhook(r.Context())
	}
	tag, err := a.DB.Exec(r.Context(), "UPDATE platform_configs SET is_active = false WHERE config_id = $1 AND user_id = $2", configID, user.UserID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("platform configuration was not found")
	}
	detail := "Disconnected locally"
	if c.Platform == "telegram" {
		detail = "Disconnected; Telegram webhook removed"
	}
	_ = a.recordHealth(r.Context(), configID, "unknown", "", detail)
	return map[string]string{"message": "已停用"}, nil
}

// ensureConfigOwner reports whether configID belongs to the caller's tenant.
func (a *App) ensureConfigOwner(ctx context.Context, configID, userID int32) bool {
	var owner int32
	return a.DB.QueryRow(ctx, "SELECT user_id FROM platform_configs WHERE config_id = $1", configID).Scan(&owner) == nil && owner == userID
}

// listPlatformWork — failed inbound/outbound + cancelled for a config.
func (a *App) listPlatformWork(w http.ResponseWriter, r *http.Request, configID int32) (any, error) {
	user, _ := UserFrom(r)
	if !a.ensureConfigOwner(r.Context(), configID, user.UserID) {
		return nil, ErrNotFound("platform configuration was not found")
	}
	inbound := make([]map[string]any, 0)
	rows, err := a.DB.Query(r.Context(),
		"SELECT event_id, external_id, platform_user_id, content, status::text, attempts, last_error, created_at FROM platform_inbound_events WHERE config_id = $1 AND status = 'failed' ORDER BY created_at DESC LIMIT 50",
		configID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var eventID int64
			var externalID, puid, content, status string
			var attempts int32
			var lastError *string
			var createdAt time.Time
			if rows.Scan(&eventID, &externalID, &puid, &content, &status, &attempts, &lastError, &createdAt) == nil {
				inbound = append(inbound, map[string]any{"event_id": eventID, "external_id": externalID, "platform_user_id": puid, "content": content, "status": status, "attempts": attempts, "last_error": lastError, "created_at": createdAt})
			}
		}
		// This is the operator's retry queue: a partial list would leave failed
		// events that are never offered for retry, with nothing saying so.
		if err := rows.Err(); err != nil {
			a.Logger.Warn("platform inbound work read failed", "config_id", configID, "error", err.Error())
			return nil, ErrInternal("查询失败")
		}
	}
	deliveries := make([]map[string]any, 0)
	drows, err := a.DB.Query(r.Context(),
		"SELECT delivery_id, chat_message_id, recipient_id, content, status::text, last_error, created_at FROM platform_outbox WHERE config_id = $1 AND status IN ('failed','cancelled') ORDER BY created_at DESC LIMIT 50",
		configID)
	if err == nil {
		defer drows.Close()
		for drows.Next() {
			var deliveryID, chatMessageID int64
			var recipientID, content, status string
			var lastError *string
			var createdAt time.Time
			if drows.Scan(&deliveryID, &chatMessageID, &recipientID, &content, &status, &lastError, &createdAt) == nil {
				deliveries = append(deliveries, map[string]any{"delivery_id": deliveryID, "chat_message_id": chatMessageID, "recipient_id": recipientID, "content": content, "status": status, "last_error": lastError, "created_at": createdAt})
			}
		}
		// Same reasoning as the inbound list above: these are the deliveries the
		// operator may retry, and a silently short list hides the ones that most
		// need retrying.
		if err := drows.Err(); err != nil {
			a.Logger.Warn("platform outbound work read failed", "config_id", configID, "error", err.Error())
			return nil, ErrInternal("查询失败")
		}
	}
	cancelled := make([]map[string]any, 0)
	for _, d := range deliveries {
		if s, _ := d["status"].(string); s == "cancelled" {
			cancelled = append(cancelled, d)
		}
	}
	return map[string]any{"inbound_events": inbound, "deliveries": deliveries, "cancelled_deliveries": cancelled}, nil
}

// retryPlatformEvent — retry a failed inbound event or delivery.
// kind is "inbound-events" or "deliveries".
func (a *App) retryPlatformEvent(w http.ResponseWriter, r *http.Request, configID int32, kind string, id int32) (any, error) {
	user, _ := UserFrom(r)
	if !a.ensureConfigOwner(r.Context(), configID, user.UserID) {
		return nil, ErrNotFound("platform configuration was not found")
	}
	if kind == "inbound-events" {
		tag, err := a.DB.Exec(r.Context(),
			"UPDATE platform_inbound_events SET status='pending', attempts=0, next_attempt_at=NOW(), locked_at=NULL, processed_at=NULL, last_error='' WHERE event_id = $1 AND config_id = $2 AND status='failed'",
			id, configID)
		if err != nil {
			return nil, ErrInternal("更新失败")
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrNotFound("inbound event not found or not failed")
		}
		return map[string]any{"status": "queued"}, nil
	}
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE platform_outbox SET status='pending', attempts=0, next_attempt_at=NOW(), locked_at=NULL, sent_at=NULL, last_error='' WHERE delivery_id = $1 AND config_id = $2 AND status='failed'",
		id, configID)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("delivery not found or not failed")
	}
	return map[string]any{"status": "queued"}, nil
}
