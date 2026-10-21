package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/config"
)

// ---------- pure helpers ----------

// A reset token must be unguessable, unique per issue, and the database form
// must be a stable hash of the raw value — never the raw value itself.
func TestNewPasswordResetTokenIsUniqueAndHashed(t *testing.T) {
	seen := make(map[string]bool, 50)
	for i := 0; i < 50; i++ {
		raw, hash, err := newPasswordResetToken()
		if err != nil {
			t.Fatalf("newPasswordResetToken: %v", err)
		}
		if seen[raw] {
			t.Fatalf("token %q issued twice", raw)
		}
		seen[raw] = true
		if len(raw) < passwordResetTokenMin || len(raw) > passwordResetTokenMax {
			t.Fatalf("raw token length %d is outside the confirm endpoint's accepted range [%d,%d]",
				len(raw), passwordResetTokenMin, passwordResetTokenMax)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(decoded) != passwordResetTokenBytes {
			t.Fatalf("token %q is not %d base64url bytes (err=%v)", raw, passwordResetTokenBytes, err)
		}
		if len(hash) != 64 {
			t.Fatalf("hash length = %d, want 64 hex chars", len(hash))
		}
		if hash != passwordResetTokenHash(raw) {
			t.Fatal("hashing the same raw token produced a different hash")
		}
		if strings.Contains(hash, raw) {
			t.Fatal("the stored hash contains the raw token")
		}
	}
}

// The link base is resolved from three sources in a fixed order, because a
// deployment may have set any one of them and the mail must never contain an
// API path as the browser-facing origin.
func TestFrontendBaseURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "PUBLIC_FRONTEND_URL wins",
			cfg: &config.Config{Server: config.ServerConfig{
				PublicFrontendURL: "https://console.example/",
				PublicAPIURL:      "https://api.example",
			}},
			want: "https://console.example",
		},
		{
			name: "PUBLIC_API_URL minus /api/v1",
			cfg:  &config.Config{Server: config.ServerConfig{PublicAPIURL: "https://api.example/api/v1/"}},
			want: "https://api.example",
		},
		{
			name: "PUBLIC_API_URL without the suffix is used as-is",
			cfg:  &config.Config{Server: config.ServerConfig{PublicAPIURL: "https://api.example"}},
			want: "https://api.example",
		},
		{
			name: "SSO_FRONTEND_URL minus /login",
			cfg:  &config.Config{SSO: config.SSOConfig{FrontendURL: "https://app.example/login"}},
			want: "https://app.example",
		},
		{name: "nothing configured", cfg: &config.Config{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&App{Cfg: tc.cfg}).frontendBaseURL(); got != tc.want {
				t.Errorf("frontendBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
	// A nil Cfg must not panic: tests build bare Apps.
	var bare App
	if got := bare.frontendBaseURL(); got != "" {
		t.Errorf("frontendBaseURL() with nil Cfg = %q, want empty", got)
	}
}

// The raw token is percent-escaped into the query string: base64url happens to
// need no escaping, but the builder must not bake that in.
func TestPasswordResetURL(t *testing.T) {
	app := &App{Cfg: &config.Config{Server: config.ServerConfig{PublicFrontendURL: "https://console.example"}}}
	raw := "a+b/c=d"
	want := "https://console.example/reset-password?token=" + url.QueryEscape(raw)
	if got := app.passwordResetURL(raw); got != want {
		t.Errorf("passwordResetURL = %q, want %q", got, want)
	}
	if got := (&App{}).passwordResetURL(raw); got != "" {
		t.Errorf("passwordResetURL without a base = %q, want empty", got)
	}
	if got := app.passwordResetURL(""); got != "" {
		t.Errorf("passwordResetURL with no token = %q, want empty", got)
	}
}

// The mail body must carry the link and the validity window, in the user's
// language; an unknown language (including the per-message "auto" setting)
// falls back to English because mail cannot detect a language.
func TestPasswordResetEmailLanguages(t *testing.T) {
	link := "https://console.example/reset-password?token=abc123"
	cases := []struct {
		lang    string
		ttl     time.Duration
		ttlText string
		subject string
	}{
		{"en", passwordResetEmailTTL, "30 minutes", "Reset your RelayChat password"},
		{"zh", passwordResetEmailTTL, "30 分钟", "重置您的 RelayChat 密码"},
		{"km", passwordResetEmailTTL, "30 នាទី", "កំណត់ពាក្យសម្ងាត់ RelayChat របស់អ្នកឡើងវិញ"},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			subject, body := passwordResetEmail(tc.lang, link, tc.ttl)
			if subject != tc.subject {
				t.Errorf("subject = %q, want %q", subject, tc.subject)
			}
			if !strings.Contains(body, link) {
				t.Errorf("body does not contain the link:\n%s", body)
			}
			if !strings.Contains(body, tc.ttlText) {
				t.Errorf("body does not state the validity window %q:\n%s", tc.ttlText, body)
			}
			if strings.Contains(body, "%") {
				t.Errorf("body still contains a format placeholder:\n%s", body)
			}
		})
	}

	// The operator link lives a day; rendering that as hours (not 1440 minutes)
	// is what makes the sentence readable.
	if _, body := passwordResetEmail("en", link, passwordResetAdminTTL); !strings.Contains(body, "24 hours") {
		t.Errorf("admin TTL is not rendered as hours:\n%s", body)
	}
	// Unknown / unset / auto languages fall back to English.
	enSubject, enBody := passwordResetEmail("en", link, passwordResetEmailTTL)
	for _, lang := range []string{"", "auto", "fr", "EN"} {
		subject, body := passwordResetEmail(lang, link, passwordResetEmailTTL)
		if subject != enSubject || body != enBody {
			t.Errorf("language %q did not fall back to English", lang)
		}
	}
}

