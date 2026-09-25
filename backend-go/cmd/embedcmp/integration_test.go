package main

// Live-database tests for the READ-ONLY contract and the corpus SQL.
//
// They are SKIPPED unless TEST_DATABASE_URL is set, so the normal `go test
// ./cmd/embedcmp/` stays hermetic. When it IS set they are the only tests that
// prove, against a real Postgres, the two things the package's contract claims:
//
//  1. every statement the tool composes is answered by the corpus SQL the tool
//     intends (the query, its filters and the vector parsing), and
//  2. a write cannot happen — not because the code is careful, but because the
//     SESSION is read-only, so Postgres refuses the statement even when the
//     layer-2 guard is bypassed entirely.
//
// (2) is the reason this file exists: the guard is testable in memory, but "the
// database itself would refuse" is a claim only a database can confirm.
//
// SAFETY: the attempted write is a no-op (`SET content = content WHERE false`),
// and the target database name must contain "embedcmp" — checked, not assumed —
// so a mistyped or production-shaped URL fails the test instead of touching a
// real knowledge base.
//
// FIXTURE — the tests assert exact counts, so recreate the database exactly:
//
//	createdb embedcmp_smoke
//	psql -d embedcmp_smoke <<'SQL'
//	CREATE TABLE knowledge_documents (doc_id serial PRIMARY KEY, title text,
//	  uploaded_by int, index_status text, embedding_model text);
//	CREATE TABLE knowledge_chunks (chunk_id serial PRIMARY KEY,
//	  doc_id int REFERENCES knowledge_documents(doc_id) ON DELETE CASCADE,
//	  chunk_index int, content text, embedding text);   -- text stands in for vector(768)
//	CREATE TABLE model_configs (config_id serial PRIMARY KEY, api_key text,
//	  model_name text, system_prompt text, max_tokens int, is_default boolean);
//	INSERT INTO knowledge_documents (doc_id, title, uploaded_by, index_status, embedding_model) VALUES
//	  (1,'payroll policy',7,'ready','gemini-embedding-001'),      -- current model
//	  (2,'clinic hours',7,'ready','gemini-embedding-001'),        -- current model
//	  (3,'stale legacy doc',7,'ready','text-embedding-004'),      -- other model: excluded
//	  (4,'not indexed yet',7,'pending','gemini-embedding-001'),   -- not ready: excluded
//	  (5,'another tenant',8,'ready','gemini-embedding-001');      -- other tenant: excluded
//	-- three chunks with an embedding, one without, all 768 wide
//	INSERT INTO knowledge_chunks (chunk_id, doc_id, content, embedding) VALUES
//	  (1,1,'payroll is paid on the 25th','[' || array_to_string(array_fill(0.1::text, ARRAY[768]), ',') || ']'),
//	  (2,1,'salary payment schedule','[' || array_to_string(array_fill(0.1::text, ARRAY[768]), ',') || ']'),
//	  (3,2,'clinic opens at 8am','[' || array_to_string(array_fill(0.2::text, ARRAY[768]), ',') || ']'),
//	  (4,2,'holiday opening times',NULL),
//	  (5,3,'legacy doc chunk','[' || array_to_string(array_fill(0.3::text, ARRAY[768]), ',') || ']'),
//	  (6,5,'other tenant chunk','[' || array_to_string(array_fill(0.4::text, ARRAY[768]), ',') || ']');
//	INSERT INTO model_configs (api_key, model_name, system_prompt, max_tokens, is_default)
//	  VALUES ('AIza-plaintext-smoke-key','gemini-3.6-flash','',2048,true);
//	SQL
//	TEST_DATABASE_URL="postgres://$USER@127.0.0.1:5432/embedcmp_smoke?sslmode=disable" \
//	  go test -race ./cmd/embedcmp/
//
// pgvector is not required: the embedding column is plain text here, which the
// corpus query casts with ::text anyway. That is enough to prove the SQL, the
// filters, the scan order and the parsing; it does not prove pgvector's own
// numeric rendering, which is why parseVectorLiteral has its own tests.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func smokeDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set: skipping the live-database tests")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL does not parse: %v", err)
	}
	if !strings.Contains(cfg.ConnConfig.Database, "embedcmp") {
		t.Fatalf("refusing to run against database %q: the name must contain \"embedcmp\" so this can never "+
			"point at a real knowledge base", cfg.ConnConfig.Database)
	}
	return dsn
}

