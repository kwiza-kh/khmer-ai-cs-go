// Package api — Google (OIDC) sign-in. Reuses the existing JWT session model:
// a successful exchange issues the same token the password login returns.
package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"khmer-ai-cs-go/internal/platform"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const googleAuthEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
const googleTokenEndpoint = "https://oauth2.googleapis.com/token"

// GoogleJWKSURL is Google's OIDC public-key set (id_token verification).
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// googleSSOEnabled — all four settings must be present.
func (a *App) googleSSOEnabled() bool {
	c := a.Cfg.SSO
	return c.Enabled && c.OIDCClientID != "" && c.OIDCClientSecret != "" &&
		c.RedirectURL != "" && c.FrontendURL != ""
}

// googleAuthURL builds the consent-screen URL with a CSRF state token.
func (a *App) googleAuthURL(state string) string {
	q := url.Values{}
	q.Set("client_id", a.Cfg.SSO.OIDCClientID)
	q.Set("redirect_uri", a.Cfg.SSO.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	return googleAuthEndpoint + "?" + q.Encode()
}

// googleStart — GET /api/v1/auth/google/start: 302 to Google's consent screen.
func (a *App) googleStart(w http.ResponseWriter, r *http.Request) {
	if !a.googleSSOEnabled() {
		a.googleRedirectError(w, r, "not_configured")
		return
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		a.googleRedirectError(w, r, "failed")
		return
	}
	state := base64.RawURLEncoding.EncodeToString(buf)
	// State lives 10 minutes in Redis: one-time use, checked on callback.
	_ = a.Redis.SetString(r.Context(), "google-state:"+state, "1", 10*time.Minute)
	// Bind the flow to the browser that started it (sso_flow cookie); without
	// this binding an attacker's (state, code) pair could be exchanged by the
	// victim's browser — silent login CSRF.
	a.ssoFlowStart(w, r, "google", state)
	http.Redirect(w, r, a.googleAuthURL(state), http.StatusFound)
}

// googleCallback — GET /api/v1/auth/google/callback: verify state, exchange
// the code, validate the id_token, find-or-create the user, then hand the
// frontend a one-time login code it swaps for the JWT.
func (a *App) googleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.googleSSOEnabled() {
		a.googleRedirectError(w, r, "not_configured")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		a.googleRedirectError(w, r, "invalid_state")
		return
	}
	if v, err := a.Redis.GetString(ctx, "google-state:"+state); err != nil || v == "" {
		a.googleRedirectError(w, r, "invalid_state")
		return
	}
	// The state must belong to the flow this browser started (sso_flow
	// cookie). Without this binding the callback is login-CSRF bait: anyone
	// can mint a live state via /start and paste it into a link.
	if !a.ssoFlowMatch(r, "google", state) {
		a.googleRedirectError(w, r, "invalid_state")
		return
	}
	// NOTE: the state key is deliberately NOT deleted here — it stays until
	// its 10-minute TTL so the login-code exchange can prove the same browser
	// started the flow (prevents an injected ?google_code= from logging the
	// visitor into someone else's account).

	if errCode := r.URL.Query().Get("error"); errCode != "" {
		a.googleRedirectError(w, r, "cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.googleRedirectError(w, r, "failed")
		return
	}

	claims, err := a.exchangeGoogleCode(ctx, code)
	if err != nil {
		a.Logger.Warn("google sso exchange failed", "error", err.Error())
		a.googleRedirectError(w, r, "failed")
		return
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if claims.Sub == "" || email == "" || !claims.EmailVerified {
		a.googleRedirectError(w, r, "email_unverified")
		return
	}

	userID, username, role, isActive, err := a.findOrCreateGoogleUser(ctx, claims.Sub, email, claims.Name)
	if err != nil {
		a.Logger.Warn("google sso user provisioning failed", "error", err.Error())
		reason := "failed"
		if strings.Contains(err.Error(), "already has a password account") {
			reason = "email_in_use"
		}
		a.googleRedirectError(w, r, reason)
		return
	}
	if !isActive {
		a.googleRedirectError(w, r, "disabled")
		return
	}
	// Accounts with TOTP enabled must not bypass the second factor by taking
	// the Google route — send them to the password flow, which enforces it.
	var totpEnabled bool
	_ = a.DB.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM user_totp WHERE user_id = $1 AND enabled = true)", userID).Scan(&totpEnabled)
	if totpEnabled {
		a.googleRedirectError(w, r, "totp_required")
		return
	}
	var tokenVersion int
	_ = a.DB.QueryRow(ctx, "SELECT token_version FROM users WHERE user_id = $1", userID).Scan(&tokenVersion)
	token, err := a.JWT.GenerateToken(userID, username, role, tokenVersion)
	if err != nil {
		a.googleRedirectError(w, r, "failed")
		return
	}
	// Hand the token to the SPA through a short-lived one-time code rather
	// than the URL fragment (keeps it out of history/referrer). The state is
	// embedded in the payload so the exchange can verify code↔flow↔state.
	loginCode := platform.NewUUID()
	payload, _ := json.Marshal(map[string]any{
		"token": token,
		"state": state,
		"user":  map[string]any{"user_id": userID, "username": username, "email": email, "role": role},
	})
	if err := a.Redis.SetString(ctx, "google-login:"+loginCode, string(payload), 2*time.Minute); err != nil {
		a.googleRedirectError(w, r, "failed")
		return
	}
	// The state travels back in the URL so the SPA can present it with the
	// code; the exchange rejects a code whose state does not match a flow
	// this browser actually started.
	http.Redirect(w, r, a.Cfg.SSO.FrontendURL+"?google_code="+url.QueryEscape(loginCode)+"&state="+url.QueryEscape(state), http.StatusFound)
}

// googleClaimExchange — POST the code to Google's token endpoint.
func (a *App) exchangeGoogleCode(ctx context.Context, code string) (*googleClaims, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", a.Cfg.SSO.OIDCClientID)
	form.Set("client_secret", a.Cfg.SSO.OIDCClientSecret)
	form.Set("redirect_uri", a.Cfg.SSO.RedirectURL)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenEndpoint, strings.NewReader(form.Encode()))
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
	return a.verifyGoogleIDToken(body.IDToken)
}

