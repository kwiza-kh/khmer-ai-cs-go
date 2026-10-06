package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Router assembles the full HTTP surface. Phases: 1 auth, 2 knowledge/RAG;
// chat/platform/admin groups follow in later phases with identical mount
// points as the Rust backend.
func (a *App) Router() http.Handler {
	mux := http.NewServeMux()

	// Probes (no auth).
	mux.HandleFunc("GET /health", a.handle(a.health))
	mux.HandleFunc("GET /ready", a.handle(a.ready))

	// Public auth — rate limited per client (Rust: rate_limit_10).
	mux.Handle("POST /api/v1/auth/login", a.rateLimit(10)(a.handle(a.login)))
	mux.Handle("POST /api/v1/auth/register", a.rateLimit(10)(a.handle(a.register)))
	// Google (OIDC) sign-in — browser redirects carry no JWT, so these are
	// public; the callback validates a one-time CSRF state token. /start is
	// rate limited too: it mints state keys, and an unbounded mint made the
	// exchange's state half trivially satisfiable.
	mux.Handle("GET /api/v1/auth/google/start", a.rateLimit(20)(http.HandlerFunc(a.googleStart)))
	mux.HandleFunc("GET /api/v1/auth/google/callback", a.googleCallback)
	mux.Handle("POST /api/v1/auth/google/exchange", a.rateLimit(20)(a.handle(a.googleLoginExchange)))
	// Telegram (OIDC) sign-in — public for the same reason: the browser arrives
	// from Telegram with no JWT, and the callback validates a one-time state
	// token that also carries the PKCE verifier.
	mux.Handle("GET /api/v1/auth/telegram/start", a.rateLimit(20)(http.HandlerFunc(a.telegramStart)))
	mux.HandleFunc("GET /api/v1/auth/telegram/callback", a.telegramCallback)
	mux.Handle("POST /api/v1/auth/telegram/exchange", a.rateLimit(20)(a.handle(a.telegramLoginExchange)))
	mux.Handle("GET /api/v1/auth/methods", a.handle(a.googleAuthMethods))

	// Website chat widget — public, authenticated by the embed token only
	// (CORS is opened for /api/v1/widget/* in a.cors).
	mux.Handle("GET /api/v1/widget/config", a.widgetRateLimit(30)(a.handle(a.widgetBootstrap)))
	mux.Handle("GET /api/v1/widget/messages", a.widgetRateLimit(60)(a.handle(a.widgetMessages)))
	mux.Handle("POST /api/v1/widget/chat", a.widgetRateLimit(20)(http.HandlerFunc(a.widgetChat)))
	mux.Handle("POST /api/v1/widget/feedback", a.widgetRateLimit(30)(a.handle(a.widgetFeedback)))

	// Data-subject self-service: look up one deletion request by the code the
	// Meta callback returned. Public for the same reason the widget routes are
	// — the subject holds no account here — and rate limited per client. The
	// code itself is the capability and carries 80 bits of entropy.
	mux.Handle("GET /api/v1/privacy/deletion-status", a.rateLimit(30)(a.handle(a.deletionStatus)))

	// PayPal webhook. Public for the same reason the widget routes are — PayPal
	// holds no account here — and authenticated by the signature PayPal signs
	// with the webhook id we configured: an unverified webhook route would grant
	// paid plans to anyone who can POST to it. Rate limited per client; PayPal
	// retries a 5xx, so a slow dependency is answered with one.
	mux.Handle("POST /api/v1/billing/paypal/webhook", a.rateLimit(120)(a.handle(a.paypalWebhook)))

	// Authenticated group.
	authed := http.NewServeMux()
	authed.Handle("PUT /api/v1/auth/password", a.handle(a.changePassword))
	authed.Handle("GET /api/v1/auth/preferences", a.handle(a.getPreferences))
	authed.Handle("PUT /api/v1/auth/preferences", a.handle(a.updatePreferences))

	// Knowledge base (RAG).
	authed.Handle("POST /api/v1/rag/query", a.handle(a.ragQuery))
	authed.Handle("POST /api/v1/knowledge/upload", a.handle(a.uploadKnowledge))
	authed.Handle("POST /api/v1/knowledge/upload/file", a.handle(a.uploadKnowledgeFile))
	authed.Handle("POST /api/v1/knowledge/upload/url", a.handle(a.ingestKnowledgeURL))
	authed.Handle("GET /api/v1/knowledge/accepted-types", a.handle(a.acceptedFileTypes))
	authed.Handle("GET /api/v1/knowledge", a.handle(a.listKnowledge))
	authed.HandleFunc("GET /api/v1/knowledge/{id}", a.handleDoc(a.getKnowledgeDocument))
	authed.HandleFunc("PUT /api/v1/knowledge/{id}", a.handleDoc(a.updateKnowledgeDocument))
	authed.HandleFunc("DELETE /api/v1/knowledge/{id}", a.handleDoc(a.deleteKnowledge))
	authed.HandleFunc("POST /api/v1/knowledge/{id}/retry", a.handleDoc(a.retryKnowledge))
	// Literal segments outrank the {id} pattern above.
	authed.Handle("GET /api/v1/knowledge/quality", a.handle(a.knowledgeDocQuality))
	authed.Handle("GET /api/v1/knowledge/gaps", a.handle(a.knowledgeGaps))
	authed.Handle("POST /api/v1/knowledge/gaps/draft", a.handle(a.knowledgeGapDraft))
	authed.Handle("GET /api/v1/knowledge/contradictions", a.handle(a.listContradictions))
	authed.HandleFunc("POST /api/v1/knowledge/contradictions/{id}/resolve", a.handleDoc(a.resolveContradiction))
	authed.HandleFunc("POST /api/v1/knowledge/contradictions/{id}/dismiss", a.handleDoc(a.dismissContradiction))
	// Model config and the RAG compile toggle are PLATFORM-GLOBAL resources
	// (model_configs has no tenant column; the compile toggle is a global Redis
	// key) — they must require the platform_admin role, not the per-tenant
	// admin role, or one merchant could redirect every tenant's AI serving.
	authed.Handle("GET /api/v1/admin/rag/settings", a.platformAdminOnly(a.handle(a.getRagSettings)))
	authed.Handle("PUT /api/v1/admin/rag/settings", a.platformAdminOnly(a.handle(a.putRagSettings)))
	// Telegram notifications — one-tap linking through the platform bot plus
	// the three delivery toggles. The bring-your-own-bot setup (save a token,
	// discover chats via getUpdates) was removed in 056; there is no endpoint
	// left that accepts a bot token or an arbitrary chat id.
	authed.Handle("GET /api/v1/settings/telegram-notify", a.handle(a.getTelegramNotify))
	authed.Handle("PUT /api/v1/settings/telegram-notify", a.handle(a.putTelegramNotify))
	authed.Handle("POST /api/v1/settings/telegram-notify/test", a.handle(a.postTelegramNotifyTest))
	authed.Handle("POST /api/v1/settings/telegram-notify/link", a.handle(a.postTelegramNotifyLink))
	// Personas — named instruction sets that replace the tenant's system prompt
	// for a turn. internal/persona resolves session → conversation → global and
	// the inbound pipeline applies the result in its resolve-persona stage. These
	// are adminOnly, not platformAdminOnly: a persona changes what this merchant's
	// AI says to this merchant's customers, so the merchant owns it.
	//
	// Bindings are addressed by (scope, target) and never by binding_id —
	// 067_personas.sql makes that pair unique per tenant, so a rebind is one PUT
	// and the console never has to carry an id back.
	authed.Handle("GET /api/v1/personas", a.tenantAdminOnly(a.handle(a.listPersonas)))
	authed.Handle("POST /api/v1/personas", a.tenantAdminOnly(a.handle(a.createPersona)))
	authed.Handle("PUT /api/v1/personas/{id}", a.tenantAdminOnly(a.handle(a.updatePersona)))
	authed.Handle("DELETE /api/v1/personas/{id}", a.tenantAdminOnly(a.handle(a.deletePersona)))
	authed.Handle("PUT /api/v1/persona-bindings", a.tenantAdminOnly(a.handle(a.putPersonaBinding)))
	authed.Handle("DELETE /api/v1/persona-bindings", a.tenantAdminOnly(a.handle(a.deletePersonaBinding)))
	// Agent copilot: one-shot translation (Khmer ↔ 中文 ↔ English).
	authed.Handle("POST /api/v1/translate", a.handle(a.translateText))
	authed.Handle("POST /api/v1/translate/batch", a.handle(a.translateBatch))
	// Personal profile (settings page).
	authed.Handle("GET /api/v1/profile", a.handle(a.getProfile))
	authed.Handle("PUT /api/v1/profile", a.handle(a.putProfile))
	authed.Handle("POST /api/v1/profile/avatar", a.handle(a.uploadAvatar))

	// Chat (plain + SSE streaming) + sessions.
	authed.Handle("POST /api/v1/chat", a.handle(a.chatPlain))
	authed.HandleFunc("POST /api/v1/chat/stream", a.chatStream)
	authed.Handle("POST /api/v1/chat/voice", a.handle(a.chatVoice))
	authed.Handle("GET /api/v1/chat/sessions", a.handle(a.listSessions))
	authed.Handle("POST /api/v1/chat/sessions", a.handle(a.createSession))
	authed.HandleFunc("PATCH /api/v1/chat/sessions/{id}", a.handleSession(a.updateSession))
	authed.HandleFunc("GET /api/v1/chat/sessions/{id}", a.handleSession(a.getSession))
	authed.HandleFunc("DELETE /api/v1/chat/sessions/{id}", a.handleSession(a.deleteSession))
	authed.HandleFunc("GET /api/v1/chat/sessions/{id}/messages", a.handleSession(a.listSessionMessages))

	// Inbox — session list + agent actions.
	authed.Handle("GET /api/v1/inbox", a.handle(a.listInbox))
	authed.HandleFunc("POST /api/v1/inbox/sessions/{id}/assign", a.handleSession(a.assignSession))
	authed.HandleFunc("POST /api/v1/inbox/sessions/{id}/takeover", a.handleSession(a.takeoverSession))
	authed.HandleFunc("POST /api/v1/inbox/sessions/{id}/reply", a.handleSession(a.agentReply))
	authed.HandleFunc("PATCH /api/v1/inbox/sessions/{id}/status", a.handleSession(a.updateSessionStatus))
	authed.HandleFunc("PUT /api/v1/inbox/sessions/{id}/tags", a.handleSession(a.setSessionTags))
	authed.HandleFunc("POST /api/v1/inbox/sessions/{id}/archive", a.handleSession(a.archiveSession))
	authed.HandleFunc("POST /api/v1/inbox/sessions/{id}/unarchive", a.handleSession(a.unarchiveSession))
	authed.HandleFunc("GET /api/v1/inbox/sessions/{id}/summary", a.handleSession(a.sessionSummary))
	authed.Handle("GET /api/v1/inbox/messages/{id}/media-url", a.handle(a.getInboundMediaURL))

	// Tenant self-service settings — every registered user manages their own
	// business hours and canned responses (migration 021 scopes both by
	// user_id). These live under /settings/ rather than /admin/ because they
	// are caller-scoped, not privileged: the /admin/ prefix implied an
	// adminOnly gate they never had and made the audit middleware record
	// every merchant's own schedule edit as an administrative action.
	authed.Handle("GET /api/v1/settings/business-hours", a.handle(a.listBusinessHours))
	authed.Handle("PUT /api/v1/settings/business-hours", a.handle(a.upsertBusinessHours))
	authed.Handle("GET /api/v1/settings/business-hours/open", a.handle(a.isBusinessOpen))
	authed.Handle("GET /api/v1/settings/canned-responses", a.handle(a.listCannedResponses))
	authed.Handle("POST /api/v1/settings/canned-responses", a.handle(a.createCannedResponse))
	authed.HandleFunc("DELETE /api/v1/settings/canned-responses/{id}", a.handleDoc(a.deleteCannedResponse))
	authed.Handle("GET /api/v1/admin/models", a.platformAdminOnly(a.handle(a.listModelConfigs)))
	// Read-only companion to the list: the built-in prompt a config falls back
	// to when its system_prompt is empty. A literal segment, so it can never be
	// swallowed by the {id} patterns below.
	authed.Handle("GET /api/v1/admin/models/default-prompt", a.platformAdminOnly(a.handle(a.defaultSystemPrompt)))
	// The region selector's candidate list — the companion to the {id}/available
	// listing below, which takes ?region=. Another literal segment at this depth,
	// for the same reason as default-prompt: it must never be read as an {id}.
	authed.Handle("GET /api/v1/admin/models/vertex-regions", a.platformAdminOnly(a.handle(a.vertexRegions)))
	authed.Handle("PUT /api/v1/admin/models/{id}", a.platformAdminOnly(a.handleDoc(a.updateModelConfig)))
	authed.Handle("POST /api/v1/admin/models/{id}/test", a.platformAdminOnly(a.handleDoc(a.testModelConfig)))
	authed.Handle("GET /api/v1/admin/models/{id}/available", a.platformAdminOnly(a.handleDoc(a.listAvailableModels)))
	// System-prompt version history: the prompt defines every customer reply, so
	// a bad edit must be recoverable. Restore is a POST because it mutates, and
	// it lands in audit_logs via the /api/v1/admin/ prefix.
	authed.Handle("GET /api/v1/admin/models/{id}/prompt-history", a.platformAdminOnly(a.handleDoc(a.listPromptVersions)))
	authed.Handle("POST /api/v1/admin/models/{id}/prompt-history/{version}/restore", a.platformAdminOnly(a.handlePromptRestore()))
	authed.Handle("GET /api/v1/admin/users", a.tenantAdminOnly(a.handle(a.listUsers)))
	authed.Handle("PUT /api/v1/admin/users/{id}/role", a.tenantAdminOnly(a.handleDoc(a.updateUserRole)))
	authed.Handle("GET /api/v1/admin/analytics/overview", a.tenantAdminOnly(a.handle(a.analyticsOverview)))
	authed.Handle("GET /api/v1/admin/rag/gaps", a.tenantAdminOnly(a.handle(a.ragGaps)))

	// Analytics: timeline, breakdowns, top queries, token stats, feedback list.
	authed.Handle("GET /api/v1/admin/analytics/timeline", a.tenantAdminOnly(a.handle(a.analyticsTimeline)))
	authed.Handle("GET /api/v1/admin/analytics/top-queries", a.tenantAdminOnly(a.handle(a.topQueries)))
	authed.Handle("GET /api/v1/admin/analytics/languages", a.tenantAdminOnly(a.handle(a.languageBreakdown)))
	authed.Handle("GET /api/v1/admin/tokens/stats", a.tenantAdminOnly(a.handle(a.tokenStats)))
	authed.Handle("GET /api/v1/admin/feedback", a.tenantAdminOnly(a.handle(a.feedbackList)))
	authed.Handle("GET /api/v1/admin/agent-performance", a.tenantAdminOnly(a.handle(a.agentPerformance)))
	authed.Handle("GET /api/v1/admin/intent-analytics", a.tenantAdminOnly(a.handle(a.intentAnalytics)))
	authed.Handle("GET /api/v1/admin/integrations/status", a.tenantAdminOnly(a.handle(a.integrationsStatus)))

	// CSV report export.
	authed.HandleFunc("GET /api/v1/reports/{kind}", a.reportCSV)

	// WhatsApp approved templates for a session.
	authed.HandleFunc("GET /api/v1/inbox/sessions/{id}/whatsapp-templates", a.handleSession(a.sessionWhatsAppTemplates))

	// Website widget token management (per tenant).
	authed.Handle("GET /api/v1/widgets", a.handle(a.listWidgetTokens))
	authed.Handle("POST /api/v1/widgets", a.handle(a.createWidgetToken))
	authed.HandleFunc("DELETE /api/v1/widgets/{id}", a.handleDoc(a.deleteWidgetToken))

	// TOTP (two-factor auth).
	authed.Handle("POST /api/v1/auth/totp/setup", a.handle(a.totpSetup))
	authed.Handle("POST /api/v1/auth/totp/verify", a.handle(a.totpVerify))
	authed.Handle("GET /api/v1/auth/totp/status", a.handle(a.totpStatus))
	authed.Handle("POST /api/v1/auth/totp/disable", a.handle(a.totpDisable))

	// API keys.
	authed.Handle("GET /api/v1/api-keys", a.handle(a.listAPIKeys))
	authed.Handle("POST /api/v1/api-keys", a.handle(a.createAPIKey))
	authed.HandleFunc("DELETE /api/v1/api-keys/{id}", a.handleDoc(a.deleteAPIKey))

	// Notifications.
	authed.Handle("GET /api/v1/notifications", a.handle(a.listNotifications))
	authed.Handle("GET /api/v1/notifications/unread-count", a.handle(a.notificationsUnread))
	authed.HandleFunc("POST /api/v1/notifications/{id}/read", a.handleDoc(a.notificationsMarkRead))
	authed.Handle("POST /api/v1/notifications/read-all", a.handle(a.notificationsReadAll))

	// SLA policies.
	authed.Handle("GET /api/v1/sla", a.handle(a.listSLA))
	authed.Handle("POST /api/v1/sla", a.handle(a.upsertSLA))
	authed.HandleFunc("DELETE /api/v1/sla/{id}", a.handleDoc(a.deleteSLA))
	authed.Handle("GET /api/v1/sla/breaches", a.handle(a.listSLABreaches))

	// Marketing campaigns.
	authed.Handle("GET /api/v1/campaigns", a.handle(a.listCampaigns))
	authed.Handle("POST /api/v1/campaigns", a.handle(a.createCampaign))
	authed.HandleFunc("POST /api/v1/campaigns/{id}/cancel", a.handleDoc(a.cancelCampaign))

	// Routing rules.
	authed.Handle("GET /api/v1/routing", a.handle(a.listRouting))
	authed.Handle("POST /api/v1/routing", a.handle(a.upsertRouting))
	authed.HandleFunc("DELETE /api/v1/routing/{id}", a.handleDoc(a.deleteRouting))

	// Macros.
	authed.Handle("GET /api/v1/macros", a.handle(a.listMacros))
	authed.Handle("POST /api/v1/macros", a.handle(a.createMacro))
	authed.HandleFunc("PUT /api/v1/macros/{id}", a.handleDoc(a.updateMacro))
	authed.HandleFunc("DELETE /api/v1/macros/{id}", a.handleDoc(a.deleteMacro))

	// Roles / user_roles were removed here (migration 069 drops the tables, both
	// were empty). They had CRUD endpoints and no authorization reader anywhere,
	// so they only looked like access control — the real boundaries are
	// agent_teams membership plus the tenant-owner checks in tenant_owner.go.

	// Webhook subscriptions.
	authed.Handle("GET /api/v1/webhooks/subscriptions", a.handle(a.listWebhooks))
	authed.Handle("POST /api/v1/webhooks/subscriptions", a.handle(a.createWebhook))
	authed.HandleFunc("DELETE /api/v1/webhooks/subscriptions/{id}", a.handleDoc(a.deleteWebhook))

	// Handoff requests.
	authed.Handle("GET /api/v1/handoff-requests", a.handle(a.listHandoffs))
	authed.Handle("POST /api/v1/handoff-requests", a.handle(a.createHandoffRequest))
	authed.HandleFunc("POST /api/v1/handoff-requests/{id}/resolve", a.handleSession(a.resolveHandoff))

	// Customers.
	authed.Handle("GET /api/v1/customers", a.handle(a.listCustomers))
	authed.HandleFunc("GET /api/v1/customers/{id}", a.handleDoc(a.customer360))
	authed.HandleFunc("PUT /api/v1/customers/{id}/notes", a.handleDoc(a.updateCustomerNotes))

	// FAQ suggestions — self-scoped like the settings above (faq_suggestions
	// is keyed by user_id), so it belongs in the tenant namespace too.
	authed.Handle("GET /api/v1/settings/faq/suggestions", a.handle(a.listFaqSuggestions))
	authed.HandleFunc("POST /api/v1/settings/faq/suggestions/{id}/accept", a.handleDoc(a.acceptFaqSuggestion))
	authed.HandleFunc("POST /api/v1/settings/faq/suggestions/{id}/dismiss", a.handleDoc(a.dismissFaqSuggestion))

	// Billing.
	authed.Handle("GET /api/v1/billing", a.handle(a.getBilling))
	authed.Handle("PUT /api/v1/billing/plan", a.handle(a.setPlan))
	// Plan catalogue (prices come from configuration) and PayPal checkout.
	// Activation is idempotent, so the capture call and the webhook racing each
	// other grant the paid window exactly once.
	//
	// Both checkout routes are deliberately open to any authenticated user of the
	// tenant: they can only ever act on the CALLER'S OWN tenant (applyPlan uses
	// user.UserID, and capture refuses an order owned by anyone else), so a role
	// check adds no safety — and it broke the common case, because a
	// self-registered or SSO-provisioned owner carries role "user", not "admin"
	// (2026-10-05: user 10 could not pay for its own plan). Cross-tenant money
	// still moves only through the platform console (setPlan), which stays
	// platform-admin only.
	authed.Handle("GET /api/v1/billing/plans", a.handle(a.billingCatalog))
	authed.Handle("POST /api/v1/billing/paypal/order", a.handle(a.paypalCreateOrder))
	authed.Handle("POST /api/v1/billing/paypal/capture", a.handle(a.paypalCapture))

	// Teams (agent management). The tenant boundary lives in each statement, not
	// in a role check: listTeam filters on owner_user_id, removeTeamAgent deletes
	// only rows it owns, and the claim path still requires a tenant owner — but
	// "owner" now means the tenant's own account, not "role == admin", which no
	// self-service or SSO signup ever satisfies (2026-10-05: user 10 could not
	// add a single agent to its own team).
	authed.Handle("GET /api/v1/team", a.handle(a.listTeam))
	// The claim path is platform break-glass now (see addTeamAgent): binding a
	// stranger's account with nothing but their user_id is what the invite flow
	// replaced, so tenants go through /team/invites, where the invitee accepts
	// with their own authenticated account.
	authed.Handle("POST /api/v1/team/agents", a.handle(a.addTeamAgent))
	authed.Handle("DELETE /api/v1/team/agents/{id}", a.handleDoc(a.removeTeamAgent))
	authed.Handle("GET /api/v1/team/invites", a.handle(a.listTeamInvites))
	authed.Handle("POST /api/v1/team/invites", a.handle(a.createTeamInvite))
	authed.Handle("POST /api/v1/team/invites/accept", a.handle(a.acceptTeamInvite))
	authed.HandleFunc("DELETE /api/v1/team/invites/{id}", a.handleDoc(a.deleteTeamInvite))

	// Copilot (agent AI suggestions).
	authed.HandleFunc("POST /api/v1/inbox/sessions/{id}/copilot/suggest", a.handleSession(a.copilotSuggest))
	authed.HandleFunc("GET /api/v1/inbox/sessions/{id}/copilot/knowledge", a.handleSession(a.copilotKnowledge))

	// Message feedback.
	authed.HandleFunc("POST /api/v1/chat/messages/{id}/feedback", a.handleDoc(a.messageFeedback))

	// Platform configs CRUD + verify + work + retry.
	authed.Handle("GET /api/v1/platforms/configs", a.handle(a.listPlatformConfigs))
	authed.Handle("PUT /api/v1/platforms/configs", a.handle(a.upsertPlatformConfig))
	authed.HandleFunc("POST /api/v1/platforms/configs/{id}/verify", a.handleDoc(a.verifyPlatformConfig))
	authed.HandleFunc("DELETE /api/v1/platforms/configs/{id}", a.handleDoc(a.deactivatePlatformConfig))
	authed.HandleFunc("GET /api/v1/platforms/configs/{id}/work", a.handleDoc(a.listPlatformWork))
	authed.HandleFunc("POST /api/v1/platforms/configs/{config_id}/inbound-events/{id}/retry", a.handlePlatformRetry("inbound-events"))
	authed.HandleFunc("POST /api/v1/platforms/configs/{config_id}/deliveries/{id}/retry", a.handlePlatformRetry("deliveries"))

	// Meta OAuth.
	authed.Handle("POST /api/v1/platforms/meta/oauth/start", a.handle(a.metaOAuthStart))
	authed.HandleFunc("GET /api/v1/platforms/meta/oauth/sessions/{id}", a.handleOAuthSession)
	authed.Handle("POST /api/v1/platforms/meta/oauth/complete", a.handle(a.metaOAuthComplete))

	// WhatsApp Embedded Signup.
	authed.Handle("GET /api/v1/platforms/whatsapp/embedded-signup/config", a.handle(a.embeddedSignupConfig))
	authed.Handle("POST /api/v1/platforms/whatsapp/embedded-signup/complete", a.handle(a.embeddedSignupComplete))

	// Platform super-admin (cross-tenant) management.
	authed.Handle("GET /api/v1/platform/tenants", a.platformAdminOnly(a.handle(a.listTenants)))
	authed.HandleFunc("GET /api/v1/platform/tenants/{id}", a.platformAdminTenant(a.tenantDetail))
	authed.HandleFunc("PUT /api/v1/platform/tenants/{id}/status", a.platformAdminTenant(a.setTenantStatus))
	authed.HandleFunc("PUT /api/v1/platform/tenants/{id}/plan", a.platformAdminTenant(a.setTenantPlan))
	authed.Handle("POST /api/v1/platform/tenants", a.platformAdminOnly(a.handle(a.createTenant)))
	authed.Handle("GET /api/v1/platform/analytics", a.platformAdminOnly(a.handle(a.platformAnalytics)))
	// Gemini spend window: one billing account serves every tenant, so both the
	// ceiling and the distance to it are platform-global, not tenant analytics.
	authed.Handle("GET /api/v1/platform/spend", a.platformAdminOnly(a.handle(a.getSpendBudget)))
	authed.Handle("GET /api/v1/platform/audit-logs", a.platformAdminOnly(a.handle(a.listAuditLogs)))

	// 180/min per user: the admin SPA legitimately aggregates 6-10 pollers
	// (inbox SWR, handoff badge, notification bell, page loads) — 60 tripped
	// during normal multi-tab use.
	mux.Handle("/api/v1/", a.authMiddleware(a.audit(a.rateLimit(180)(authed))))

	// Realtime inbox stream. More specific than the "/api/v1/" catch-all so it
	// bypasses the header-auth chain: the browser cannot send an Authorization
	// header on a WebSocket handshake, so the hub authenticates the subprotocol
	// token itself.
	if a.Realtime != nil {
		mux.Handle("GET /api/v1/realtime/inbox", a.Realtime)
	}

	// Platform webhooks (mounted by main; no auth — signature-verified).
	if a.WebhookHandler != nil {
		mux.Handle("/api/v1/webhook/", a.WebhookHandler)
		// Meta OAuth browser callback: Facebook redirects the user here with
		// no JWT. Public by design — the handler validates the signed state.
		mux.Handle("/api/v1/platforms/meta/oauth/callback", a.WebhookHandler)
	}

	// Outer chain: request-id → CORS → request logging.
	var handler http.Handler = mux
	handler = a.logging(handler)
	handler = a.cors(handler)
	handler = requestID(handler)
	return handler
}

