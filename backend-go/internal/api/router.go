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

	// Website chat widget — public, authenticated by the embed token only
	// (CORS is opened for /api/v1/widget/* in a.cors).
	mux.Handle("GET /api/v1/widget/config", a.widgetRateLimit(30)(a.handle(a.widgetBootstrap)))
	mux.Handle("GET /api/v1/widget/messages", a.widgetRateLimit(60)(a.handle(a.widgetMessages)))
	mux.Handle("POST /api/v1/widget/chat", a.widgetRateLimit(20)(http.HandlerFunc(a.widgetChat)))
	mux.Handle("POST /api/v1/widget/feedback", a.widgetRateLimit(30)(a.handle(a.widgetFeedback)))

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
	authed.HandleFunc("GET /api/v1/inbox/sessions/{id}/summary", a.handleSession(a.sessionSummary))
	authed.Handle("GET /api/v1/inbox/messages/{id}/media-url", a.handle(a.getInboundMediaURL))

	// Admin — business hours, canned, models, users, analytics, rag gaps.
	authed.Handle("GET /api/v1/admin/business-hours", a.handle(a.listBusinessHours))
	authed.Handle("PUT /api/v1/admin/business-hours", a.handle(a.upsertBusinessHours))
	authed.Handle("GET /api/v1/admin/business-hours/open", a.handle(a.isBusinessOpen))
	authed.Handle("GET /api/v1/admin/canned-responses", a.handle(a.listCannedResponses))
	authed.Handle("POST /api/v1/admin/canned-responses", a.handle(a.createCannedResponse))
	authed.HandleFunc("DELETE /api/v1/admin/canned-responses/{id}", a.handleDoc(a.deleteCannedResponse))
	authed.Handle("GET /api/v1/admin/models", a.adminOnly(a.handle(a.listModelConfigs)))
	authed.Handle("PUT /api/v1/admin/models/{id}", a.adminOnly(a.handleDoc(a.updateModelConfig)))
	authed.Handle("POST /api/v1/admin/models/{id}/test", a.adminOnly(a.handleDoc(a.testModelConfig)))
	authed.Handle("GET /api/v1/admin/models/{id}/available", a.adminOnly(a.handleDoc(a.listAvailableModels)))
	authed.Handle("GET /api/v1/admin/users", a.adminOnly(a.handle(a.listUsers)))
	authed.Handle("PUT /api/v1/admin/users/{id}/role", a.adminOnly(a.handleDoc(a.updateUserRole)))
	authed.Handle("GET /api/v1/admin/analytics/overview", a.adminOnly(a.handle(a.analyticsOverview)))
	authed.Handle("GET /api/v1/admin/rag/gaps", a.adminOnly(a.handle(a.ragGaps)))

	// Analytics: timeline, breakdowns, top queries, token stats, feedback list.
	authed.Handle("GET /api/v1/admin/analytics/timeline", a.adminOnly(a.handle(a.analyticsTimeline)))
	authed.Handle("GET /api/v1/admin/analytics/top-queries", a.adminOnly(a.handle(a.topQueries)))
	authed.Handle("GET /api/v1/admin/analytics/languages", a.adminOnly(a.handle(a.languageBreakdown)))
	authed.Handle("GET /api/v1/admin/tokens/stats", a.adminOnly(a.handle(a.tokenStats)))
	authed.Handle("GET /api/v1/admin/feedback", a.adminOnly(a.handle(a.feedbackList)))
	authed.Handle("GET /api/v1/admin/agent-performance", a.adminOnly(a.handle(a.agentPerformance)))
	authed.Handle("GET /api/v1/admin/intent-analytics", a.adminOnly(a.handle(a.intentAnalytics)))
	authed.Handle("GET /api/v1/admin/integrations/status", a.adminOnly(a.handle(a.integrationsStatus)))

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

	// Roles.
	authed.Handle("GET /api/v1/roles", a.handle(a.listRoles))
	authed.Handle("POST /api/v1/roles", a.handle(a.createRole))
	authed.HandleFunc("DELETE /api/v1/roles/{id}", a.handleDoc(a.deleteRole))
	authed.HandleFunc("POST /api/v1/roles/{id}/assign", a.handleDoc(a.assignRole))
	authed.HandleFunc("POST /api/v1/roles/{id}/unassign", a.handleDoc(a.unassignRole))

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

	// FAQ suggestions.
	authed.Handle("GET /api/v1/admin/faq/suggestions", a.handle(a.listFaqSuggestions))
	authed.HandleFunc("POST /api/v1/admin/faq/suggestions/{id}/accept", a.handleDoc(a.acceptFaqSuggestion))
	authed.HandleFunc("POST /api/v1/admin/faq/suggestions/{id}/dismiss", a.handleDoc(a.dismissFaqSuggestion))

	// Billing.
	authed.Handle("GET /api/v1/billing", a.handle(a.getBilling))
	authed.Handle("PUT /api/v1/billing/plan", a.handle(a.setPlan))

	// Teams (agent management).
	authed.Handle("GET /api/v1/team", a.handle(a.listTeam))
	authed.Handle("POST /api/v1/team/agents", a.handle(a.addTeamAgent))
	authed.HandleFunc("DELETE /api/v1/team/agents/{id}", a.handleDoc(a.removeTeamAgent))

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
	authed.Handle("GET /api/v1/platform/audit-logs", a.platformAdminOnly(a.handle(a.listAuditLogs)))

	mux.Handle("/api/v1/", a.authMiddleware(a.audit(a.rateLimit(60)(authed))))

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

// audit records every non-GET mutation on the admin/platform surface into
// audit_logs, joined to the acting user. Best-effort.
func (a *App) audit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isAdminSurface := strings.Contains(path, "/admin/") || strings.Contains(path, "/platform/") || strings.HasSuffix(path, "/admin") || strings.HasSuffix(path, "/platform")
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if isAdminSurface && r.Method != http.MethodGet && r.Method != http.MethodOptions && rec.status < 300 {
			user, _ := UserFrom(r)
			uid := int32(0)
			ip := ""
			if user != nil {
				uid = user.UserID
			}
			if f := r.Header.Get("X-Forwarded-For"); f != "" {
				ip = strings.Split(f, ",")[0]
			} else {
				ip = r.RemoteAddr
			}
			details, _ := json.Marshal(map[string]any{"method": r.Method, "path": path, "status": rec.status})
			_, _ = a.DB.Exec(r.Context(),
				"INSERT INTO audit_logs (admin_id, action, target_type, target_id, details, ip_address) VALUES ($1,$2,'admin','',$3::jsonb,$4)",
				uid, r.Method+" "+path, string(details), ip)
		}
	})
}

// adminOnly guards handlers that require the tenant admin (or platform admin).
func (a *App) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFrom(r)
		if !ok || !user.IsAdmin() {
			WriteJSON(w, http.StatusForbidden, map[string]string{"error": "需要管理员权限"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// platformAdminOnly guards handlers that require the platform_admin role.
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
