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
