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
	"strconv"
	"syscall"
	"time"

	"khmer-ai-cs-go/internal/api"
	"khmer-ai-cs-go/internal/auth"
	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/db"
	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/llm"
	"khmer-ai-cs-go/internal/paypal"
	"khmer-ai-cs-go/internal/platform"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/realtime"
	"khmer-ai-cs-go/internal/redisstore"
	"khmer-ai-cs-go/internal/replycache"
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

	// Migration 061 put RLS backstop policies on chat_messages/knowledge_chunks,
	// keyed to the app.user_id GUC. Two independent facts make them inert today,
	// and the warning below has to state both — the previous text ("switch to a
	// non-superuser role for it to apply") read as if a role change were all
	// that stood between this deployment and an active backstop.
	//
	//   * Nothing in Go sets app.user_id. Only the tests call app_set_tenant()
	//     (internal/migrations/rls_backstop_test.go), so app_tenant_id() is
	//     NULL and the policy condition `app_tenant_id() IS NULL OR …`
	//     short-circuits to TRUE. The policies allow everything, for EVERY
	//     role. 061 is scaffolding that is not wired up, not a live control.
	//   * Even once wired, a superuser or BYPASSRLS role bypasses row security
	//     unconditionally, so the role does matter — but only second.
	//
	// Next step is a choice: finish the wiring (a tenant-scoped executor that
	// runs each request inside a transaction that calls app_set_tenant — the
	// plan in docs/DEVELOPMENT.md 「十」), or keep 061 explicitly documented as
	// inert. Warning rather than exiting: the deployment ran fine before 061 and
	// must keep starting either way.
	var dbRoleSuper bool
	if err := pool.QueryRow(ctx,
		"SELECT rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&dbRoleSuper); err != nil {
		logger.Warn("could not inspect DB role for the RLS backstop", "error", err.Error())
	} else {
		roleNote := "a non-superuser role"
		if dbRoleSuper {
			roleNote = "a superuser role (row security is bypassed unconditionally for it)"
		}
		logger.Warn("RLS tenant backstop (061) is inert: no production code sets the app.user_id GUC, "+
			"so app_tenant_id() is NULL and both policies fail open for every role. Treat it as "+
			"unwired scaffolding, not an active control — a role change alone does not enable it; "+
			"see docs/DEVELOPMENT.md 「十」 for the wiring plan",
			"db_role", roleNote, "superuser", dbRoleSuper)
	}

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

	// Gemini service — prefer the GEMINI row of the model config (the console is the
	// source of truth once a key is saved); fall back to env. See llm.LoadGemini: generation
	// may be served by Claude, while embeddings and retrieval always stay on Gemini.
	var gem *gemini.Service
	if geminiRow, ok := llm.LoadGemini(ctx, pool); ok {
		apiKey, modelName, systemPrompt, maxTokens, region, temperature := geminiRow.APIKey, geminiRow.ModelName, geminiRow.SystemPrompt, geminiRow.MaxTokens, geminiRow.Region, geminiRow.Temperature
		gem = gemini.FromPartsFull(sealer.DecryptOrKeep(apiKey), modelName, systemPrompt, maxTokens)
		// Sampling temperature, from the same row the console edits. A NULL column
		// stays NULL: the request then carries no temperature at all and the
		// platform default applies (1.0 — what Google recommends for Gemini 3, and
		// what this deployment has always sent in effect). This column was displayed
		// by the admin page and read by nothing until 2026-09-29; applying it here is
		// what makes the console's value real, without changing the bytes on the
		// wire for a deployment that never set it.
		gem.SetTemperature(temperature)
		// The Vertex location the console last switched to, applied before anything
		// can serve. This is what makes a switch survive a restart: without it the
		// process would quietly go back to GEMINI_VERTEX_REGION on every deploy
		// while the console still displayed the switched one.
		//
		// A stored value that does not validate is WARNED about and skipped, not
		// fatal: the deployment then serves from the region it booted with — a
		// working state — instead of refusing to start the whole platform (inbox,
		// webhooks, TTS) over one setting that was ignored anyway. The API cannot
		// store such a value (it validates what it writes), so reaching this means
		// the row was edited by hand.
		if err := gem.SetVertexRegion(region); err != nil {
			logger.Warn("ignoring the Vertex region stored in the model config; serving from the environment's region",
				"stored_region", region, "serving_region", gem.Region(), "error", err.Error())
		}
		if gem.IsConfigured() {
			logger.Info("Gemini configured from database model config",
				"model", gem.ModelName(),
				// Empty on the studio transport, which has no locations at all.
				"region", gem.Region(),
				// "platform default" when the column is NULL, which is a real setting
				// (see SetTemperature) and not a missing one.
				"temperature", temperatureLabel(gem.Temperature()),
				// Which credential is actually in use. Under vertex the DB key
				// is dead data and the service-account file authenticates, so a
				// log line naming only the model sends whoever is debugging a
				// 401 to rotate a key that was never read.
				"credential", string(gemini.CredentialSourceOf()))
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

	// The generation router. The default row names the provider in force. A Claude
	// default gets its own client, built from that row with its key unsealed here; the
	// Gemini client above keeps serving embeddings and retrieval whichever provider
	// generates. SetProvider runs before the spend check below, which reads the provider.
	servingRow, haveServingRow := llm.LoadDefault(ctx, pool)
	router := llm.NewRouter(gem)
	if haveServingRow {
		if llm.IsClaude(servingRow.Provider) {
			router.InstallClaude(servingRow, sealer.DecryptOrKeep(servingRow.APIKey))
		}
		router.SetProvider(servingRow.Provider)
	}
	logger.Info("generation provider in force", "provider", router.Provider())
	if llm.IsClaude(router.Provider()) && !router.Model().IsConfigured() {
		// The Gemini client warns above when it falls back to mock mode; a Claude provider has
		// no mock. Without this line the first customer turn is the one that discovers an
		// unreadable key or service-account file.
		logger.Error("the serving provider is not configured — customer replies will fail",
			"provider", router.Provider())
	}

	// Fail the boot, not every turn, on a GEMINI_PROVIDER=vertex deployment
	// that cannot work (missing service-account file, no project). Without
	// this the process starts healthy and the first customer turn is the one
	// that discovers the misconfiguration — as a 500 — while /ready, systemd
	// and every dashboard still report a serving backend. No-op for studio
	// (the default), so it cannot change today's behaviour; it reads only the
	// local key file and never the network, so a Google outage cannot block a
	// deploy.
	if err := gemini.ValidateProviderConfig(); err != nil {
		logger.Error("invalid Gemini provider configuration", "error", err.Error())
		os.Exit(1)
	}

	// The spend guardrail is the second half of the same check. On vertex the
	// limit means "our own budget" while on studio it means Google's upstream
	// wall, so a value left over from the other provider silently either bars
	// customers for no reason or protects nothing. ValidateSpendConfig rejects
	// exactly those leftovers; usage.Budget additionally refuses to arm the gate
	// when the config is invalid, so a missing wiring cannot reject anyone.
	if err := usage.ValidateSpendConfig(); err != nil {
		logger.Error("invalid Gemini spend configuration", "error", err.Error())
		os.Exit(1)
	}
	slog.Info("gemini spend guardrail",
		"limit_usd", usage.SpendLimitUSD(),
		"basis", string(usage.SpendLimitBasis()))

	// Hold a pooled connection to the model host open, so the first query
	// embedding of a turn does not pay the handshake. On the multi-region
	// (global) endpoint the cold cost is ~11.2s against a 5s embedding budget,
	// so a cold turn loses its dense retrieval leg entirely and answers from
	// lexical+trigram only. Regional endpoints measure ~0.3s cold and do not
	// need this, but the probe is one tiny embedding either way and
	// GEMINI_EMBED_KEEPWARM_SEC=0 turns it off.
	gem.StartEmbedKeepWarm(ctx, gemini.EmbedKeepWarmInterval())

	// Jev (TypeSafe System One) powers every typed judgment — turn
	// classification, rerank, routing, guardrails, notify triage. It is nil
	// without TYPESAFE_API_KEY, and every site then keeps its previous path.
	jev := typesafe.NewFromEnv(logger)

	// Semantic reply cache: identical asks skip retrieval+generation entirely.
	// Any knowledge-base change drops the tenant's cache via KBChanged below.
	replyCache := &replycache.Service{DB: pool, Gemini: gem, Logger: logger, Serving: func() string { return router.Model().ModelName() }}

	ragService := &rag.Service{DB: pool, Gemini: gem, Redis: redisClient, Logger: logger, Jev: jev, LLM: router}
	ragService.KBChanged = replyCache.InvalidateTenant

	// Attribute auxiliary model spend to whichever tenant tagged the context.
	// Previously only the four main chat paths recorded usage, so the
	// auxiliary calls — the ingest-time compile above all, a 4096-token call
	// per document — were invisible to every cost dashboard.
	//
	// Installed BEFORE SpawnIndexWorkers: those workers start compiling
	// documents immediately, and the ingest compile is exactly the call this
	// observer exists to record. Assigning it afterwards was an unsynchronised
	// write racing their first reads.
	gemini.AuxUsageObserver = func(ctx context.Context, model string, prompt, completion, cached int) {
		if uid, ok := usage.UserFrom(ctx); ok {
			usage.Record(ctx, pool, uid, nil, model, prompt, completion, cached)
		}
	}

	ragService.SpawnIndexWorkers(ctx)

	// R2 object storage (inbound media replay + TTS audio; inert without creds).
	media := storager2.New(cfg.R2.AccountID, cfg.R2.AccessKey, cfg.R2.SecretKey, cfg.R2.Bucket, cfg.R2.PublicURL)

	// Platform pipeline (inbound AI replies + outbound delivery).
	pipe := &platform.Pipeline{DB: pool, Redis: redisClient, Cfg: cfg, Gemini: gem, RAG: ragService, Sealer: sealer, Media: media, Logger: logger, Jev: jev, Cache: replyCache, LLM: router}
	// Make a Jev outage audible. Every Jev call site degrades to a slower, less
	// accurate path when it fails, so without this the product changes
	// behaviour and only journalctl knows (2026-09-22: a 6-hour episode).
	//
	// Installed BEFORE StartKeepWarm below: that call probes immediately and
	// reports through this observer, so installing it afterwards left the first
	// probe — the one that establishes the hot connection — writing into an
	// observer field that was still being assigned.
	platform.InstallJevHealth(pipe)

	// Production traffic is a handful of messages a day, so every turn would
	// otherwise meet a cold connection and pay a 0.4-3.6s TLS handshake. That
	// blows the reply-path budgets (route 4s, guard 3s) and hands the decision
	// to the slower model Jev exists to replace. Hold the connection open.
	// Deliberately after InstallJevHealth — see above.
	jev.StartKeepWarm(ctx, typesafe.KeepWarmInterval())

	pipe.SpawnWorkers(ctx)

	app := &api.App{
		Cfg:    cfg,
		DB:     pool,
		Redis:  redisClient,
		JWT:    auth.NewJWT(cfg.JWT.Secret, cfg.JWT.ExpireHour),
		Gemini: gem,
		LLM:    router,
		RAG:    ragService,
		Logger: logger,
		Sealer: sealer,
		Media:  media,
		Pipe:   pipe,
		Cache:  replyCache,
		// The platform's own PayPal business account. Left unconfigured (no
		// credentials) the billing endpoints answer "not configured" and nothing
		// in the console offers a purchase, which is the correct state for a
		// deployment that is not selling yet.
		PayPal: paypal.New(paypal.Config{
			ClientID:     cfg.PayPal.ClientID,
			ClientSecret: cfg.PayPal.ClientSecret,
			Mode:         cfg.PayPal.Mode,
		}),
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
	webhooks := &platform.Webhooks{
		DB: pool, Pipe: pipe, Sealer: sealer,
		MetaVerifyToken: cfg.MetaVerifyToken,
		// The Data Deletion callback is signed with the app secret, which is a
		// different credential from the verify token used by the hub.challenge GET.
		MetaAppSecret: cfg.Meta.AppSecret,
		PublicBaseURL: cfg.Server.PublicAPIURL,
		Logger:        logger,
	}
	whMux := http.NewServeMux()
	// Every tenant provider callback, plus the unified /api/v1/webhook/{platform}
	// dispatcher. The table lives in the platform package (webhook_routes.go) so a
	// new channel no longer has to edit this file — and so there is one place that
	// answers "which platforms can reach us".
	platform.RegisterWebhookRoutes(whMux, webhooks)
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

// temperatureLabel renders the serving temperature for the boot log: the number
// when requests carry one, "platform default" when the column is NULL and the
// request therefore omits it. The distinction is the finding — until 2026-09-29
// this deployment always ran on the platform default while the console displayed
// 0.7, and a log line that printed "0.7" for both states would hide it again.
func temperatureLabel(t *float64) string {
	if t == nil {
		return "platform default"
	}
	return strconv.FormatFloat(*t, 'f', -1, 64)
}