// Self-service e-mail is a deployment capability: mail settings AND a frontend
// URL. Half a configuration must not announce a flow that cannot deliver.
func TestPasswordResetEmailAvailable(t *testing.T) {
	on := config.Config{
		Server: config.ServerConfig{PublicFrontendURL: "https://console.example"},
		Email:  config.EmailConfig{SMTPHost: "smtp.example.com", SMTPFrom: "ops@example.com"},
	}
	noFrontend := config.Config{Email: on.Email}
	hostOnly := config.Config{
		Server: on.Server,
		Email:  config.EmailConfig{SMTPHost: "smtp.example.com"},
	}
	fromOnly := config.Config{
		Server: on.Server,
		Email:  config.EmailConfig{SMTPFrom: "ops@example.com"},
	}
	apiFallback := config.Config{
		Server: config.ServerConfig{PublicAPIURL: "https://api.example/api/v1"},
		Email:  on.Email,
	}
	// EMAIL_ENABLED is the notification-channel switch, not this feature's.
	disabledChannel := on
	disabledChannel.Email.Enabled = false

	cases := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"configured", &on, true},
		{"mail not configured", &config.Config{Server: on.Server}, false},
		{"no frontend url", &noFrontend, false},
		{"host without from", &hostOnly, false},
		{"from without host", &fromOnly, false},
		{"frontend derived from the api url", &apiFallback, true},
		{"email channel disabled is irrelevant", &disabledChannel, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&App{Cfg: tc.cfg}).passwordResetEmailAvailable(); got != tc.want {
				t.Errorf("passwordResetEmailAvailable() = %v, want %v", got, tc.want)
			}
		})
	}
	var bare App
	if bare.passwordResetEmailAvailable() {
		t.Error("a bare App (nil Cfg) reports mail as available")
	}
}

