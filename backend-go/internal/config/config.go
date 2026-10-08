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
	AppID                          string
	AppSecret                      string
	OAuthRedirectURL               string
	OAuthFrontendURL               string
	GraphAPIVersion                string
	WhatsAppEmbeddedSignupConfigID string
	OAuthScopes                    string // META_OAUTH_SCOPES — optional override of the Facebook Login scope list
}

type EmailConfig struct {
	Enabled        bool
	SMTPHost       string
	InboundDomains string
}

type VoiceConfig struct {
	Enabled          bool
	TwilioAccountSID string
	FromNumber       string
}

type SSOConfig struct {
	Enabled      bool
	Provider     string
	OIDCIssuer   string
	OIDCClientID string
	// OIDCClientSecret is read by the Google token exchange (a confidential client).
	OIDCClientSecret string
	// RedirectURL — the public callback registered in the provider console
	// (e.g. https://host/api/v1/auth/google/callback). FrontendURL receives
	// the one-time login code after a successful exchange.
	RedirectURL string
	FrontendURL string
	// AllowSignup — Google sign-in may auto-provision an account even when
	// password registration is closed. Decoupled from ALLOW_REGISTRATION so a
	// deployment can restrict self-service password signups while still
	// letting staff sign in with their Google Workspace account.
	AllowSignup bool
	// SkipIDTokenVerify — escape hatch for deployments whose egress to the
	// identity provider's JWKS endpoint is blocked. When true the id_token is
	// trusted on transport (the pre-audit behaviour) instead of failing
	// closed; leave it false unless the deployment cannot reach the JWKS URL.
	SkipIDTokenVerify bool
}

// TelegramLoginConfig — "Log in with Telegram" via Telegram's OpenID Connect
// flow, configured in @BotFather (Bot Settings → Web Login).
//
// Deliberately separate from SSOConfig: that one is Google-specific (its
// default issuer and endpoints are hard-coded), and a deployment may well run
// both at once. The bot used here must be a dedicated platform-level bot —
// the per-tenant bots in platform_configs / telegram_notify_settings belong to
// individual merchants and must not be repurposed for sign-in.
type TelegramLoginConfig struct {
	Enabled      bool
	ClientID     string
	ClientSecret string // NOT the bot token — BotFather issues a separate secret
	// RedirectURL is the public callback registered in BotFather's
	// "Redirect URIs" list. Must match exactly, or Telegram refuses to redirect.
	RedirectURL string
	// FrontendURL receives the one-time login code after a successful exchange.
	FrontendURL string
	AllowSignup bool
	// RequestPhone adds the `phone` scope, returning Telegram's verified phone
	// number. SMS verification is expensive in this market, so it is worth
	// capturing at sign-in even before anything consumes it.
	RequestPhone bool
}

// PlatformBotConfig — the operator-owned Telegram bot.
//
// Distinct from every other bot in the system: the ones in platform_configs
// and telegram_notify_settings belong to individual merchants. This one is
// yours, it is the only one, and it serves the whole installation — operator
// alerts, merchant account-linking, the admin command console and the support
// inbox all arrive here.
//
// Its token is a platform credential: it can message every linked merchant, so
// it warrants the same care as the JWT secret.
type PlatformBotConfig struct {
	Token string // PLATFORM_TELEGRAM_BOT_TOKEN (BotFather)
	// WebhookSecret is passed to Telegram as secret_token and comes back on
	// every delivery as X-Telegram-Bot-Api-Secret-Token. Without it the
	// webhook endpoint would accept updates from anyone who knows the URL.
	WebhookSecret string
	// AdminIDs are the Telegram user ids allowed to run operator commands.
	// Telegram asserts from.id, so this list is an identity check — but the
	// handler still re-checks the linked account is a platform_admin.
	AdminIDs []int64 // PLATFORM_TELEGRAM_ADMINS, comma-separated
	// AlertChatID receives operational alerts. Defaults to the first admin id.
	AlertChatID string // PLATFORM_TELEGRAM_ALERT_CHAT
	// PublicBotUsername is the @handle, used to build the t.me deep link for
	// merchant account-linking (e.g. "relaychat_bot").
	PublicBotUsername string // PLATFORM_TELEGRAM_BOT_USERNAME
}

