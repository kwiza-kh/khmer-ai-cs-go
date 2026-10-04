// Package api — personal profile (the settings page's profile section).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"khmer-ai-cs-go/internal/textutil"
	"net/http"
	"strings"
	"time"
)

const maxAvatarBytes = 2 << 20 // 2 MB — plenty for a 512px avatar.

// allowedAvatarTypes — magic-byte sniffed, never trusting the client mime.
var allowedAvatarTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// sniffImageType identifies the image format from the leading bytes.
func sniffImageType(data []byte) string {
	if len(data) < 12 {
		return ""
	}
	switch {
	case data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case string(data[0:4]) == "GIF8":
		return "image/gif"
	case string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// uploadAvatar — POST /api/v1/profile/avatar (multipart "file"): stores the
// image in R2 and saves its URL on the user, returning the updated profile.
func (a *App) uploadAvatar(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if a.Media == nil {
		return nil, ErrServiceUnavailable("媒体存储未配置")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+512<<10)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		return nil, ErrBadRequest("图片过大或格式错误（上限 2MB）")
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, ErrBadRequest("缺少图片文件")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil {
		return nil, ErrBadRequest("读取图片失败")
	}
	if len(data) > maxAvatarBytes {
		return nil, ErrBadRequest("图片不能超过 2MB")
	}
	contentType := sniffImageType(data)
	ext, ok := allowedAvatarTypes[contentType]
	if !ok {
		return nil, ErrBadRequest("仅支持 JPG / PNG / WebP / GIF 图片")
	}
	_ = header

	key := fmt.Sprintf("avatars/%d-%d%s", user.UserID, time.Now().UnixNano(), ext)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.Media.PutObject(ctx, key, data, contentType); err != nil {
		a.Logger.Warn("avatar upload failed", "error", err.Error())
		return nil, &ApiError{http.StatusBadGateway, "头像上传失败，请重试"}
	}
	publicURL := strings.TrimSuffix(a.Cfg.R2.PublicURL, "/") + "/" + key
	if a.Cfg.R2.PublicURL == "" {
		return nil, ErrServiceUnavailable("媒体存储未配置公开域名")
	}
	if _, err := a.DB.Exec(r.Context(),
		"UPDATE users SET avatar_url = $1, updated_at = NOW() WHERE user_id = $2", publicURL, user.UserID); err != nil {
		return nil, ErrInternal("保存头像失败")
	}
	return a.getProfile(w, r)
}

// profileFields is the JSON shape shared by GET and PUT.
type profileFields struct {
	UserID      int32  `json:"user_id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	JobTitle    string `json:"job_title"`
	Phone       string `json:"phone"`
	Timezone    string `json:"timezone"`
	AvatarURL   string `json:"avatar_url"`
	// Linked login methods (read-only, derived).
	HasPassword bool   `json:"has_password"`
	HasGoogle   bool   `json:"has_google"`
	HasTelegram bool   `json:"has_telegram"`
	CreatedAt   string `json:"created_at"`
}

// getProfile — GET /api/v1/profile: the caller's full profile.
func (a *App) getProfile(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var p profileFields
	var displayName, jobTitle, phone, timezone, avatarURL, passwordHash, googleSub, telegramSub *string
	var createdAt string
	// email is nullable since 053 (Telegram sign-in supplies none) — scanning a
	// NULL into the string field would fail the whole query and surface as
	// "用户不存在" for an account that plainly exists.
	err := a.DB.QueryRow(r.Context(),
		"SELECT user_id, username, COALESCE(email,''), role::text, display_name, job_title, phone, timezone, avatar_url, "+
			"password_hash, google_sub, telegram_sub, created_at::text FROM users WHERE user_id = $1", user.UserID).
		Scan(&p.UserID, &p.Username, &p.Email, &p.Role, &displayName, &jobTitle, &phone, &timezone, &avatarURL,
			&passwordHash, &googleSub, &telegramSub, &createdAt)
	if err != nil {
		return nil, ErrNotFound("用户不存在")
	}
	p.DisplayName = textutil.DerefString(displayName)
	p.JobTitle = textutil.DerefString(jobTitle)
	p.Phone = textutil.DerefString(phone)
	p.Timezone = textutil.DerefString(timezone)
	p.AvatarURL = textutil.DerefString(avatarURL)
	p.HasPassword = passwordHash != nil && *passwordHash != ""
	p.HasGoogle = googleSub != nil && *googleSub != ""
	p.HasTelegram = telegramSub != nil && *telegramSub != ""
	p.CreatedAt = createdAt
	return p, nil
}

// putProfile — PUT /api/v1/profile: update the editable fields only (email,
// role, and login methods are deliberately not self-service).
func (a *App) putProfile(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req struct {
		DisplayName *string `json:"display_name"`
		JobTitle    *string `json:"job_title"`
		Phone       *string `json:"phone"`
		Timezone    *string `json:"timezone"`
		AvatarURL   *string `json:"avatar_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	clean := func(v *string, max int) *string {
		if v == nil {
			return nil
		}
		s := strings.TrimSpace(*v)
		if len([]rune(s)) > max {
			s = string([]rune(s)[:max])
		}
		return &s
	}
	displayName := clean(req.DisplayName, 80)
	jobTitle := clean(req.JobTitle, 80)
	phone := clean(req.Phone, 32)
	timezone := clean(req.Timezone, 64)
	avatarURL := clean(req.AvatarURL, 500)
	if avatarURL != nil && *avatarURL != "" && !strings.HasPrefix(*avatarURL, "https://") {
		return nil, ErrBadRequest("头像地址必须是 https 链接")
	}
	if _, err := a.DB.Exec(r.Context(),
		"UPDATE users SET display_name = COALESCE($2, display_name), job_title = COALESCE($3, job_title), "+
			"phone = COALESCE($4, phone), timezone = COALESCE($5, timezone), avatar_url = COALESCE($6, avatar_url), "+
			"updated_at = NOW() WHERE user_id = $1",
		user.UserID, displayName, jobTitle, phone, timezone, avatarURL); err != nil {
		return nil, ErrInternal("保存失败")
	}
	return a.getProfile(w, r)
}
