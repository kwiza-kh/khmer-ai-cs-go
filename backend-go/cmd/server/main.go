// Command server boots the Go backend: config → postgres → redis → HTTP with
// graceful shutdown. The migration step lives in cmd/migrate (parity with the
// Rust deployment).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"khmer-ai-cs-go/internal/api"
	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/db"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/redisstore"
	"khmer-ai-cs-go/internal/security"
	"khmer-ai-cs-go/internal/storager2"
	"khmer-ai-cs-go/internal/typesafe"
	"khmer-ai-cs-go/internal/usage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("postgres unavailable", "error", err.Error())
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	redisClient, err := redisstore.Connect(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		logger.Error("redis unavailable", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("connected to redis")

	// Platform credential sealer. Built before the Gemini service so the
	// platform-global model key can be decrypted on load: model_configs.api_key
	// lives in the same sealed-column regime as the channel credentials.
	sealer, err := security.NewSealer(cfg.PlatformCredentialKey)
	if err != nil {
		logger.Error("invalid platform credential key", "error", err.Error())
		os.Exit(1)
	}

	// Gemini service — prefer the DB default model config (admin Models page is
	// the source of truth once a key is saved), fall back to env.
	var gem *gemini.Service
	if apiKey, modelName, systemPrompt, maxTokens, ok := gemini.LoadDefaultConfig(ctx, pool); ok {
		gem = gemini.FromPartsFull(sealer.DecryptOrKeep(apiKey), modelName, systemPrompt, maxTokens)
		if gem.IsConfigured() {
			logger.Info("Gemini configured from database model config", "model", gem.ModelName())
		} else {
			logger.Warn("Gemini DB config has no key — falling back to env/mock")
			gem = gemini.New(cfg.Gemini.APIKey, cfg.Gemini.Model, cfg.Gemini.MaxTokens)
		}
	} else {
		gem = gemini.New(cfg.Gemini.APIKey, cfg.Gemini.Model, cfg.Gemini.MaxTokens)
	}
	if !gem.IsConfigured() {
		logger.Warn("Gemini not configured — running in mock mode")
	}

	// Jev (TypeSafe System One) powers every typed judgment — turn
	// classification, rerank, routing, guardrails, notify triage. It is nil
	// without TYPESAFE_API_KEY, and every site then keeps its previous path.
	jev := typesafe.NewFromEnv(logger)

	ragService := &rag.Service{DB: pool, Gemini: gem, Redis: redisClient, Logger: logger, Jev: jev}
	ragService.SpawnIndexWorkers(ctx)

	// Attribute auxiliary model spend to whichever tenant tagged the context.
	// Previously only the four main chat paths recorded usage, so the
	// auxiliary calls — the ingest-time compile above all, a 4096-token call
	// per document — were invisible to every cost dashboard.
	gemini.AuxUsageObserver = func(ctx context.Context, model string, prompt, completion, cached int) {
		if uid, ok := usage.UserFrom(ctx); ok {
			usage.Record(ctx, pool, uid, nil, model, prompt, completion, cached)
		}
	}

	// R2 object storage (inbound media replay + TTS audio; inert without creds).
	media := storager2.New(cfg.R2.AccountID, cfg.R2.AccessKey, cfg.R2.SecretKey, cfg.R2.Bucket, cfg.R2.PublicURL)

	// Platform pipeline (inbound AI replies + outbound delivery).
	pipe := &platform.Pipeline{DB: pool, Redis: redisClient, Cfg: cfg, Gemini: gem, RAG: ragService, Sealer: sealer, Media: media, Logger: logger, Jev: jev}
	pipe.SpawnWorkers(ctx)

	app := &api.App{
		Cfg:    cfg,
		DB:     pool,
		Redis:  redisClient,
		JWT:    auth.NewJWT(cfg.JWT.Secret, cfg.JWT.ExpireHour),
		Gemini: gem,
		RAG:    ragService,
		Logger: logger,
		Sealer: sealer,
		Media:  media,
		Pipe:   pipe,
	}

	// Converge migration-era plaintext secrets to sealed form. Migration 052's
	// header claims read-path re-sealing that was never implemented; this
	// boot-time backfill is the convergence mechanism, so legacy rows do not
	// hold working TOTP secrets / channel credentials in cleartext forever.
	if n, err := api.BackfillLegacySecrets(ctx, pool, sealer); err != nil {
		logger.Warn("legacy secret backfill failed; rows stay plaintext", "error", err.Error())
	} else if n > 0 {
		logger.Info("legacy plaintext secrets re-sealed at boot", "count", n)
	}

	// Google JWKS cache for id_token signature verification.
	app.SSOJWKS = api.NewJWKSCache(api.GoogleJWKSURL)

	// Realtime inbox hub (WebSocket fan-out fed by Redis pub/sub).
	allowedOrigins := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowedOrigins[o] = true
	}
	hub := realtime.NewHub(app.JWT, redisClient, logger,
		func(ctx context.Context, userID int32) (bool, int) {
			var active bool
			var tokenVersion int
			if err := pool.QueryRow(ctx, "SELECT is_active, token_version FROM users WHERE user_id = $1", userID).Scan(&active, &tokenVersion); err != nil {
				return false, 0
			}
			return active, tokenVersion
		},
		func(origin string) bool { return origin == "" || allowedOrigins[origin] },
	)
	app.Realtime = hub
	hub.Start(ctx)

	// Webhook handlers (Meta/WhatsApp/Telegram/LINE).
	webhooks := &platform.Webhooks{DB: pool, Pipe: pipe, Sealer: sealer, MetaVerifyToken: cfg.MetaVerifyToken}
	whMux := http.NewServeMux()
	whMux.HandleFunc("/api/v1/webhook/meta", webhooks.MetaWebhook)
	whMux.HandleFunc("/api/v1/webhook/whatsapp", webhooks.WhatsAppWebhook)
	whMux.HandleFunc("/api/v1/webhook/telegram", webhooks.TelegramWebhook)
	whMux.HandleFunc("/api/v1/webhook/line", webhooks.LineWebhook)
	whMux.HandleFunc("/api/v1/webhook/zalo", webhooks.ZaloWebhook)
	// Operator bot — merchant account-linking, the admin console and the
	// merchant support inbox. Authenticated by Telegram's secret_token header,
	// not by the tenant signature scheme the routes above use.
	whMux.HandleFunc("/api/v1/webhook/telegram-platform", pipe.PlatformBotWebhook)
	// Meta OAuth browser callback (public, redirects to frontend). The
	// /platforms path is the one typically configured in the Meta dashboard
	// and META_OAUTH_REDIRECT_URL; keep both alive so either works.
	whMux.HandleFunc("/api/v1/webhook/meta/oauth/callback", app.MetaOAuthCallbackRaw())
	whMux.HandleFunc("/api/v1/platforms/meta/oauth/callback", app.MetaOAuthCallbackRaw())
	app.WebhookHandler = whMux

	// Background periodic jobs (campaign dispatch, SLA scan, URL refresh, billing).
	app.StartBackgroundTasks(ctx)

	srv := &http.Server{
		Addr:              ":" + cfg.Server.Port,
		Handler:           app.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown: drain for up to 20s on SIGINT/SIGTERM.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("shutdown error", "error", err.Error())
		}
	}()

	logger.Info("server listening", "addr", "127.0.0.1:"+cfg.Server.Port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("server stopped cleanly")
}
