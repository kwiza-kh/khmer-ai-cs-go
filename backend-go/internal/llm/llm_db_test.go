package llm

// Row selection decides which credentials Gemini serves with once Claude is the
// default, so it is pinned against a real table. Each test runs inside a transaction
// that is rolled back, and only when DATABASE_URL names a migrated database. The table
// already holds the seeded rows, so every expectation is computed from the rule in the
// same transaction rather than assuming the table is empty.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openRolledBackTx(t *testing.T) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unusable: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Skipf("cannot begin a transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	// Start with no default row, so the default each test sets is the only one.
	if _, err := tx.Exec(ctx, "UPDATE model_configs SET is_default = false"); err != nil {
		t.Fatalf("clear defaults inside the transaction: %v", err)
	}
	return tx
}

func insertTestRow(t *testing.T, tx pgx.Tx, name, provider string, isDefault bool) int32 {
	t.Helper()
	var id int32
	if err := tx.QueryRow(context.Background(),
		"INSERT INTO model_configs (name, provider, model_name, api_key, is_default) VALUES ($1, $2, 'claude-haiku-5-5', 'sealed', $3) RETURNING config_id",
		name, provider, isDefault).Scan(&id); err != nil {
		t.Fatalf("insert %s: %v", name, err)
	}
	return id
}

// oldestGeminiRow is the rule's own answer for "the oldest Gemini row", read from the table.
func oldestGeminiRow(t *testing.T, tx pgx.Tx) int32 {
	t.Helper()
	var id int32
	if err := tx.QueryRow(context.Background(),
		"SELECT min(config_id) FROM model_configs WHERE provider NOT IN ('anthropic', 'deepseek')").Scan(&id); err != nil {
		t.Fatalf("oldest gemini row: %v", err)
	}
	return id
}

func TestDefaultRowDecidesTheProviderAndGeminiNeverTakesTheClaudeRow(t *testing.T) {
	tx := openRolledBackTx(t)
	ctx := context.Background()
	claudeRow := insertTestRow(t, tx, "claude row", ProviderAnthropic, true)

	def, ok := LoadDefault(ctx, tx)
	if !ok || def.ConfigID != claudeRow || def.Provider != ProviderAnthropic {
		t.Fatalf("LoadDefault = %+v, %v; want the Claude row marked default", def, ok)
	}
	g, ok := LoadGemini(ctx, tx)
	if !ok {
		t.Fatal("LoadGemini found no Gemini row, but the seeded table holds one")
	}
	if g.ConfigID == claudeRow || IsClaude(g.Provider) {
		t.Fatalf("LoadGemini returned the Claude row (%s): its key would become the Gemini credential", g.Provider)
	}
	if want := oldestGeminiRow(t, tx); g.ConfigID != want {
		t.Errorf("LoadGemini = config %d, want the oldest Gemini row %d", g.ConfigID, want)
	}
}

func TestGeminiPrefersItsDefaultRowThenTheOldest(t *testing.T) {
	tx := openRolledBackTx(t)
	ctx := context.Background()
	insertTestRow(t, tx, "newer gemini", ProviderGemini, false)
	defaultRow := insertTestRow(t, tx, "default gemini", ProviderGemini, true)

	if g, ok := LoadGemini(ctx, tx); !ok || g.ConfigID != defaultRow {
		t.Fatalf("with a default Gemini row, LoadGemini = %+v; want that row", g)
	}
	if _, err := tx.Exec(ctx, "UPDATE model_configs SET is_default = false"); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	if g, ok := LoadGemini(ctx, tx); !ok || g.ConfigID != oldestGeminiRow(t, tx) {
		t.Fatalf("with no default Gemini row, LoadGemini = %+v; want the oldest Gemini row", g)
	}
}

func TestGeminiNeverReadsAClaudeRow(t *testing.T) {
	tx := openRolledBackTx(t)
	ctx := context.Background()
	insertTestRow(t, tx, "claude default", ProviderAnthropic, true)
	insertTestRow(t, tx, "claude second", ProviderAnthropic, false)

	if g, ok := LoadGemini(ctx, tx); ok && IsClaude(g.Provider) {
		t.Fatalf("LoadGemini returned a Claude row (%s): Claude's key would become the Gemini credential", g.Provider)
	}
	if def, ok := LoadDefault(ctx, tx); !ok || def.Provider != ProviderAnthropic {
		t.Fatalf("LoadDefault = %+v, %v; want the default Claude row", def, ok)
	}
}

func TestGeminiNeverReadsADeepSeekRow(t *testing.T) {
	tx := openRolledBackTx(t)
	ctx := context.Background()
	deepseekRow := insertTestRow(t, tx, "deepseek default", ProviderDeepSeek, true)

	def, ok := LoadDefault(ctx, tx)
	if !ok || def.ConfigID != deepseekRow || def.Provider != ProviderDeepSeek {
		t.Fatalf("LoadDefault = %+v, %v; want the DeepSeek row marked default", def, ok)
	}
	g, ok := LoadGemini(ctx, tx)
	if !ok {
		t.Fatal("LoadGemini found no Gemini row, but the seeded table holds one")
	}
	if g.ConfigID == deepseekRow || IsDeepSeek(g.Provider) {
		t.Fatalf("LoadGemini returned the DeepSeek row (%s): its key would become the Gemini credential", g.Provider)
	}
	if want := oldestGeminiRow(t, tx); g.ConfigID != want {
		t.Errorf("LoadGemini = config %d, want the oldest Gemini row %d", g.ConfigID, want)
	}
}
