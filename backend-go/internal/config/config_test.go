package config

import "testing"

func TestValidateRejectsBadConfigs(t *testing.T) {
	valid := func() *Config {
		return &Config{
			Server:                ServerConfig{Port: "8080", Mode: "debug"},
			DatabaseURL:           "postgres://khmer:secret@localhost:5432/db",
			JWT:                   JwtConfig{Secret: "0123456789abcdef0123456789abcdef", ExpireHour: 24},
			PlatformCredentialKey: "Zm9v",
			AllowedOrigins:        []string{"http://localhost:3000"},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	// Short JWT secret.
	c := valid()
	c.JWT.Secret = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("short secret must be rejected")
	}
	// Insecure default secret.
	c = valid()
	c.JWT.Secret = InsecureDefaultJwtSecret
	if err := c.Validate(); err == nil {
		t.Fatal("insecure default must be rejected")
	}
	// Missing platform credential key.
	c = valid()
	c.PlatformCredentialKey = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing credential key must be rejected")
	}
	// Wildcard CORS.
	c = valid()
	c.AllowedOrigins = []string{"*"}
	if err := c.Validate(); err == nil {
		t.Fatal("wildcard origin must be rejected")
	}
	// Compose default password.
	c = valid()
	c.DatabaseURL = "postgres://khmer:khmer_secret@localhost:5432/db"
	if err := c.Validate(); err == nil {
		t.Fatal("default compose password must be rejected")
	}
	// Missing DATABASE_URL.
	c = valid()
	c.DatabaseURL = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing DATABASE_URL must be rejected")
	}
	// Non-postgres scheme.
	c = valid()
	c.DatabaseURL = "mysql://khmer:secret@localhost:5432/db"
	if err := c.Validate(); err == nil {
		t.Fatal("non-postgres scheme must be rejected")
	}
}

// TestSSOEnvMappingCoversEveryFieldTheGateReads pins the inputs googleSSOEnabled()
// reads. A field whose env mapping is missing stays empty, the gate returns false,
// and the console silently loses the Google button — not hypothetical: on
// 2026-10-08 the OIDCClientSecret mapping was removed with a "dead config" sweep
// and Google sign-in vanished from the login page for hours while every test
// stayed green. The next sweep has to break this test instead.
func TestSSOEnvMappingCoversEveryFieldTheGateReads(t *testing.T) {
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("PLATFORM_CREDENTIAL_KEY", "Zm9v")
	t.Setenv("ALLOWED_ORIGINS", "https://example.com")
	t.Setenv("SSO_ENABLED", "true")
	t.Setenv("SSO_PROVIDER", "google")
	t.Setenv("SSO_OIDC_ISSUER", "https://accounts.google.com")
	t.Setenv("SSO_OIDC_CLIENT_ID", "id.apps.googleusercontent.com")
	t.Setenv("SSO_OIDC_CLIENT_SECRET", "secret-value")
	t.Setenv("SSO_REDIRECT_URL", "https://example.com/api/v1/auth/google/callback")
	t.Setenv("SSO_FRONTEND_URL", "https://example.com/login")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.SSO.Enabled {
		t.Error("SSO_ENABLED=true must enable SSO")
	}
	if cfg.SSO.OIDCIssuer == "" {
		t.Error("SSO_OIDC_ISSUER must reach the config: the docs list it as required")
	}
	if cfg.SSO.OIDCClientID == "" || cfg.SSO.OIDCClientSecret == "" ||
		cfg.SSO.RedirectURL == "" || cfg.SSO.FrontendURL == "" {
		t.Fatalf("the Google gate reads empty fields: id=%q secret=%q redirect=%q frontend=%q",
			cfg.SSO.OIDCClientID, cfg.SSO.OIDCClientSecret, cfg.SSO.RedirectURL, cfg.SSO.FrontendURL)
	}
}

