// Package api — Telegram (OIDC) sign-in.
//
// Mirrors the Google flow in google_sso.go: authorize → callback → one-time
// code → JWT. Two deliberate differences:
//
//   - PKCE (S256). Telegram advertises code_challenge_methods_supported, so the
//     authorization code is bound to this browser session and is useless if it
//     leaks from the redirect URL.
//   - No e-mail. Telegram returns sub / preferred_username / name / picture /
//     phone_number. Accounts are keyed on `sub` only, and users.email is
//     nullable since migration 053.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/textutil"
)

const (
	telegramAuthEndpoint  = "https://oauth.telegram.org/auth"
	telegramTokenEndpoint = "https://oauth.telegram.org/token"
	telegramIssuer        = "https://oauth.telegram.org"
)

// telegramSSOEnabled — all four settings must be present.
func (a *App) telegramSSOEnabled() bool {
	c := a.Cfg.TelegramLogin
	return c.Enabled && c.ClientID != "" && c.ClientSecret != "" &&
		c.RedirectURL != "" && c.FrontendURL != ""
}

// telegramScopes — `openid` is required; `profile` yields name/picture and
// preferred_username. `phone` returns Telegram's verified number, which is
// worth capturing because SMS verification is costly in this market.
func (a *App) telegramScopes() string {
	scopes := "openid profile"
	if a.Cfg.TelegramLogin.RequestPhone {
		scopes += " phone"
	}
	return scopes
}

// telegramAuthURL builds the consent URL. codeChallenge is the S256 PKCE
// challenge derived from the verifier held in Redis until the callback.
func (a *App) telegramAuthURL(state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", a.Cfg.TelegramLogin.ClientID)
	q.Set("redirect_uri", a.Cfg.TelegramLogin.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", a.telegramScopes())
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return telegramAuthEndpoint + "?" + q.Encode()
}

// telegramStart — GET /api/v1/auth/telegram/start: 302 to Telegram.
func (a *App) telegramStart(w http.ResponseWriter, r *http.Request) {
	if !a.telegramSSOEnabled() {
		a.telegramRedirectError(w, r, "not_configured")
		return
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		a.telegramRedirectError(w, r, "failed")
		return
	}
	state := base64.RawURLEncoding.EncodeToString(buf)

	verifierBuf := make([]byte, 32)
	if _, err := rand.Read(verifierBuf); err != nil {
		a.telegramRedirectError(w, r, "failed")
		return
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBuf)

	// The state key holds the PKCE verifier, so the callback can complete the
	// exchange without trusting anything the browser sends back except the
	// opaque state. 10 minutes, one-time use.
	_ = a.Redis.SetString(r.Context(), "telegram-state:"+state, verifier, 10*time.Minute)
	// Bind the flow to the browser that started it (sso_flow cookie) — same
	// login-CSRF fix as the Google flow.
	a.ssoFlowStart(w, r, "telegram", state)

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	http.Redirect(w, r, a.telegramAuthURL(state, challenge), http.StatusFound)
}

// telegramCallback — GET /api/v1/auth/telegram/callback: verify state, exchange
// the code with PKCE, validate the id_token, find-or-create the user, then hand
// the frontend a one-time login code it swaps for the JWT.
func (a *App) telegramCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.telegramSSOEnabled() {
		a.telegramRedirectError(w, r, "not_configured")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		a.telegramRedirectError(w, r, "invalid_state")
		return
	}
	verifier, err := a.Redis.GetString(ctx, "telegram-state:"+state)
	if err != nil || verifier == "" {
		a.telegramRedirectError(w, r, "invalid_state")
		return
	}
	// The state must belong to the flow this browser started (mirrors the
	// Google callback; without it the callback is login-CSRF bait).
	if !a.ssoFlowMatch(r, "telegram", state) {
		a.telegramRedirectError(w, r, "invalid_state")
		return
	}
	// NOTE: the state key is deliberately NOT deleted here — it lives until its
	// 10-minute TTL so the login-code exchange can prove the same browser
	// started the flow (mirrors googleCallback).

	if errCode := r.URL.Query().Get("error"); errCode != "" {
		a.telegramRedirectError(w, r, "cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.telegramRedirectError(w, r, "failed")
		return
	}

	claims, err := a.exchangeTelegramCode(ctx, code, verifier)
	if err != nil {
		a.Logger.Warn("telegram sso exchange failed", "error", err.Error())
		a.telegramRedirectError(w, r, "failed")
		return
	}
	if claims.Sub == "" {
		a.telegramRedirectError(w, r, "failed")
		return
	}

	userID, username, role, isActive, err := a.findOrCreateTelegramUser(ctx, claims)
	if err != nil {
		a.Logger.Warn("telegram sso user provisioning failed", "error", err.Error())
		reason := "failed"
		if strings.Contains(err.Error(), "signup disabled") {
			reason = "signup_disabled"
		}
		a.telegramRedirectError(w, r, reason)
		return
	}
	if !isActive {
		a.telegramRedirectError(w, r, "disabled")
		return
	}
	// Accounts with TOTP enabled must not bypass the second factor by taking
	// the Telegram route — same posture as the Google flow.
	var totpEnabled bool
	_ = a.DB.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM user_totp WHERE user_id = $1 AND enabled = true)", userID).Scan(&totpEnabled)
	if totpEnabled {
		a.telegramRedirectError(w, r, "totp_required")
		return
	}
	var tokenVersion int
	_ = a.DB.QueryRow(ctx, "SELECT token_version FROM users WHERE user_id = $1", userID).Scan(&tokenVersion)
	token, err := a.JWT.GenerateToken(userID, username, role, tokenVersion)
	if err != nil {
		a.telegramRedirectError(w, r, "failed")
		return
	}
	// Hand the token to the SPA through a short-lived one-time code rather than
	// the URL fragment (keeps it out of history/referrer). The state rides in
	// the payload so the exchange can verify code↔flow↔state.
	loginCode := newUUIDv4()
	payload, _ := json.Marshal(map[string]any{
		"token": token,
		"state": state,
		"user": map[string]any{
			// Telegram supplies no address; the SPA renders a blank rather
			// than a fabricated one.
			"user_id": userID, "username": username, "email": "", "role": role,
		},
	})
	if err := a.Redis.SetString(ctx, "telegram-login:"+loginCode, string(payload), 2*time.Minute); err != nil {
		a.telegramRedirectError(w, r, "failed")
		return
	}
	http.Redirect(w, r,
		a.Cfg.TelegramLogin.FrontendURL+"?telegram_code="+url.QueryEscape(loginCode)+"&state="+url.QueryEscape(state),
		http.StatusFound)
}

