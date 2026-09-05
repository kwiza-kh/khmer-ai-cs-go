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

	// Gemini service — prefer the DB default model config (admin Models page is
	// the source of truth once a key is saved), fall back to env.
	var gem *gemini.Service
	if apiKey, modelName, systemPrompt, maxTokens, ok := gemini.LoadDefaultConfig(ctx, pool); ok {
		gem = gemini.FromPartsFull(apiKey, modelName, systemPrompt, maxTokens)
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

	ragService := &rag.Service{DB: pool, Gemini: gem, Logger: logger}
	ragService.SpawnIndexWorkers(ctx)

	// Platform credential sealer.
	sealer, err := security.NewSealer(cfg.PlatformCredentialKey)
	if err != nil {
		logger.Error("invalid platform credential key", "error", err.Error())
		os.Exit(1)
	}

	// Platform pipeline (inbound AI replies + outbound delivery).
	pipe := &platform.Pipeline{DB: pool, Redis: redisClient, Cfg: cfg, Gemini: gem, RAG: ragService, Sealer: sealer, Logger: logger}
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
		Pipe:   pipe,
	}

	// Realtime inbox hub (WebSocket fan-out fed by Redis pub/sub).
	allowedOrigins := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowedOrigins[o] = true
	}
	hub := realtime.NewHub(app.JWT, redisClient, logger,
		func(ctx context.Context, userID int32) bool {
			var active bool
			if err := pool.QueryRow(ctx, "SELECT is_active FROM users WHERE user_id = $1", userID).Scan(&active); err != nil {
				return false
			}
			return active
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
	// Meta OAuth browser callback (public, redirects to frontend).
	whMux.HandleFunc("/api/v1/webhook/meta/oauth/callback", app.MetaOAuthCallbackRaw())
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
