// Package rag — document ingestion queue, embedding, pgvector hybrid search
// and grounding (port of the Rust service.rs; itself a port of the Go one).
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
)

const (
	DefaultChunkSize   = 1000
	DefaultChunkOverlap = 200
	DefaultTopK        = int64(5)

	indexWorkerCount     = 2
	searchCandidateFactor = int64(4)
	maxSearchCandidates   = int64(40)
	rrfRankConstant       = 60.0
	// rerankWindow — how many fused candidates the rerank model sees.
	rerankWindow = 15
)

// Thresholds (env-overridable, identical knobs to the Rust backend).
type thresholds struct {
	floor      float64
	ratio      float64
	rerankMin  float32
	rerankSkip float64
}

var thresholdsOnce sync.Once
var cachedThresholds *thresholds

func currentThresholds() *thresholds {
	thresholdsOnce.Do(func() {
		cachedThresholds = &thresholds{
			floor:      envF64("RAG_SIMILARITY_FLOOR", 0.35),
			ratio:      envF64("RAG_SIMILARITY_RATIO", 0.75),
			rerankMin:  float32(envF64("RAG_RERANK_MIN", 3.0)),
			rerankSkip: envF64("RAG_RERANK_SKIP", 0.70),
		}
	})
	return cachedThresholds
}

func envF64(name string, fallback float64) float64 {
	if v := os.Getenv(name); v != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return fallback
}

// SearchChunk is one candidate chunk from any retrieval path.
type SearchChunk struct {
	ChunkID    int32
	DocID      int32
	Content    string
	Title      string
	Similarity float64
	DenseSim   *float64 // cosine when the chunk came from the vector path
}

