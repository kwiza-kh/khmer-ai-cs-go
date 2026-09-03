package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"time"

	"khmer-ai-cs-go/internal/auth"
)

// ============================================
// TOTP (two-factor auth)
// ============================================

// totpSetup — begin 2FA enrollment (returns secret + provisioning URI).
func (a *App) totpSetup(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	secret := generateTOTPSecret()
	uri := "otpauth://totp/khmer-ai-cs:" + user.Username + "?secret=" + secret + "&issuer=khmer-ai-cs"
	// Store pending (disabled until verified).
	_, _ = a.DB.Exec(r.Context(),
		"INSERT INTO user_totp (user_id, secret, enabled) VALUES ($1,$2,false) "+
			"ON CONFLICT (user_id) DO UPDATE SET secret = EXCLUDED.secret, enabled = false",
		user.UserID, secret)
	return map[string]any{"secret": secret, "otpauth_uri": uri}, nil
}

type totpVerifyRequest struct {
	Code string `json:"code"`
}

// totpVerify — confirm the code and enable 2FA.
func (a *App) totpVerify(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req totpVerifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	var secret string
	if err := a.DB.QueryRow(r.Context(), "SELECT secret FROM user_totp WHERE user_id = $1", user.UserID).Scan(&secret); err != nil {
		return nil, ErrNotFound("请先调用 setup")
	}
	if !auth.VerifyTOTP(secret, req.Code) {
		return nil, ErrBadRequest("验证码错误")
	}
	_, _ = a.DB.Exec(r.Context(), "UPDATE user_totp SET enabled = true WHERE user_id = $1", user.UserID)
	return map[string]any{"message": "两步验证已启用"}, nil
}

// totpStatus — whether 2FA is enabled.
func (a *App) totpStatus(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var enabled bool
	_ = a.DB.QueryRow(r.Context(), "SELECT enabled FROM user_totp WHERE user_id = $1", user.UserID).Scan(&enabled)
	return map[string]any{"enabled": enabled}, nil
}

// totpDisable — turn off 2FA (requires a valid code).
func (a *App) totpDisable(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req totpVerifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	var secret string
	if err := a.DB.QueryRow(r.Context(), "SELECT secret FROM user_totp WHERE user_id = $1", user.UserID).Scan(&secret); err != nil {
		return nil, ErrNotFound("未启用两步验证")
	}
	if !auth.VerifyTOTP(secret, req.Code) {
		return nil, ErrBadRequest("验证码错误")
	}
	_, _ = a.DB.Exec(r.Context(), "DELETE FROM user_totp WHERE user_id = $1", user.UserID)
	return map[string]any{"message": "两步验证已关闭"}, nil
}

func generateTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// generateAPIKey returns (plaintext "kcs_<hex>", sha256 hex hash).
func generateAPIKey() (string, string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", ""
	}
	plain := "kcs_" + hexEncode(b)
	return plain, sha256Hex(plain)
}

func hexEncode(b []byte) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexChars[c>>4]
		out[i*2+1] = hexChars[c&0x0f]
	}
	return string(out)
}

func sha256Hex(s string) string {
	return hexEncode(sha256Sum([]byte(s)))
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// ============================================
// API keys
// ============================================

// listAPIKeys — the caller's API keys (hash never returned).
func (a *App) listAPIKeys(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT key_id, name, key_prefix, last_used_at, expires_at, is_active, created_at FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var keyID int
		var name, keyPrefix string
		var lastUsed, expiresAt *time.Time
		var isActive bool
		var createdAt time.Time
		if err := rows.Scan(&keyID, &name, &keyPrefix, &lastUsed, &expiresAt, &isActive, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"key_id": keyID, "name": name, "key_prefix": keyPrefix,
			"last_used_at": lastUsed, "expires_at": expiresAt, "is_active": isActive, "created_at": createdAt,
		})
	}
	return out, nil
}

type createAPIKeyRequest struct {
	Name string `json:"name"`
}

// createAPIKey — generate a new API key (plaintext shown once).
func (a *App) createAPIKey(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req createAPIKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.Name == "" {
		return nil, ErrBadRequest("key name is required")
	}
	plain, hash := generateAPIKey()
	var keyID int
	if err := a.DB.QueryRow(r.Context(),
		"INSERT INTO api_keys (user_id, name, key_prefix, key_hash, is_active) VALUES ($1,$2,$3,$4,true) RETURNING key_id",
		user.UserID, req.Name, "kcs_", hash).Scan(&keyID); err != nil {
		return nil, ErrInternal("创建失败")
	}
	return map[string]any{"key_id": keyID, "key": plain, "message": "请保存此密钥,只显示一次"}, nil
}

// deleteAPIKey — revoke an API key.
func (a *App) deleteAPIKey(w http.ResponseWriter, r *http.Request, keyID int32) (any, error) {
	user, _ := UserFrom(r)
	tag, err := a.DB.Exec(r.Context(), "UPDATE api_keys SET is_active = false WHERE key_id = $1 AND user_id = $2", keyID, user.UserID)
	if err != nil {
		return nil, ErrInternal("删除失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("key not found")
	}
	return map[string]string{"message": "已撤销"}, nil
}

// ============================================
// Notifications
// ============================================

// listNotifications — the caller's recent notifications.
func (a *App) listNotifications(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	rows, err := a.DB.Query(r.Context(),
		"SELECT notification_id, kind, title, body, session_id, is_read, created_at FROM notifications WHERE user_id = $1 ORDER BY created_at DESC LIMIT 50",
		user.UserID)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var nid int
		var kind, title, body string
		var sessionID *string
		var isRead bool
		var createdAt time.Time
		if err := rows.Scan(&nid, &kind, &title, &body, &sessionID, &isRead, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"notification_id": nid, "kind": kind, "title": title, "body": body,
			"session_id": sessionID, "is_read": isRead, "created_at": createdAt,
		})
	}
	return out, nil
}

// notificationsUnread — unread count.
func (a *App) notificationsUnread(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var count int64
	_ = a.DB.QueryRow(r.Context(), "SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND is_read = false", user.UserID).Scan(&count)
	return map[string]any{"count": count}, nil
}

// notificationsMarkRead — mark one notification read.
func (a *App) notificationsMarkRead(w http.ResponseWriter, r *http.Request, nid int32) (any, error) {
	user, _ := UserFrom(r)
	_, _ = a.DB.Exec(r.Context(), "UPDATE notifications SET is_read = true WHERE notification_id = $1 AND user_id = $2", nid, user.UserID)
	return map[string]string{"message": "已读"}, nil
}

// notificationsReadAll — mark all read.
func (a *App) notificationsReadAll(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	_, _ = a.DB.Exec(r.Context(), "UPDATE notifications SET is_read = true WHERE user_id = $1", user.UserID)
	return map[string]string{"message": "全部已读"}, nil
}
