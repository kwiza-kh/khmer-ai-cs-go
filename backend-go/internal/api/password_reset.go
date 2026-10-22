package api

// Password reset — two delivery paths over one token store (migration 087).
//
// Production runs without SMTP, so e-mail self-service cannot be the only way
// back into an account. A platform admin can always mint a one-time link and
// hand it over out of band; e-mail self-service switches on by configuration
// only (SMTP_HOST + SMTP_FROM + a frontend URL). Both paths issue the same
// SHA-256-hashed, single-use, expiring token, so the confirm endpoint has one
// rule set no matter how the link was created.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/mailer"
	"khmer-ai-cs-go/internal/realtime"
)

const (
	// passwordResetEmailTTL is short on purpose: a self-service link travels
	// through a mail provider and is normally opened within minutes. The shorter
	// the window, the less a forwarded mailbox or a shared screen is worth.
	passwordResetEmailTTL = 30 * time.Minute
	// passwordResetAdminTTL is long because the operator hands the link over
	// out of band (chat, phone) and the merchant may only act the next day —
	// a link that expired before it was read would just be re-issued. Bounded
	// all the same: an eternal reset link is a permanent backdoor.
	passwordResetAdminTTL = 24 * time.Hour

	passwordResetTokenBytes  = 32
	passwordResetTokenMin    = 20 // chars; a real token is 43 base64url ASCII chars
	passwordResetTokenMax    = 200
	passwordResetPasswordMin = 8
	passwordResetPasswordMax = 200
)

// newPasswordResetToken returns a fresh 256-bit token and the SHA-256 hex hash
// that is stored. Only the hash leaves this function; the raw value goes into
// the e-mail body or the operator response and is never written to the database
// or a log line.
func newPasswordResetToken() (raw, hash string, err error) {
	buf := make([]byte, passwordResetTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, passwordResetTokenHash(raw), nil
}

// passwordResetTokenHash is the storage form of a token. A plain SHA-256 is
// enough here (unlike a password): the input is 256 random bits, so there is no
// dictionary to brute-force, and a slow KDF would only make confirm requests
// expensive for no gain.
func passwordResetTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// frontendBaseURL resolves the browser origin a reset link should point at.
//
// Order: PUBLIC_FRONTEND_URL, then PUBLIC_API_URL with a trailing /api/v1
// removed, then SSO_FRONTEND_URL with a trailing /login removed. The fallbacks
// exist so a deployment that already set one of the older variables gets a
// working link without a new setting; an empty result means no link can be
// built, and the admin endpoint says so instead of returning a dead URL.
func (a *App) frontendBaseURL() string {
	if a.Cfg == nil {
		return ""
	}
	trim := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	if base := trim(a.Cfg.Server.PublicFrontendURL); base != "" {
		return base
	}
	if api := trim(a.Cfg.Server.PublicAPIURL); api != "" {
		return strings.TrimSuffix(api, "/api/v1")
	}
	if sso := trim(a.Cfg.SSO.FrontendURL); sso != "" {
		return strings.TrimSuffix(sso, "/login")
	}
	return ""
}

// passwordResetURL builds the console link the user opens. The raw token is
// query-escaped: base64url needs no escaping today, but the link builder must
// not depend on that choice staying true.
func (a *App) passwordResetURL(raw string) string {
	base := a.frontendBaseURL()
	if base == "" || raw == "" {
		return ""
	}
	return base + "/reset-password?token=" + url.QueryEscape(raw)
}

// passwordResetEmailAvailable reports whether e-mail self-service is usable in
// this deployment: mail settings AND a base URL to put in the mail. It says
// nothing about whether a particular account exists — that is what keeps the
// request endpoint from becoming an account oracle.
func (a *App) passwordResetEmailAvailable() bool {
	if a.Cfg == nil || !a.Cfg.Email.SMTPConfigured() {
		return false
	}
	return a.frontendBaseURL() != ""
}

// passwordResetEmail renders the three-language plain-text mail. Pure, so the
// wording and the TTL sentence are testable without SMTP. Unknown and "auto"
// languages fall back to English: a mail cannot detect the recipient's language
// the way an AI reply can.
func passwordResetEmail(language, link string, ttl time.Duration) (subject, body string) {
	lang := normalizePasswordResetLanguage(language)
	validFor := passwordResetTTLText(lang, ttl)
	switch lang {
	case "zh":
		return "重置您的 MekongChat 密码",
			"我们收到了重置 MekongChat 密码的请求。\n\n" +
				"打开下面的链接设置新密码（" + validFor + "内有效）：\n" + link + "\n\n" +
				"该链接只能使用一次。如果这不是您发起的请求，请忽略本邮件，您的密码不会被修改。"
	case "km":
		return "កំណត់ពាក្យសម្ងាត់ MekongChat របស់អ្នកឡើងវិញ",
			"យើងបានទទួលសំណើដើម្បីកំណត់ពាក្យសម្ងាត់ MekongChat របស់អ្នកឡើងវិញ។\n\n" +
				"បើកតំណខាងក្រោមដើម្បីជ្រើសរើសពាក្យសម្ងាត់ថ្មី (មានសុពលភាព " + validFor + ")：\n" + link + "\n\n" +
				"តំណនេះអាចប្រើបានតែម្ដង។ ប្រសិនបើអ្នកមិនបានស្នើសុំ សូមមិនអើពើអ៊ីមែលនេះ — ពាក្យសម្ងាត់របស់អ្នកនឹងមិនផ្លាស់ប្ដូរទេ។"
	default:
		return "Reset your MekongChat password",
			"We received a request to reset your MekongChat password.\n\n" +
				"Open the link below to choose a new password (valid for " + validFor + "):\n" + link + "\n\n" +
				"This link can be used only once. If you did not request it, ignore this e-mail — your password will not change."
	}
}

func normalizePasswordResetLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "km":
		return "km"
	case "zh":
		return "zh"
	default:
		return "en"
	}
}

