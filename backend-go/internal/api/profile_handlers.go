// Package api — personal profile (the settings page's profile section).
package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

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
	HasPassword bool `json:"has_password"`
	HasGoogle   bool `json:"has_google"`
	CreatedAt   string `json:"created_at"`
}

// getProfile — GET /api/v1/profile: the caller's full profile.
func (a *App) getProfile(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var p profileFields
	var displayName, jobTitle, phone, timezone, avatarURL, passwordHash, googleSub *string
	var createdAt string
	err := a.DB.QueryRow(r.Context(),
		"SELECT user_id, username, email, role::text, display_name, job_title, phone, timezone, avatar_url, "+
			"password_hash, google_sub, created_at::text FROM users WHERE user_id = $1", user.UserID).
		Scan(&p.UserID, &p.Username, &p.Email, &p.Role, &displayName, &jobTitle, &phone, &timezone, &avatarURL,
			&passwordHash, &googleSub, &createdAt)
	if err != nil {
		return nil, ErrNotFound("用户不存在")
	}
	p.DisplayName = derefStr(displayName)
	p.JobTitle = derefStr(jobTitle)
	p.Phone = derefStr(phone)
	p.Timezone = derefStr(timezone)
	p.AvatarURL = derefStr(avatarURL)
	p.HasPassword = passwordHash != nil && *passwordHash != ""
	p.HasGoogle = googleSub != nil && *googleSub != ""
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