// verifyGoogleIDToken checks the RS256 signature against Google's published
// JWKS keys (OIDC Core requires signature validation even in the code flow),
// then parses the claims. Transport alone is not the authenticity guarantee:
// any interposition on the server's egress TLS would otherwise yield account
// impersonation. SSO_SKIP_ID_TOKEN_VERIFY=true restores the old
// transport-trust posture for deployments that cannot reach the JWKS URL.
func (a *App) verifyGoogleIDToken(idToken string) (*googleClaims, error) {
	if !a.Cfg.SSO.SkipIDTokenVerify {
		if a.SSOJWKS == nil {
			a.SSOJWKS = NewJWKSCache(GoogleJWKSURL)
		}
		key, err := jwksKidPublicKey(a.SSOJWKS, idToken)
		if err != nil {
			return nil, err
		}
		if err := verifyRS256(idToken, key); err != nil {
			return nil, err
		}
	}
	return parseGoogleIDToken(idToken, a.Cfg.SSO.OIDCClientID)
}

type googleClaims struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
}

// parseGoogleIDToken decodes the JWT payload and verifies the standard
// claims. Signature verification happens separately in verifyGoogleIDToken
// (RS256 against Google's JWKS) before this function is reached.
func parseGoogleIDToken(idToken, clientID string) (*googleClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode id_token payload: %w", err)
	}
	var raw struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Exp           int64  `json:"exp"`
	}
	if json.Unmarshal(payload, &raw) != nil {
		return nil, fmt.Errorf("id_token claims")
	}
	if raw.Aud != clientID {
		return nil, fmt.Errorf("id_token audience mismatch")
	}
	if raw.Iss != "https://accounts.google.com" && raw.Iss != "accounts.google.com" {
		return nil, fmt.Errorf("id_token issuer mismatch")
	}
	if raw.Exp > 0 && time.Now().Unix() > raw.Exp {
		return nil, fmt.Errorf("id_token expired")
	}
	return &googleClaims{Sub: raw.Sub, Email: raw.Email, EmailVerified: raw.EmailVerified, Name: raw.Name}, nil
}

