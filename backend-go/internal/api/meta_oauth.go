package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// stripURLErr drops the *url.Error wrapper, whose message embeds the full
// request URL. Meta request URLs must stay free of credentials AND free of
// echo: keeping only the underlying cause (timeout, DNS, refused, TLS) means
// any error string that reaches a tenant or a log can never carry request
// material.
func stripURLErr(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// ============================================
// Meta OAuth (Facebook / Instagram Login)
// ============================================

const metaOAuthSessionTTL = 10 * time.Minute

type metaOAuthPage struct {
	PageID            string `json:"page_id"`
	PageName          string `json:"page_name"`
	PageAccessToken   string `json:"page_access_token"`
	InstagramBusiness string `json:"instagram_business_id"`
	InstagramName     string `json:"instagram_name"`
}

func (a *App) metaOAuthEnabled() bool {
	c := a.Cfg.Meta
	return strings.TrimSpace(c.AppID) != "" && strings.TrimSpace(c.AppSecret) != "" &&
		validURL(c.OAuthRedirectURL) && validURL(c.OAuthFrontendURL)
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

func (a *App) graphVersion() string {
	v := strings.TrimSpace(a.Cfg.Meta.GraphAPIVersion)
	v = strings.TrimPrefix(v, "/")
	if v == "" {
		return "v24.0"
	}
	return v
}

func (a *App) graphAPIBase() string {
	return "https://graph.facebook.com/" + a.graphVersion()
}

func (a *App) authorizationURL(oauthState string) string {
	c := a.Cfg.Meta
	scope := strings.TrimSpace(c.OAuthScopes)
	if scope == "" {
		// Official permission names for Messenger-API Instagram messaging
		// (Meta docs: business-messaging/instagram-messaging/get-started).
		// They are only valid for apps that enabled Instagram in Messenger
		// settings — until then the dialog hard-fails for developers on
		// invalid scopes; set META_OAUTH_SCOPES without the instagram_*
		// entries to unblock Messenger-only connections meanwhile.
		scope = strings.Join([]string{
			"pages_show_list", "pages_manage_metadata", "pages_messaging",
			"instagram_basic", "instagram_manage_messages",
		}, ",")
	}
	return fmt.Sprintf("https://www.facebook.com/%s/dialog/oauth?client_id=%s&redirect_uri=%s&response_type=code&state=%s&scope=%s",
		a.graphVersion(),
		url.QueryEscape(c.AppID),
		url.QueryEscape(c.OAuthRedirectURL),
		url.QueryEscape(oauthState),
		url.QueryEscape(scope),
	)
}

// metaRequest — Meta Graph API helper. Credentials never travel in the URL:
// the access token rides the Authorization header, and POST parameters (which
// include client_secret during code exchanges) go in the form body, because a
// transport-level *url.Error renders the full request URL into the error
// string that reaches tenants on failure paths.
func (a *App) metaRequest(ctx context.Context, method, path string, params [][2]string, accessToken string) (map[string]any, error) {
	u := a.graphAPIBase() + path
	client := &http.Client{Timeout: 20 * time.Second}
	var req *http.Request
	var err error
	if method == "POST" {
		form := url.Values{}
		for _, kv := range params {
			form.Set(kv[0], kv[1])
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		var query []string
		for _, kv := range params {
			query = append(query, url.QueryEscape(kv[0])+"="+url.QueryEscape(kv[1]))
		}
		if len(query) > 0 {
			u += "?" + strings.Join(query, "&")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "OAuth "+accessToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Meta API request failed: %w", stripURLErr(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Meta API request failed: %w", stripURLErr(err))
	}
	text := string(body)
	if resp.StatusCode >= 400 {
		preview := text
		if len(preview) > 512 {
			preview = preview[:512]
		}
		return nil, fmt.Errorf("Meta API returned HTTP %d: %s", resp.StatusCode, preview)
	}
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return nil, fmt.Errorf("decode Meta API response: %w", err)
	}
	return v, nil
}

// metaOAuthStart — return the Facebook Login URL.
func (a *App) metaOAuthStart(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if !a.metaOAuthEnabled() {
		return nil, ErrServiceUnavailable("Meta OAuth is not configured. Set META_APP_ID, META_APP_SECRET, META_OAUTH_REDIRECT_URL, and META_OAUTH_FRONTEND_URL.")
	}
	oauthState := newOAuthState()
	stateHash := sha256HexStr(oauthState)
	now := time.Now()
	sessionID := newUUID()
	if _, err := a.DB.Exec(r.Context(),
		"INSERT INTO platform_oauth_sessions (session_id, user_id, state_hash, payload_json, expires_at, created_at) VALUES ($1,$2,$3,'[]',$4,$5)",
		sessionID, user.UserID, stateHash, now.Add(metaOAuthSessionTTL), now); err != nil {
		return nil, ErrInternal("Could not start Meta authorization")
	}
	return map[string]any{"authorize_url": a.authorizationURL(oauthState)}, nil
}

// metaOAuthSession — selectable page metadata (tokens never exposed).
func (a *App) metaOAuthSession(w http.ResponseWriter, r *http.Request, sessionIDStr string) (any, error) {
	user, _ := UserFrom(r)
	var payload string
	var expiresAt time.Time
	err := a.DB.QueryRow(r.Context(),
		"SELECT payload_json::text, expires_at FROM platform_oauth_sessions WHERE session_id = $1 AND user_id = $2 AND expires_at > NOW() AND oauth_completed_at IS NOT NULL AND consumed_at IS NULL",
		sessionIDStr, user.UserID).Scan(&payload, &expiresAt)
	if err != nil {
		return nil, ErrNotFound("OAuth session was not found or has expired")
	}
	pages, err := decodeMetaPages(payload)
	if err != nil {
		return nil, ErrInternal("Could not read authorized Meta accounts")
	}
	options := make([]map[string]any, 0)
	for _, p := range pages {
		opt := map[string]any{"page_id": p.PageID, "page_name": p.PageName}
		if p.InstagramBusiness != "" {
			opt["instagram_business_id"] = p.InstagramBusiness
			opt["instagram_name"] = p.InstagramName
		}
		options = append(options, opt)
	}
	return map[string]any{"session_id": sessionIDStr, "pages": options, "expires_at": expiresAt}, nil
}

type completeMetaOAuthRequest struct {
	SessionID       string `json:"session_id"`
	PageID          string `json:"page_id"`
	EnableMessenger bool   `json:"enable_messenger"`
	EnableInstagram bool   `json:"enable_instagram"`
}

// metaOAuthComplete — subscribe chosen page(s), save configs, consume session.
func (a *App) metaOAuthComplete(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req completeMetaOAuthRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if !req.EnableMessenger && !req.EnableInstagram {
		return nil, ErrBadRequest("Select Messenger, Instagram, or both")
	}
	var payload string
	err := a.DB.QueryRow(r.Context(),
		"SELECT payload_json::text FROM platform_oauth_sessions WHERE session_id = $1 AND user_id = $2 AND expires_at > NOW() AND oauth_completed_at IS NOT NULL AND consumed_at IS NULL",
		req.SessionID, user.UserID).Scan(&payload)
	if err != nil {
		return nil, ErrNotFound("OAuth session was not found or has expired")
	}
	pages, err := decodeMetaPages(payload)
	if err != nil {
		return nil, ErrInternal("Could not read authorized Meta accounts")
	}
	var page *metaOAuthPage
	for i := range pages {
		if pages[i].PageID == req.PageID && pages[i].PageAccessToken != "" {
			page = &pages[i]
			break
		}
	}
	if page == nil {
		return nil, ErrBadRequest("The selected Meta Page is not available")
	}
	if req.EnableInstagram && page.InstagramBusiness == "" {
		return nil, ErrBadRequest("The selected Page has no connected Instagram professional account")
	}
	if !a.metaOAuthEnabled() {
		return nil, ErrServiceUnavailable("Meta OAuth is not configured")
	}

	// Subscribe webhooks before saving.
	if req.EnableMessenger {
		_, err := a.metaRequest(r.Context(), "POST", "/"+url.QueryEscape(page.PageID)+"/subscribed_apps",
			[][2]string{{"subscribed_fields", "messages,messaging_postbacks,message_deliveries,message_reads"}}, page.PageAccessToken)
		if err != nil {
			return nil, &ApiError{http.StatusBadGateway, err.Error()}
		}
	}
	if req.EnableInstagram {
		subFields := [][2]string{{"subscribed_fields", "messages,messaging_postbacks,message_deliveries,message_reads"}}
		// Instagram messaging webhooks arrive with the Instagram account ID as
		// the entry id; subscribe that account, falling back to the Page (which
		// also delivers Instagram messages) so the connection still goes live.
		_, err := a.metaRequest(r.Context(), "POST", "/"+url.QueryEscape(page.InstagramBusiness)+"/subscribed_apps", subFields, page.PageAccessToken)
		if err != nil {
			if _, pageErr := a.metaRequest(r.Context(), "POST", "/"+url.QueryEscape(page.PageID)+"/subscribed_apps", subFields, page.PageAccessToken); pageErr != nil {
				return nil, &ApiError{http.StatusBadGateway, err.Error()}
			}
		}
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return nil, ErrInternal("tx begin")
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var configIDs []int32
	configsJSON := make([]map[string]any, 0)
	if req.EnableMessenger {
		cid, err := a.saveMetaConfigTx(r.Context(), tx, user.UserID, "meta", page.PageID, page.PageAccessToken, a.Cfg.Meta.AppSecret)
		if err != nil {
			return nil, err
		}
		configIDs = append(configIDs, cid)
		configsJSON = append(configsJSON, map[string]any{"config_id": cid, "platform": "meta", "user_id": user.UserID, "page_id": page.PageID, "is_active": true})
	}
	if req.EnableInstagram {
		cid, err := a.saveMetaConfigTx(r.Context(), tx, user.UserID, "instagram", page.InstagramBusiness, page.PageAccessToken, a.Cfg.Meta.AppSecret)
		if err != nil {
			return nil, err
		}
		configIDs = append(configIDs, cid)
		configsJSON = append(configsJSON, map[string]any{"config_id": cid, "platform": "instagram", "user_id": user.UserID, "instagram_business_id": page.InstagramBusiness, "is_active": true})
	}
	tag, err := tx.Exec(r.Context(),
		"UPDATE platform_oauth_sessions SET consumed_at = NOW() WHERE session_id = $1 AND user_id = $2 AND consumed_at IS NULL",
		req.SessionID, user.UserID)
	if err != nil {
		return nil, ErrInternal("consume session")
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrConflict("This OAuth session has already been used")
	}
	if err := tx.Commit(r.Context()); err != nil {
		return nil, ErrInternal("tx commit")
	}
	for _, cid := range configIDs {
		account := page.PageName
		if req.EnableInstagram {
			account = page.InstagramName
		}
		_ = a.recordHealth(r.Context(), cid, "unknown", account, "Webhook subscription completed through Meta authorization; waiting for a signed webhook event")
	}
	return map[string]any{"configs": configsJSON}, nil
}

// saveMetaConfigTx saves/refreshes a Meta config inside a transaction.
func (a *App) saveMetaConfigTx(ctx context.Context, tx pgx.Tx, userID int32, platformType, integrationID, pageToken, appSecret string) (int32, error) {
	var conflict int64
	if platformType == "instagram" {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM platform_configs WHERE platform='instagram' AND is_active=true AND user_id <> $1 AND instagram_business_id = $2",
			userID, integrationID).Scan(&conflict); err != nil {
			return 0, ErrInternal("冲突检查失败")
		}
	} else {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM platform_configs WHERE platform='meta' AND is_active=true AND user_id <> $1 AND page_id = $2",
			userID, integrationID).Scan(&conflict); err != nil {
			return 0, ErrInternal("冲突检查失败")
		}
	}
	if conflict > 0 {
		return 0, ErrConflict("This Meta account is already connected to another customer")
	}
	var existing *int32
	var cid int32
	var err error
	if platformType == "instagram" {
		err = tx.QueryRow(ctx, "SELECT config_id FROM platform_configs WHERE platform='instagram' AND user_id = $1 AND instagram_business_id = $2 ORDER BY config_id DESC LIMIT 1", userID, integrationID).Scan(&cid)
	} else {
		err = tx.QueryRow(ctx, "SELECT config_id FROM platform_configs WHERE platform='meta' AND user_id = $1 AND page_id = $2 ORDER BY config_id DESC LIMIT 1", userID, integrationID).Scan(&cid)
	}
	if err == nil {
		existing = &cid
	}
	encToken, err := a.Sealer.Encrypt(pageToken)
	if err != nil {
		return 0, ErrInternal("加密失败")
	}
	encSecret, _ := a.Sealer.Encrypt(appSecret)
	secretHash := sha256HexStr(appSecret)
	if existing != nil {
		if _, err := tx.Exec(ctx,
			"UPDATE platform_configs SET access_token=$2, webhook_secret=$3, webhook_secret_hash=$4, is_active=true WHERE config_id=$1",
			*existing, encToken, encSecret, secretHash); err != nil {
			return 0, ErrInternal("save meta config")
		}
		return *existing, nil
	}
	pageIDVal := ""
	igVal := ""
	if platformType == "meta" {
		pageIDVal = integrationID
	} else {
		igVal = integrationID
	}
	// Only a new Meta/Instagram connection consumes the plan's channel allowance
	// (the update path above returns early for an existing config).
	if err := a.checkChannelLimit(ctx, userID); err != nil {
		return 0, err
	}
	var configID int32
	if err := tx.QueryRow(ctx,
		"INSERT INTO platform_configs (user_id, platform, access_token, page_id, instagram_business_id, webhook_secret, webhook_secret_hash, is_active) VALUES ($1,$2::platform_type,$3,$4,$5,$6,$7,true) RETURNING config_id",
		userID, platformType, encToken, pageIDVal, igVal, encSecret, secretHash).Scan(&configID); err != nil {
		return 0, ErrInternal("save meta config")
	}
	return configID, nil
}

func decodeMetaPages(payload string) ([]metaOAuthPage, error) {
	var pages []metaOAuthPage
	if err := json.Unmarshal([]byte(payload), &pages); err != nil {
		return nil, err
	}
	return pages, nil
}

// MetaOAuthCallbackRaw exposes the public OAuth callback for the webhook mux.
func (a *App) MetaOAuthCallbackRaw() http.HandlerFunc {
	return a.metaOAuthCallback
}

// metaOAuthCallback — public browser endpoint: exchange code, 303 to frontend.
func (a *App) metaOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	queryCode := q.Get("code")
	queryState := q.Get("state")
	queryError := q.Get("error")
	frontend := a.Cfg.Meta.OAuthFrontendURL
	redirect := func(extra string) {
		http.Redirect(w, r, frontend+"?"+extra, http.StatusSeeOther)
	}
	if !a.metaOAuthEnabled() {
		redirect("meta_oauth_error=not_configured")
		return
	}
	if queryState == "" {
		redirect("meta_oauth_error=missing_state")
		return
	}
	stateHash := sha256HexStr(queryState)
	var sessionID string
	err := a.DB.QueryRow(r.Context(),
		"SELECT session_id FROM platform_oauth_sessions WHERE state_hash = $1 AND expires_at > NOW() AND oauth_completed_at IS NULL",
		stateHash).Scan(&sessionID)
	if err != nil {
		redirect("meta_oauth_error=invalid_state")
		return
	}
	if queryError != "" || queryCode == "" {
		_ = a.consumeOAuthState(r.Context(), sessionID)
		redirect("meta_oauth_error=cancelled")
		return
	}
	userToken, err := a.exchangeCode(r.Context(), queryCode)
	if err != nil {
		_ = a.consumeOAuthState(r.Context(), sessionID)
		redirect("meta_oauth_error=failed")
		return
	}
	longToken, err := a.exchangeLongLived(r.Context(), userToken)
	if err != nil {
		_ = a.consumeOAuthState(r.Context(), sessionID)
		redirect("meta_oauth_error=failed")
		return
	}
	pages, err := a.listPages(r.Context(), longToken)
	if err != nil {
		_ = a.consumeOAuthState(r.Context(), sessionID)
		redirect("meta_oauth_error=failed")
		return
	}
	payload, _ := json.Marshal(pages)
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE platform_oauth_sessions SET payload_json = $1::jsonb, oauth_completed_at = NOW() WHERE session_id = $2 AND oauth_completed_at IS NULL",
		string(payload), sessionID)
	if err != nil {
		redirect("meta_oauth_error=failed")
		return
	}
	if tag.RowsAffected() == 1 {
		redirect("meta_oauth_session=" + sessionID)
	} else {
		redirect("meta_oauth_error=already_used")
	}
}