// exchangeTelegramCode POSTs the authorization code plus the PKCE verifier to
// Telegram's token endpoint and returns the parsed id_token claims.
func (a *App) exchangeTelegramCode(ctx context.Context, code, codeVerifier string) (*telegramClaims, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", a.Cfg.TelegramLogin.ClientID)
	form.Set("client_secret", a.Cfg.TelegramLogin.ClientSecret)
	form.Set("redirect_uri", a.Cfg.TelegramLogin.RedirectURL)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error_description"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil, fmt.Errorf("token response decode")
	}
	if resp.StatusCode != http.StatusOK || body.IDToken == "" {
		return nil, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, body.Error)
	}
	return parseTelegramIDToken(body.IDToken, a.Cfg.TelegramLogin.ClientID)
}

type telegramClaims struct {
	Sub      string
	Username string // preferred_username — mutable, display only
	Name     string
	Picture  string
	Phone    string
}

// parseTelegramIDToken decodes the JWT payload. The token arrives directly from
// Telegram over TLS in response to our client_secret-authenticated request, so
// the transport is the authenticity guarantee here — the same posture
// parseGoogleIDToken takes. Audience and issuer are still checked so a token
// minted for a different client cannot be replayed at us.
func parseTelegramIDToken(idToken, clientID string) (*telegramClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode id_token payload: %w", err)
	}
	var raw struct {
		Iss               string          `json:"iss"`
		Aud               json.RawMessage `json:"aud"`
		Sub               string          `json:"sub"`
		PreferredUsername string          `json:"preferred_username"`
		Name              string          `json:"name"`
		Picture           string          `json:"picture"`
		PhoneNumber       string          `json:"phone_number"`
		Exp               int64           `json:"exp"`
	}
	if json.Unmarshal(payload, &raw) != nil {
		return nil, fmt.Errorf("id_token claims")
	}
	// `aud` may be a bare string or an array, depending on the provider build.
	if !audienceMatches(raw.Aud, clientID) {
		return nil, fmt.Errorf("id_token audience mismatch")
	}
	if strings.TrimSuffix(raw.Iss, "/") != telegramIssuer {
		return nil, fmt.Errorf("id_token issuer mismatch")
	}
	if raw.Exp > 0 && time.Now().Unix() > raw.Exp {
		return nil, fmt.Errorf("id_token expired")
	}
	return &telegramClaims{
		Sub:      raw.Sub,
		Username: raw.PreferredUsername,
		Name:     raw.Name,
		Picture:  raw.Picture,
		Phone:    raw.PhoneNumber,
	}, nil
}

// audienceMatches accepts either the string or the array form of `aud`.
func audienceMatches(raw json.RawMessage, clientID string) bool {
	if len(raw) == 0 {
		return false
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == clientID
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == clientID {
				return true
			}
		}
	}
	return false
}

// telegramUsernameRe strips anything that is not safe in a username. Telegram
// @usernames allow [A-Za-z0-9_], but a display name can contain anything.
var telegramUsernameRe = regexp.MustCompile(`[^a-z0-9_]+`)