// findOrCreateGoogleUser resolves the Google subject to a local account:
//  1. google_sub match (returning user);
//  2. same verified email (bind the existing password account);
//  3. create a new account when registration is allowed.
func (a *App) findOrCreateGoogleUser(ctx context.Context, sub, email, name string) (int32, string, string, bool, error) {
	var userID int32
	var username, role string
	var isActive bool
	var existingHash string
	err := a.DB.QueryRow(ctx,
		"SELECT user_id, username, role::text, is_active FROM users WHERE google_sub = $1", sub).
		Scan(&userID, &username, &role, &isActive)
	if err == nil {
		return userID, username, role, isActive, nil
	}

	err = a.DB.QueryRow(ctx,
		"SELECT user_id, username, role::text, is_active, COALESCE(password_hash,'') FROM users WHERE lower(email) = $1", email).
		Scan(&userID, &username, &role, &isActive, &existingHash)
	if err == nil {
		// Pre-hijack guard: a password account must never be silently adopted
		// by whoever first proves this email via Google. Anyone can register
		// an arbitrary address (the register endpoint does not verify the
		// inbox), so auto-binding would hand them the Google user's future
		// data. Only accounts with no password yet (i.e. created through SSO)
		// may be linked.
		if existingHash != "" {
			return 0, "", "", false, fmt.Errorf("email already has a password account")
		}
		_, _ = a.DB.Exec(ctx, "UPDATE users SET google_sub = $1, updated_at = NOW() WHERE user_id = $2", sub, userID)
		return userID, username, role, isActive, nil
	}

	if !a.Cfg.SSO.AllowSignup {
		return 0, "", "", false, fmt.Errorf("google signup disabled")
	}
	// Derive a unique username from the email local-part.
	base := strings.ToLower(strings.Split(email, "@")[0])
	if base == "" {
		base = "user"
	}
	username = base
	for i := 0; i < 20; i++ {
		candidate := username
		if i > 0 {
			candidate = fmt.Sprintf("%s%d", base, i+1)
		}
		err = a.DB.QueryRow(ctx,
			"INSERT INTO users (username, email, google_sub, role, is_active) "+
				"VALUES ($1,$2,$3,'user',true) RETURNING user_id",
			candidate, email, sub).Scan(&userID)
		if err == nil {
			username = candidate
			return userID, username, "user", true, nil
		}
		if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "unique") {
			return 0, "", "", false, err
		}
	}
	return 0, "", "", false, fmt.Errorf("could not allocate username")
}

// googleAuthMethods — GET /api/v1/auth/methods: public capability probe the
// login page uses to decide which sign-in buttons to render. Covers every
// provider, despite the name.
func (a *App) googleAuthMethods(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{
		"google":             a.googleSSOEnabled(),
		"allow_registration": a.Cfg.AllowRegistration,
		"google_signup":      a.Cfg.SSO.AllowSignup,
		"telegram":           a.telegramSSOEnabled(),
		"telegram_signup":    a.Cfg.TelegramLogin.AllowSignup,
	}, nil
}

// googleRedirectError sends the browser back to the login page with a reason.
func (a *App) googleRedirectError(w http.ResponseWriter, r *http.Request, reason string) {
	target := a.Cfg.SSO.FrontendURL
	if target == "" {
		target = "/login"
	}
	http.Redirect(w, r, target+"?google_error="+url.QueryEscape(reason), http.StatusFound)
}

// googleLoginExchange — POST /api/v1/auth/google/exchange {code}: swaps the
// one-time code for the JWT payload (single use, 2-minute TTL).
func (a *App) googleLoginExchange(w http.ResponseWriter, r *http.Request) (any, error) {
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
	// The state must still be live AND belong to the flow this browser started
	// (cookie binding). Both are deleted on first successful use below, which
	// makes the whole exchange single-shot.
	stateKey := "google-state:" + state
	if v, err := a.Redis.GetString(r.Context(), stateKey); err != nil || v == "" {
		return nil, ErrUnauthorized("登录会话已失效，请重新登录")
	}
	if !a.ssoFlowMatch(r, "google", state) {
		return nil, ErrUnauthorized("登录会话与当前浏览器不匹配，请重新登录")
	}
	_ = a.Redis.Del(r.Context(), stateKey)
	a.ssoFlowConsume(r, "google")
	raw, err := a.Redis.GetString(r.Context(), "google-login:"+code)
	if err != nil || raw == "" {
		return nil, ErrUnauthorized("登录码已失效，请重新登录")
	}
	_ = a.Redis.Del(r.Context(), "google-login:"+code)
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
