// Package tenantscope is the static tenant-isolation gate.
//
// WHY A STATIC CHECK — the tests that pin tenant scoping (list_scope_test.go,
// platform_tenants_test.go, …) all need a live database, so they skip on
// `DATABASE_URL` being unset — which is every CI run. The database-backed
// assertions are the right tool for the rows they check, but between runs
// nothing stops a new query from reaching a tenant table without a tenant
// predicate. That is precisely the leak class a 2026-09-12 audit found five
// live instances of, and the class the RLS backstop (061) only half covers:
// the backstop is inert until something sets `app.user_id`.
//
// WHAT IT ASSERTS — for every complete SQL statement extracted from the Go
// source (the same extraction internal/sqlcheck uses), when the statement
// touches a table that belongs to one tenant, the statement must bind that
// table to its tenant in one of these ways:
//
//  1. it names the tenant column of every tenant table it touches
//     (`user_id`, `uploaded_by`, `owner_user_id`, … — alias prefixes are
//     fine), or
//  2. it is an INSERT whose column list names the tenant column, so the
//     writer must supply it, or
//  3. it lives in a function that first runs one of the reviewed funnels
//     (ensureSessionAccess, ensureConfigOwner, requireOwnerOfTenant, …),
//     which is how session-scoped statements (chat_messages and friends,
//     whose own table has no tenant column) are authorised, or
//  4. the enclosing function carries a `// tenantscope:ok <reason>` marker,
//     or
//  5. it matches a reviewed exception in exceptions.go (file + function +
//     why) or a whole cross-tenant-by-design file.
//
// A new unguarded statement fails the test with the file, line, enclosing
// function and tables; the fix is one of the five shapes above. A sixth
// shape — "add it to the list and move on" — is made expensive on purpose:
// exceptions are keyed per function and a stale entry (one no function uses
// any more) is itself a failure, so the list cannot rot into a blanket
// allowance.
//
// WHAT IT CANNOT SEE (accepted limits, stated so nobody oversells it):
//   - it does not resolve placeholder arguments, so it cannot tell
//     `user_id = $1` bound to `user.Tenant()` from the same text bound to a
//     request field. Binding mistakes are what the database-backed tests and
//     review are for;
//   - statements built by concatenation at runtime (a WHERE fragment in a
//     variable) are seen without their tail — exactly like sqlcheck, they
//     are then judged by their enclosing function's funnel/marker;
//   - `cmd/` tools and the platform pipeline are cross-tenant by design and
//     are exempted as files, not per statement.
package tenantscope

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// tenantKeyed maps a table that holds per-tenant content to the column that
// binds a row to its tenant. A statement touching one of these tables must
// name that column (or live under a funnel/exception).
var tenantKeyed = map[string]string{
	"users":                      "user_id",
	"platform_configs":           "user_id",
	"token_usage":                "user_id",
	"sessions":                   "user_id",
	"knowledge_documents":        "uploaded_by",
	"api_keys":                   "user_id",
	"business_hours":             "user_id",
	"canned_responses":           "user_id",
	"platform_oauth_sessions":    "user_id",
	"human_handoff_requests":     "user_id",
	"faq_suggestions":            "user_id",
	"customer_profiles":          "user_id",
	"agent_teams":                "owner_user_id",
	"tenant_billing":             "user_id",
	"marketing_campaigns":        "user_id",
	"notifications":              "user_id",
	"sla_policies":               "user_id",
	"sla_breaches":               "user_id",
	"routing_rules":              "user_id",
	"macros":                     "user_id",
	"user_totp":                  "user_id",
	"message_notes":              "user_id",
	"webhook_subscriptions":      "user_id",
	"rag_query_logs":             "user_id",
	"widget_tokens":              "user_id",
	"telegram_notify_settings":   "user_id",
	"kb_contradictions":          "user_id",
	"platform_support_messages":  "user_id",
	"reply_cache":                "user_id",
	"scheduled_jobs":             "user_id",
	"personas":                   "user_id",
	"persona_bindings":           "user_id",
	"payments":                   "user_id",
	"team_invites":               "owner_user_id",
	"platform_inbound_events":    "config_id",
	"platform_outbox":            "config_id",
	"platform_delivery_receipts": "config_id",
	"platform_connection_health": "config_id",
}

