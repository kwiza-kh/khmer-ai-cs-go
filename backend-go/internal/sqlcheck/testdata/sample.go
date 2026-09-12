// Package sample is a fixture for sqlcheck's extractor tests. The Go tool
// ignores everything under testdata, so this file is never compiled — it exists
// only to be parsed.
package sample

// chainedStatement — one statement split across literals joined by `+`. It must
// be reported ONCE, as the whole statement.
const chainedStatement = "SELECT u.user_id, COALESCE(b.plan,'free') " +
	"FROM users u LEFT JOIN tenant_billing b ON b.user_id = u.user_id " +
	"WHERE u.role <> 'platform_admin'"

// splitByVariable — a chain broken mid-way by a variable. The literals before
// the break are recoverable; the tail after it is a fragment and must never be
// reported as a statement of its own (that is what produced a false
// "column does not exist" during the first audit).
func splitByVariable(where string) string {
	return "SELECT u.user_id FROM users u " +
		"LEFT JOIN tenant_billing b ON b.user_id = u.user_id " +
		where +
		" ORDER BY u.user_id"
}

// insertTail — the tail of a concatenated INSERT. It starts with VALUES, which
// is a valid statement on its own, but here it has no table: reporting it would
// guarantee a false positive.
const insertHead = "INSERT INTO sla_policies (user_id, name, priority) " +
	"VALUES ($1,$2,$3) RETURNING sla_id"

// routeRegistration — DELETE is an HTTP method as well as a SQL keyword.
const routeRegistration = "DELETE /api/v1/knowledge/{id}"

// oauthParameter — starts with the letters "select" but is not a keyword.
const oauthParameter = "select_account"

// prose — a sentence that happens to begin with the word Select.
const prose = "Select Messenger, Instagram, or both"

// fragmentAlone — a WHERE clause with no head. It cannot be prepared on its
// own, so the extractor drops it rather than reporting a statement that would
// only ever produce a misleading result.
const fragmentAlone = "WHERE user_id = $1 AND is_active = true ORDER BY created_at DESC"

// notSQL — a multi-literal chain that is not SQL at all. It flattens, so the
// extractor sees it, but it must be marked incomplete so verification skips it
// instead of reporting a parse error as though it were a defect.
func notSQL() string {
	return "could not fetch chats — check the token " +
		"and try again later"
}