// The request endpoint may only 400 on malformed input. Every lookup outcome —
// unknown account, disabled account, no mail configured — is a 200 with the same
// sentence, because a distinguishable answer is an account-existence oracle.
// The mail-off path is exercised here; it must not touch the database at all, so
// this test runs on a bare App.
func TestPasswordResetRequestIsGenericAndNeverLeaks(t *testing.T) {
	app := &App{}
	for _, body := range []string{
		"{",
		"{}",
		`{"login":"   "}`,
		fmt.Sprintf(`{"login":%q}`, strings.Repeat("a", 201)),
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset/request", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		_, err := app.passwordResetRequest(httptest.NewRecorder(), req)
		apiErr, ok := err.(*ApiError)
		if !ok || apiErr.Status != http.StatusBadRequest || apiErr.Message != "请求格式错误" {
			t.Errorf("body %.40q: err = %v, want 400 请求格式错误", body, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password-reset/request",
		strings.NewReader(`{"login":"someone@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	out, err := app.passwordResetRequest(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatalf("request with mail off: %v", err)
	}
	resp, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("request returned %T, want map[string]any", out)
	}
	if resp["message"] != "如果该账号存在且已配置邮件，重置链接已发送" {
		t.Errorf("message = %#v, want the generic sentence", resp["message"])
	}
	if resp["email_available"] != false {
		t.Errorf("email_available = %#v, want false without SMTP", resp["email_available"])
	}
}

// The login page decides between "forgot password" and "contact the admin" from
// this capability bit, so it must be present without dropping the existing keys.
func TestAuthMethodsAdvertisesPasswordResetEmail(t *testing.T) {
	methods := func(app *App) map[string]any {
		t.Helper()
		out, err := app.googleAuthMethods(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/api/v1/auth/methods", nil))
		if err != nil {
			t.Fatalf("googleAuthMethods: %v", err)
		}
		m, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("googleAuthMethods returned %T, want map[string]any", out)
		}
		for _, key := range []string{"google", "allow_registration", "google_signup", "telegram", "telegram_signup"} {
			if _, present := m[key]; !present {
				t.Errorf("auth methods lost the existing key %q", key)
			}
		}
		return m
	}

	if got := methods(&App{Cfg: &config.Config{}})["password_reset_email"]; got != false {
		t.Errorf("password_reset_email = %#v without SMTP, want false", got)
	}
	configured := &App{Cfg: &config.Config{
		Server: config.ServerConfig{PublicFrontendURL: "https://console.example"},
		Email:  config.EmailConfig{SMTPHost: "smtp.example.com", SMTPFrom: "ops@example.com"},
	}}
	if got := methods(configured)["password_reset_email"]; got != true {
		t.Errorf("password_reset_email = %#v with SMTP configured, want true", got)
	}
}

// ---------- database-backed: the two paths share one token store ----------

// A confirmed reset must rotate the credential AND every derived auth artefact:
// old password dead, token_version bumped, API keys revoked, every outstanding
// link dead, the used token unreplayable, expired tokens refused. The operator
// path must issue a working link and invalidate the previous one.
//
// Gated on DATABASE_URL like the other tests that need real tables.
func TestPasswordResetConfirmAndAdminLink(t *testing.T) {
	p := newMemberProbe(t)
	target := p.user("pwreset_target", "user")
	admin := p.user("pwreset_admin", "user")

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// audit_logs.admin_id is a plain FK, so the probe's own user cleanup would
		// fail while these rows exist — remove them before it runs (cleanups are LIFO).
		_, _ = p.pool.Exec(c, "DELETE FROM audit_logs WHERE admin_id = $1", admin)
		_, _ = p.pool.Exec(c, "DELETE FROM audit_logs WHERE target_id = ANY($1::text[])",
			[]string{strconv.Itoa(int(target)), strconv.Itoa(int(admin))})
		_, _ = p.pool.Exec(c,
			"DELETE FROM password_reset_tokens WHERE user_id = ANY($1::int[]) OR created_by = ANY($1::int[])",
			[]int32{target, admin})
	})

	oldHash, err := auth.HashPassword("old-password-1")
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	if _, err := p.pool.Exec(p.ctx,
		"UPDATE users SET password_hash = $1, language = 'en' WHERE user_id = $2", oldHash, target); err != nil {
		t.Fatalf("seed password: %v", err)
	}
	// A key that outlives the password would defeat the rotation.
	keyHash := fmt.Sprintf("pwreset-probe-%d-%d", target, time.Now().UnixNano())
	if _, err := p.pool.Exec(p.ctx,
		`INSERT INTO api_keys (user_id, name, key_prefix, key_hash, is_active, expires_at)
		 VALUES ($1, 'probe', 'kcs_', $2, true, NOW() + interval '30 days')`, target, keyHash); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	// The helper both paths use to create a token.
	raw, expiresAt, err := p.app.issuePasswordResetToken(p.ctx, target, passwordResetEmailTTL, nil)
	if err != nil {
		t.Fatalf("issuePasswordResetToken: %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("issued token already expired: %v", expiresAt)
	}
	confirm := func(token, password string) (any, error) {
		body := fmt.Sprintf(`{"token":%q,"new_password":%q}`, token, password)
		return p.call(&CurrentUser{UserID: target}, body, p.app.passwordResetConfirm)
	}
	if _, err := confirm(raw, "new-password-2"); err != nil {
		t.Fatalf("passwordResetConfirm: %v", err)
	}

	var newHash string
	var tokenVersion int
	if err := p.pool.QueryRow(p.ctx,
		"SELECT password_hash, token_version FROM users WHERE user_id = $1", target).Scan(&newHash, &tokenVersion); err != nil {
		t.Fatalf("read user after reset: %v", err)
	}
	if !auth.VerifyPassword("new-password-2", newHash) {
		t.Error("the new password does not verify")
	}
	if auth.VerifyPassword("old-password-1", newHash) {
		t.Error("the old password still verifies")
	}
	if tokenVersion != 1 {
		t.Errorf("token_version = %d, want 1 (all pre-reset JWTs must die)", tokenVersion)
	}
	var keyActive bool
	if err := p.pool.QueryRow(p.ctx, "SELECT is_active FROM api_keys WHERE key_hash = $1", keyHash).Scan(&keyActive); err != nil {
		t.Fatalf("read api key: %v", err)
	}
	if keyActive {
		t.Error("API key still active after the password reset")
	}
	var usedAt *time.Time
	if err := p.pool.QueryRow(p.ctx,
		"SELECT used_at FROM password_reset_tokens WHERE token_hash = $1", passwordResetTokenHash(raw)).Scan(&usedAt); err != nil {
		t.Fatalf("read reset token: %v", err)
	}
	if usedAt == nil {
		t.Error("the redeemed token was not marked used")
	}
	// A password.reset audit row must exist, and it must not carry the token.
	var details string
	if err := p.pool.QueryRow(p.ctx,
		"SELECT details::text FROM audit_logs WHERE action = 'password.reset' AND target_id = $1 ORDER BY log_id DESC LIMIT 1",
		strconv.Itoa(int(target))).Scan(&details); err != nil {
		t.Fatalf("password.reset audit row missing: %v", err)
	}
	if strings.Contains(details, raw) {
		t.Error("the audit details contain the reset token")
	}

	// Replay: the same link must never work twice.
	if _, err := confirm(raw, "replayed-password-3"); err == nil {
		t.Fatal("a redeemed token was accepted a second time")
	} else {
		p.expect(err, http.StatusBadRequest)
		if apiErr, ok := err.(*ApiError); !ok || apiErr.Message != "重置链接无效或已过期" {
			t.Errorf("replay error = %v, want \"重置链接无效或已过期\"", err)
		}
	}

	// Expired: issued, then aged past its window.
	expiredRaw, _, err := p.app.issuePasswordResetToken(p.ctx, target, passwordResetEmailTTL, nil)
	if err != nil {
		t.Fatalf("issue expired token: %v", err)
	}
	if _, err := p.pool.Exec(p.ctx,
		"UPDATE password_reset_tokens SET expires_at = NOW() - interval '1 minute' WHERE token_hash = $1",
		passwordResetTokenHash(expiredRaw)); err != nil {
		t.Fatalf("age token: %v", err)
	}
	if _, err := confirm(expiredRaw, "expired-password-3"); err == nil {
		t.Fatal("an expired token was accepted")
	} else {
		p.expect(err, http.StatusBadRequest)
	}

	// ---- operator path ----
	p.app.Cfg.Server.PublicFrontendURL = "https://console.example"
	previousRaw, _, err := p.app.issuePasswordResetToken(p.ctx, target, passwordResetEmailTTL, nil)
	if err != nil {
		t.Fatalf("issue previous token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/platform/users/"+strconv.Itoa(int(target))+"/password-reset", nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey,
		&CurrentUser{UserID: admin, Username: "probe", Role: "platform_admin"}))
	out, err := p.app.platformPasswordReset(httptest.NewRecorder(), req, target)
	if err != nil {
		t.Fatalf("platformPasswordReset: %v", err)
	}
	resp, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("platformPasswordReset returned %T, want map[string]any", out)
	}
	var wantUsername string
	if err := p.pool.QueryRow(p.ctx, "SELECT username FROM users WHERE user_id = $1", target).Scan(&wantUsername); err != nil {
		t.Fatalf("read username: %v", err)
	}
	if resp["username"] != wantUsername {
		t.Errorf("username = %#v, want %q", resp["username"], wantUsername)
	}
	resetURL, _ := resp["reset_url"].(string)
	if !strings.HasPrefix(resetURL, "https://console.example/reset-password?token=") {
		t.Fatalf("reset_url = %q, want the configured frontend's reset page", resetURL)
	}
	// mail is not configured in this test app, so nothing was sent.
	if resp["emailed"] != false {
		t.Errorf("emailed = %#v, want false without SMTP", resp["emailed"])
	}
	expiresText, _ := resp["expires_at"].(string)
	if _, err := time.Parse(time.RFC3339, expiresText); err != nil {
		t.Errorf("expires_at = %q is not RFC3339: %v", expiresText, err)
	}
	// The operator's link really works.
	parsed, err := url.Parse(resetURL)
	if err != nil {
		t.Fatalf("parse reset_url: %v", err)
	}
	if _, err := confirm(parsed.Query().Get("token"), "admin-link-password-4"); err != nil {
		t.Fatalf("the operator-issued link does not confirm: %v", err)
	}
	// Issuing a new link killed the previous outstanding one.
	var previousUsed *time.Time
	if err := p.pool.QueryRow(p.ctx,
		"SELECT used_at FROM password_reset_tokens WHERE token_hash = $1",
		passwordResetTokenHash(previousRaw)).Scan(&previousUsed); err != nil {
		t.Fatalf("read previous token: %v", err)
	}
	if previousUsed == nil {
		t.Error("an older e-mail link survived the operator issuing a new one")
	}

	// The audit row names the operator and never the token/URL.
	var auditAction string
	var adminID *int32
	var adminDetails string
	if err := p.pool.QueryRow(p.ctx,
		"SELECT action, admin_id, details::text FROM audit_logs WHERE action = 'password.reset.link' AND target_id = $1 ORDER BY log_id DESC LIMIT 1",
		strconv.Itoa(int(target))).Scan(&auditAction, &adminID, &adminDetails); err != nil {
		t.Fatalf("password.reset.link audit row missing: %v", err)
	}
	if adminID == nil || *adminID != admin {
		t.Errorf("audit admin_id = %v, want the acting operator %d", adminID, admin)
	}
	if strings.Contains(adminDetails, parsed.Query().Get("token")) || strings.Contains(adminDetails, "reset-password") {
		t.Errorf("audit details leak the link material: %s", adminDetails)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(adminDetails), &decoded); err != nil {
		t.Errorf("audit details are not JSON: %v", err)
	} else if decoded["username"] != wantUsername {
		t.Errorf("audit details username = %#v, want %q", decoded["username"], wantUsername)
	}
}