func (a *App) consumeOAuthState(ctx context.Context, sessionID string) error {
	_, err := a.DB.Exec(ctx, "UPDATE platform_oauth_sessions SET oauth_completed_at = NOW() WHERE session_id = $1 AND oauth_completed_at IS NULL", sessionID)
	return err
}

func (a *App) exchangeCode(ctx context.Context, code string) (string, error) {
	c := a.Cfg.Meta
	v, err := a.metaRequest(ctx, "POST", "/oauth/access_token", [][2]string{
		{"client_id", c.AppID}, {"client_secret", c.AppSecret},
		{"redirect_uri", c.OAuthRedirectURL}, {"code", code},
	}, "")
	if err != nil {
		return "", err
	}
	if t, ok := v["access_token"].(string); ok {
		return t, nil
	}
	return "", fmt.Errorf("Meta authorization code exchange failed")
}

func (a *App) exchangeLongLived(ctx context.Context, userToken string) (string, error) {
	c := a.Cfg.Meta
	v, err := a.metaRequest(ctx, "POST", "/oauth/access_token", [][2]string{
		{"grant_type", "fb_exchange_token"}, {"client_id", c.AppID},
		{"client_secret", c.AppSecret}, {"fb_exchange_token", userToken},
	}, "")
	if err != nil {
		return "", err
	}
	if t, ok := v["access_token"].(string); ok {
		return t, nil
	}
	return "", fmt.Errorf("Meta long-lived token exchange failed")
}

