package main

// Retrieval primitives for embedcmp: the read-only corpus load, the shared
// cosine ranking, and the READ-ONLY guard.
//
// ─────────────────────────────────────────────────────────────────────────────
// READ-ONLY CONTRACT — embedcmp MUST NOT modify the database. Ever.
// ─────────────────────────────────────────────────────────────────────────────
//
// This is a measurement harness, and it runs against a REAL tenant's production
// knowledge base (65 documents / 615 chunks for the recorded baseline). A write
// here would corrupt the very corpus the measurement depends on:
//
//   - Path B needs Vertex document vectors. They are computed IN MEMORY on
//     purpose. Storing them would overwrite knowledge_chunks.embedding — the
//     AI Studio + RETRIEVAL_DOCUMENT vectors that path A is defined by — so a
//     single UPDATE would destroy the "before" picture and make the comparison
//     unrepeatable;
//   - and it would change what production retrieves *while* the experiment
//     runs, so the numbers would describe a corpus that no longer exists.
//
// Three independent layers enforce the contract, so no single mistake writes:
//
//  1. Documentation: this comment, plus a notice printed in every run header.
//  2. A mechanical guard: every statement THIS TOOL composes goes through
//     selectQuery, which refuses — before the string reaches Postgres —
//     anything it cannot prove is a single SELECT (assertSelectOnly). The
//     guard is deliberately conservative: it understands no string literals,
//     so a literal containing ';' is refused rather than risked. That is
//     acceptable because embedcmp composes its own parameterised SQL and never
//     embeds a literal. (gemini.LoadDefaultConfig issues one further SELECT for
//     the API key; it is the only statement in the process not composed here,
//     and it is a SELECT in that package.)
//  3. The connection itself: default_transaction_read_only=on is set as a
//     startup parameter, so Postgres itself rejects any write that somehow
//     slipped past the guard, with "cannot execute … in a read-only
//     transaction". This layer holds even for statements this tool does not
//     compose — including the one in layer 2's parenthesis.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// corpusChunk is one knowledge_chunks row joined to its document, plus the two
// document vectors the two paths compare:
//
//	stored — the vector already in the column. By construction it is the
//	         production artifact: AI Studio, task-conditioned with
//	         RETRIEVAL_DOCUMENT, gemini-embedding-001, 768 dims. Path A uses it
//	         exactly as production does, so path A is production retrieval.
//	vertex — recomputed this run from the same text via the Vertex transport,
//	         which sends no task field at all. Path B uses it.
//
// Both fields hold the SAME chunk of the SAME text. That is what makes the
// comparison a measurement of the embedding convention and nothing else.
type corpusChunk struct {
	chunkID int32
	docID   int32
	title   string
	content string
	stored  []float32
	vertex  []float32
}

// corpusStats describes the retrieval corpus actually loaded, so a surprising
// result can be traced back to a surprising corpus instead of blamed on the
// embedding convention. Every counter here is a way the measured corpus can
// differ from "the tenant's knowledge base".
type corpusStats struct {
	Chunks        int // chunks in the dense candidate set (vector present)
	Docs          int // distinct documents those chunks belong to
	NoVector      int // chunks in that set with embedding IS NULL — skipped
	DocsReady     int // documents of this tenant that dense search can see
	DocsTotal     int // all documents of this tenant
	DocsNotReady  int // index_status <> 'ready' — invisible to dense search
	DocsOtherCell int // embedding_model <> gemini-embedding-001 — ditto
}

// corpusSQL loads the dense candidate set: exactly the rows production's
// searchDense can rank, minus its similarity floor and its LIMIT.
//
// The WHERE clause is copied from rag.searchDense on purpose — uploaded_by =
// tenant, index_status = 'ready', embedding_model = gemini-embedding-001 — so
// both paths search the same corpus production searches. Widening it (say, by
// dropping the embedding_model check) would let documents into the ranking that
// the live service cannot retrieve, inflating both paths equally but making the
// absolute numbers describe a system nobody runs.
//
// ORDER BY chunk_id makes the load deterministic, which in turn makes the
// tie-break in rankChunks deterministic across runs.
//
// The vector is read as text (embedding::text → "[0.1,0.2,…]") rather than
// through a pgvector type: the module has no pgvector dependency, and the text
// form is the value pgvector stores, bit for bit.
const corpusSQL = `SELECT kc.chunk_id, kc.doc_id, kd.title, kc.content, kc.embedding::text
FROM knowledge_chunks kc
JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id
WHERE kd.uploaded_by = $1
  AND kd.index_status = 'ready'
  AND kd.embedding_model = $2
  AND kc.embedding IS NOT NULL
ORDER BY kc.chunk_id`

