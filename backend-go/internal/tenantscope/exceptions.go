package tenantscope

import "strings"

// crossTenantFiles are whole files that operate on tenant data without a
// per-statement tenant predicate *by design*. Each entry states why; the
// check reports the reason when a file is exempted so the decision is
// visible in the source and not only in a reviewer's head.
//
// Keep this list short. A file belongs here only when every statement in it
// takes the tenant from the row it already owns (a config_id or claim token
// resolved by an earlier statement in the same file), or when the file is
// reached only by operators/platform staff.
var crossTenantFiles = []struct {
	suffix string
	why    string
}{
	{"internal/platform/", "pipeline + workers: the row under work carries its tenant (config_id/session_id), and the webhook resolvers are the code that *decides* the tenant from a verified provider identity"},
	{"internal/api/platform_admin_handlers.go", "platform-admin console: cross-tenant reads are the feature"},
	{"internal/api/platform_ops_handlers.go", "platform-admin operations console (support, revenue, channel health)"},
	{"internal/api/platform_handlers.go", "platform-admin channel console; the tenant-facing handlers in the same file go through ensureConfigOwner"},
	{"internal/api/platform_tokens.go", "platform-admin token/cost board"},
	{"internal/api/platform_metrics.go", "platform-admin metrics"},
	{"internal/api/backfill_secrets.go", "one-shot operator CLI that re-seals legacy plaintext secrets across every tenant"},
	{"internal/api/migrations_status.go", "operator schema-status endpoint"},
	{"internal/api/tenant_owner.go", "this is the ownership resolver itself"},
	{"internal/api/middleware.go", "seat/tenant resolution itself; the queries read the caller's own row"},
	{"internal/migrations/", "migration runner (DDL, not tenant data access)"},
	{"internal/sqlcheck/", "the SQL extractor used by this package"},
	{"internal/tenantscope/", "this package"},
	{"cmd/", "operator CLIs (migrate, retention, billing reconcile, evaluation harnesses)"},
	{"internal/usage/", "usage accounting: every statement is keyed by the tenant id its caller passes"},
	{"internal/realtime/", "WebSocket fan-out keyed by the authenticated handshake's user id"},
	{"internal/scheduler/", "job store; the registered handlers own their own scoping"},
	{"internal/gemini/", "provider client, no tenant tables"},
	{"internal/anthropic/", "provider client, no tenant tables"},
	{"internal/llm/", "provider router, no tenant tables"},
	{"internal/security/", "crypto helper, no tenant tables"},
	{"internal/textutil/", "text helper, no tenant tables"},
	{"internal/storager2/", "object storage client; callers namespace keys by tenant"},
	{"internal/auth/", "JWT library, no tenant tables"},
	{"internal/config/", "configuration loader"},
}

// crossTenantFile returns the exemption reason for a file, or "".
func crossTenantFile(path string) string {
	for _, e := range crossTenantFiles {
		if strings.HasPrefix(path, e.suffix) {
			return e.why
		}
	}
	return ""
}

// exceptionKey identifies one reviewed statement context: the enclosing
// function of the statement. File + function (not line) keeps the entry valid
// across edits while still failing when a *new* unscoped statement appears in
// any other function.
func exceptionKey(file, fn string) string { return file + "|" + fn }