// passwordResetTTLText renders the validity sentence's duration. Hours are used
// for a whole number of hours (the 24h admin link), minutes otherwise.
func passwordResetTTLText(language string, ttl time.Duration) string {
	if ttl >= time.Hour && ttl%time.Hour == 0 {
		hours := int(ttl / time.Hour)
		switch language {
		case "zh":
			return fmt.Sprintf("%d 小时", hours)
		case "km":
			return fmt.Sprintf("%d ម៉ោង", hours)
		default:
			return fmt.Sprintf("%d hours", hours)
		}
	}
	minutes := int(ttl / time.Minute)
	switch language {
	case "zh":
		return fmt.Sprintf("%d 分钟", minutes)
	case "km":
		return fmt.Sprintf("%d នាទី", minutes)
	default:
		return fmt.Sprintf("%d minutes", minutes)
	}
}

// logPasswordReset keeps the nil-Logger test apps working; every failure on
// this surface is deliberately silent to the caller (it must not reveal whether
// an account exists), so the log is the only place an operator can see it.
func (a *App) logPasswordReset(msg string, args ...any) {
	if a.Logger != nil {
		a.Logger.Warn(msg, args...)
	}
}

// sendPasswordResetEmail detaches the send from the request context and bounds
// it at 15s. The request context dies the moment the browser has its response,
// and a mail cancelled by that moment is indistinguishable to the user from one
// that was never sent.
func (a *App) sendPasswordResetEmail(ctx context.Context, to, subject, body string) error {
	if a.Mailer == nil {
		return mailer.ErrNotConfigured
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return a.Mailer.Send(sendCtx, to, subject, body)
}

// ---------- token storage ----------

// issuePasswordResetToken creates a token, stores its hash and returns the raw
// value plus the expiry the database recorded (so the admin response and the
// row can never disagree).
func (a *App) issuePasswordResetToken(ctx context.Context, userID int32, ttl time.Duration, createdBy *int32) (string, time.Time, error) {
	raw, hash, err := newPasswordResetToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt, err := a.storePasswordResetToken(ctx, userID, hash, ttl, createdBy)
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

// storePasswordResetToken invalidates the account's outstanding links and
// inserts the new one in a single transaction. The order matters: a link handed
// out a minute ago must stop working the instant a newer one exists, and two
// autocommit statements left a window where both were redeemable.
func (a *App) storePasswordResetToken(ctx context.Context, userID int32, tokenHash string, ttl time.Duration, createdBy *int32) (time.Time, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		"UPDATE password_reset_tokens SET used_at = NOW() WHERE user_id = $1 AND used_at IS NULL",
		userID); err != nil {
		return time.Time{}, err
	}
	var expiresAt time.Time
	// make_interval(secs => $3), not ($3 || ' seconds')::interval: with an
	// unknown-typed parameter Postgres resolves || to text and pgx then cannot
	// encode the Go int (the SLA scan learned this the hard way; see
	// recordSLABreaches).
	if err := tx.QueryRow(ctx,
		"INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, created_by) "+
			"VALUES ($1, $2, NOW() + make_interval(secs => $3), $4) RETURNING expires_at",
		userID, tokenHash, int64(ttl.Seconds()), createdBy).Scan(&expiresAt); err != nil {
		return time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

// ---------- public: request a link ----------

// passwordResetRequest — POST /api/v1/auth/password-reset/request {login}.
//
// The response is ALWAYS 200 and always the same sentence for "sent", "no such
// account", "disabled account" and "mail not configured": a different answer
// turns this endpoint into a cheap account-existence oracle. email_available is
// a deployment fact (can this server send mail at all), identical for every
// caller, so it is safe to expose and the console needs it to decide between
// "check your inbox" and "contact your provider".
func (a *App) passwordResetRequest(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	login := strings.TrimSpace(req.Login)
	// Rune count, like every other user-entered length check in this package:
	// a 200-character Khmer username is 200 characters, not 600 bytes.
	if n := len([]rune(login)); n == 0 || n > 200 {
		return nil, ErrBadRequest("请求格式错误")
	}

	emailAvailable := a.passwordResetEmailAvailable()
	response := map[string]any{
		"message":         "如果该账号存在且已配置邮件，重置链接已发送",
		"email_available": emailAvailable,
	}
	if !emailAvailable {
		return response, nil
	}

	ctx := r.Context()
	var userID int32
	var username, email, language string
	var isActive bool
	err := a.DB.QueryRow(ctx,
		"SELECT user_id, username, COALESCE(email,''), COALESCE(language,''), is_active "+
			"FROM users WHERE lower(username) = lower($1) OR lower(email) = lower($1) LIMIT 1",
		login).Scan(&userID, &username, &email, &language, &isActive)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.logPasswordReset("password reset user lookup failed", "error", err.Error())
		}
		return response, nil
	}
	// A disabled account must not receive a working takeover link; an account
	// without a mailbox cannot receive anything. Both answer like "not found".
	if !isActive || strings.TrimSpace(email) == "" {
		return response, nil
	}

	// Per-account throttle: 3 links / 15 minutes, on top of the route's per-IP
	// limit. The route limit bounds one source; this bounds one inbox being
	// flooded from many sources. On a Redis error we fail closed for SENDING
	// (no mail) and open for the response — a cache outage must not turn into
	// unlimited mail being sent. A nil client (bare test App) takes the same
	// fail-closed branch instead of panicking.
	if a.Redis == nil {
		a.logPasswordReset("password reset throttle unavailable; not sending", "user_id", userID)
		return response, nil
	}
	allowed, throttleErr := a.Redis.IncrWindow(ctx, fmt.Sprintf("pwreset-req:%d", userID), 3, 15*time.Minute)
	if throttleErr != nil {
		a.logPasswordReset("password reset throttle unavailable; not sending", "user_id", userID, "error", throttleErr.Error())
		return response, nil
	}
	if !allowed {
		return response, nil
	}

	raw, _, err := a.issuePasswordResetToken(ctx, userID, passwordResetEmailTTL, nil)
	if err != nil {
		a.logPasswordReset("password reset token insert failed", "user_id", userID, "error", err.Error())
		return response, nil
	}
	link := a.passwordResetURL(raw)
	if link == "" {
		// Unreachable while emailAvailable is true (it requires a base URL), but
		// a token must never be mailed as a bare /reset-password path.
		a.logPasswordReset("password reset link base URL missing", "user_id", userID)
		return response, nil
	}
	subject, body := passwordResetEmail(language, link, passwordResetEmailTTL)
	if err := a.sendPasswordResetEmail(ctx, email, subject, body); err != nil {
		// The token stays valid: deleting it here could race a message that did
		// reach the relay. A retry is another request (which invalidates this
		// token) or the operator's link.
		a.logPasswordReset("password reset e-mail send failed", "user_id", userID, "username", username, "error", err.Error())
	}
	return response, nil
}

// ---------- public: confirm the new password ----------

// passwordResetConfirm — POST /api/v1/auth/password-reset/confirm
// {token, new_password}.
//
// Everything that a password change implies happens in one transaction: the new
// hash, a token_version bump (all JWTs die), every other outstanding reset link
// (single use across the account), every API key (no token_version to revoke
// them) and the audit row.
func (a *App) passwordResetConfirm(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	token := strings.TrimSpace(req.Token)
	if len(token) < passwordResetTokenMin || len(token) > passwordResetTokenMax {
		return nil, ErrBadRequest("重置链接无效或已过期")
	}
	if n := len([]rune(req.NewPassword)); n < passwordResetPasswordMin || n > passwordResetPasswordMax {
		return nil, ErrBadRequest("密码至少 8 位")
	}

	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tokenID int64
	var userID int32
	var isActive bool
	// FOR UPDATE serialises two concurrent confirmations of the same token: the
	// second waits, re-reads used_at (now set) and gets "invalid" instead of
	// overwriting the first user's new password.
	err = tx.QueryRow(ctx,
		"SELECT t.token_id, t.user_id, u.is_active FROM password_reset_tokens t "+
			"JOIN users u ON u.user_id = t.user_id "+
			"WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > NOW() FOR UPDATE",
		passwordResetTokenHash(token)).Scan(&tokenID, &userID, &isActive)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrBadRequest("重置链接无效或已过期")
		}
		return nil, ErrInternal("更新失败")
	}
	if !isActive {
		return nil, ErrBadRequest("重置链接无效或已过期")
	}

	// bcrypt runs only after the token proved valid: hashing first would let an
	// unauthenticated caller spend one cost-12 hash per request with a bogus
	// token. The row lock is held for ~250ms, acceptable for this rare action.
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return nil, ErrInternal("密码处理失败")
	}
	if _, err := tx.Exec(ctx,
		"UPDATE users SET password_hash = $1, token_version = token_version + 1 WHERE user_id = $2",
		newHash, userID); err != nil {
		return nil, ErrInternal("更新失败")
	}
	// Every other outstanding link dies with the password, not only the one used.
	if _, err := tx.Exec(ctx,
		"UPDATE password_reset_tokens SET used_at = NOW() WHERE user_id = $1 AND used_at IS NULL",
		userID); err != nil {
		return nil, ErrInternal("更新失败")
	}
	// API keys are long-lived bearer tokens with no token_version; leaving them
	// active would defeat the rotation (same rule as changePassword).
	if _, err := tx.Exec(ctx,
		"UPDATE api_keys SET is_active = false WHERE user_id = $1", userID); err != nil {
		return nil, ErrInternal("更新失败")
	}
	details, _ := json.Marshal(map[string]string{"source": "password-reset"})
	// admin_id NULL: the account itself acted. details deliberately carries no
	// token or hash — audit_logs is read by the platform console and must never
	// become the place a reset link can be recovered from.
	if _, err := tx.Exec(ctx,
		"INSERT INTO audit_logs (admin_id, action, target_type, target_id, details, ip_address) "+
			"VALUES (NULL, 'password.reset', 'user', $1, $2::jsonb, $3)",
		strconv.Itoa(int(userID)), string(details), clientIP(r)); err != nil {
		return nil, ErrInternal("更新失败")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, ErrInternal("更新失败")
	}

	// Open websockets still carry the old token_version; close them now rather
	// than at JWT expiry. Nil Redis is safe (PublishRevoke returns early).
	realtime.PublishRevoke(ctx, a.Redis, userID)
	return map[string]string{"message": "密码已重置，请使用新密码登录"}, nil
}