func openSmokePool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := openReadOnlyPool(ctx, dsn)
	if err != nil {
		t.Fatalf("openReadOnlyPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestReadOnlySessionIsActuallyReadOnly(t *testing.T) {
	ctx := context.Background()
	pool := openSmokePool(t, smokeDSN(t))

	// The startup parameter must have reached the server. Read through the guard,
	// because that is how the tool reads everything.
	var setting string
	if err := selectRow(ctx, pool, "SELECT current_setting('default_transaction_read_only')", nil, &setting); err != nil {
		t.Fatalf("read default_transaction_read_only: %v", err)
	}
	if setting != "on" {
		t.Fatalf("session default_transaction_read_only = %q, want \"on\": the read-only contract would "+
			"depend on the code alone", setting)
	}

	var before int
	if err := selectRow(ctx, pool, "SELECT count(*)::int FROM knowledge_chunks", nil, &before); err != nil {
		t.Fatalf("count chunks: %v", err)
	}

	// Bypass the layer-2 guard on purpose: this is the layer-3 claim. Each
	// statement is a no-op (WHERE false / content = content) so that a session
	// which somehow IS writable still cannot damage anything.
	for _, stmt := range []string{
		"UPDATE knowledge_chunks SET content = content WHERE false",
		"DELETE FROM knowledge_chunks WHERE false",
		"INSERT INTO knowledge_chunks (doc_id, content) SELECT doc_id, content FROM knowledge_chunks WHERE false",
		"CREATE TABLE embedcmp_must_not_exist (a int)",
	} {
		if _, err := pool.Exec(ctx, stmt); err == nil {
			t.Errorf("Postgres ACCEPTED a write on the read-only pool: %s", stmt)
		} else if !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%s failed for the wrong reason (want a read-only refusal): %v", stmt, err)
		}
	}

	var after int
	if err := selectRow(ctx, pool, "SELECT count(*)::int FROM knowledge_chunks", nil, &after); err != nil {
		t.Fatalf("count chunks after the write attempts: %v", err)
	}
	if before != after {
		t.Errorf("row count changed from %d to %d: the read-only contract was violated", before, after)
	}
}

func TestLoadCorpusAgainstLiveDatabase(t *testing.T) {
	ctx := context.Background()
	pool := openSmokePool(t, smokeDSN(t))

	// The fixture (see the smoke database's setup): tenant 7 has four documents —
	// two ready and current, one ready but embedded with a different model, one
	// not ready — plus a fifth document belonging to tenant 8. Four chunks belong
	// to the current ready documents, one of which has a NULL vector.
	corpus, stats, err := loadCorpus(ctx, pool, 7, "gemini-embedding-001")
	if err != nil {
		t.Fatalf("loadCorpus: %v", err)
	}

	if len(corpus) != 3 {
		t.Fatalf("loaded %d chunks, want 3 (NULL vectors, other tenants, other models and unready documents excluded): %+v",
			len(corpus), corpus)
	}
	// ORDER BY chunk_id: the corpus order is what the tie-break in rankChunks
	// depends on, so it is asserted rather than assumed.
	if corpus[0].chunkID != 1 || corpus[1].chunkID != 2 || corpus[2].chunkID != 3 {
		t.Errorf("corpus order = %d,%d,%d, want 1,2,3", corpus[0].chunkID, corpus[1].chunkID, corpus[2].chunkID)
	}
	if corpus[0].title != "payroll policy" || corpus[2].title != "clinic hours" {
		t.Errorf("titles not joined: %q, %q", corpus[0].title, corpus[2].title)
	}
	if len(corpus[0].stored) != expectedDims {
		t.Fatalf("parsed vector has %d dims, want %d", len(corpus[0].stored), expectedDims)
	}
	if corpus[0].stored[0] != float32(0.1) || corpus[0].stored[767] != float32(0.1) {
		t.Errorf("parsed vector edges = %v … %v, want 0.1 at both ends", corpus[0].stored[0], corpus[0].stored[767])
	}
	if corpus[2].stored[0] != float32(0.2) {
		t.Errorf("second document's vector = %v, want 0.2 (values must not bleed between rows)", corpus[2].stored[0])
	}

	if stats.Chunks != 4 {
		t.Errorf("stats.Chunks = %d, want 4 (the NULL-vector chunk is in the filter, not in the ranking)", stats.Chunks)
	}
	if stats.Docs != 2 {
		t.Errorf("stats.Docs = %d, want 2", stats.Docs)
	}
	if stats.NoVector != 1 {
		t.Errorf("stats.NoVector = %d, want 1", stats.NoVector)
	}
	if stats.DocsTotal != 4 || stats.DocsReady != 3 || stats.DocsOtherCell != 1 {
		t.Errorf("document stats = total %d, ready %d, other model %d; want 4, 3, 1",
			stats.DocsTotal, stats.DocsReady, stats.DocsOtherCell)
	}
	if stats.DocsNotReady != 1 {
		t.Errorf("stats.DocsNotReady = %d, want 1", stats.DocsNotReady)
	}
}

func TestStudioKeyResolutionReadsModelConfigs(t *testing.T) {
	ctx := context.Background()
	pool := openSmokePool(t, smokeDSN(t))

	// The fixture stores a LEGACY PLAINTEXT key and no PLATFORM_CREDENTIAL_KEY is
	// set, which is exactly the case DecryptOrKeep passes through — and the case
	// where refusing to run would be wrong, because the server itself accepts it.
	t.Setenv("PLATFORM_CREDENTIAL_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	key, source, err := resolveStudioKey(ctx, pool)
	if err != nil {
		t.Fatalf("resolveStudioKey: %v", err)
	}
	if key != "AIza-plaintext-smoke-key" {
		t.Errorf("key = %q, want the model_configs default row's value", key)
	}
	if !strings.Contains(source, "model_configs") {
		t.Errorf("key source = %q, want it to name model_configs", source)
	}
}
