package api

// The AI Studio key fallback behind the Gemini model list: it runs when the console asks
// for a catalog for a config that has no key of its own, and it must read a GEMINI row.
//
// After a provider switch the default row is a Claude row, and an Anthropic key is not an
// AI Studio credential — sending it to generativelanguage.googleapis.com answers 401, which
// the operator would read as "this region is broken".
//
// DB-gated like the other tests that need real rows: probe rows are committed under unique
// names, removed again, and the previous default row is restored.

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStudioKeyFallbackNeverReturnsAClaudeKey(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — this test needs a real migrated database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("DATABASE_URL unparseable, skipping: %v", err)
	}
	t.Cleanup(pool.Close)

	// Remember which rows are default so the probe can put them back.
	rows, err := pool.Query(ctx, "SELECT config_id FROM model_configs WHERE is_default = true")
	if err != nil {
		t.Skipf("model_configs unreadable, skipping: %v", err)
	}
	var wasDefault []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err == nil {
			wasDefault = append(wasDefault, id)
		}
	}
	rows.Close()
	if _, err := pool.Exec(ctx, "UPDATE model_configs SET is_default = false"); err != nil {
		t.Fatalf("clear defaults: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	insert := func(name, provider, key string, isDefault bool) int32 {
		t.Helper()
		var id int32
		if err := pool.QueryRow(ctx,
			"INSERT INTO model_configs (name, provider, model_name, api_key, is_default) VALUES ($1,$2,'probe-model',$3,$4) RETURNING config_id",
			name, provider, key, isDefault).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
		return id
	}
	geminiWithKey := insert("__key_gemini_"+suffix, "gemini", "sealed-gemini-fallback", false)
	keylessGemini := insert("__key_less_"+suffix, "gemini", "", false)
	claudeDefault := insert("__key_claude_"+suffix, "anthropic", "sealed-claude-fallback", true)

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = pool.Exec(c, "DELETE FROM model_configs WHERE config_id = ANY($1::int[])",
			[]int32{geminiWithKey, keylessGemini, claudeDefault})
		if len(wasDefault) > 0 {
			_, _ = pool.Exec(c, "UPDATE model_configs SET is_default = true WHERE config_id = ANY($1::int[])", wasDefault)
		}
	})

	// The keyless row is what triggers the fallback: its own column is empty.
	got := studioAPIKeyFromDB(ctx, pool, keylessGemini)
	if got == "sealed-claude-fallback" {
		t.Fatal("the fallback returned the default row's Anthropic key: it cannot list Gemini's models")
	}
	if got == "" {
		t.Fatal("the fallback found no key although a Gemini row holds one — the console would report 未设置 API Key")
	}
}