// exceptions are the reviewed cases the static check cannot see through. Each
// states the tenant binding the statement actually has. An entry that no
// longer matches a statement is a test failure (see Report.Stale), so this map
// cannot grow into a blanket allowance.
var exceptions = map[string]string{
	// — chat path: the session id comes from resolveChatSession (the reviewed
	// funnel) or from an inbox handler's ensureSessionAccess; these helpers
	// only ever see an already-authorized session.
	"internal/api/chat_handlers.go|resolveChatSession": "the SELECT/UPDATEs run after this same function's WHERE session_id = $1 AND user_id = $2 ownership check",
	"internal/api/chat_handlers.go|chatHistory":        "session id from an authorized turn (resolveChatSession)",
	"internal/api/chat_handlers.go|persistModelReply":  "session id from an authorized turn (resolveChatSession / ensureSessionAccess)",
	"internal/api/chat_handlers.go|persistSystemReply": "session id from an authorized turn (chatPlain/chatStream/widgetChat all resolved it first)",
	"internal/api/chat_handlers.go|webHandoffCheck":    "session id from an authorized turn",
	"internal/api/chat_handlers.go|customerLanguage":   "session id from an authorized turn",
	"internal/api/chat_handlers.go|listSessions":       "scope lives in the constant `where` clause (WHERE user_id = $1), see the statement",

	// — inbox: helpers called only after ensureSessionAccess in the same request.
	"internal/api/inbox_handlers.go|markHandoffAssigned": "called by assignSession/takeoverSession after ensureSessionAccess",
	"internal/api/inbox_handlers.go|markHandoffResolved": "called by updateSessionStatus after ensureSessionAccess",
	"internal/api/misc2_handlers.go|loadSessionLatest":   "called by copilotSuggest after ensureSessionAccess",

	// — knowledge: the doc id was just created by the same request, or the
	// scope clause is a concatenated literal.
	"internal/api/knowledge_handlers.go|uploadKnowledgeFile":    "doc_id returned by RAG.UploadDocument for the caller's own tenant in this request",
	"internal/api/knowledge_handlers.go|listContradictions":     "rows are pre-filtered by c.user_id = $1; the joined documents come through those rows",
	"internal/api/knowledge_handlers.go|setContradictionStatus": "the WHERE (contradiction_id AND user_id = $3) is a concatenated literal the extractor stops at",

	// — analytics: the count/page pair shares one constant `where` clause
	// (WHERE s.user_id=$1), which the extractor cannot see through the variable.
	"internal/api/analytics_handlers.go|feedbackList": "scope lives in the constant `where` clause (WHERE s.user_id=$1), see the statement",

	// — admin: listUsers builds its scope in a constant `where` clause that
	// resolves the caller's tenant, an owner-plus-seats set (pinned by
	// list_scope_test.go).
	"internal/api/admin_handlers.go|listUsers": "scope lives in the constant `where` clause (tenant owner plus owned seats), see the statement",

	// — billing: the PayPal order id is a capability handed to one tenant by
	// the provider; capture cross-checks payments.user_id against the caller
	// before activating (billing_paypal.go:sqlPaymentOwner).
	"internal/api/billing_paypal.go|?": "payments rows are addressed by provider order id; the capture path compares user_id to the caller first",

	// — Meta OAuth: the row is keyed by a server-issued state/session id from a
	// tenantAdminOnly start; the tenant-facing readers all add user_id = $2.
	"internal/api/meta_oauth.go|saveMetaConfigTx":  "config id came from the caller's own completed OAuth session",
	"internal/api/meta_oauth.go|metaOAuthCallback": "addressed by the one-time state hash issued to one tenant; users read through user_id = $2",
	"internal/api/meta_oauth.go|consumeOAuthState": "single-use state capability; the tenant-facing reader adds user_id = $2",

	// — handoffs: createHandoffRequest verifies the session against user.Tenant()
	// first; listHandoffs scopes the page with user.Tenant() but its COUNT uses a
	// concatenated placeholder.
	"internal/api/misc3_handlers.go|createHandoffRequest": "the session is verified against user.Tenant() at the top of the function",
	"internal/api/misc2_handlers.go|listHandoffs":         "scope lives in the concatenated where clause (h.user_id = user.Tenant()), see the statement",

	// — background workers: cross-tenant scans whose per-row work is scoped by
	// the row's own user_id/campaign id (never by a request).
	"internal/api/tasks.go|StartBackgroundTasks": "crash recovery for campaigns left in 'sending' by a previous process",
	"internal/api/tasks.go|dispatchDueCampaigns": "iterates due campaigns; each dispatch uses that campaign's own user_id and config_id",
	"internal/api/tasks.go|dispatchOneCampaign":  "one campaign row's own config_id + platform_user_id address the session",
	"internal/api/tasks.go|resetBillingCycles":   "monthly quota reset across every tenant, by cycle_end",

	// — team invites: the invite token is the capability; the owner scopes the
	// list, and the uses table is read through owner-scoped invite ids.
	"internal/api/team_invites.go|?": "invites are selected by owner_user_id or by the invite token; uses are read via owner-scoped invite ids",

	// — widget: the public token resolves to one tenant, and every session used
	// below is either owner-verified (owner == t.ownerID) or freshly created for
	// that owner; feedback verifies the message's session belongs to the token's
	// tenant before writing.
	"internal/api/widget_handlers.go|widgetMessages":       "session owner compared to the widget token's tenant",
	"internal/api/widget_handlers.go|widgetChat":           "the session is bound to the token's tenant (owner == t.ownerID) or created for it, before any turn statement",
	"internal/api/widget_handlers.go|widgetFeedback":       "the message's session is verified against the token's tenant before the UPDATE",
	"internal/api/widget_handlers.go|releaseWebHandoff":    "called with the widget session already bound to its owner",
	"internal/api/widget_handlers.go|classifyWebTurnAsync": "called with the widget session already bound to its owner",

	// — RAG workers: statements claim or update a document row by its own id
	// and status; the tenant-facing search/CRUD paths all filter uploaded_by.
	"internal/rag/service.go|SpawnIndexWorkers":       "index-worker crash recovery, by index_status",
	"internal/rag/service.go|refreshStaleURLDocs":     "URL refresh sweep across tenants; each update is by doc_id",
	"internal/rag/service.go|indexDocument":           "worker claims one pending doc row; chunks are written under that doc_id",
	"internal/rag/service.go|markDocumentFailed":      "worker updates the doc row it claimed",
	"internal/rag/service.go|setCompileStatus":        "worker/compile status write, by the doc row it owns",
	"internal/rag/service.go|compileDocument":         "ingest-time compile of the doc row just indexed",
	"internal/rag/service.go|backfillSegmentedChunks": "one-off backfill of segments across the table; each update is by chunk_id",
	"internal/rag/service.go|UpdateDocument":          "the document is verified with uploaded_by = userID before the chunk delete and content update",
}