// corpusStatsSQL counts the same set plus the ways it can be smaller than the
// tenant's document list. Kept separate from corpusSQL so the row query stays a
// plain scan and the diagnostics can never fail the measurement.
const corpusStatsSQL = `SELECT
  count(*)::int,
  count(DISTINCT kc.doc_id)::int,
  count(*) FILTER (WHERE kc.embedding IS NULL)::int
FROM knowledge_chunks kc
JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id
WHERE kd.uploaded_by = $1
  AND kd.index_status = 'ready'
  AND kd.embedding_model = $2`

// documentStatsSQL explains a corpus that is smaller than expected: documents
// that exist for the tenant but that dense search cannot reach.
const documentStatsSQL = `SELECT
  count(*)::int,
  count(*) FILTER (WHERE kd.index_status = 'ready')::int,
  count(*) FILTER (WHERE kd.index_status = 'ready' AND kd.embedding_model <> $2)::int
FROM knowledge_documents kd
WHERE kd.uploaded_by = $1`

// assertSelectOnly rejects any statement that is not exactly one SELECT.
//
// It is the mechanical half of the READ-ONLY CONTRACT at the top of this file.
// The value is that it fails at the point a future edit introduces a write,
// with the offending SQL in the message, rather than at the point a customer's
// knowledge base has been overwritten.
func assertSelectOnly(sql string) error {
	code := stripSQLComments(sql)
	trimmed := strings.TrimSpace(code)
	if trimmed == "" {
		return errors.New("refusing to run an empty statement")
	}
	// One statement per call. A trailing semicolon is fine; anything after one
	// is not — `SELECT 1; DELETE FROM knowledge_chunks` must never be accepted
	// just because it starts with SELECT.
	body := strings.TrimSuffix(trimmed, ";")
	if strings.Contains(body, ";") {
		return fmt.Errorf("refusing to run more than one statement: %s", oneLine(sql, 80))
	}
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return errors.New("refusing to run an empty statement")
	}
	if !strings.EqualFold(fields[0], "SELECT") {
		return fmt.Errorf("refusing to run a %s statement — embedcmp is READ-ONLY and may only SELECT: %s",
			strings.ToUpper(fields[0]), oneLine(sql, 80))
	}
	return nil
}

// stripSQLComments removes -- and /* */ comments, replacing each with a space
// so two tokens separated by a comment never fuse into one.
//
// It is not a SQL lexer and does not try to be: it does not respect string
// literals. See the READ-ONLY CONTRACT — embedcmp composes parameterised SQL
// with no literals, so the strictness costs nothing and removes a class of
// "the guard was fooled by a quoted semicolon" bugs.
func stripSQLComments(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case strings.HasPrefix(sql[i:], "/*"):
			i += 2
			for i < len(sql) && !strings.HasPrefix(sql[i:], "*/") {
				i++
			}
			i += 2 // past the closing marker, or past the end
			b.WriteByte(' ')
		default:
			b.WriteByte(sql[i])
			i++
		}
	}
	return b.String()
}