// docHandler is a knowledge-document handler with a parsed path id.
type docHandler func(w http.ResponseWriter, r *http.Request, docID int32) (any, error)

// handleDoc wraps docHandler with id parsing + the JSON envelope.
func (a *App) handleDoc(h docHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		id, err := strconv.ParseInt(idStr, 10, 32)
		if err != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的文档 ID"})
			return
		}
		val, herr := h(w, r, int32(id))
		if herr != nil {
			apiErr, ok := herr.(*ApiError)
			if !ok {
				apiErr = ErrInternal(herr.Error())
			}
			WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
			return
		}
		WriteJSON(w, http.StatusOK, val)
	}
}

// sessionHandler is a session-scoped handler with the path id as a string.
type sessionHandler func(w http.ResponseWriter, r *http.Request, sessionID string) (any, error)

// handleSession wraps sessionHandler with id extraction + the JSON envelope.
func (a *App) handleSession(h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的会话 ID"})
			return
		}
		val, herr := h(w, r, id)
		if herr != nil {
			apiErr, ok := herr.(*ApiError)
			if !ok {
				apiErr = ErrInternal(herr.Error())
			}
			WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
			return
		}
		WriteJSON(w, http.StatusOK, val)
	}
}

// handleOAuthSession wraps metaOAuthSession (session id is a UUID string).
func (a *App) handleOAuthSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的会话 ID"})
		return
	}
	val, herr := a.metaOAuthSession(w, r, id)
	if herr != nil {
		apiErr, ok := herr.(*ApiError)
		if !ok {
			apiErr = ErrInternal(herr.Error())
		}
		WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
		return
	}
	WriteJSON(w, http.StatusOK, val)
}

