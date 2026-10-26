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
	// PublicFrontendURL is the browser origin used to build user-facing links,
	// most importantly the password-reset link. Empty means "derive it from
	// PUBLIC_API_URL minus /api/v1, then SSO_FRONTEND_URL minus /login" — see
	// api.App.frontendBaseURL. It is separate from PUBLIC_API_URL because the
	// API and the console are routinely different hosts (and the API origin is
	// what backends fetch, never what a user should see in an e-mail).
	PublicFrontendURL string // PUBLIC_FRONTEND_URL
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
	// CommentDM answers new comments on a Page post / an IG post with one private
	// message, which is how a commenter enters the DM thread the AI serves. Off by
	// default: the feed/comments webhook field needs pages_read_engagement +
	// pages_manage_engagement (or instagram_manage_comments) through App Review,
	// and receiving the events without them is not possible.
	CommentDM bool
}

type EmailConfig struct {
	// SMTPHost/SMTPFrom together switch on outbound mail (password-reset
	// e-mails). SMTP_PORT/USERNAME/PASSWORD complete the relay settings.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
}

// SMTPConfigured reports whether outbound mail has the minimum two settings:
// a relay host and a sender address. It deliberately ignores Enabled —
// EMAIL_ENABLED gates the notification channel, while a deployment can want
// password-reset mail without notifications (or the reverse). The pair rule
// matters: a half-configured relay is a deployment mistake, and sending to a
// guessed host is worse than not sending.
func (e EmailConfig) SMTPConfigured() bool {
	return strings.TrimSpace(e.SMTPHost) != "" && strings.TrimSpace(e.SMTPFrom) != ""
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
	// merchant account-linking (e.g. "mekongchat_bot").
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

// KHQRPayConfig is the second one-time checkout channel: Cambodia's KHQR
// standard through the khqr.cc / anajakpay.com gateway (internal/khqrpay).
//
// It is deliberately not folded into PayPalConfig: different credentials,
// possibly a different currency, its own price list (the merchant may price the
// local rail differently), and its own switch — the 2026-09-11 NBC notice that
// khqr.cc is unlicensed means operations must be able to remove this channel
// with one environment variable and no deploy (docs/khqrpay-integration-spec.md §二).
type KHQRPayConfig struct {
	// Toggle is KHQRPAY_ENABLED — the operator's switch, and nothing more.
	// Every sell path asks Enabled() instead: a switch without credentials is
	// not a channel, and offering a payment button that cannot work is worse
	// than offering none.
	Toggle bool // KHQRPAY_ENABLED, default false
	// ProfileID is the merchant profile id and a path segment of every call.
	ProfileID string // KHQRPAY_PROFILE_ID
	// Secret signs every create/verify call. Env only: never in the database,
	// never in a log line or an error string (see internal/khqrpay).
	Secret string // KHQRPAY_SECRET
	// BaseURL exists so a test profile or a staging host never needs a code
	// change; no trailing slash. Defaults to anajakpay.com: khqr.cc 301s there and
	// both hosts reach the same application, but the endpoint verified with real
	// credentials (2026-10-26) lives on the former.
	BaseURL string // KHQRPAY_BASE_URL, default https://anajakpay.com
	// CreatePath replaces the whole create path. Empty keeps the built-in
	// template; a value may contain {profile}, substituted with the escaped
	// merchant id at request time, and is used verbatim when it does not. It
	// exists because the marketing page's documented path answered 404 on the real
	// gateway (probed 2026-10-26) — a wrong route has to be correctable in
	// .env-go, not by rebuilding this service.
	CreatePath string // KHQRPAY_CREATE_PATH
	// Currency is the sale currency for this channel; amounts are compared
	// against it exactly (USD and KHR are not interchangeable).
	Currency string // KHQRPAY_CURRENCY, default USD
	// A plan with no price here is not sold through KHQR — same rule as the
	// PayPal price fields, and the reason free never needs an entry.
	PricePro        string // KHQRPAY_PRICE_PRO
	PriceEnterprise string // KHQRPAY_PRICE_ENTERPRISE
	// VerifyPath is the provider's verification endpoint, relative to
	// BaseURL/{ProfileID}. Empty (the shipped default) means webhook-only: the
	// callback is then a trigger and the local row is the record. Filling it
	// in later upgrades to a server-side confirmation with no code change.
	VerifyPath string // KHQRPAY_VERIFY_PATH
	// WebhookToken is the unguessable path segment of the callback URL
	// (POST /api/v1/billing/khqr/webhook/{token}). It proves the caller was given
	// the URL, which only the gateway was.
	//
	// It is a separate secret from Secret on purpose, and the reason is where each
	// one travels: this value sits in a URL, so it lands in nginx access logs, in
	// a proxy's history and in whatever an operator pastes into a merchant
	// console. The signing secret must never be in any of those places, so one
	// string cannot serve both roles. Empty = settlement refused (fail closed):
	// without it the callback route is an unauthenticated "grant me a plan" and
	// any shop owner could POST their own paid event with a transaction id they
	// were just handed.
	WebhookToken string // KHQRPAY_WEBHOOK_TOKEN
}

// Enabled reports whether this deployment can actually take KHQR money: the
// switch is on and both credentials are present. A half-configured channel is a
// boot warning (validateKHQRPay), not a crash — the route then answers "not
// configured" and the upgrade page offers no QR button.
func (k KHQRPayConfig) Enabled() bool {
	return k.Toggle && strings.TrimSpace(k.ProfileID) != "" && strings.TrimSpace(k.Secret) != ""
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
	Email                  EmailConfig
	SSO                    SSOConfig
	TelegramLogin          TelegramLoginConfig
	PlatformBot            PlatformBotConfig
	TTS                    TTSConfig
	AllowRegistration      bool
	RegistrationInviteCode string
	PayPal                 PayPalConfig
	KHQRPay                KHQRPayConfig
}

// Load reads .env from the working directory (real environment variables win)
// and validates the result, aborting boot on any violation.
func Load() (*Config, error) {
	_ = godotenv.Load() // ignore missing file; process env is authoritative

	cfg := &Config{
		Server: ServerConfig{
			Port:              env("SERVER_PORT", "8080"),
			Mode:              env("GIN_MODE", "debug"),
			PublicAPIURL:      env("PUBLIC_API_URL", ""),
			PublicFrontendURL: env("PUBLIC_FRONTEND_URL", ""),
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
			CommentDM:                      envBool("META_COMMENT_DM_ENABLED", false),
		},
		MetaVerifyToken: env("META_VERIFY_TOKEN", ""),
		Email: EmailConfig{
			SMTPHost:     env("SMTP_HOST", ""),
			SMTPPort:     envInt("SMTP_PORT", 587),
			SMTPUsername: env("SMTP_USERNAME", ""),
			SMTPPassword: env("SMTP_PASSWORD", ""),
			SMTPFrom:     env("SMTP_FROM", ""),
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
		KHQRPay: KHQRPayConfig{
			Toggle:          envBool("KHQRPAY_ENABLED", false),
			ProfileID:       env("KHQRPAY_PROFILE_ID", ""),
			Secret:          env("KHQRPAY_SECRET", ""),
			BaseURL:         env("KHQRPAY_BASE_URL", "https://anajakpay.com"),
			Currency:        env("KHQRPAY_CURRENCY", "USD"),
			PricePro:        env("KHQRPAY_PRICE_PRO", ""),
			PriceEnterprise: env("KHQRPAY_PRICE_ENTERPRISE", ""),
			VerifyPath:      env("KHQRPAY_VERIFY_PATH", ""),
			CreatePath:      env("KHQRPAY_CREATE_PATH", ""),
			WebhookToken:    env("KHQRPAY_WEBHOOK_TOKEN", ""),
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
	c.validateKHQRPay()
	// Not a fatal error: password-reset e-mail is optional (the platform admin
	// can generate a link instead), but a half-set pair is almost always an
	// unfinished rollout and would otherwise only show up as silent "no mail".
	// Same style as the PayPal no-price warning below.
	if host, from := strings.TrimSpace(c.Email.SMTPHost) != "", strings.TrimSpace(c.Email.SMTPFrom) != ""; host != from {
		fmt.Fprintln(os.Stderr, "config: SMTP_HOST and SMTP_FROM must be set together; password-reset e-mail is disabled until both are present (the platform admin can still generate reset links)")
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

// validateKHQRPay warns about the half-finished states this channel can boot in:
// a switch without credentials, a configuration with nothing priced, a missing
// callback token, and a token that is weak or doubles as the signing secret. It
// never fails the boot, on purpose: the channel is off by default and must stay
// removable with one environment variable, so a wrong KHQRPAY_* value has to cost
// a log line, not a stopped service. It also never prints the secret or the token
// — only which variable is wrong and how.
func (c *Config) validateKHQRPay() {
	k := c.KHQRPay
	if !k.Toggle {
		return
	}
	hasProfile, hasSecret := strings.TrimSpace(k.ProfileID) != "", strings.TrimSpace(k.Secret) != ""
	if !hasProfile || !hasSecret {
		missing := "KHQRPAY_PROFILE_ID"
		if hasProfile {
			missing = "KHQRPAY_SECRET"
		}
		fmt.Fprintln(os.Stderr, "config: KHQRPAY_ENABLED=true but "+missing+" is empty; the KHQR channel stays disabled (no route will offer it)")
		return
	}
	if strings.TrimSpace(k.PricePro) == "" && strings.TrimSpace(k.PriceEnterprise) == "" {
		fmt.Fprintln(os.Stderr, "config: KHQRPay is configured but no plan has a price (KHQRPAY_PRICE_PRO / KHQRPAY_PRICE_ENTERPRISE); the upgrade page will offer no QR button")
	}
	if strings.TrimSpace(k.VerifyPath) == "" {
		// Not a violation — the shipped design works webhook-only — but the
		// operator should know the callback is a trigger rather than a judge
		// (docs/khqrpay-integration-spec.md §三.2).
		fmt.Fprintln(os.Stderr, "config: KHQRPAY_VERIFY_PATH is empty; KHQR payments are confirmed from the webhook payload and the local order row only")
	}
	// Say out loud when the create route is not the built-in one. The vendor's
	// marketing page documented a path that the real gateway answered with 404
	// (2026-10-26), so a deployment running a custom route is legitimate — and
	// otherwise invisible, which is how the next person concludes the code and
	// production disagree.
	createPath := strings.TrimSpace(k.CreatePath)
	if createPath != "" {
		fmt.Fprintln(os.Stderr, "config: KHQRPAY_CREATE_PATH is set; QR creation uses the custom path "+createPath+" instead of the built-in "+"/{profile}/payment-gateway/v1/payments/qr-api-khqr")
		if !strings.Contains(createPath, "{profile}") {
			// Legitimate (the merchant id may belong in a query string), but much
			// more often a forgotten placeholder.
			fmt.Fprintln(os.Stderr, "config: KHQRPAY_CREATE_PATH has no {profile} placeholder; the merchant profile id will not appear anywhere in the create URL")
		}
	}
	// The token is a hard requirement for settlement, so say it loudly at boot
	// instead of letting the first real callback discover it: refusing every
	// callback is the correct behaviour, and the operator needs to know why the
	// gateway is retrying.
	token := strings.TrimSpace(k.WebhookToken)
	switch {
	case token == "":
		fmt.Fprintln(os.Stderr, "config: KHQRPAY_WEBHOOK_TOKEN is empty; KHQR callbacks will be REFUSED (503) and no payment can settle until it is set to a random 32+ character string that is not KHQRPAY_SECRET")
	case token == strings.TrimSpace(k.Secret):
		fmt.Fprintln(os.Stderr, "config: KHQRPAY_WEBHOOK_TOKEN must not be the signing secret; the callback URL ends up in access logs, the signing key must not")
	case len(token) < 32:
		fmt.Fprintln(os.Stderr, "config: KHQRPAY_WEBHOOK_TOKEN is shorter than 32 characters; it is the only thing standing between a logged-in tenant and a free plan")
	}
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