// selectQuery is the ONLY way this tool talks to the database. It guards, then
// queries. Nothing else in embedcmp may call pool.Query directly.
func selectQuery(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (pgx.Rows, error) {
	if err := assertSelectOnly(sql); err != nil {
		return nil, err
	}
	return pool.Query(ctx, sql, args...)
}

// selectRow is selectQuery for a single row: same guard, same rule.
func selectRow(ctx context.Context, pool *pgxpool.Pool, sql string, args []any, dest ...any) error {
	if err := assertSelectOnly(sql); err != nil {
		return err
	}
	return pool.QueryRow(ctx, sql, args...).Scan(dest...)
}

// loadCorpus reads the tenant's dense candidate set and its diagnostics.
func loadCorpus(ctx context.Context, pool *pgxpool.Pool, tenant int32, embeddingModel string) ([]corpusChunk, corpusStats, error) {
	var st corpusStats

	rows, err := selectQuery(ctx, pool, corpusSQL, tenant, embeddingModel)
	if err != nil {
		return nil, st, fmt.Errorf("load corpus: %w", err)
	}
	out := make([]corpusChunk, 0, 1024)
	for rows.Next() {
		var c corpusChunk
		var vecText string
		if err := rows.Scan(&c.chunkID, &c.docID, &c.title, &c.content, &vecText); err != nil {
			rows.Close()
			return nil, st, fmt.Errorf("load corpus: scan: %w", err)
		}
		vec, err := parseVectorLiteral(vecText)
		if err != nil {
			rows.Close()
			return nil, st, fmt.Errorf("load corpus: chunk %d: %w", c.chunkID, err)
		}
		c.stored = vec
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, st, fmt.Errorf("load corpus: %w", err)
	}

	// Diagnostics are best-effort: a failure here must not lose the measurement,
	// so each error only blanks its own counters.
	if err := selectRow(ctx, pool, corpusStatsSQL, []any{tenant, embeddingModel},
		&st.Chunks, &st.Docs, &st.NoVector); err != nil {
		st.Chunks, st.Docs, st.NoVector = len(out), 0, 0
	}
	if err := selectRow(ctx, pool, documentStatsSQL, []any{tenant, embeddingModel},
		&st.DocsTotal, &st.DocsReady, &st.DocsOtherCell); err != nil {
		st.DocsTotal, st.DocsReady, st.DocsOtherCell = 0, 0, 0
	}
	st.DocsNotReady = st.DocsTotal - st.DocsReady
	if st.Chunks == 0 {
		st.Chunks = len(out)
	}
	return out, st, nil
}

// parseVectorLiteral parses pgvector's text form: "[1,2,3]" (optionally with
// spaces, as produced by some clients).
//
// A malformed or empty value is an ERROR rather than a zero vector: silently
// scoring a chunk at similarity 0 would look exactly like a retrieval failure
// caused by the embedding convention, which is the one thing this tool must not
// fake.
func parseVectorLiteral(s string) ([]float32, error) {
	trimmed := strings.TrimSpace(s)
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	if strings.TrimSpace(trimmed) == "" {
		return nil, errors.New("empty vector literal")
	}
	parts := strings.Split(trimmed, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, fmt.Errorf("vector element %d: %w", i, err)
		}
		out[i] = float32(f)
	}
	return out, nil
}

// rankedChunk is one corpus row with its similarity to one query vector.
type rankedChunk struct {
	ChunkID int32
	DocID   int32
	Sim     float64
}

// cosine is the similarity pgvector's `<=>` measures as 1 - distance, computed
// here so BOTH paths are ranked by the same code.
//
// WHY IN GO AND NOT IN SQL: path B's document vectors do not exist in the
// database and must not be written there (see the READ-ONLY CONTRACT). Ranking
// path A through `ORDER BY kc.embedding <=> $1` and path B through this
// function would compare two ranking implementations as well as two embedding
// conventions — and any difference could then be a bug in one of them rather
// than a quality loss. One implementation, two vector sets, one variable.
//
// Accumulation is float64 over float32 inputs, while pgvector computes in
// float4. The two can disagree only for pairs whose similarities differ by
// ~1e-7, i.e. pairs already tied.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// rankChunks scores every corpus chunk against one query vector and returns
// them best-first. vecOf selects the path's document vectors, so path A and
// path B differ only in the slice they read from.
//
// Ties break by ascending chunk_id. Exact cosine ties do happen (duplicate
// chunks are common in a real KB), and a non-deterministic order there would
// make two runs of the same experiment disagree.
func rankChunks(query []float32, corpus []corpusChunk, vecOf func(corpusChunk) []float32) []rankedChunk {
	out := make([]rankedChunk, 0, len(corpus))
	for _, c := range corpus {
		out = append(out, rankedChunk{ChunkID: c.chunkID, DocID: c.docID, Sim: cosine(query, vecOf(c))})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sim != out[j].Sim {
			return out[i].Sim > out[j].Sim
		}
		return out[i].ChunkID < out[j].ChunkID
	})
	return out
}

// docRanking collapses a chunk ranking into a document ranking, keeping the
// order in which each document FIRST appears.
//
// This is production's semantics (rag.chunkDocIDs): a document competes with
// its best chunk, and a second chunk of the same document does not take up a
// second slot. Measuring recall on documents instead would let one long
// document occupy several top-5 positions and hide the others.
func docRanking(ranked []rankedChunk) []int32 {
	seen := make(map[int32]bool, len(ranked))
	out := make([]int32, 0, len(ranked))
	for _, r := range ranked {
		if seen[r.DocID] {
			continue
		}
		seen[r.DocID] = true
		out = append(out, r.DocID)
	}
	return out
}