// Source is what the chat handler needs from RAG.
type Source struct {
	DocID   int32   `json:"doc_id"`
	Title   string  `json:"title"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// GroundingContext carries retrieval results into prompt construction.
type GroundingContext struct {
	Sources    []Source
	ContextStr string
	HasMatch   bool
}

// Service bundles the RAG dependencies (DB pool + Gemini client).
type Service struct {
	DB     *pgxpool.Pool
	Gemini *gemini.Service
	Logger *slog.Logger
}

func isCJK(c rune) bool {
	return (c >= 0x3400 && c <= 0x4DBF) || (c >= 0x4E00 && c <= 0x9FFF)
}

func hasCJK(s string) bool {
	for _, c := range s {
		if isCJK(c) {
			return true
		}
	}
	return false
}

// cjkBigrams — character bigrams from the CJK runs of the query (lexical unit
// for ILIKE recall; tsquery('simple') cannot segment Chinese).
func cjkBigrams(s string, capN int) []string {
	var runes []rune
	for _, c := range s {
		if isCJK(c) {
			runes = append(runes, c)
		}
	}
	seen := make(map[string]bool)
	var out []string
	for i := 0; i+1 < len(runes); i++ {
		b := string(runes[i : i+2])
		if seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
		if len(out) >= capN {
			break
		}
	}
	return out
}

func isSentenceEnd(c rune) bool {
	switch c {
	case '.', '!', '?', ';', '\n', '。', '！', '？', '；', '។', '៕':
		return true
	}
	return false
}

// ============================================
// Indexing workers
// ============================================

// SpawnIndexWorkers recovers interrupted jobs (indexing → pending), then runs
// the polling workers + the URL freshness sweep. Port of the Rust scheduler.
func (s *Service) SpawnIndexWorkers(ctx context.Context) {
	go func() {
		_, _ = s.DB.Exec(ctx,
			"UPDATE knowledge_documents SET index_status = 'pending', index_error = '' WHERE index_status = 'indexing'")
		// Vectors from a different embedding model are not comparable with
		// today's query vectors — re-embed those docs with the current model.
		_, _ = s.DB.Exec(ctx,
			"UPDATE knowledge_documents SET index_status = 'pending' "+
				"WHERE index_status = 'ready' AND embedding_model <> '' AND embedding_model <> $1",
			gemini.EmbeddingModel)
	}()
	for i := 0; i < indexWorkerCount; i++ {
		go func() {
			for {
				s.indexNextPending(ctx)
				select {
				case <-ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
				}
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.refreshStaleURLDocs(ctx); err != nil {
					s.Logger.Warn("url freshness sweep failed", "error", err.Error())
				}
			}
		}
	}()
}

// refreshStaleURLDocs re-fetches URL-derived documents older than 24h.
func (s *Service) refreshStaleURLDocs(ctx context.Context) error {
	rows, err := s.DB.Query(ctx,
		"SELECT doc_id, COALESCE(source_url, ''), COALESCE(content, '') FROM knowledge_documents "+
			"WHERE source = 'url' AND source_url IS NOT NULL AND source_url <> '' "+
			"AND index_status = 'ready' AND updated_at < NOW() - INTERVAL '24 hours' "+
			"ORDER BY updated_at ASC LIMIT 20")
	if err != nil {
		return fmt.Errorf("sweep query: %w", err)
	}
	defer rows.Close()
	type doc struct {
		id         int32
		url        string
		oldContent string
	}
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.id, &d.url, &d.oldContent); err != nil {
			return err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range docs {
		_, newText, err := FetchURLContent(ctx, d.url)
		if err == nil && strings.TrimSpace(newText) != strings.TrimSpace(d.oldContent) && strings.TrimSpace(newText) != "" {
			_, _ = s.DB.Exec(ctx,
				"UPDATE knowledge_documents SET content = $1, index_status = 'pending', index_error = '', updated_at = NOW() WHERE doc_id = $2",
				newText, d.id)
			s.Logger.Info("url doc changed; re-index queued", "doc_id", d.id)
		} else {
			_, _ = s.DB.Exec(ctx, "UPDATE knowledge_documents SET updated_at = NOW() WHERE doc_id = $1", d.id)
		}
	}
	return nil
}

// indexNextPending claims and indexes the oldest pending document (if any).
func (s *Service) indexNextPending(ctx context.Context) bool {
	var docID int32
	var content string
	err := s.DB.QueryRow(ctx,
		"UPDATE knowledge_documents SET index_status = 'indexing', index_error = '' "+
			"WHERE doc_id = (SELECT doc_id FROM knowledge_documents "+
			"WHERE index_status = 'pending' ORDER BY created_at ASC LIMIT 1) "+
			"RETURNING doc_id, content").Scan(&docID, &content)
	if err != nil {
		return false
	}
	if err := s.indexDocument(ctx, docID, content); err != nil {
		s.Logger.Warn("knowledge document indexing failed", "error", err.Error())
		s.markDocumentFailed(ctx, docID, err.Error())
	}
	return true
}

func (s *Service) indexDocument(ctx context.Context, docID int32, content string) error {
	chunks := ChunkMarkdown(content)
	embeddings, err := s.Gemini.GenerateEmbeddings(ctx, chunks)
	if err != nil {
		return fmt.Errorf("embed chunks: %w", err)
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "DELETE FROM knowledge_chunks WHERE doc_id = $1", docID); err != nil {
		return fmt.Errorf("clear chunks: %w", err)
	}
	now := time.Now()
	// Pipelined batch insert — one network round trip for all chunks.
	batch := &pgx.Batch{}
	for i := range chunks {
		batch.Queue(
			"INSERT INTO knowledge_chunks (doc_id, chunk_index, content, embedding, created_at) VALUES ($1, $2, $3, $4::vector, $5)",
			docID, int32(i), chunks[i], gemini.FormatVector(embeddings[i]), now)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("insert chunks: %w", err)
	}
	if _, err := tx.Exec(ctx,
		"UPDATE knowledge_documents SET chunk_count = $1, embedding_model = $2, index_status = 'ready', index_error = '', last_embedded_at = $3 WHERE doc_id = $4",
		len(chunks), gemini.EmbeddingModel, now, docID); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("tx commit: %w", err)
	}
	s.Logger.Info("knowledge document indexed", "doc_id", docID, "chunks", len(chunks))
	return nil
}

func (s *Service) markDocumentFailed(ctx context.Context, docID int32, message string) {
	msg := truncateRunes(message, 500)
	if msg == "" {
		msg = "indexing failed"
	}
	_, _ = s.DB.Exec(ctx,
		"UPDATE knowledge_documents SET index_status = 'failed', index_error = $1, chunk_count = 0 WHERE doc_id = $2",
		msg, docID)
}

// UploadDocument creates a document (pending) and lets the worker index it.
// sourceURL marks the document as URL-derived (source='url').
func (s *Service) UploadDocument(ctx context.Context, userID int32, title, content, language, category string, tags []string, sourceURL *string) (map[string]any, error) {
	// Auto-detect the document script when no language was chosen.
	lang := language
	if lang == "" {
		if det := gemini.DetectLanguage(content); det != "" {
			lang = det
		} else {
			lang = "km"
		}
	}
	source := "manual"
	if sourceURL != nil {
		source = "url"
	}
	row := s.DB.QueryRow(ctx,
		"INSERT INTO knowledge_documents (title, content, language, category, tags, uploaded_by, index_status, source, source_url) "+
			"VALUES ($1, $2, $3, $4, $5::text[], $6, 'pending', $7, $8) "+
			"RETURNING doc_id, title, language, category, chunk_count, source, index_status, index_error, created_at, updated_at",
		title, content, lang, category, tags, userID, source, sourceURL)
	doc, err := scanDocumentRow(row)
	if err != nil {
		return nil, err
	}
	// Non-blocking near-duplicate warning so the operator can dedupe instead
	// of hosting contradictory copies.
	if similar := s.findSimilarDocs(ctx, userID, title+" "+truncateRunes(content, 1000)); len(similar) > 0 {
		doc["similar_docs"] = similar
	}
	return doc, nil
}

// findSimilarDocs — dense search with a strict floor, used to warn about
// near-duplicate uploads before they fragment the knowledge base.
func (s *Service) findSimilarDocs(ctx context.Context, userID int32, sample string) []Source {
	vec, err := s.Gemini.GenerateQueryEmbedding(ctx, sample)
	if err != nil {
		return nil
	}
	rows, err := s.DB.Query(ctx,
		"SELECT kd.doc_id, kd.title, 1 - (kc.embedding <=> $1::vector) AS similarity "+
			"FROM knowledge_chunks kc "+
			"JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id "+
			"WHERE kd.uploaded_by = $2 AND kd.index_status = 'ready' AND kd.embedding_model = $3 "+
			"AND 1 - (kc.embedding <=> $1::vector) > 0.88 "+
			"ORDER BY kc.embedding <=> $1::vector LIMIT 3",
		gemini.FormatVector(vec), userID, gemini.EmbeddingModel)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]Source, 0)
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.DocID, &src.Title, &src.Score); err == nil {
			out = append(out, src)
		}
	}
	return out
}

// ============================================
// 检索 (dense + lexical + trigram → RRF fusion)
// ============================================

// Search — hybrid retrieval with RRF fusion, relative-similarity gating and
// LLM rerank; every enhancement degrades gracefully to plain RRF.
func (s *Service) Search(ctx context.Context, userID int32, query string, topK int64) ([]Source, error) {
	if topK <= 0 {
		topK = DefaultTopK
	}
	candidateLimit := searchCandidateLimit(topK)
	if int64(rerankWindow) > candidateLimit {
		candidateLimit = int64(rerankWindow)
	}
	th := currentThresholds()

	queryEmbedding, embErr := s.Gemini.GenerateQueryEmbedding(ctx, query)
	var dense []SearchChunk
	var denseErr error
	if embErr != nil {
		s.Logger.Warn("vector knowledge search failed; retaining lexical results", "error", embErr.Error())
	} else {
		dense, denseErr = s.searchDense(ctx, userID, gemini.FormatVector(queryEmbedding), candidateLimit)
		if denseErr != nil {
			s.Logger.Warn("dense search failed", "error", denseErr.Error())
		}
	}
	var lexical []SearchChunk
	lexicalRows, lexicalErr := s.searchLexical(ctx, userID, query, candidateLimit)
	if lexicalErr != nil {
		s.Logger.Warn("lexical knowledge search failed; retaining vector results", "error", lexicalErr.Error())
	} else {
		lexical = lexicalRows
	}
	var trigram []SearchChunk
	if hasCJK(query) {
		if rows, err := s.searchTrigram(ctx, userID, query, candidateLimit); err == nil {
			trigram = rows
		}
	}
	if denseErr != nil && lexical == nil && len(trigram) == 0 {
		return nil, fmt.Errorf("knowledge search failed")
	}

	fused := fuseSearchResults(dense, lexical, trigram)

	// Rerank-skip agreement: when the very same chunk leads BOTH the dense
	// and the lexical lists, the signals already agree — an extra LLM rerank
	// would add latency without information.
	topAgrees := false
	if len(dense) > 0 && len(lexical) > 0 && dense[0].ChunkID == lexical[0].ChunkID {
		topAgrees = true
	}

	// Relative similarity gate: keep lexical/trigram-only hits; drop dense hits
	// far below the leader.
	var topDense *float64
	for _, c := range fused {
		if c.DenseSim != nil {
			if topDense == nil || *c.DenseSim > *topDense {
				v := *c.DenseSim
				topDense = &v
			}
		}
	}
	if topDense != nil {
		minDense := *topDense * th.ratio
		if th.floor > minDense {
			minDense = th.floor
		}
		kept := fused[:0]
		for _, c := range fused {
			if c.DenseSim == nil || *c.DenseSim >= minDense {
				kept = append(kept, c)
			}
		}
		fused = kept
	}

	// LLM rerank unless the dense leader is already a clear match, or dense
	// and lexical agree on the same leader with a solid score (agreement
	// lowers the skip bar from 0.70 to 0.60).
	leaderClear := topDense != nil && *topDense >= th.rerankSkip
	signalsAgree := topAgrees && topDense != nil && *topDense >= 0.60
	if !leaderClear && !signalsAgree {
		fused = s.rerankCandidates(ctx, query, fused, topK)
	}

	out := make([]Source, 0, len(fused))
	for _, c := range fused {
		out = append(out, Source{DocID: c.DocID, Title: c.Title, Content: c.Content, Score: c.Similarity})
	}
	return out, nil
}

// rerankCache memoizes LLM rerank scores per (query, candidate ids) so
// repeated follow-up questions don't pay for a second rerank call. Entries
// expire after 30 minutes; a plain map under a mutex (not a reassigned
// sync.Map) keeps Load/Store race-free.
type rerankCacheEntry struct {
	scores    []float32
	createdAt time.Time
}

var (
	rerankCacheMu sync.Mutex
	rerankCache   = map[string]rerankCacheEntry{}
)

func rerankCacheKey(query string, chunks []SearchChunk) string {
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		ids[i] = strconv.Itoa(int(c.ChunkID))
	}
	sum := sha256.Sum256([]byte(query + "\x00" + strings.Join(ids, ",")))
	return hex.EncodeToString(sum[:])
}

const rerankCacheTTL = 30 * time.Minute
const rerankCacheMax = 400

func rerankCacheGet(key string) ([]float32, bool) {
	rerankCacheMu.Lock()
	defer rerankCacheMu.Unlock()
	e, ok := rerankCache[key]
	if !ok || time.Since(e.createdAt) >= rerankCacheTTL {
		if ok {
			delete(rerankCache, key)
		}
		return nil, false
	}
	return e.scores, true
}

func rerankCachePut(key string, scores []float32) {
	rerankCacheMu.Lock()
	defer rerankCacheMu.Unlock()
	if len(rerankCache) >= rerankCacheMax {
		// Drop expired entries first; if still full, clear the oldest half by
		// insertion order is not tracked — a full reset keeps the cap simple
		// and the cost is only cache warmth.
		for k, e := range rerankCache {
			if time.Since(e.createdAt) >= rerankCacheTTL {
				delete(rerankCache, k)
			}
		}
		if len(rerankCache) >= rerankCacheMax {
			rerankCache = make(map[string]rerankCacheEntry, rerankCacheMax/2)
		}
	}
	rerankCache[key] = rerankCacheEntry{scores: scores, createdAt: time.Now()}
}

func (s *Service) rerankCandidates(ctx context.Context, query string, chunks []SearchChunk, topK int64) []SearchChunk {
	if int64(len(chunks)) <= topK {
		return chunks
	}
	n := rerankWindow
	if n > len(chunks) {
		n = len(chunks)
	}
	key := rerankCacheKey(query, chunks[:n])
	if scores, ok := rerankCacheGet(key); ok {
		return applyRerankScores(chunks, scores, topK)
	}
	texts := make([]string, n)
	for i := 0; i < n; i++ {
		texts[i] = chunks[i].Content
	}
	scores := s.Gemini.RerankChunks(ctx, query, texts)
	if scores == nil {
		return chunks
	}
	rerankCachePut(key, scores)
	return applyRerankScores(chunks, scores, topK)
}

// applyRerankScores keeps chunks scoring above rerankMin, best first.
func applyRerankScores(chunks []SearchChunk, scores []float32, topK int64) []SearchChunk {
	minScore := currentThresholds().rerankMin
	type scored struct {
		chunk SearchChunk
		score float32
	}
	var paired []scored
	for i := 0; i < len(scores) && i < len(chunks); i++ {
		if scores[i] >= minScore {
			paired = append(paired, scored{chunk: chunks[i], score: scores[i]})
		}
	}
	sort.Slice(paired, func(i, j int) bool { return paired[i].score > paired[j].score })
	out := make([]SearchChunk, 0, len(paired))
	for _, p := range paired {
		out = append(out, p.chunk)
	}
	return out
}

func (s *Service) searchDense(ctx context.Context, userID int32, vector string, limit int64) ([]SearchChunk, error) {
	rows, err := s.DB.Query(ctx,
		"SELECT kc.chunk_id, kc.doc_id, kc.content, kd.title, 1 - (kc.embedding <=> $1::vector) AS similarity "+
			"FROM knowledge_chunks kc "+
			"JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id "+
			"WHERE kd.uploaded_by = $4 AND kd.index_status = 'ready' "+
			"AND kd.embedding_model = $5 "+
			"AND 1 - (kc.embedding <=> $1::vector) > $2 "+
			"ORDER BY kc.embedding <=> $1::vector LIMIT $3",
			vector, currentThresholds().floor, limit, userID, gemini.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()
	var out []SearchChunk
	for rows.Next() {
		var c SearchChunk
		if err := rows.Scan(&c.ChunkID, &c.DocID, &c.Content, &c.Title, &c.Similarity); err != nil {
			return nil, fmt.Errorf("vector search scan: %w", err)
		}
		sim := c.Similarity
		c.DenseSim = &sim
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) searchLexical(ctx context.Context, userID int32, query string, limit int64) ([]SearchChunk, error) {
	rows, err := s.DB.Query(ctx,
		"WITH search_query AS (SELECT websearch_to_tsquery('simple', $1) AS terms) "+
			"SELECT kc.chunk_id, kc.doc_id, kc.content, kd.title, "+
			"ts_rank_cd(to_tsvector('simple', kc.content), sq.terms)::float8 AS similarity "+
			"FROM knowledge_chunks kc "+
			"JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id "+
			"CROSS JOIN search_query sq "+
			"WHERE kd.uploaded_by = $2 AND kd.index_status = 'ready' "+
			"AND to_tsvector('simple', kc.content) @@ sq.terms "+
			"ORDER BY similarity DESC, kc.chunk_id ASC LIMIT $3",
		query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()
	var out []SearchChunk
	for rows.Next() {
		var c SearchChunk
		if err := rows.Scan(&c.ChunkID, &c.DocID, &c.Content, &c.Title, &c.Similarity); err != nil {
			return nil, fmt.Errorf("lexical search scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// searchTrigram — CJK bigram recall via ILIKE (pg_trgm optional).
func (s *Service) searchTrigram(ctx context.Context, userID int32, query string, limit int64) ([]SearchChunk, error) {
	bigrams := cjkBigrams(query, 8)
	if len(bigrams) == 0 {
		return nil, nil
	}
	var conditions, scoreParts []string
	args := make([]any, 0, len(bigrams)+2)
	args = append(args, userID, limit)
	for i := range bigrams {
		p := i + 3 // $1 user, $2 limit, bigrams start at $3
		conditions = append(conditions, fmt.Sprintf("kc.content ILIKE $%d", p))
		scoreParts = append(scoreParts, fmt.Sprintf("(CASE WHEN kc.content ILIKE $%d THEN 1 ELSE 0 END)", p))
		args = append(args, "%"+bigrams[i]+"%")
	}
	sql := fmt.Sprintf(
		"SELECT kc.chunk_id, kc.doc_id, kc.content, kd.title, (%s)::float8 AS similarity "+
			"FROM knowledge_chunks kc "+
			"JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id "+
			"WHERE kd.uploaded_by = $1 AND kd.index_status = 'ready' "+
			"AND (%s) > 0 "+
			"ORDER BY similarity DESC, kc.chunk_id ASC LIMIT $2",
		strings.Join(scoreParts, " + "), strings.Join(conditions, " OR "))
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("trigram search: %w", err)
	}
	defer rows.Close()
	var out []SearchChunk
	for rows.Next() {
		var c SearchChunk
		if err := rows.Scan(&c.ChunkID, &c.DocID, &c.Content, &c.Title, &c.Similarity); err != nil {
			return nil, fmt.Errorf("trigram search scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func searchCandidateLimit(topK int64) int64 {
	limit := topK * searchCandidateFactor
	if limit > maxSearchCandidates {
		return maxSearchCandidates
	}
	return limit
}

// fuseSearchResults — reciprocal-rank fusion across up to three paths.
func fuseSearchResults(dense, lexical, trigram []SearchChunk) []SearchChunk {
	type fusedEntry struct {
		chunk SearchChunk
		rrf   float64
	}
	fused := make(map[int32]*fusedEntry)
	add := func(chunks []SearchChunk) {
		for rank, chunk := range chunks {
			entry, ok := fused[chunk.ChunkID]
			if !ok {
				entry = &fusedEntry{chunk: chunk}
				fused[chunk.ChunkID] = entry
			}
			if chunk.Similarity > entry.chunk.Similarity {
				entry.chunk = chunk
			}
			entry.rrf += 1.0 / (rrfRankConstant + float64(rank) + 1.0)
		}
	}
	add(dense)
	add(lexical)
	add(trigram)

	ranked := make([]*fusedEntry, 0, len(fused))
	for _, e := range fused {
		ranked = append(ranked, e)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].rrf != ranked[j].rrf {
			return ranked[i].rrf > ranked[j].rrf
		}
		if ranked[i].chunk.Similarity != ranked[j].chunk.Similarity {
			return ranked[i].chunk.Similarity > ranked[j].chunk.Similarity
		}
		return ranked[i].chunk.ChunkID < ranked[j].chunk.ChunkID
	})
	out := make([]SearchChunk, 0, len(ranked))
	for _, e := range ranked {
		out = append(out, e.chunk)
	}
	return out
}

// ============================================
// Grounding / Query
// ============================================

// Ground retrieves grounding context for a chat message. RAG failures never
// fail the chat — log and proceed ungrounded.
func (s *Service) Ground(ctx context.Context, userID int32, sessionID *string, message, language string, history []gemini.HistoryItem, topK int64) GroundingContext {
	// Rewrite turns follow-up phrasing into a searchable query; the first
	// turn has nothing to resolve, so skip the extra LLM call.
	effectiveQuery := message
	var rewritten *string
	if len(history) > 0 {
		rewritten = s.Gemini.RewriteSearchQuery(ctx, message, history)
		if rewritten != nil {
			effectiveQuery = *rewritten
		}
	}

	sources, err := s.Search(ctx, userID, effectiveQuery, topK)
	if err != nil {
		s.Logger.Warn("RAG grounding failed; proceeding ungrounded", "error", err.Error())
		s.logRAGQuery(ctx, userID, sessionID, message, rewritten, 0, nil, false)
		return GroundingContext{}
	}
	if len(sources) == 0 {
		s.logRAGQuery(ctx, userID, sessionID, message, rewritten, 0, nil, false)
		return GroundingContext{}
	}
	topScore := float32(sources[0].Score)
	var b strings.Builder
	b.WriteString("📚 Knowledge base references (ground your answer ONLY in these facts):\nNever mention the sources, scores or markers like [Source 1] in your reply — answer naturally.\n\n")
	for i, src := range sources {
		fmt.Fprintf(&b, "--- Source %d: %s (score %.2f) ---\n%s\n\n", i+1, src.Title, src.Score, truncateRunes(src.Content, groundSourceLimit(src.Content)))
	}
	s.logRAGQuery(ctx, userID, sessionID, message, rewritten, len(sources), &topScore, true)
	return GroundingContext{Sources: sources, ContextStr: b.String(), HasMatch: true}
}

// groundSourceLimit — price lists and tables need more room than prose
// (800 runes cuts tables mid-row and the model then hallucinates the rest).
func groundSourceLimit(content string) int {
	for _, line := range strings.Split(content, "\n") {
		if isTableRow(line) {
			return 2000
		}
	}
	return 800
}

func (s *Service) logRAGQuery(ctx context.Context, userID int32, sessionID *string, query string, rewritten *string, hitCount int, topScore *float32, usedInReply bool) {
	_, _ = s.DB.Exec(ctx,
		"INSERT INTO rag_query_logs (user_id, session_id, query, rewritten_query, hit_count, top_score, used_in_reply) VALUES ($1, $2, $3, $4, $5, $6, $7)",
		userID, sessionID, query, rewritten, hitCount, topScore, usedInReply)
}

// KnowledgeGaps — distinct queries that produced no usable grounding over the
// last N days, ranked by hit frequency.
func (s *Service) KnowledgeGaps(ctx context.Context, userID int32, days int64) ([]map[string]any, error) {
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	rows, err := s.DB.Query(ctx,
		"SELECT query, COUNT(*)::bigint AS hits, MAX(top_score) AS top_score, MAX(created_at) AS last_seen "+
			"FROM rag_query_logs "+
			"WHERE user_id = $1 AND NOT used_in_reply AND created_at > NOW() - ($2 || ' days')::interval "+
			"GROUP BY lower(query), query "+
			"ORDER BY hits DESC, last_seen DESC "+
			"LIMIT 12",
		userID, strconv.FormatInt(days, 10))
	if err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var q string
		var hits int64
		var topScore *float32
		var lastSeen time.Time
		if err := rows.Scan(&q, &hits, &topScore, &lastSeen); err != nil {
			return nil, fmt.Errorf("查询失败: %w", err)
		}
		out = append(out, map[string]any{
			"query":     q,
			"hits":      hits,
			"top_score": topScore,
			"last_seen": lastSeen,
		})
	}
	return out, rows.Err()
}

// AugmentMessage builds the augmented prompt for a grounded chat turn.
func AugmentMessage(message string, ctx *GroundingContext) string {
	if !ctx.HasMatch {
		return message
	}
	return ctx.ContextStr + "\n---\n📝 User question: " + message +
		"\n\nAnswer based on the knowledge base above when relevant. If the KB doesn't cover it, say so honestly. " +
		"Never output citation markers like [Source 1] or (Source 1), never mention source names or document titles, " +
		"and never end the reply with a title line — write naturally as customer-facing text."
}

// NoMatchReply — standalone RAG query fallback when nothing clears the
// threshold.
func NoMatchReply(language string) string {
	switch language {
	case "km":
		return "ប្រព័ន្ធមិនអាចរកឃើញព័ត៌មានដែលត្រូវគ្នាក្នុងប្រព័ន្ធចំណេះដឹងរបស់យើងទេ។ សូមព្យាយាមសរសេរសំណួរឲ្យបានច្បាស់លាស់ ឬទាក់ទងភ្នាក់ងារផ្ទាល់។\n\n_(No matching knowledge found — please rephrase or contact a human agent.)_"
	case "zh":
		return "抱歉，知识库中未找到相关内容。请尝试换种问法，或联系人工客服。\n\n_(No matching knowledge found.)_"
	default:
		return "I couldn't find anything relevant in our knowledge base. Could you rephrase your question, or would you like me to connect you with a human agent?\n\n_(No matching knowledge found.)_"
	}
}

// ============================================
// Chunking
// ============================================

// ChunkText — rune-based sliding window with overlap; when a window would cut
// mid-sentence, the end is pulled back to the last sentence terminator within
// the lower half of the window.
// TableChunkSize — atomic budget for a contiguous table block so prices and
// rows never get split across chunks.
const TableChunkSize = 1800

// isTableRow reports whether a line looks like part of a markdown/aligned
// table ("| a | b |" or "col1 | col2").
func isTableRow(line string) bool {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "|") {
		return true
	}
	return len(t) > 8 && strings.Count(t, "|") >= 2
}

type textBlock struct {
	content string
	table   bool
}

// splitTableBlocks segments text into contiguous table blocks (kept atomic)
// and prose blocks.
func splitTableBlocks(text string) []textBlock {
	lines := strings.Split(text, "\n")
	var blocks []textBlock
	var cur []string
	curTable := false
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, textBlock{content: strings.Join(cur, "\n"), table: curTable})
			cur = nil
		}
	}
	for _, line := range lines {
		if t := isTableRow(line); t != curTable {
			flush()
			curTable = t
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}

func ChunkText(text string) []string {
	if len([]rune(text)) <= DefaultChunkSize {
		return []string{text}
	}
	var chunks []string
	for _, blk := range splitTableBlocks(text) {
		if blk.table && len([]rune(blk.content)) <= TableChunkSize {
			chunks = append(chunks, blk.content)
			continue
		}
		chunks = append(chunks, chunkProse(blk.content)...)
	}
	return chunks
}

// chunkProse — sliding window with sentence-boundary cuts (the original
// ChunkText behaviour).
func chunkProse(text string) []string {
	runes := []rune(text)
	if len(runes) <= DefaultChunkSize {
		return []string{text}
	}
	step := DefaultChunkSize - DefaultChunkOverlap
	if step < 1 {
		step = 1
	}
	var chunks []string
	start := 0
	for start < len(runes) {
		end := start + DefaultChunkSize
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			floor := start + DefaultChunkSize/2
			pos := end
			for pos > floor {
				pos--
				if isSentenceEnd(runes[pos]) {
					end = pos + 1
					break
				}
			}
		}
		if end <= start {
			end = start + DefaultChunkSize
			if end > len(runes) {
				end = len(runes)
			}
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
		start += step
	}
	return chunks
}

// ChunkMarkdown — split on ATX headings (h1-h4), keep each heading glued to
// its body, then slide within a section with the heading as prefix. Falls
// back to ChunkText without headings.
func ChunkMarkdown(text string) []string {
	hasHeadings := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") {
			hasHeadings = true
			break
		}
	}
	if !hasHeadings {
		return ChunkText(text)
	}

	type section struct{ heading, body string }
	var sections []section
	currentHeading := ""
	var currentBody strings.Builder
	flush := func() {
		if strings.TrimSpace(currentBody.String()) != "" || currentHeading != "" {
			sections = append(sections, section{heading: currentHeading, body: currentBody.String()})
		}
		currentHeading = ""
		currentBody.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			level := 0
			for _, c := range trimmed {
				if c != '#' {
					break
				}
				level++
			}
			if level >= 1 && level <= 4 {
				flush()
				currentHeading = line
				continue
			}
		}
		if currentBody.Len() > 0 {
			currentBody.WriteByte('\n')
		}
		currentBody.WriteString(line)
	}
	flush()

	// Merge heading-only sections (e.g. a bare document title) into the next
	// one so they do not become orphan one-line chunks.
	var merged []section
	for i, sec := range sections {
		if strings.TrimSpace(sec.body) == "" && sec.heading != "" && i+1 < len(sections) {
			sections[i+1].heading = strings.TrimRight(sec.heading+"\n"+sections[i+1].heading, "\n")
			continue
		}
		merged = append(merged, sec)
	}

	var chunks []string
	for _, sec := range merged {
		prefix := ""
		if sec.heading != "" {
			prefix = sec.heading + "\n"
		}
		for _, c := range ChunkText(sec.body) {
			chunks = append(chunks, prefix+c)
		}
	}
	if len(chunks) == 0 {
		return ChunkText(text)
	}
	return chunks
}

// ============================================
// 文档管理
// ============================================

const summaryCols = "doc_id, title, language, category, chunk_count, source, index_status, index_error, created_at, updated_at"
const fullCols = "doc_id, title, content, language, category, tags, chunk_count, source, index_status, index_error, created_at, updated_at"

// ListDocuments — paginated document summaries.
func (s *Service) ListDocuments(ctx context.Context, userID int32, page, pageSize int64) (map[string]any, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize
	var total int64
	if err := s.DB.QueryRow(ctx, "SELECT COUNT(*) FROM knowledge_documents WHERE uploaded_by = $1", userID).Scan(&total); err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	rows, err := s.DB.Query(ctx,
		"SELECT "+summaryCols+" FROM knowledge_documents WHERE uploaded_by = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
		userID, pageSize, offset)
	if err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	defer rows.Close()
	docs := make([]map[string]any, 0)
	for rows.Next() {
		d, err := scanDocumentSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("查询失败: %w", err)
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("查询失败: %w", err)
	}
	return map[string]any{"data": docs, "total": total, "page": page, "page_size": pageSize}, nil
}

// GetDocument — full row (content + tags) for the editor.
func (s *Service) GetDocument(ctx context.Context, userID int32, docID int32) (map[string]any, error) {
	row := s.DB.QueryRow(ctx,
		"SELECT "+fullCols+" FROM knowledge_documents WHERE doc_id = $1 AND uploaded_by = $2", docID, userID)
	return scanDocumentFull(row)
}

// UpdateDocument — update content (+metadata), delete chunks, re-queue.
func (s *Service) UpdateDocument(ctx context.Context, userID int32, docID int32, content string, title, language, category *string, tags []string) (map[string]any, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("document content cannot be empty")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("tx begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var owned int32
	err = tx.QueryRow(ctx, "SELECT doc_id FROM knowledge_documents WHERE doc_id = $1 AND uploaded_by = $2", docID, userID).Scan(&owned)
	if err != nil {
		return nil, fmt.Errorf("document not found")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM knowledge_chunks WHERE doc_id = $1", docID); err != nil {
		return nil, fmt.Errorf("删除失败: %w", err)
	}
	newTitle := ""
	if title != nil && strings.TrimSpace(*title) != "" {
		newTitle = strings.TrimSpace(*title)
	}
	// Re-detect the script from the new content when no language is supplied.
	lang := ""
	if language != nil && strings.TrimSpace(*language) != "" {
		lang = strings.TrimSpace(*language)
	} else if det := gemini.DetectLanguage(content); det != "" {
		lang = det
	} else {
		lang = "km"
	}
	cat := ""
	if category != nil {
		cat = *category
	}
	_, err = tx.Exec(ctx,
		"UPDATE knowledge_documents SET content = $1, title = CASE WHEN $3 = '' THEN title ELSE $3 END, "+
			"language = $4, category = $5, tags = COALESCE($6::text[], tags), "+
			"chunk_count = 0, index_status = 'pending', index_error = '', last_embedded_at = NULL WHERE doc_id = $2",
		content, docID, newTitle, lang, cat, tags)
	if err != nil {
		return nil, fmt.Errorf("更新失败: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("tx commit: %w", err)
	}
	return s.GetDocument(ctx, userID, docID)
}

// DeleteDocument removes a document (and its chunks via FK cascade).
func (s *Service) DeleteDocument(ctx context.Context, userID int32, docID int32) error {
	tag, err := s.DB.Exec(ctx, "DELETE FROM knowledge_documents WHERE doc_id = $1 AND uploaded_by = $2", docID, userID)
	if err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document not found")
	}
	return nil
}

// RetryDocument — reset a failed/pending doc to pending and re-queue.
func (s *Service) RetryDocument(ctx context.Context, userID int32, docID int32) error {
	tag, err := s.DB.Exec(ctx,
		"UPDATE knowledge_documents SET index_status = 'pending', index_error = '', chunk_count = 0, last_embedded_at = NULL WHERE doc_id = $1 AND uploaded_by = $2",
		docID, userID)
	if err != nil {
		return fmt.Errorf("更新失败: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document not found")
	}
	return nil
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