// handlePlatformRetry wraps retryPlatformEvent with a fixed kind + two path ids.
func (a *App) handlePlatformRetry(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configID := parseIntOr(r.PathValue("config_id"), 0)
		id := parseIntOr(r.PathValue("id"), 0)
		if configID == 0 || id == 0 {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的参数"})
			return
		}
		val, herr := a.retryPlatformEvent(w, r, int32(configID), kind, int32(id))
		if herr != nil {
			apiErr, ok := herr.(*ApiError)
			if !ok {
				apiErr = ErrInternal(herr.Error())
			}
			WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
			return
		}
		WriteJSON(w, http.StatusOK, val)
	}
}

// auditedPrefixes are the route namespaces whose mutations must land in
// audit_logs. Matching is prefix-based rather than a substring search so that
// the plural /api/v1/platforms/ (the channel-credential surface) and the team,
// billing and credential namespaces are covered too: the old substring test on
// "/admin/" or "/platform/" missed the routes that create the agent_teams
// tenant edge, rotate provider credentials, or change a plan through the
// billing twin of the audited platform path.
var auditedPrefixes = []string{
	"/api/v1/admin/",
	"/api/v1/platform/",
	"/api/v1/platforms/",
	"/api/v1/team/",
	"/api/v1/billing/",
	"/api/v1/api-keys",
	// Credential-changing self-service routes. The public login/register/SSO
	// endpoints are deliberately excluded: they have no authenticated actor and
	// are already covered by the per-IP limiter and the request log.
	"/api/v1/auth/password",
	"/api/v1/auth/totp/",
}

