package rag

// Throughput check for the migration-058 backfill on a larger table. Gated by
// RAG_TEST_DSN like the other DB tests; creates and drops its own schema.
//
//RAG_TEST_DSN='postgres://...' go test ./internal/rag/ -run TestBackfillThroughput -v

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const backfillTestSchema = "rag_backfill_perf"

func TestBackfillThroughput(t *testing.T) {
	dsn := os.Getenv("RAG_TEST_DSN")
	if dsn == "" {
		t.Skip("RAG_TEST_DSN not set — backfill throughput check")
	}
	const rows = 2000
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, dsn)
	mustEval(err, t)
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+backfillTestSchema+" CASCADE")
	mustEval(err, t)
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+backfillTestSchema)
	mustEval(err, t)
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+backfillTestSchema+" CASCADE")

	cfg, perr := pgxpool.ParseConfig(dsn)
	mustEval(perr, t)
	cfg.ConnConfig.RuntimeParams["search_path"] = backfillTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	mustEval(err, t)
	defer pool.Close()

	_, err = pool.Exec(ctx, "CREATE TABLE knowledge_chunks (chunk_id serial PRIMARY KEY, content text NOT NULL, content_seg text, content_tsv tsvector)")
	mustEval(err, t)
	chunk := "ម៉ាស៊ីនចម្រោះទឹក RO តម្លៃ 189 ដុល្លារ ធានា 12 ខែ សម្រាប់គ្រួសារ 3 ទៅ 5 នាក់ "
	batch := &pgx.Batch{}
	for i := 0; i < rows; i++ {
		batch.Queue("INSERT INTO knowledge_chunks (content) VALUES ($1)", fmt.Sprintf("%s%d", chunk, i))
	}
	mustEval(pool.SendBatch(ctx, batch).Close(), t)

	svc := &Service{DB: pool, Logger: slog.Default()}
	start := time.Now()
	svc.backfillSegmentedChunks(ctx)
	elapsed := time.Since(start)

	var remaining int
	mustEval(pool.QueryRow(ctx, "SELECT count(*) FROM knowledge_chunks WHERE content_tsv IS NULL").Scan(&remaining), t)
	if remaining == 0 {
	} else {
		t.Fatalf("backfill left %d rows unsegmented", remaining)
	}
	t.Logf("backfill: %d rows in %s (%.0f rows/s)", rows, elapsed.Round(time.Millisecond), float64(rows)/elapsed.Seconds())
}
