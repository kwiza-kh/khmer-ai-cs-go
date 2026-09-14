package rag

// DB-backed check for the segmented lexical path (migration 058). Gated by
// RAG_TEST_DSN, mirroring how internal/sqlcheck is gated by DATABASE_URL. It
// creates a throwaway schema with minimal tables (no pgvector needed) and
// drops it afterwards.
//
//RAG_TEST_DSN='postgres://postgres@localhost:5432/postgres' \
//  go test ./internal/rag/ -run TestSegmentedLexical -v

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const segTestSchema = "rag_seg_test"

func TestSegmentedLexicalAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("RAG_TEST_DSN")
	if dsn == "" {
		t.Skip("RAG_TEST_DSN not set — DB-backed segmented lexical check")
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, dsn)
	mustEval(err, t)
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+segTestSchema+" CASCADE")
	mustEval(err, t)
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+segTestSchema)
	mustEval(err, t)
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+segTestSchema+" CASCADE")

	cfg, perr := pgxpool.ParseConfig(dsn)
	mustEval(perr, t)
	cfg.ConnConfig.RuntimeParams["search_path"] = segTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	mustEval(err, t)
	defer pool.Close()

	for _, stmt := range []string{
		"CREATE TABLE knowledge_documents (doc_id int PRIMARY KEY, title text NOT NULL DEFAULT '', uploaded_by int NOT NULL, index_status text NOT NULL DEFAULT 'ready')",
		"CREATE TABLE knowledge_chunks (chunk_id serial PRIMARY KEY, doc_id int NOT NULL, content text NOT NULL, content_seg text, content_tsv tsvector)",
		"CREATE INDEX seg_test_tsv ON knowledge_chunks USING GIN (content_tsv)",
	} {
		_, err = pool.Exec(ctx, stmt)
		mustEval(err, t)
	}
	_, err = pool.Exec(ctx,
		"INSERT INTO knowledge_documents (doc_id, title, uploaded_by) VALUES (1, 'price list', 7), (2, 'refund policy', 7)")
	mustEval(err, t)
	insertSegmented := func(docID int32, content string) {
		seg := SegmentForSearch(content)
		_, ierr := pool.Exec(ctx,
			"INSERT INTO knowledge_chunks (doc_id, content, content_seg, content_tsv) VALUES ($1, $2, $3, to_tsvector('simple', $3))",
			docID, content, seg)
		mustEval(ierr, t)
	}
	insertSegmented(1, "តម្លៃផលិតផល ១២៣")
	insertSegmented(2, "refund policy details")
	// Legacy row: content_tsv NULL, exercises the fallback branch.
	_, err = pool.Exec(ctx, "INSERT INTO knowledge_chunks (doc_id, content) VALUES (2, 'refund policy details')")
	mustEval(err, t)

	svc := &Service{DB: pool, Logger: slog.Default()}

	khmer, err := svc.searchLexical(ctx, 7, "តម្លៃ", 5)
	mustEval(err, t)
	if len(khmer) == 0 {
		t.Fatalf("segmented lexical returned nothing for a Khmer query")
	}
	if khmer[0].DocID == 1 {
	} else {
		t.Fatalf("Khmer query top hit doc %d, want 1", khmer[0].DocID)
	}

	withDigits, err := svc.searchLexical(ctx, 7, "តម្លៃ ១២៣", 5)
	mustEval(err, t)
	if len(withDigits) == 0 {
		t.Fatalf("Khmer+Khmer-digit query returned nothing")
	}

	english, err := svc.searchLexical(ctx, 7, "refund", 5)
	mustEval(err, t)
	if len(english) == 0 {
		t.Fatalf("legacy English row not reachable through the fallback branch")
	}
	if english[0].DocID == 2 {
	} else {
		t.Fatalf("English query top hit doc %d, want 2", english[0].DocID)
	}

	trigram, err := svc.searchTrigram(ctx, 7, "តម្លៃ", 5)
	mustEval(err, t)
	if len(trigram) == 0 {
		t.Fatalf("trigram fallback returned nothing — SQL operator regression")
	}
}
