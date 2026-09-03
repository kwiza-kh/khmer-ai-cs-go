// Package config loads and validates environment configuration — a faithful
// port of the Rust `config.rs` (itself a port of the original Go
// `internal/config`), so every documented variable behaves identically.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// InsecureDefaultJwtSecret must never be used in production.
const InsecureDefaultJwtSecret = "khmer-ai-cs-jwt-secret-change-in-production"

type ServerConfig struct {
	Port         string // SERVER_PORT — listen port (default 8080)
	Mode         string // GIN_MODE — kept for compatibility; "release" gates Gemini
	PublicAPIURL string // PUBLIC_API_URL — public HTTPS origin without /api/v1
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type GeminiConfig struct {
	APIKey    string
	Model     string
	MaxTokens int
	CacheTTL  int
}

type R2Config struct {
	AccountID string
	AccessKey string
	SecretKey string
	Bucket    string
	PublicURL string
}

type JwtConfig struct {
	Secret     string
	ExpireHour int64
}

type MetaConfig struct {
	AppID                             string
	AppSecret                         string
	OAuthRedirectURL                  string
	OAuthFrontendURL                  string
	GraphAPIVersion                   string
	WhatsAppEmbeddedSignupConfigID    string
}

type EmailConfig struct {
	Enabled        bool
	SMTPHost       string
	SMTPPort       int
	SMTPUsername   string
	SMTPPassword   string
	FromAddress    string
	InboundDomains string
}

type VoiceConfig struct {
	Enabled            bool
	TwilioAccountSID   string
	TwilioAuthToken    string
	FromNumber         string
}

type SSOConfig struct {
	Enabled          bool
	Provider         string
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
}

type Config struct {
	Server                  ServerConfig
	DatabaseURL             string
	Redis                   RedisConfig
	Gemini                  GeminiConfig
	R2                      R2Config
	JWT                     JwtConfig
	PlatformCredentialKey   string
	AllowedOrigins          []string
	Meta                    MetaConfig
	MetaVerifyToken         string
	TelegramBotToken        string
	InitialAdminPassword    string
	Email                   EmailConfig
	Voice                   VoiceConfig
	SSO                     SSOConfig
	AllowRegistration       bool
	RegistrationInviteCode  string
}

// Load reads .env from the working directory (real environment variables win)
// and validates the result, aborting boot on any violation.
func Load() (*Config, error) {
	_ = godotenv.Load() // ignore missing file; process env is authoritative

	cfg := &Config{
		Server: ServerConfig{
			Port:         env("SERVER_PORT", "8080"),
			Mode:         env("GIN_MODE", "debug"),
			PublicAPIURL: env("PUBLIC_API_URL", ""),
		},
		DatabaseURL: env("DATABASE_URL", ""),
		Redis: RedisConfig{
			Addr:     env("REDIS_ADDR", "localhost:6379"),
			Password: env("REDIS_PASSWORD", ""),
			DB:       envInt("REDIS_DB", 0),
		},
		Gemini: GeminiConfig{
			APIKey:    env("GEMINI_API_KEY", ""),
			Model:     env("GEMINI_MODEL", "gemini-2.5-flash"),
			MaxTokens: envInt("GEMINI_MAX_TOKENS", 2048),
			CacheTTL:  envInt("GEMINI_CACHE_TTL", 3600),
		},
		R2: R2Config{
			AccountID: env("R2_ACCOUNT_ID", ""),
			AccessKey: env("R2_ACCESS_KEY", ""),
			SecretKey: env("R2_SECRET_KEY", ""),
			Bucket:    env("R2_BUCKET", "khmer-ai-cs"),
			PublicURL: env("R2_PUBLIC_URL", ""),
		},
		JWT: JwtConfig{
			Secret:     env("JWT_SECRET", ""),
			ExpireHour: int64(envInt("JWT_EXPIRE_HOUR", 24)),
		},
		PlatformCredentialKey: env("PLATFORM_CREDENTIAL_KEY", ""),
		Meta: MetaConfig{
			AppID:                          env("META_APP_ID", ""),
			AppSecret:                      env("META_APP_SECRET", ""),
			OAuthRedirectURL:               env("META_OAUTH_REDIRECT_URL", ""),
			OAuthFrontendURL:               env("META_OAUTH_FRONTEND_URL", ""),
			GraphAPIVersion:                env("META_GRAPH_API_VERSION", "v24.0"),
			WhatsAppEmbeddedSignupConfigID: env("META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID", ""),
		},
		MetaVerifyToken:      env("META_VERIFY_TOKEN", ""),
		TelegramBotToken:     env("TELEGRAM_BOT_TOKEN", ""),
		InitialAdminPassword: env("INITIAL_ADMIN_PASSWORD", ""),
		Email: EmailConfig{
			Enabled:        envBool("EMAIL_ENABLED", false),
			SMTPHost:       env("SMTP_HOST", ""),
			SMTPPort:       envInt("SMTP_PORT", 587),
			SMTPUsername:   env("SMTP_USERNAME", ""),
			SMTPPassword:   env("SMTP_PASSWORD", ""),
			FromAddress:    env("SMTP_FROM", ""),
			InboundDomains: env("EMAIL_INBOUND_DOMAINS", ""),
		},
		Voice: VoiceConfig{
			Enabled:          envBool("VOICE_ENABLED", false),
			TwilioAccountSID: env("TWILIO_ACCOUNT_SID", ""),
			TwilioAuthToken:  env("TWILIO_AUTH_TOKEN", ""),
			FromNumber:       env("TWILIO_FROM_NUMBER", ""),
		},
		SSO: SSOConfig{
			Enabled:          envBool("SSO_ENABLED", false),
			Provider:         env("SSO_PROVIDER", ""),
			OIDCIssuer:       env("SSO_OIDC_ISSUER", ""),
			OIDCClientID:     env("SSO_OIDC_CLIENT_ID", ""),
			OIDCClientSecret: env("SSO_OIDC_CLIENT_SECRET", ""),
		},
		AllowRegistration:      envBool("ALLOW_REGISTRATION", false),
		RegistrationInviteCode: env("REGISTRATION_INVITE_CODE", ""),
	}

	for _, o := range strings.Split(env("ALLOWED_ORIGINS", "http://localhost:3000"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, o)
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate aborts with a clear message on any violation (same rules as the
// Rust/Go implementations).
func (c *Config) Validate() error {
	if len(c.JWT.Secret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	if c.JWT.Secret == InsecureDefaultJwtSecret {
		return fmt.Errorf("JWT_SECRET must not be the insecure default")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	for _, o := range c.AllowedOrigins {
		if o == "*" {
			return fmt.Errorf("ALLOWED_ORIGINS must not be '*'")
		}
	}
	if c.PlatformCredentialKey == "" {
		return fmt.Errorf("PLATFORM_CREDENTIAL_KEY is required")
	}
	return c.validateDatabaseURL()
}

func (c *Config) validateDatabaseURL() error {
	url := strings.TrimSpace(c.DatabaseURL)
	if !strings.HasPrefix(url, "postgres://") && !strings.HasPrefix(url, "postgresql://") {
		return fmt.Errorf("DATABASE_URL must be a postgres:// or postgresql:// URL")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(url, "postgres://"), "postgresql://")
	authority := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		authority = rest[:i]
	}
	creds := authority
	if i := strings.Index(authority, "@"); i >= 0 {
		creds = authority[:i]
	}
	user, pass, found := strings.Cut(creds, ":")
	if !found || user == "" || pass == "" {
		return fmt.Errorf("DATABASE_URL must include user:password credentials")
	}
	if pass == "khmer_secret" {
		return fmt.Errorf("DATABASE_URL must not use the default password")
	}
	return nil
}

// R2Enabled reports whether all R2 credentials are present.
func (c *Config) R2Enabled() bool {
	return c.R2.AccountID != "" && c.R2.AccessKey != "" && c.R2.SecretKey != ""
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return fallback
}