// TTSConfig gates voice replies (Gemini TTS → R2 → platform audio message).
// Requires R2 so the synthesized WAV has somewhere providers can download.
type TTSConfig struct {
	Enabled bool // TTS_ENABLED — off by default (per-voice replies surprise merchants)
}

// PayPalConfig configures collecting money through the platform's own PayPal
// business account. Prices live in the environment because they are a business
// decision, not a code constant: a plan with no price is simply not purchasable
// (the upgrade page offers nothing), so the service can ship with credentials and
// no prices and still be correct.
type PayPalConfig struct {
	ClientID        string // PAYPAL_CLIENT_ID
	ClientSecret    string // PAYPAL_CLIENT_SECRET
	Mode            string // PAYPAL_MODE: sandbox (default) | live
	WebhookID       string // PAYPAL_WEBHOOK_ID — required to accept webhooks
	Currency        string // PAYPAL_CURRENCY, default USD
	PricePro        string // PAYPAL_PRICE_PRO — empty = not for sale
	PriceEnterprise string // PAYPAL_PRICE_ENTERPRISE — empty = not for sale
}

// Enabled reports whether the platform can take money at all.
func (p PayPalConfig) Enabled() bool {
	return strings.TrimSpace(p.ClientID) != "" && strings.TrimSpace(p.ClientSecret) != ""
}

type Config struct {
	Server                 ServerConfig
	DatabaseURL            string
	Redis                  RedisConfig
	Gemini                 GeminiConfig
	R2                     R2Config
	JWT                    JwtConfig
	PlatformCredentialKey  string
	AllowedOrigins         []string
	Meta                   MetaConfig
	MetaVerifyToken        string
	TelegramBotToken       string
	InitialAdminPassword   string
	Email                  EmailConfig
	Voice                  VoiceConfig
	SSO                    SSOConfig
	TelegramLogin          TelegramLoginConfig
	PlatformBot            PlatformBotConfig
	TTS                    TTSConfig
	AllowRegistration      bool
	RegistrationInviteCode string
	PayPal                 PayPalConfig
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
			APIKey: env("GEMINI_API_KEY", ""),
			Model:  env("GEMINI_MODEL", "gemini-3.5-flash"),
			// 1024, not 2048: the cap does not change the average bill (output is
			// billed as produced — the measured mean is 135 tokens, so headroom
			// above that costs nothing), it bounds the rare runaway answer that
			// would eat a rolling spend window. Halved rather than cut to the
			// mean because a truncated price-list answer is worse than the
			// tokens it saves. Once a model_configs row exists its max_tokens
			// wins — set it there too (admin Models page).
			MaxTokens: envInt("GEMINI_MAX_TOKENS", 1024),
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
			OAuthScopes:                    env("META_OAUTH_SCOPES", ""),
		},
		MetaVerifyToken:      env("META_VERIFY_TOKEN", ""),
		TelegramBotToken:     env("TELEGRAM_BOT_TOKEN", ""),
		InitialAdminPassword: env("INITIAL_ADMIN_PASSWORD", ""),
		Email: EmailConfig{
			Enabled:        envBool("EMAIL_ENABLED", false),
			SMTPHost:       env("SMTP_HOST", ""),
			InboundDomains: env("EMAIL_INBOUND_DOMAINS", ""),
		},
		Voice: VoiceConfig{
			Enabled:          envBool("VOICE_ENABLED", false),
			TwilioAccountSID: env("TWILIO_ACCOUNT_SID", ""),
			FromNumber:       env("TWILIO_FROM_NUMBER", ""),
		},
		TelegramLogin: TelegramLoginConfig{
			Enabled:      envBool("TELEGRAM_LOGIN_ENABLED", false),
			ClientID:     env("TELEGRAM_LOGIN_CLIENT_ID", ""),
			ClientSecret: env("TELEGRAM_LOGIN_CLIENT_SECRET", ""),
			RedirectURL:  env("TELEGRAM_LOGIN_REDIRECT_URL", ""),
			FrontendURL:  env("TELEGRAM_LOGIN_FRONTEND_URL", ""),
			AllowSignup:  envBool("TELEGRAM_LOGIN_ALLOW_SIGNUP", true),
			RequestPhone: envBool("TELEGRAM_LOGIN_REQUEST_PHONE", true),
		},
		PlatformBot: PlatformBotConfig{
			Token:             env("PLATFORM_TELEGRAM_BOT_TOKEN", ""),
			WebhookSecret:     env("PLATFORM_TELEGRAM_WEBHOOK_SECRET", ""),
			AdminIDs:          envInt64List("PLATFORM_TELEGRAM_ADMINS"),
			AlertChatID:       env("PLATFORM_TELEGRAM_ALERT_CHAT", ""),
			PublicBotUsername: env("PLATFORM_TELEGRAM_BOT_USERNAME", ""),
		},
		SSO: SSOConfig{
			Enabled:      envBool("SSO_ENABLED", false),
			Provider:     env("SSO_PROVIDER", ""),
			OIDCIssuer:   env("SSO_OIDC_ISSUER", ""),
			OIDCClientID: env("SSO_OIDC_CLIENT_ID", ""),
			// NOT dead config: googleSSOEnabled() gates the login page's Google
			// button on this field, and the token exchange sends it. Deleting the
			// mapping (not just the field) turns Google sign-in off silently —
			// which is exactly what happened on 2026-10-08.
			OIDCClientSecret:  env("SSO_OIDC_CLIENT_SECRET", ""),
			RedirectURL:       env("SSO_REDIRECT_URL", ""),
			FrontendURL:       env("SSO_FRONTEND_URL", ""),
			AllowSignup:       envBool("SSO_ALLOW_SIGNUP", true),
			SkipIDTokenVerify: envBool("SSO_SKIP_ID_TOKEN_VERIFY", false),
		},
		TTS: TTSConfig{
			Enabled: envBool("TTS_ENABLED", false),
		},
		AllowRegistration:      envBool("ALLOW_REGISTRATION", false),
		RegistrationInviteCode: env("REGISTRATION_INVITE_CODE", ""),
		PayPal: PayPalConfig{
			ClientID:        env("PAYPAL_CLIENT_ID", ""),
			ClientSecret:    env("PAYPAL_CLIENT_SECRET", ""),
			Mode:            env("PAYPAL_MODE", "sandbox"),
			WebhookID:       env("PAYPAL_WEBHOOK_ID", ""),
			Currency:        env("PAYPAL_CURRENCY", "USD"),
			PricePro:        env("PAYPAL_PRICE_PRO", ""),
			PriceEnterprise: env("PAYPAL_PRICE_ENTERPRISE", ""),
		},
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
	if err := c.validatePayPal(); err != nil {
		return err
	}
	return c.validateDatabaseURL()
}