// ---------- platform admin: issue a link by hand ----------

// platformPasswordReset — POST /api/v1/platform/users/{id}/password-reset.
//
// The break-glass path for a deployment without SMTP: it never sets a password
// (the operator must not learn the merchant's credential) and never requires
// e-mail. It hands the operator a one-time link that is also mailed when mail
// is configured and the target has an address.
func (a *App) platformPasswordReset(w http.ResponseWriter, r *http.Request, userID int32) (any, error) {
	ctx := r.Context()
	var username, email, language string
	err := a.DB.QueryRow(ctx,
		"SELECT username, COALESCE(email,''), COALESCE(language,'') FROM users WHERE user_id = $1",
		userID).Scan(&username, &email, &language)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound("用户不存在")
		}
		return nil, ErrInternal("用户查询失败")
	}

	// Check the base URL BEFORE issuing: minting a token we cannot turn into a
	// link would invalidate the user's still-working e-mail link and hand the
	// operator nothing in return.
	if a.frontendBaseURL() == "" {
		return nil, ErrInternal("PUBLIC_API_URL 未配置，无法生成重置链接")
	}

	admin, _ := UserFrom(r)
	var createdBy *int32
	if admin != nil {
		id := admin.UserID
		createdBy = &id
	}
	raw, expiresAt, err := a.issuePasswordResetToken(ctx, userID, passwordResetAdminTTL, createdBy)
	if err != nil {
		a.logPasswordReset("admin password reset token insert failed", "user_id", userID, "error", err.Error())
		return nil, ErrInternal("生成重置链接失败")
	}
	resetURL := a.passwordResetURL(raw)
	if resetURL == "" {
		// Unreachable after the check above, but a success carrying an empty
		// link is worse than an error.
		return nil, ErrInternal("PUBLIC_API_URL 未配置，无法生成重置链接")
	}

	// Best effort: the link is the deliverable and goes back in the response;
	// the e-mail is a convenience so the operator does not have to copy it.
	emailed := false
	if a.passwordResetEmailAvailable() && strings.TrimSpace(email) != "" && a.Mailer != nil {
		subject, body := passwordResetEmail(language, resetURL, passwordResetAdminTTL)
		if err := a.sendPasswordResetEmail(ctx, email, subject, body); err != nil {
			a.logPasswordReset("admin password reset e-mail failed", "user_id", userID, "error", err.Error())
		} else {
			emailed = true
		}
	}

	// Durable record of who generated a credential for whom, with JSONB details
	// carrying only the username (never the token or URL). Best effort: the link
	// is already valid and exists only in this response, so failing the request
	// would destroy it without revoking anything — a retry mints a fresh token.
	details, _ := json.Marshal(map[string]string{"username": username})
	if _, err := a.DB.Exec(ctx,
		"INSERT INTO audit_logs (admin_id, action, target_type, target_id, details, ip_address) "+
			"VALUES ($1, 'password.reset.link', 'user', $2, $3::jsonb, $4)",
		createdBy, strconv.Itoa(int(userID)), string(details), clientIP(r)); err != nil {
		a.logPasswordReset("admin password reset audit write failed", "user_id", userID, "error", err.Error())
	}

	return map[string]any{
		"reset_url":  resetURL,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
		"emailed":    emailed,
		"username":   username,
	}, nil
}
