package rag

// Offline retrieval eval harness — a measurement tool, not a pass/fail test.
//
// Usage:
//   DATABASE_URL=postgres://... RAG_EVAL_USER=1 RAG_EVAL_FILE=eval.json \
//     go test ./internal/rag/ -run TestRetrievalEval -v
//
// eval.json holds {"queries":[{"query":"...","expect":[doc_id,...]}]} (a bare
// array also works). Without GEMINI_API_KEY the dense leg is skipped and only
// lexical/trigram/fused-over-those are measured. Scores never fail the test;
// recall@5, recall@10 and MRR@10 are printed per leg so the same file can be
// run before and after a retrieval change.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
)

type evalCase struct {
	Query  string  `json:"query"`
	Expect []int32 `json:"expect"`
}

type evalRun struct {
	name string
	top  func(ctx context.Context, query string, k int) ([]int32, error)
}

func mustEval(err error, t *testing.T) {
	if err == nil {
		return
	}
	t.Fatal(err)
}

func loadEvalCases(t *testing.T, path string) []evalCase {
	raw, err := os.ReadFile(path)
	mustEval(err, t)
	var doc struct {
		Queries []evalCase `json:"queries"`
	}
	if err := json.Unmarshal(raw, &doc); err == nil && len(doc.Queries) > 0 {
		return doc.Queries
	}
	var arr []evalCase
	mustEval(json.Unmarshal(raw, &arr), t)
	return arr
}

func containsID(ids []int32, id int32) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// chunkIDs — doc ids in rank order, de-duplicated: several chunks can belong to
// one document and the eval labels documents, not chunks.
func chunkIDs(chunks []SearchChunk) []int32 {
	seen := make(map[int32]bool)
	out := make([]int32, 0, len(chunks))
	for _, c := range chunks {
		if seen[c.DocID] {
			continue
		}
		seen[c.DocID] = true
		out = append(out, c.DocID)
	}
	return out
}

func evalLeg(t *testing.T, cases []evalCase, run evalRun, ctx context.Context) {
	t.Helper()
	hits5, hits10 := 0, 0
	mrr := 0.0
	for _, c := range cases {
		ids, err := run.top(ctx, c.Query, 10)
		if err == nil {
		} else {
			t.Logf("  %s: query failed: %v", run.name, err)
			continue
		}
		best := 0
		for i, id := range ids {
			if containsID(c.Expect, id) {
				best = i + 1
				break
			}
		}
		if best > 0 && best <= 5 {
			hits5++
		}
		if best > 0 {
			hits10++
			mrr += 1.0 / float64(best)
		} else {
			top := ids
			if len(top) > 5 {
				top = top[:5]
			}
			t.Logf("  %s MISS %q expected=%v top5=%v", run.name, c.Query, c.Expect, top)
		}
	}
	n := len(cases)
	if n == 0 {
		return
	}
	t.Logf("LEG %-8s recall@5 %d/%d (%d%%)  recall@10 %d/%d  MRR@10 %.3f",
		run.name, hits5, n, 100*hits5/n, hits10, n, mrr/float64(n))
}

func TestRetrievalEval(t *testing.T) {
	path := os.Getenv("RAG_EVAL_FILE")
	if path == "" {
		t.Skip("RAG_EVAL_FILE not set — offline retrieval eval harness")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — the eval needs an indexed knowledge base")
	}
	var userID int32 = 1
	if v := os.Getenv("RAG_EVAL_USER"); v == "" {
	} else {
		parsed, err := strconv.ParseInt(v, 10, 32)
		mustEval(err, t)
		userID = int32(parsed)
	}
	cases := loadEvalCases(t, path)
	if len(cases) == 0 {
		t.Fatal("eval file has no cases")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	mustEval(err, t)
	defer pool.Close()

	svc := &Service{DB: pool, Gemini: gemini.New(os.Getenv("GEMINI_API_KEY"), "", 0), Logger: slog.Default()}
	legs := []evalRun{
		{name: "lexical", top: func(ctx context.Context, q string, k int) ([]int32, error) {
			rows, lerr := svc.searchLexical(ctx, userID, q, int64(k))
			return chunkIDs(rows), lerr
		}},
		{name: "trigram", top: func(ctx context.Context, q string, k int) ([]int32, error) {
			rows, terr := svc.searchTrigram(ctx, userID, q, int64(k))
			return chunkIDs(rows), terr
		}},
	}
	if svc.Gemini.IsConfigured() {
		legs = append(legs, evalRun{name: "dense", top: func(ctx context.Context, q string, k int) ([]int32, error) {
			vec, verr := svc.Gemini.GenerateQueryEmbedding(ctx, q)
			if verr == nil {
			} else {
				return nil, verr
			}
			rows, derr := svc.searchDense(ctx, userID, gemini.FormatVector(vec), int64(k))
			return chunkIDs(rows), derr
		}})
	} else {
		t.Log("GEMINI_API_KEY not set: dense leg skipped")
	}
	legs = append(legs, evalRun{name: "fused", top: func(ctx context.Context, q string, k int) ([]int32, error) {
		srcs, serr := svc.Search(ctx, userID, q, int64(k))
		ids := make([]int32, 0, len(srcs))
		for _, src := range srcs {
			ids = append(ids, src.DocID)
		}
		return ids, serr
	}})
	t.Logf("eval: %d queries, user %d", len(cases), userID)
	for _, leg := range legs {
		evalLeg(t, cases, leg, ctx)
	}
}