// findOrCreateTelegramUser resolves the Telegram subject to a local account:
//  1. telegram_sub match (returning user);
//  2. create a new account when signup is allowed.
//
// Unlike the Google flow there is no e-mail step: Telegram supplies no address,
// so there is nothing to match an existing password account on. That also
// removes the pre-hijack concern the Google path guards against — nobody can
// claim an existing account here, because proving a Telegram identity proves
// only that identity.
func (a *App) findOrCreateTelegramUser(ctx context.Context, c *telegramClaims) (int32, string, string, bool, error) {
	var userID int32
	var username, role string
	var isActive bool
	err := a.DB.QueryRow(ctx,
		"SELECT user_id, username, role::text, is_active FROM users WHERE telegram_sub = $1", c.Sub).
		Scan(&userID, &username, &role, &isActive)
	if err == nil {
		// Refresh the profile fields Telegram owns; the user may have changed
		// their display name or photo since the last sign-in.
		a.updateTelegramProfile(ctx, userID, c)
		return userID, username, role, isActive, nil
	}

	if !a.Cfg.TelegramLogin.AllowSignup {
		return 0, "", "", false, fmt.Errorf("telegram signup disabled")
	}

	// Prefer the Telegram handle; fall back to the immutable subject when it is
	// absent (many accounts have no @username) or already taken locally.
	base := telegramUsernameRe.ReplaceAllString(strings.ToLower(c.Username), "")
	if base == "" || len(base) > 40 {
		base = "tg_" + telegramUsernameRe.ReplaceAllString(strings.ToLower(c.Sub), "")
	}
	if base == "" {
		base = "tg_user"
	}
	for i := 0; i < 20; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s%d", base, i+1)
		}
		err = a.DB.QueryRow(ctx,
			"INSERT INTO users (username, email, display_name, avatar_url, phone, telegram_sub, role, is_active) "+
				"VALUES ($1,NULL,$2,$3,$4,$5,'user',true) RETURNING user_id",
			candidate, textutil.NullIfEmpty(c.Name), textutil.NullIfEmpty(c.Picture), textutil.NullIfEmpty(c.Phone), c.Sub).Scan(&userID)
		if err == nil {
			return userID, candidate, "user", true, nil
		}
		if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "unique") {
			return 0, "", "", false, err
		}
	}
	return 0, "", "", false, fmt.Errorf("could not allocate username")
}

// updateTelegramProfile writes the fields Telegram is authoritative for. Only
// non-empty values are written, so a user who cleared their Telegram photo does
// not lose the avatar they set here.
func (a *App) updateTelegramProfile(ctx context.Context, userID int32, c *telegramClaims) {
	_, _ = a.DB.Exec(ctx,
		"UPDATE users SET display_name = COALESCE(NULLIF($1,''), display_name), "+
			"avatar_url = COALESCE(NULLIF($2,''), avatar_url), "+
			"phone = COALESCE(NULLIF($3,''), phone), updated_at = NOW() WHERE user_id = $4",
		c.Name, c.Picture, c.Phone, userID)
}

// nullIfEmpty maps "" to SQL NULL so an absent optional claim does not store a
// misleading empty string.

// telegramRedirectError sends the browser back to the login page with a reason.
func (a *App) telegramRedirectError(w http.ResponseWriter, r *http.Request, reason string) {
	target := a.Cfg.TelegramLogin.FrontendURL
	if target == "" {
		target = "/login"
	}
	http.Redirect(w, r, target+"?telegram_error="+url.QueryEscape(reason), http.StatusFound)
}

// telegramLoginExchange — POST /api/v1/auth/telegram/exchange {code,state}:
// swaps the one-time code for the JWT payload (single use, 2-minute TTL).
func (a *App) telegramLoginExchange(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	code := strings.TrimSpace(req.Code)
	state := strings.TrimSpace(req.State)
	if code == "" || len(code) > 64 {
		return nil, ErrBadRequest("无效的登录码")
	}
	if state == "" {
		return nil, ErrBadRequest("缺少登录会话标识")
	}
	stateKey := "telegram-state:" + state
	if v, err := a.Redis.GetString(r.Context(), stateKey); err != nil || v == "" {
		return nil, ErrUnauthorized("登录会话已失效，请重新登录")
	}
	if !a.ssoFlowMatch(r, "telegram", state) {
		return nil, ErrUnauthorized("登录会话与当前浏览器不匹配，请重新登录")
	}
	_ = a.Redis.Del(r.Context(), stateKey)
	a.ssoFlowConsume(r, "telegram")
	raw, err := a.Redis.GetString(r.Context(), "telegram-login:"+code)
	if err != nil || raw == "" {
		return nil, ErrUnauthorized("登录码已失效，请重新登录")
	}
	_ = a.Redis.Del(r.Context(), "telegram-login:"+code)
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return nil, ErrInternal("登录数据处理失败")
	}
	// The code must have been issued for THIS state (code↔flow↔state binding).
	if s, _ := payload["state"].(string); s != state {
		return nil, ErrUnauthorized("登录码与会话不匹配，请重新登录")
	}
	delete(payload, "state")
	return payload, nil
}