// audit records every non-GET mutation on the admin, platform and credential
// surface into audit_logs, joined to the acting user. Best-effort.
//
// Denied attempts are recorded too: an authorization probe against a
// privileged or credential route is exactly what an operator needs to see, and
// the previous status<300 filter discarded it.
func (a *App) audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isAdminSurface := strings.HasSuffix(path, "/admin") || strings.HasSuffix(path, "/platform")
		if !isAdminSurface {
			for _, p := range auditedPrefixes {
				if strings.HasPrefix(path, p) {
					isAdminSurface = true
					break
				}
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if isAdminSurface && r.Method != http.MethodGet && r.Method != http.MethodOptions && rec.status > 0 {
			user, _ := UserFrom(r)
			// NULL when there is no authenticated actor: audit_logs.admin_id is a
			// foreign key to users, so 0 would violate it and silently drop the row.
			var uid any
			if user != nil {
				uid = user.UserID
			}
			// Same trust-boundary resolution as rate limiting: the audit IP
			// must not be the client-suppliable first XFF element.
			ip := clientIP(r)
			details, _ := json.Marshal(map[string]any{"method": r.Method, "path": path, "status": rec.status})
			_, _ = a.DB.Exec(r.Context(),
				"INSERT INTO audit_logs (admin_id, action, target_type, target_id, details, ip_address) VALUES ($1,$2,'admin','',$3::jsonb,$4)",
				uid, r.Method+" "+path, string(details), ip)
		}
	})
}

