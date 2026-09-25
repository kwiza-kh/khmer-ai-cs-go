package sqlcheck_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"khmer-ai-cs-go/internal/sqlcheck"
)

// TestExtractClassifiesLiteralsCorrectly guards the extractor itself. Every
// assertion here corresponds to a false positive or a missed statement from the
// first hand-rolled version of this check — without them the real check drowns
// in noise and gets ignored, which is worse than not having it.
func TestExtractClassifiesLiteralsCorrectly(t *testing.T) {
	stmts, err := sqlcheck.Extract("testdata")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	bySQL := map[string]sqlcheck.Stmt{}
	for _, s := range stmts {
		bySQL[normalize(s.SQL)] = s
	}

	want := []struct {
		desc string
		sql  string
		full bool
	}{
		{
			"a chain is reported once, whole",
			"SELECT u.user_id, COALESCE(b.plan,'free') FROM users u LEFT JOIN tenant_billing b ON b.user_id = u.user_id WHERE u.role <> 'platform_admin'",
			true,
		},
		{
			"an INSERT keeps its VALUES clause",
			"INSERT INTO sla_policies (user_id, name, priority) VALUES ($1,$2,$3) RETURNING sla_id",
			true,
		},
		{
			"the recoverable head of a variable-split chain is a statement",
			"SELECT u.user_id FROM users u LEFT JOIN tenant_billing b ON b.user_id = u.user_id",
			true,
		},
		{
			"a non-SQL chain is kept but marked incomplete",
			"could not fetch chats — check the token and try again later",
			false,
		},
	}
	for _, w := range want {
		got, ok := bySQL[normalize(w.sql)]
		if !ok {
			t.Errorf("%s: statement not extracted:\n  %s", w.desc, w.sql)
			continue
		}
		if got.Full != w.full {
			t.Errorf("%s: Full = %v, want %v (%s)", w.desc, got.Full, w.full, got)
		}
	}

	// Things that must NOT be reported as statements at all. The first would
	// produce a false "column does not exist" (no table to resolve against);
	// the rest are not SQL, and reporting them would bury the real findings.
	for _, bad := range []string{
		"VALUES ($1,$2,$3) RETURNING sla_id",                               // INSERT tail, no table
		"DELETE /api/v1/knowledge/{id}",                                    // HTTP route
		"select_account",                                                   // OAuth parameter
		"Select Messenger, Instagram, or both",                             // prose
		"WHERE user_id = $1 AND is_active = true ORDER BY created_at DESC", // unverifiable fragment
	} {
		if s, ok := bySQL[normalize(bad)]; ok {
			t.Errorf("false positive: %q extracted as a statement (Full=%v) at %s", bad, s.Full, s)
		}
	}
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestSQLObjectsExist prepares every complete SQL statement in the module
// against a real database.
//
// It is skipped unless DATABASE_URL is set, because the check is only
// meaningful against a schema with the migrations applied. Point it at any
// database that has them — production included: PREPARE parses and plans but
// never executes, and every statement runs inside a transaction that is rolled
// back.
//
//	cd backend-go
//	set -a; . ./.env-go; set +a          # or export DATABASE_URL=...
//	go test ./internal/sqlcheck/ -v
//
// A failure here means an endpoint is broken in a way no other check can see.
//
// SQLCHECK_REQUIRED=1 turns the skip into a FAILURE. The skip is right for
// everyday `go test ./...` — most local work has no database and a red suite
// would just teach people to ignore it — but it also means CI, which sets no
// DATABASE_URL, was reporting success while this check never ran. That is the
// worst outcome for a gate against "references a column that does not exist":
// the project shipped those bugs precisely because nothing exercised the
// statements. So the release flow (deploy-khmer-ai-cs/SKILL.md §4, after
// `migrate-go` and before starting the new binary) sets SQLCHECK_REQUIRED=1
// and the missing DSN fails loudly with instructions. The switch exists to let
// the RELEASE be strict, not to make daily `go test` red.
func TestSQLObjectsExist(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		if os.Getenv("SQLCHECK_REQUIRED") == "1" {
			t.Fatal("SQLCHECK_REQUIRED=1, but DATABASE_URL is not set — this check is the " +
				"only thing that catches SQL referencing columns or tables that do not exist, " +
				"and it needs a migrated database to do it. Export DATABASE_URL (point it at " +
				"any migrated database, production included: PREPARE never executes and every " +
				"statement is rolled back), or unset SQLCHECK_REQUIRED if you are not releasing.")
		}
		t.Skip("DATABASE_URL not set — skipping; the schema check needs a migrated database " +
			"(set SQLCHECK_REQUIRED=1 to make this a hard failure instead)")
	}
	root := filepath.Join("..", "..")
	stmts, err := sqlcheck.Extract(root)
	if err != nil {
		t.Fatalf("extract from %s: %v", root, err)
	}
	if len(stmts) < 100 {
		t.Fatalf("only %d statements extracted from %s — the walker is probably looking at the wrong root", len(stmts), root)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rep, err := sqlcheck.Verify(ctx, dsn, stmts)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	t.Logf("checked %d statements: %d prepared, %d skipped, %d broken, %d unparseable",
		rep.Checked, rep.Passed, rep.Skipped, len(rep.Broken), len(rep.Syntax))

	// Syntax failures are extraction artifacts, not source defects — a fragment
	// that looked like a statement. Surfaced for visibility, never fatal.
	for _, f := range rep.Syntax {
		t.Logf("note: could not parse (likely a fragment) %s", f)
	}

	for _, f := range rep.Broken {
		t.Errorf("statement references a database object that does not exist:\n    %s", f)
	}
}