func (a *App) listPages(ctx context.Context, userToken string) ([]metaOAuthPage, error) {
	v, err := a.metaRequest(ctx, "GET", "/me/accounts", [][2]string{
		{"fields", "id,name,access_token,instagram_business_account{id,name,username}"},
		{"limit", "100"},
	}, userToken)
	if err != nil {
		return nil, err
	}
	pages := make([]metaOAuthPage, 0)
	data, _ := v["data"].([]any)
	for _, item := range data {
		m, _ := item.(map[string]any)
		id, _ := m["id"].(string)
		token, _ := m["access_token"].(string)
		if id == "" || token == "" {
			continue
		}
		ig, _ := m["instagram_business_account"].(map[string]any)
		igID := ""
		igName := ""
		if ig != nil {
			igID, _ = ig["id"].(string)
			igName, _ = ig["name"].(string)
			if igName == "" {
				igName, _ = ig["username"].(string)
			}
		}
		name, _ := m["name"].(string)
		pages = append(pages, metaOAuthPage{PageID: id, PageName: name, PageAccessToken: token, InstagramBusiness: igID, InstagramName: igName})
	}
	return pages, nil
}

// ============================================
// WhatsApp Embedded Signup
// ============================================

func (a *App) embeddedSignupEnabled() bool {
	c := a.Cfg.Meta
	return strings.TrimSpace(c.AppID) != "" && strings.TrimSpace(c.AppSecret) != "" &&
		strings.TrimSpace(c.WhatsAppEmbeddedSignupConfigID) != ""
}