// tenantAdminOnly guards handlers that act on the caller's own tenant and
// therefore require that tenant's owner — not "the role string reads admin",
// which no self-service or SSO signup ever satisfies (see isTenantOwner).
//
// The platform console keeps platformAdminOnly: that is where money and other
// tenants' data actually move.
//
// This App method is the live gate — every tenant-scoped route uses it. middleware.go used to
// carry a package-level function of the same name (reached by no route) that
// answered 401 instead of 403 for a missing token; it was dead code and is
// gone. Reinstating that shape would silently change the status code callers
// see, so keep the gate on App and keep it single.
func (a *App) tenantAdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFrom(r)
		if !ok || !a.isTenantOwner(r.Context(), user) {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "需要租户管理员权限"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// platformAdminOnly guards handlers that require the platform_admin role.
// See adminOnly: the package-level twin in middleware.go was dead code and was
// removed for the same reason (401 vs 403 divergence).
func (a *App) platformAdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFrom(r)
		if !ok || !user.IsPlatformAdmin() {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "需要平台管理员权限"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// platformAdminTenant guards a platform-admin handler that takes a tenant id.
func (a *App) platformAdminTenant(h func(http.ResponseWriter, *http.Request, int32) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFrom(r)
		if !ok || !user.IsPlatformAdmin() {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "需要平台管理员权限"})
			return
		}
		id := parseIntOr(r.PathValue("id"), 0)
		if id == 0 {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "无效的参数"})
			return
		}
		val, herr := h(w, r, int32(id))
		if herr != nil {
			apiErr, ok := herr.(*ApiError)
			if !ok {
				apiErr = ErrInternal(herr.Error())
			}
			WriteJSON(w, apiErr.Status, map[string]string{"error": apiErr.Message})
			return
		}
		WriteJSON(w, http.StatusOK, val)
	}
}