// tenantDerived are the content tables with no tenant column of their own:
// their tenant is reached through a join (chat_messages → sessions,
// knowledge_chunks → knowledge_documents, webhook_deliveries →
// webhook_subscriptions). A statement touching one must name the join key and
// then either scope the join's table or live under a funnel — the two cases
// 061's RLS policies exist for.
var tenantDerived = map[string]string{
	"chat_messages":       "session_id",
	"knowledge_chunks":    "doc_id",
	"session_assignments": "session_id",
	"webhook_deliveries":  "subscription_id",
	// platform_user_sessions is keyed by session: its tenant is reached through
	// sessions.user_id (in the same statement, or via a session funnel).
	"platform_user_sessions": "session_id",
	// team_invite_uses.user_id is the invitee, not the tenant: the row belongs
	// to the invite, which belongs to the owner.
	"team_invite_uses": "invite_id",
}

// tenantColumns is every binding column the checker accepts as a predicate.
var tenantColumns = []string{
	"user_id", "owner_user_id", "uploaded_by", "agent_user_id", "platform_user_id",
	"created_by", "tenant_id", "app_tenant_id",
}

// funnels authorise a following session- or config-scoped statement. They are
// the codebase's own authorization funnels, not a heuristic.
var funnels = []string{
	"ensureSessionAccess", "ensureConfigOwner", "requireOwnerOfTenant",
	"isTenantOwner", "tenantOwnerFor", "isTenantOwnerID",
	// The chat path's session funnel: resolves the session inside the caller's
	// tenant (creating it when absent) before any turn statement runs.
	"resolveChatSession",
}

// Gap is one statement that reaches tenant data without a visible binding.
type Gap struct {
	File   string
	Line   int
	Func   string
	Tables []string
	SQL    string
}

func (g Gap) String() string {
	return fmt.Sprintf("%s:%d %s() tables=%s\n    %s",
		g.File, g.Line, g.Func, strings.Join(g.Tables, ","), oneLine(g.SQL, 160))
}

// Report is the outcome of one analysis.
type Report struct {
	Statements int
	Tenant     int // statements that touch tenant data
	Bound      int // …and bind it in-statement
	Funnelled  int // …or sit under a reviewed funnel
	Marked     int // …or carry an inline marker
	Excepted   int // …or match a reviewed exception
	Gaps       []Gap
	// Stale lists exceptions no longer matching any statement, so the list
	// cannot silently grow into a blanket allowance.
	Stale []string
}

var tableRE = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE|DELETE\s+FROM|EXISTS\s*\(\s*SELECT[^)]*?\bFROM)\s+(?:ONLY\s+)?([a-z_][a-z0-9_.]*)`)

// notTables are SQL keywords the table regex can catch ("FROM VALUES", CTE
// helpers, function calls).
var notTables = map[string]bool{
	"select": true, "values": true, "set": true, "lateral": true,
	"generate_series": true, "unnest": true,
	"jsonb_array_elements": true, "jsonb_array_elements_text": true,
	"jsonb_each": true, "jsonb_each_text": true, "jsonb_object_keys": true,
	"regexp_split_to_table": true, "string_to_table": true,
}

// referencedTables returns the base tables named after a FROM/JOIN/INTO
// keyword. CTE names are returned too; they never collide with the tenant
// tables below, so a CTE reference is simply ignored.
func referencedTables(sql string) []string {
	cleaned := stripSQLComments(sql)
	seen := map[string]bool{}
	for _, m := range tableRE.FindAllStringSubmatch(cleaned, -1) {
		t := strings.ToLower(m[1])
		if i := strings.LastIndex(t, "."); i >= 0 {
			t = t[i+1:]
		}
		if t == "" || notTables[t] {
			continue
		}
		seen[t] = true
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func hasWord(sql, word string) bool {
	re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(sql)
}

// insertNamesTenant reports whether the statement is an INSERT whose column
// list names a tenant column, so the caller must bind one.
var insertColsRE = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+[a-z_][a-z0-9_]*\s*\(([^)]*)\)`)

func insertNamesTenant(sql string) bool {
	m := insertColsRE.FindStringSubmatch(sql)
	if m == nil {
		return false
	}
	for _, col := range tenantColumns {
		if hasWord(m[1], col) {
			return true
		}
	}
	return false
}