// embeddedSignupConfig — app_id + config_id for the frontend SDK.
func (a *App) embeddedSignupConfig(w http.ResponseWriter, r *http.Request) (any, error) {
	if !a.embeddedSignupEnabled() {
		return nil, ErrServiceUnavailable("WhatsApp Embedded Signup is not configured")
	}
	return map[string]any{"app_id": a.Cfg.Meta.AppID, "config_id": a.Cfg.Meta.WhatsAppEmbeddedSignupConfigID}, nil
}

type completeWhatsAppSignupRequest struct {
	Code              string `json:"code"`
	PhoneNumberID     string `json:"phone_number_id"`
	BusinessAccountID string `json:"business_account_id"`
}

// embeddedSignupComplete — exchange code, verify phone/WABA, subscribe, save.
func (a *App) embeddedSignupComplete(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req completeWhatsAppSignupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	phone := strings.TrimSpace(req.PhoneNumberID)
	waba := strings.TrimSpace(req.BusinessAccountID)
	code := strings.TrimSpace(req.Code)
	if !validMetaObjectID(phone) || !validMetaObjectID(waba) {
		return nil, ErrBadRequest("Invalid WhatsApp account identifier")
	}
	if !a.embeddedSignupEnabled() {
		return nil, ErrServiceUnavailable("WhatsApp Embedded Signup is not configured")
	}
	c := a.Cfg.Meta
	tokenV, err := a.metaRequest(r.Context(), "POST", "/oauth/access_token", [][2]string{
		{"client_id", c.AppID}, {"client_secret", c.AppSecret}, {"code", code},
	}, "")
	if err != nil {
		return nil, &ApiError{http.StatusBadGateway, err.Error()}
	}
	accessToken, _ := tokenV["access_token"].(string)
	if accessToken == "" {
		return nil, &ApiError{http.StatusBadGateway, "Meta rejected the WhatsApp authorization code"}
	}
	verifyV, err := a.metaRequest(r.Context(), "GET", "/"+phone, [][2]string{{"fields", "whatsapp_business_account{id}"}}, accessToken)
	if err != nil {
		return nil, &ApiError{http.StatusBadGateway, err.Error()}
	}
	verifyWaba := ""
	if w, ok := verifyV["whatsapp_business_account"].(map[string]any); ok {
		verifyWaba, _ = w["id"].(string)
	}
	if verifyWaba != waba {
		return nil, &ApiError{http.StatusBadGateway, "The selected WhatsApp phone number does not belong to the selected business account"}
	}
	if _, err := a.metaRequest(r.Context(), "POST", "/"+waba+"/subscribed_apps", nil, accessToken); err != nil {
		return nil, &ApiError{http.StatusBadGateway, err.Error()}
	}
	var conflict int64
	_ = a.DB.QueryRow(r.Context(),
		"SELECT COUNT(*) FROM platform_configs WHERE platform='whatsapp' AND is_active=true AND user_id <> $1 AND page_id = $2",
		user.UserID, phone).Scan(&conflict)
	if conflict > 0 {
		return nil, ErrConflict("This WhatsApp phone number is already connected to another customer")
	}
	encToken, err := a.Sealer.Encrypt(accessToken)
	if err != nil {
		return nil, ErrInternal("加密失败")
	}
	encSecret, _ := a.Sealer.Encrypt(c.AppSecret)
	secretHash := sha256HexStr(c.AppSecret)
	// Plan limit before saving: the embedded-signup path always creates a new
	// config (there is no update branch here).
	if err := a.checkChannelLimit(r.Context(), user.UserID); err != nil {
		return nil, err
	}
	var configID int32
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO platform_configs (user_id, platform, access_token, page_id, whatsapp_business_account_id, webhook_secret, webhook_secret_hash, is_active) VALUES ($1,'whatsapp',$2,$3,$4,$5,$6,true) RETURNING config_id",
		user.UserID, encToken, phone, waba, encSecret, secretHash).Scan(&configID); err != nil {
		return nil, ErrInternal("Could not save WhatsApp connection")
	}
	_ = a.recordHealth(r.Context(), configID, "unknown", phone, "Embedded Signup completed; waiting for a signed WhatsApp event")
	return map[string]any{"config_id": configID, "platform": "whatsapp", "user_id": user.UserID, "page_id": phone, "whatsapp_business_account_id": waba, "is_active": true}, nil
}

func validMetaObjectID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ============================================
// helpers
// ============================================

func newOAuthState() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func sha256HexStr(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