// Enabled() is what every KHQR sell path asks, and it deliberately means more
// than the switch: a deployment that sets KHQRPAY_ENABLED=true and forgets a
// credential must not render a payment button whose every click answers 503
// (docs/khqrpay-integration-spec.md §四.1).
func TestKHQRPayEnabledNeedsCredentialsNotJustTheSwitch(t *testing.T) {
	cases := []struct {
		name string
		cfg  KHQRPayConfig
		want bool
	}{
		{"default: off, no credentials", KHQRPayConfig{}, false},
		{"switch off, credentials present", KHQRPayConfig{ProfileID: "p", Secret: "s"}, false},
		{"switch on, no credentials", KHQRPayConfig{Toggle: true}, false},
		{"switch on, profile only", KHQRPayConfig{Toggle: true, ProfileID: "p"}, false},
		{"switch on, secret only", KHQRPayConfig{Toggle: true, Secret: "s"}, false},
		{"switch on, blank-looking credentials", KHQRPayConfig{Toggle: true, ProfileID: "  ", Secret: "s"}, false},
		{"switch on with both credentials", KHQRPayConfig{Toggle: true, ProfileID: "p", Secret: "s"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Same lesson as the SSO test above: an env mapping that goes missing leaves the
// field empty, which closes the gate silently. KHQR's gate is the one place the
// frontend asks about, so every variable is read back here.
func TestKHQRPayEnvMappingCoversEveryFieldTheGateReads(t *testing.T) {
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("PLATFORM_CREDENTIAL_KEY", "Zm9v")
	t.Setenv("ALLOWED_ORIGINS", "https://example.com")
	t.Setenv("KHQRPAY_ENABLED", "true")
	t.Setenv("KHQRPAY_PROFILE_ID", "prof-1")
	t.Setenv("KHQRPAY_SECRET", "signing-secret")
	t.Setenv("KHQRPAY_BASE_URL", "https://sandbox.khqr.test")
	t.Setenv("KHQRPAY_CURRENCY", "KHR")
	t.Setenv("KHQRPAY_PRICE_PRO", "10.00")
	t.Setenv("KHQRPAY_PRICE_ENTERPRISE", "99.00")
	t.Setenv("KHQRPAY_VERIFY_PATH", "/payments/verify")
	t.Setenv("KHQRPAY_CREATE_PATH", "/api/payments/qr-api-khqr")
	t.Setenv("KHQRPAY_WEBHOOK_TOKEN", "token-0123456789abcdef0123456789abcdef")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.KHQRPay.Enabled() {
		t.Fatal("KHQRPAY_ENABLED=true with credentials must enable the channel")
	}
	k := cfg.KHQRPay
	if k.ProfileID != "prof-1" || k.Secret != "signing-secret" {
		t.Errorf("credentials did not reach the config: profile=%q secret set=%v", k.ProfileID, k.Secret != "")
	}
	if k.BaseURL != "https://sandbox.khqr.test" || k.Currency != "KHR" {
		t.Errorf("base/currency = %q/%q", k.BaseURL, k.Currency)
	}
	if k.PricePro != "10.00" || k.PriceEnterprise != "99.00" {
		t.Errorf("prices = %q/%q", k.PricePro, k.PriceEnterprise)
	}
	if k.VerifyPath != "/payments/verify" {
		t.Errorf("verify path = %q", k.VerifyPath)
	}
	// The create route is configurable because the vendor's documented one 404s;
	// its mapping has to be pinned like the rest, or a deployment "fixes" the path
	// in .env-go and nothing happens.
	if k.CreatePath != "/api/payments/qr-api-khqr" {
		t.Errorf("create path = %q", k.CreatePath)
	}
	// The callback token is its own secret (it travels in a URL, so it ends up in
	// access logs, which is exactly where the signing key must never be): own
	// mapping, and never fed from KHQRPAY_SECRET.
	if k.WebhookToken != "token-0123456789abcdef0123456789abcdef" {
		t.Errorf("webhook token = %q", k.WebhookToken)
	}
	if k.WebhookToken == k.Secret {
		t.Error("webhook token must not be the signing secret")
	}
}

// Unset KHQR variables must leave the channel off with the documented defaults —
// the risk notice in the spec's §二 makes "on by accident" the one unacceptable
// boot state.
func TestKHQRPayDefaultsToOff(t *testing.T) {
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("PLATFORM_CREDENTIAL_KEY", "Zm9v")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KHQRPay.Enabled() {
		t.Error("KHQRPay must be off unless KHQRPAY_ENABLED=true")
	}
	// anajakpay.com, the host the real endpoint was verified on (khqr.cc 301s there).
	if cfg.KHQRPay.BaseURL != "https://anajakpay.com" {
		t.Errorf("default base URL = %q", cfg.KHQRPay.BaseURL)
	}
	if cfg.KHQRPay.Currency != "USD" {
		t.Errorf("default currency = %q", cfg.KHQRPay.Currency)
	}
	if cfg.KHQRPay.VerifyPath != "" {
		t.Errorf("verify path must default to empty (webhook-only), got %q", cfg.KHQRPay.VerifyPath)
	}
	// Empty means "refuse every callback", never "accept unverified ones".
	if cfg.KHQRPay.WebhookToken != "" {
		t.Errorf("webhook token must default to empty, got %q", cfg.KHQRPay.WebhookToken)
	}
	// Empty CreatePath means the built-in template, so the shipped URL does not
	// depend on anyone remembering to set it.
	if cfg.KHQRPay.CreatePath != "" {
		t.Errorf("create path must default to empty (built-in template), got %q", cfg.KHQRPay.CreatePath)
	}
}
