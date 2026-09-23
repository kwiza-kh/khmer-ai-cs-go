package migrations

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Tests for the 061 RLS backstop (chat_messages / knowledge_chunks). They need
// a real database with the migrations applied — exactly like internal/sqlcheck,
// they only run when DATABASE_URL is set, so a plain `go test ./...` skips them.
//
// Hygiene: probe rows ARE committed (the GUC must be observable across
// separate transactions, so seeding inside the checked transaction is not an
// option — that mistake rolled the probes away and made every assertion lie).
// t.Cleanup removes them in FK-safe order (documents before users: uploaded_by
// has no cascade; sessions and their messages cascade from users). If the test
// process is killed mid-run, leftovers are trivially identifiable by the
// __rls_ prefix + timestamp suffix. Pointing DATABASE_URL at production stays
// acceptable — the probes are synthetic rows under dedicated throwaway users.

const rlsBackstopSkip = "DATABASE_URL not set — RLS backstop tests need a real migrated database"

func rlsBackstopPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip(rlsBackstopSkip)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)

	// Superusers and BYPASSRLS roles bypass row security unconditionally —
	// assertions about enforcement would fail for the wrong reason. Same
	// warning the server logs at startup applies here.
	var rolsuper bool
	if err := pool.QueryRow(context.Background(),
		"SELECT rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&rolsuper); err != nil {
		t.Skipf("cannot inspect DB role: %v", err)
	}
	if rolsuper {
		t.Skip("connected as a superuser — row security is bypassed on this connection")
	}
	return pool
}

// TestRLSBackstopPoliciesInstalled checks migration 061 landed: both tables
// have row security enabled AND forced (the connecting role owns the tables —
// without FORCE the owner would bypass every policy), with at least one
// policy present.
func TestRLSBackstopPoliciesInstalled(t *testing.T) {
	pool := rlsBackstopPool(t)
	ctx := context.Background()

	for _, table := range []string{"chat_messages", "knowledge_chunks"} {
		var enabled, forced bool
		err := pool.QueryRow(ctx,
			"SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1", table).
			Scan(&enabled, &forced)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: RLS enabled=%v forced=%v — run migrate-go; 061 must have both", table, enabled, forced)
		}

		var policies int
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = $1", table).
			Scan(&policies); err != nil {
			t.Fatalf("%s: pg_policies: %v", table, err)
		}
		if policies == 0 {
			t.Errorf("%s: no RLS policy found — 061 did not apply on this database", table)
		}
	}
}

