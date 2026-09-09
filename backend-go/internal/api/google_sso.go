// Package api — Google (OIDC) sign-in. Reuses the existing JWT session model:
// a successful exchange issues the same token the password login returns.
package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

)

const googleAuthEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
const googleTokenEndpoint = "https://oauth2.googleapis.com/token"
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
	_ = a.Redis.Del(ctx, "google-state:"+state)

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
		a.googleRedirectError(w, r, "failed")
		return
	}
	if !isActive {
		a.googleRedirectError(w, r, "disabled")
		return
	}
	token, err := a.JWT.GenerateToken(userID, username, role)
	if err != nil {
		a.googleRedirectError(w, r, "failed")
		return
	}
	// Hand the token to the SPA through a short-lived one-time code rather
	// than the URL fragment (keeps it out of history/referrer).
	loginCode := newUUIDv4()
	payload, _ := json.Marshal(map[string]any{
		"token": token,
		"user":  map[string]any{"user_id": userID, "username": username, "email": email, "role": role},
	})
	if err := a.Redis.SetString(ctx, "google-login:"+loginCode, string(payload), 2*time.Minute); err != nil {
		a.googleRedirectError(w, r, "failed")
		return
	}
	http.Redirect(w, r, a.Cfg.SSO.FrontendURL+"?google_code="+url.QueryEscape(loginCode), http.StatusFound)
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
	return parseGoogleIDToken(body.IDToken, a.Cfg.SSO.OIDCClientID)
}

type googleClaims struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
}

// parseGoogleIDToken decodes the JWT payload. The token arrives directly from
// Google over TLS in response to our client_secret-authenticated request, so
// the transport is the authenticity guarantee here; we still verify audience
// and issuer to reject a token minted for another client.
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
	err := a.DB.QueryRow(ctx,
		"SELECT user_id, username, role::text, is_active FROM users WHERE google_sub = $1", sub).
		Scan(&userID, &username, &role, &isActive)
	if err == nil {
		return userID, username, role, isActive, nil
	}

	err = a.DB.QueryRow(ctx,
		"SELECT user_id, username, role::text, is_active FROM users WHERE lower(email) = $1", email).
		Scan(&userID, &username, &role, &isActive)
	if err == nil {
		// Existing password account with the same verified email — bind it.
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
// login page uses to decide whether to render the Google button.
func (a *App) googleAuthMethods(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{
		"google":             a.googleSSOEnabled(),
		"allow_registration": a.Cfg.AllowRegistration,
		"google_signup":      a.Cfg.SSO.AllowSignup,
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
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > 64 {
		return nil, ErrBadRequest("无效的登录码")
	}
	raw, err := a.Redis.GetString(r.Context(), "google-login:"+code)
	if err != nil || raw == "" {
		return nil, ErrUnauthorized("登录码已失效，请重新登录")
	}
	_ = a.Redis.Del(r.Context(), "google-login:"+code)
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return nil, ErrInternal("登录数据处理失败")
	}
	return payload, nil
}