// validatePayPal refuses a half-configured payment integration. No credentials is
// fine (payments simply stay off); a half-set pair or an unknown mode is a
// deployment mistake that would otherwise only surface at checkout.
func (c *Config) validatePayPal() error {
	p := c.PayPal
	hasID, hasSecret := strings.TrimSpace(p.ClientID) != "", strings.TrimSpace(p.ClientSecret) != ""
	if hasID != hasSecret {
		return fmt.Errorf("PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET must be set together")
	}
	if !hasID {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(p.Mode)) {
	case "", "sandbox", "live":
	default:
		return fmt.Errorf("PAYPAL_MODE must be sandbox or live")
	}
	if strings.TrimSpace(p.Currency) == "" {
		return fmt.Errorf("PAYPAL_CURRENCY is required when PayPal is configured")
	}
	if strings.TrimSpace(p.PricePro) == "" && strings.TrimSpace(p.PriceEnterprise) == "" {
		// Not a violation — credentials may land before the pricing decision — but
		// an id/secret pair with nothing sellable is almost always an unfinished
		// rollout, so say it out loud instead of only failing at checkout.
		fmt.Fprintln(os.Stderr, "config: PayPal is configured but no plan has a price (PAYPAL_PRICE_PRO / PAYPAL_PRICE_ENTERPRISE); the upgrade page will offer nothing")
	}
	return nil
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

// TTSActive reports whether voice replies can run: the opt-in flag plus R2
// storage (the synthesized WAV must be downloadable by the platform).
func (c *Config) TTSActive() bool {
	return c.TTS.Enabled && c.R2Enabled()
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

// envInt64List parses a comma-separated list of integers (Telegram user ids,
// which exceed int32 on some accounts). Blank entries and unparsable values are
// skipped rather than aborting startup — a typo in the allow-list should not
// take the service down, it should just not grant anyone access.
func envInt64List(key string) []int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
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