// TestRLSBackstopEnforcesWhenGUCSet is the behavioral contract of 061:
//
//  1. GUC set → reads on both tables are constrained to the session/document
//     owner, even without a WHERE clause (the whole point: a forgotten WHERE
//     can no longer leak), in both directions.
//  2. GUC set → a cross-tenant INSERT is rejected by WITH CHECK, a
//     same-tenant INSERT goes through (the wiring must not over-block).
//  3. GUC unset → everything is visible: the fail-open branch that keeps old
//     binaries, system paths and binary rollback working. If this assertion
//     ever fails because someone flipped the default to fail-closed, that is
//     a deploy-breaking change and needs the runbook updated, not a test edit.
func TestRLSBackstopEnforcesWhenGUCSet(t *testing.T) {
	pool := rlsBackstopPool(t)
	ctx := context.Background()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	// --- Seed (committed; see the hygiene note above) ---------------------
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}

	userID := func(tag string) int32 {
		t.Helper()
		var id int32
		if err := seed.QueryRow(ctx,
			"INSERT INTO users (username, email, password_hash) VALUES ($1, $2, 'rls-probe') RETURNING user_id",
			"__rls_"+tag+"_"+suffix, "__rls_"+tag+"_"+suffix+"@rls-probe.invalid").Scan(&id); err != nil {
			t.Fatalf("seed user %s: %v", tag, err)
		}
		return id
	}
	userA, userB := userID("a"), userID("b")

	sessionID := func(owner int32) string {
		t.Helper()
		var id string
		if err := seed.QueryRow(ctx,
			"INSERT INTO sessions (user_id, platform) VALUES ($1, 'telegram') RETURNING session_id::text",
			owner).Scan(&id); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		return id
	}
	sessA, sessB := sessionID(userA), sessionID(userB)

	for _, s := range []string{sessA, sessB} {
		if _, err := seed.Exec(ctx,
			"INSERT INTO chat_messages (session_id, role, content) VALUES ($1, 'user', 'rls-probe')", s); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	docID := func(owner int32) int32 {
		t.Helper()
		var id int32
		if err := seed.QueryRow(ctx,
			"INSERT INTO knowledge_documents (title, content, uploaded_by) VALUES ('rls-probe', 'rls-probe', $1) RETURNING doc_id",
			owner).Scan(&id); err != nil {
			t.Fatalf("seed document: %v", err)
		}
		if _, err := seed.Exec(ctx,
			"INSERT INTO knowledge_chunks (doc_id, chunk_index, content) VALUES ($1, 0, 'rls-probe')", id); err != nil {
			t.Fatalf("seed chunk: %v", err)
		}
		return id
	}
	docA, docB := docID(userA), docID(userB)

	if err := seed.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// uploaded_by has no ON DELETE CASCADE (001), so documents go first;
		// their chunks cascade. users cascades to sessions and messages.
		_, _ = pool.Exec(cleanupCtx,
			"DELETE FROM knowledge_documents WHERE uploaded_by = ANY($1::int[])", []int32{userA, userB})
		_, _ = pool.Exec(cleanupCtx,
			"DELETE FROM users WHERE user_id = ANY($1::int[])", []int32{userA, userB})
	})

	seen := func(q pgx.Tx, query string, args ...any) []string {
		t.Helper()
		rows, err := q.Query(ctx, query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, v)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return out
	}

	// --- Contracts 1+2: GUC set → enforced -------------------------------
	// The GUC goes through app_set_tenant() — 061's wiring helper — because
	// that is exactly what the future tenant-scoped executor will call, and
	// this test then also guards the helper itself.
	txA, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if _, err := txA.Exec(ctx, "SELECT app_set_tenant($1)", userA); err != nil {
		t.Fatalf("app_set_tenant A: %v", err)
	}
	if got := seen(txA, "SELECT session_id::text FROM chat_messages WHERE session_id = ANY($1::uuid[])", []string{sessA, sessB}); len(got) != 1 || got[0] != sessA {
		t.Errorf("as user A, chat_messages visible = %v, want only session A (a forgotten WHERE would just have leaked)", got)
	}
	if got := seen(txA, "SELECT doc_id::text FROM knowledge_chunks WHERE doc_id = ANY($1::int[])", []int32{docA, docB}); len(got) != 1 || got[0] != strconv.Itoa(int(docA)) {
		t.Errorf("as user A, knowledge_chunks visible = %v, want only doc A", got)
	}
	if _, err := txA.Exec(ctx,
		"INSERT INTO chat_messages (session_id, role, content) VALUES ($1, 'user', 'own-tenant-write')", sessA); err != nil {
		t.Errorf("same-tenant INSERT must pass WITH CHECK: %v", err)
	}
	// Each expected rejection gets its own savepoint: a pgx nested Tx is
	// closed by its Rollback, and the rejected INSERT aborts everything above
	// it until rolled back.
	expectRejected := func(query string, args ...any) {
		t.Helper()
		sp, err := txA.Begin(ctx)
		if err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		if _, err := sp.Exec(ctx, query, args...); err == nil {
			t.Errorf("accepted but should violate WITH CHECK: %s", query)
		} else {
			t.Logf("rejected as expected: %v", err)
		}
		if err := sp.Rollback(ctx); err != nil {
			t.Fatalf("rollback to savepoint: %v", err)
		}
	}
	expectRejected("INSERT INTO chat_messages (session_id, role, content) VALUES ($1, 'user', 'cross-tenant-write')", sessB)
	expectRejected("INSERT INTO knowledge_chunks (doc_id, chunk_index, content) VALUES ($1, 9, 'cross-tenant-chunk')", docB)
	if err := txA.Rollback(ctx); err != nil {
		t.Fatalf("rollback enforced tx: %v", err)
	}

	// B sees only B's rows (isolation is not one-directional).
	txB, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer func() { _ = txB.Rollback(ctx) }()
	if _, err := txB.Exec(ctx, "SELECT app_set_tenant($1)", userB); err != nil {
		t.Fatalf("app_set_tenant B: %v", err)
	}
	if got := seen(txB, "SELECT session_id::text FROM chat_messages WHERE session_id = ANY($1::uuid[])", []string{sessA, sessB}); len(got) != 1 || got[0] != sessB {
		t.Errorf("as user B, chat_messages visible = %v, want only session B", got)
	}
	if err := txB.Rollback(ctx); err != nil {
		t.Fatalf("rollback B: %v", err)
	}

	// --- Contract 3: GUC unset → fail-open (today's behavior) ------------
	// Fresh transactions: the pool guarantees a SET LOCAL from a finished tx
	// cannot leak into the next borrower. The chat_messages count is over
	// DISTINCT sessions because contract 2 legitimately added a third message
	// to session A above.
	txC, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin open: %v", err)
	}
	defer func() { _ = txC.Rollback(ctx) }()
	if got := seen(txC, "SELECT count(DISTINCT session_id::text) FROM chat_messages WHERE session_id = ANY($1::uuid[])", []string{sessA, sessB}); got[0] != "2" {
		t.Errorf("GUC unset: chat_messages visible sessions = %s, want 2 (fail-open contract changed — update the deploy runbook)", got[0])
	}
	if got := seen(txC, "SELECT count(*) FROM knowledge_chunks WHERE doc_id = ANY($1::int[])", []int32{docA, docB}); got[0] != "2" {
		t.Errorf("GUC unset: knowledge_chunks visible count = %s, want 2 (fail-open contract changed — update the deploy runbook)", got[0])
	}
}
