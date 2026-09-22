package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/auth"
)

const (
	lockoutThreshold = 5
	lockoutTTL       = 15 * time.Minute
)

type loginRequest struct {
	Username string  `json:"username"`
	Password string  `json:"password"`
	TOTPCode *string `json:"totp_code"`
}

type registerRequest struct {
	Username   string  `json:"username"`
	Email      string  `json:"email"`
	Password   string  `json:"password"`
	InviteCode *string `json:"invite_code"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

type preferencesRequest struct {
	Language         *string `json:"language"`
	NotificationPref *string `json:"notification_pref"`
}

// Login — account lockout (5 failures / 15min), bcrypt verify, legacy-cost
// hash upgrade, TOTP gate, then issue the HS256 token. Rust parity.
func (a *App) login(w http.ResponseWriter, r *http.Request) (any, error) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Username == "" || req.Password == "" {
		return nil, ErrBadRequest("请求格式错误")
	}

	ctx := r.Context()
	lockKey := "lockout:" + strings.ToLower(req.Username)
	failKey := "loginfail:" + strings.ToLower(req.Username)

	if locked, _ := a.Redis.GetJSON(ctx, lockKey); locked != nil {
		return nil, ErrTooMany("登录尝试过多，账号已锁定，请稍后再试")
	}

	var userID int32
	var username, email, role string
	var passwordHash *string // NULL for Google-provisioned accounts
	var isActive bool
	var tokenVersion int
	err := a.DB.QueryRow(ctx,
		"SELECT user_id, username, COALESCE(email,''), password_hash, role::text, is_active, token_version FROM users WHERE username = $1",
		req.Username).Scan(&userID, &username, &email, &passwordHash, &role, &isActive, &tokenVersion)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			a.recordLoginFailure(ctx, failKey, lockKey)
			return nil, ErrUnauthorized("用户名或密码错误")
		}
		return nil, ErrInternal("用户查询失败")
	}

	if passwordHash == nil || *passwordHash == "" {
		// Google-only account: there is no password to verify against.
		a.recordLoginFailure(ctx, failKey, lockKey)
		return nil, ErrUnauthorized("该账号使用 Google 登录，请点击「使用 Google 登录」")
	}
	if !auth.VerifyPassword(req.Password, *passwordHash) {
		a.recordLoginFailure(ctx, failKey, lockKey)
		return nil, ErrUnauthorized("用户名或密码错误")
	}

	// Upgrade legacy-cost hashes at login.
	if auth.PasswordNeedsRehash(*passwordHash) {
		if newHash, herr := auth.HashPassword(req.Password); herr == nil {
			_, _ = a.DB.Exec(ctx, "UPDATE users SET password_hash = $1 WHERE user_id = $2", newHash, userID)
		}
	}

	if !isActive {
		return nil, ErrForbidden("账户已被禁用")
	}

	// TOTP gate: when enabled the login must carry a valid 6-digit code. The
	// missing-code response carries action=2fa_required so the login screen
	// can reveal its (previously absent) code field instead of dead-ending.
	var totpSecret *string
	var secret string
	err = a.DB.QueryRow(ctx, "SELECT secret FROM user_totp WHERE user_id = $1 AND enabled = true", userID).Scan(&secret)
	if err == nil {
		// Secrets are sealed at rest (migration 052); pre-052 rows are
		// plaintext and pass through the sealer unchanged.
		opened := a.openTOTP(secret)
		totpSecret = &opened
	} else if !strings.Contains(err.Error(), "no rows") {
		return nil, ErrInternal("2FA 查询失败")
	}
	if totpSecret != nil {
		code := ""
		if req.TOTPCode != nil {
			code = strings.TrimSpace(*req.TOTPCode)
		}
		if code == "" {
			return WithStatus{Status: http.StatusUnauthorized, Body: map[string]any{
				"error":  "需要两步验证码",
				"action": "2fa_required",
			}}, nil
		}
		if !auth.VerifyTOTP(*totpSecret, code) {
			// Second-factor failures count toward the same account lockout as
			// password failures; without this the advertised 5/15min lockout
			// never applies to TOTP guessing once the password is known.
			a.recordLoginFailure(ctx, failKey, lockKey)
			return nil, ErrUnauthorized("验证码错误")
		}
	}

	// Both factors passed — only now clear the failure counter.
	_ = a.Redis.SetJSON(ctx, failKey, 0, time.Second)

	token, err := a.JWT.GenerateToken(userID, username, role, tokenVersion)
	if err != nil {
		return nil, ErrInternal("令牌生成失败")
	}
	return map[string]any{
		"token": token,
		"user": map[string]any{
			"user_id":  userID,
			"username": username,
			"email":    email,
			"role":     role,
		},
	}, nil
}

// recordLoginFailure increments the counter and sets the lockout when it
// crosses the threshold. The increment is a single atomic INCR: the previous
// read-modify-write lost one increment per concurrent round, so a burst of
// simultaneous failures advanced the counter by roughly one and the advertised
// 5-per-15min bound scaled with attacker concurrency instead of bounding it.
func (a *App) recordLoginFailure(ctx context.Context, failKey, lockKey string) {
	count, err := a.Redis.IncrCounter(ctx, failKey, lockoutTTL)
	if err != nil {
		return
	}
	if count >= lockoutThreshold {
		_ = a.Redis.SetJSON(ctx, lockKey, "locked", lockoutTTL)
	}
}

// Register — gated by ALLOW_REGISTRATION (+ optional invite code).
func (a *App) register(w http.ResponseWriter, r *http.Request) (any, error) {
	if !a.Cfg.AllowRegistration {
		return nil, ErrForbidden("注册已关闭，请联系管理员")
	}
	invite := strings.TrimSpace(a.Cfg.RegistrationInviteCode)
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if invite != "" {
		supplied := ""
		if req.InviteCode != nil {
			supplied = strings.TrimSpace(*req.InviteCode)
		}
		if supplied == "" || supplied != invite {
			return nil, ErrForbidden("邀请码无效")
		}
	}

	username := strings.TrimSpace(req.Username)
	if n := len([]rune(username)); n < 3 || n > 50 {
		return nil, ErrBadRequest("请求格式错误")
	}
	if !isValidEmail(req.Email) {
		return nil, ErrBadRequest("请求格式错误")
	}
	if len([]rune(req.Password)) < 6 {
		return nil, ErrBadRequest("请求格式错误")
	}

	ctx := r.Context()
	var existing int32
	err := a.DB.QueryRow(ctx, "SELECT user_id FROM users WHERE username = $1 OR email = $2", username, req.Email).Scan(&existing)
	if err == nil {
		return nil, ErrConflict("用户名或邮箱已存在")
	}
	if !strings.Contains(err.Error(), "no rows") {
		return nil, ErrInternal("用户查询失败")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, ErrInternal("密码处理失败")
	}
	var userID int32
	err = a.DB.QueryRow(ctx,
		"INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, $3, 'user') RETURNING user_id",
		username, req.Email, hash).Scan(&userID)
	if err != nil {
		return nil, ErrInternal("注册失败")
	}

	// A brand-new account starts at token_version 0 (the column default).
	token, err := a.JWT.GenerateToken(userID, username, "user", 0)
	if err != nil {
		return nil, ErrInternal("令牌生成失败")
	}
	return WithStatus{Status: http.StatusCreated, Body: map[string]any{
		"token": token,
		"user": map[string]any{
			"user_id":  userID,
			"username": username,
			"email":    req.Email,
			"role":     "user",
		},
	}}, nil
}

// ChangePassword verifies the old password then rotates the hash.
// bumpTokenVersion invalidates every JWT already issued for userID. Called
// whenever a credential or privilege changes, so a token minted before the
// change cannot outlive it.
func (a *App) bumpTokenVersion(ctx context.Context, userID int32) {
	_, _ = a.DB.Exec(ctx, "UPDATE users SET token_version = token_version + 1 WHERE user_id = $1", userID)
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req changePasswordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if len([]rune(req.NewPassword)) < 6 {
		return nil, ErrBadRequest("请求格式错误或新密码过短 (最少 6 位)")
	}

	ctx := r.Context()
	var hashPtr *string
	err := a.DB.QueryRow(ctx, "SELECT password_hash FROM users WHERE user_id = $1", user.UserID).Scan(&hashPtr)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, ErrUnauthorized("用户不存在")
		}
		return nil, ErrInternal("用户查询失败")
	}
	if hashPtr == nil || *hashPtr == "" {
		// Google-only account — allow setting an initial password without
		// asking for a "current" one that does not exist.
		if strings.TrimSpace(req.OldPassword) != "" {
			return nil, ErrUnauthorized("该账号尚未设置密码，请留空原密码")
		}
	} else if !auth.VerifyPassword(req.OldPassword, *hashPtr) {
		return nil, ErrUnauthorized("原密码错误")
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return nil, ErrInternal("密码处理失败")
	}
	// Bump the token version in the same statement: changing a password must
	// terminate every session minted with the old one, otherwise a token
	// stolen before the change stays valid for the rest of its 24h lifetime.
	var newVersion int
	if err := a.DB.QueryRow(ctx,
		"UPDATE users SET password_hash = $1, token_version = token_version + 1 WHERE user_id = $2 RETURNING token_version",
		newHash, user.UserID).Scan(&newVersion); err != nil {
		return nil, ErrInternal("更新失败")
	}
	// Hand this session a token carrying the new version so the person who
	// just changed their password is not logged out along with the attackers.
	token, err := a.JWT.GenerateToken(user.UserID, user.Username, user.Role, newVersion)
	if err != nil {
		return map[string]string{"message": "密码已更新，请重新登录"}, nil
	}
	return map[string]any{"message": "密码已更新", "token": token}, nil
}

// GetPreferences returns the caller's saved language + notification settings.
func (a *App) getPreferences(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var language, notificationPref string
	if err := a.DB.QueryRow(r.Context(),
		"SELECT language, notification_pref FROM users WHERE user_id = $1", user.UserID,
	).Scan(&language, &notificationPref); err != nil {
		return nil, ErrInternal("查询失败")
	}
	if language == "" {
		language = "auto"
	}
	return map[string]any{
		"language":          language,
		"notification_pref": notificationPref,
	}, nil
}

// UpdatePreferences validates and stores language / notification_pref.
func (a *App) updatePreferences(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req preferencesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	ctx := r.Context()
	if req.Language != nil {
		lang := strings.ToLower(strings.TrimSpace(*req.Language))
		switch lang {
		case "auto", "km", "en", "zh":
		default:
			return nil, ErrBadRequest("不支持的语言")
		}
		stored := lang
		if lang == "auto" {
			stored = ""
		}
		if _, err := a.DB.Exec(ctx, "UPDATE users SET language = $1 WHERE user_id = $2", stored, user.UserID); err != nil {
			return nil, ErrInternal("更新失败")
		}
	}
	if req.NotificationPref != nil {
		pref := strings.ToLower(strings.TrimSpace(*req.NotificationPref))
		switch pref {
		case "all", "escalations", "none":
		default:
			return nil, ErrBadRequest("不支持的通知偏好")
		}
		if _, err := a.DB.Exec(ctx, "UPDATE users SET notification_pref = $1 WHERE user_id = $2", pref, user.UserID); err != nil {
			return nil, ErrInternal("更新失败")
		}
	}
	return map[string]string{"message": "ok"}, nil
}

// Health — liveness probe (Docker hits this every 30s).
// Version is the deployed binary identity, stamped at build time via
// -ldflags "-X khmer-ai-cs-go/internal/api.Version=<git-sha>". A build that
// skips the stamp falls back to dev, which still beats a constant that makes
// every build indistinguishable.
var Version = "dev"

func (a *App) health(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	dbOK := a.DB.Ping(ctx) == nil
	redisOK := a.Redis.Ping(ctx)
	status, code := "ok", http.StatusOK
	if !dbOK || !redisOK {
		status, code = "degraded", http.StatusServiceUnavailable
	}
	body := map[string]any{
		"status":  status,
		"service": "relaychat",
		"version": Version,
		"checks":  map[string]bool{"database": dbOK, "redis": redisOK},
	}
	WriteJSON(w, code, body)
	return nil, nil
}

// Ready — readiness probe (same as health for now).
func (a *App) ready(w http.ResponseWriter, r *http.Request) (any, error) {
	return a.health(w, r)
}

// isValidEmail mirrors the Rust/Go structural check.
func isValidEmail(email string) bool {
	email = strings.TrimSpace(email)
	if len(email) > 100 || !strings.Contains(email, "@") {
		return false
	}
	local, domain, _ := strings.Cut(email, "@")
	return local != "" && strings.Contains(domain, ".") && len(domain) >= 3
}
