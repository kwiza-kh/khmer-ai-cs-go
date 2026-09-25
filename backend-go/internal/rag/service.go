// Package rag — document ingestion queue, embedding, pgvector hybrid search
// and grounding (port of the Rust service.rs; itself a port of the Go one).
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"khmer-ai-cs-go/internal/redisstore"
	"khmer-ai-cs-go/internal/typesafe"
	"khmer-ai-cs-go/internal/usage"
)

const (
	DefaultChunkSize    = 1000
	DefaultChunkOverlap = 200
	// DefaultTopK was 5. Three sources carry the answer in practice — the
	// per-turn prompt is what the spend limit is spent on, and since Google
	// bills every retrieved rune on every turn (there is no reuse across
	// turns), the fourth and fifth documents were the cheapest thing to cut:
	// measured at 2,361 prompt tokens on a thin KB against 3,400-4,200 on a
	// thick one (docs/GEMINI-RATE-LIMIT.md §5.2). diversifyByDoc still allows
	// at most 2 chunks per document, so this is 3 distinct documents at worst.
	// Env RAG_TOP_K overrides, and rageval measures recall@5 against it.
	DefaultTopK = int64(3)

	indexWorkerCount      = 2
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

// envI — positive integer env override; unset, malformed or non-positive
// values fall back to the default.
func envI(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
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
	Redis  *redisstore.Client
	Logger *slog.Logger
	// Jev optionally replaces the LLM reranker with typed Score judgments.
	// nil = disabled: reranking keeps the Gemini path.
	Jev *typesafe.Client
	// KBChanged, when set, fires after a tenant's grounded content changes —
	// a document finished indexing (upload/update/URL refresh/compile all
	// funnel through the index worker) or was deleted. The reply cache uses
	// it to drop that tenant's cached answers; nil = no callback.
	KBChanged func(ctx context.Context, userID int32)
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
	// Legacy chunks (indexed before 058) have content_tsv NULL; segment
	// them in the background so the lexical leg covers them too. Pure lexical
	// work: embeddings are never touched.
	go s.backfillSegmentedChunks(ctx)
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
		newText = NormalizeText(newText)
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
	var docID, userID int32
	var content, title, language, origin string
	err := s.DB.QueryRow(ctx,
		"UPDATE knowledge_documents SET index_status = 'indexing', index_error = '' "+
			"WHERE doc_id = (SELECT doc_id FROM knowledge_documents "+
			"WHERE index_status = 'pending' ORDER BY created_at ASC FOR UPDATE SKIP LOCKED LIMIT 1) "+
			"RETURNING doc_id, content, title, language, origin, uploaded_by").
		Scan(&docID, &content, &title, &language, &origin, &userID)
	if err != nil {
		return false
	}
	if err := s.indexDocument(ctx, docID, content); err != nil {
		s.Logger.Warn("knowledge document indexing failed", "error", err.Error())
		s.markDocumentFailed(ctx, docID, err.Error())
		return true
	}
	// The tenant's grounded content just changed — drop its reply cache so no
	// stale answer survives the update.
	if s.KBChanged != nil {
		s.KBChanged(ctx, userID)
	}
	// Ingest-time compile (llm-wiki pattern): distill the freshly indexed
	// source into an FAQ/summary child document and flag contradictions with
	// the existing KB. Dispatched to its own lane — see spawnCompile — because
	// running it here held one of only two index workers for up to a minute
	// per document, so a queue of uploads indexed serially behind the slowest
	// LLM call. The document is already indexed and retrievable either way.
	s.spawnCompile(ctx, docID, userID, title, content, language, origin)
	return true
}

func (s *Service) indexDocument(ctx context.Context, docID int32, content string) error {
	content = NormalizeText(content)
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
		seg := SegmentForSearch(chunks[i])
		batch.Queue(
			"INSERT INTO knowledge_chunks (doc_id, chunk_index, content, content_seg, content_tsv, embedding, created_at) "+
				"VALUES ($1, $2, $3, $4, to_tsvector('simple', $4), $5::vector, $6)",
			docID, int32(i), chunks[i], seg, gemini.FormatVector(embeddings[i]), now)
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

// ============================================
// Ingest-time compile (llm-wiki pattern)
// ============================================

// Compile thresholds are env-overridable because short documents are skipped
// entirely: a 400-rune FAQ page never reaches the contradiction check, so an
// operator who wants small pages compiled lowers RAG_COMPILE_MIN_RUNES.
var (
	compileMinRunes = envI("RAG_COMPILE_MIN_RUNES", compileMinDefault)
	compileMaxRunes = envI("RAG_COMPILE_MAX_RUNES", compileMaxDefault)
)

const (
	compileMinDefault   = 600
	compileMaxDefault   = 60000
	compileRedisKey     = "rag:compile_enabled"
	compiledTitleSuffix = " · AI 编译摘要"
)

// compileEnabled — Redis toggle written by PUT /api/v1/admin/rag/settings;
// absent key means enabled (compile is on by default).
func (s *Service) compileEnabled(ctx context.Context) bool {
	if s.Redis == nil {
		return true
	}
	v, err := s.Redis.GetString(ctx, compileRedisKey)
	if err != nil || v == "" {
		return true
	}
	return v == "1" || v == "true"
}

// CompileEnabledPublic exposes the toggle state for the admin settings GET.
func (s *Service) CompileEnabledPublic(ctx context.Context) bool {
	return s.compileEnabled(ctx)
}

// SetCompileEnabled persists the admin toggle ("1"/"0", no expiry).
func (s *Service) SetCompileEnabled(ctx context.Context, enabled bool) error {
	v := "0"
	if enabled {
		v = "1"
	}
	return s.Redis.SetString(ctx, compileRedisKey, v, 0)
}

type compileContradiction struct {
	NewClaim    string `json:"new_claim"`
	OldClaim    string `json:"old_claim"`
	OldDocTitle string `json:"old_doc_title"`
	Severity    string `json:"severity"`
}

type compileResult struct {
	FaqMarkdown    string                 `json:"faq_markdown"`
	Contradictions []compileContradiction `json:"contradictions"`
}

// compileSem bounds concurrent ingest-time compiles. Each holds a slot for up
// to ~90s (a 4096-token LLM call plus a nested hybrid search that may itself
// rerank), so this lane is deliberately small; index throughput matters more
// than eager summaries.
var compileSem = make(chan struct{}, 3)

// spawnCompile hands a freshly indexed document to the compile lane and
// returns immediately, so the index worker can claim the next pending doc.
// When the lane is saturated the compile is skipped rather than queued: the
// document is already searchable, and only the summary is lost.
func (s *Service) spawnCompile(ctx context.Context, docID, userID int32, title, content, language, origin string) {
	// Tag the owner so the compile's LLM spend is billed to the right tenant
	// (see gemini.AuxUsageObserver).
	ctx = usage.WithUser(ctx, userID)
	select {
	case compileSem <- struct{}{}:
	default:
		ctx := context.WithoutCancel(ctx)
		s.setCompileStatus(ctx, docID, "skipped")
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.Logger.Warn("knowledge compile panic recovered", "doc_id", docID, "panic", r)
			}
			<-compileSem
		}()
		// Detached from the worker's lifecycle so a shutdown or request
		// cancellation doesn't abort a compile that already holds a slot.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		defer cancel()
		s.maybeCompileDocument(runCtx, docID, userID, title, content, language, origin)
	}()
}

// maybeCompileDocument distills a freshly indexed source document into an
// FAQ/summary child document (queued through the normal indexing path) and
// records contradictions against the existing KB for human review. Indexing
// has already succeeded at this point, so every failure here degrades to a
// compile_status marker instead of an error.
func (s *Service) maybeCompileDocument(ctx context.Context, docID, userID int32, title, content, language, origin string) {
	if origin == "compiled" {
		return
	}
	runes := len([]rune(content))
	if runes < compileMinRunes || runes > compileMaxRunes || !s.compileEnabled(ctx) {
		s.setCompileStatus(ctx, docID, "skipped")
		return
	}
	if err := s.compileDocument(ctx, docID, userID, title, content, language); err != nil {
		s.Logger.Warn("knowledge compile failed", "doc_id", docID, "error", err.Error())
		s.setCompileStatus(ctx, docID, "failed")
		return
	}
	s.setCompileStatus(ctx, docID, "done")
}

func (s *Service) setCompileStatus(ctx context.Context, docID int32, status string) {
	_, _ = s.DB.Exec(ctx, "UPDATE knowledge_documents SET compile_status = $1 WHERE doc_id = $2", status, docID)
}

func (s *Service) compileDocument(ctx context.Context, docID, userID int32, title, content, language string) error {
	// NOTE: the previous compiled child is replaced at the END of this
	// function, in one transaction with the new insert — not here. Deleting up
	// front meant a failed or timed-out LLM call (the likely outcome for a
	// 60s / 4096-token request) destroyed the existing summary and left the
	// source with none until an operator noticed and re-indexed it.

	// Existing KB excerpts feed the contradiction check (self and compiled
	// children excluded). Kept as a slice so the Jev confirmation/sweep can
	// address each excerpt individually.
	var excerpts []kbExcerpt
	if sources, err := s.Search(ctx, userID, title+" "+truncateRunes(content, 300), 6); err == nil {
		for _, src := range sources {
			// Skip the document itself and AI-compiled children (a source must
			// not be flagged as contradicting its own previous summary).
			if src.DocID == docID || strings.HasSuffix(src.Title, compiledTitleSuffix) {
				continue
			}
			excerpts = append(excerpts, kbExcerpt{Title: src.Title, Content: truncateRunes(src.Content, 400)})
		}
	}
	var excerptsBlob strings.Builder
	for _, ex := range excerpts {
		fmt.Fprintf(&excerptsBlob, "--- EXISTING DOC: %s ---\n%s\n\n", ex.Title, ex.Content)
	}

	prompt := "You maintain a customer-support knowledge base.\n\n" +
		"NEW DOCUMENT (title: " + title + "):\n" + truncateRunes(content, 9000) + "\n\n" +
		"EXISTING KB EXCERPTS:\n" + truncateRunes(excerptsBlob.String(), 4000) + "\n" +
		"TASK 1 — compile the NEW DOCUMENT into a concise support-ready page. Write field \"faq_markdown\" as Markdown:\n" +
		"  line 1 exactly: \"# " + title + compiledTitleSuffix + "\"\n" +
		"  then a summary of at most 300 characters,\n" +
		"  then at most 8 lines formatted \"- **Q**: <question> **A**: <answer>\" covering the document's key facts.\n" +
		"  Keep it under 1200 characters total. Write in the same language as the NEW DOCUMENT (" + language + ").\n" +
		"TASK 2 — compare the NEW DOCUMENT against the EXISTING KB EXCERPTS and list factual contradictions " +
		"(different prices, dates, policies, specs). Field \"contradictions\": array of at most 5 " +
		"{\"new_claim\",\"old_claim\",\"old_doc_title\",\"severity\"} (severity: high|medium|low); empty array when none.\n" +
		"Reply with ONLY a JSON object: {\"faq_markdown\": string, \"contradictions\": [...]}"
	// 4096 output tokens: the FAQ + contradictions JSON overflows the 2048
	// default and a truncated payload fails to parse.
	reply, ok := s.Gemini.GenerateFastMax(ctx, prompt, 60*time.Second, 4096)
	if !ok {
		return fmt.Errorf("compile LLM call failed")
	}
	trimmed := strings.TrimSpace(reply)
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var res compileResult
	if err := json.Unmarshal([]byte(trimmed), &res); err != nil {
		// Fallback: the model sometimes answers with the Markdown page directly
		// instead of the JSON envelope. Accept it (with no contradictions)
		// rather than losing the compile entirely.
		if head, ok := strings.CutPrefix(strings.TrimSpace(reply), "# "); ok {
			res = compileResult{FaqMarkdown: "# " + head}
		} else {
			return fmt.Errorf("parse compile output: %w", err)
		}
	}
	faq := strings.TrimSpace(res.FaqMarkdown)
	if faq == "" {
		return fmt.Errorf("empty compile output")
	}

	var category *string
	var tags []string
	_ = s.DB.QueryRow(ctx, "SELECT category, tags FROM knowledge_documents WHERE doc_id = $1", docID).Scan(&category, &tags)
	compiledTags := append(append([]string{}, tags...), "auto-compiled")

	// Swap the old compiled child for the new one atomically. The insert is
	// guarded by the unique partial index from migration 049, so if a
	// concurrent compile already produced a child the insert reports no rows —
	// in that case we return without committing, which rolls the DELETE back
	// and leaves the other compile's child in place.
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin compiled doc swap: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "DELETE FROM knowledge_documents WHERE compiled_from = $1", docID); err != nil {
		return fmt.Errorf("clear old compiled doc: %w", err)
	}
	var compiledID int32
	err = tx.QueryRow(ctx,
		"INSERT INTO knowledge_documents (title, content, language, category, tags, uploaded_by, index_status, source, origin, compiled_from, compile_status) "+
			"VALUES ($1, $2, $3, $4, $5::text[], $6, 'pending', 'manual', 'compiled', $7, 'none') "+
			"ON CONFLICT (compiled_from) WHERE compiled_from IS NOT NULL DO NOTHING RETURNING doc_id",
		title+compiledTitleSuffix, faq, language, category, compiledTags, userID, docID).Scan(&compiledID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Another compile already produced the child — roll back our
			// delete so that child survives.
			return nil
		}
		return fmt.Errorf("insert compiled doc: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit compiled doc: %w", err)
	}
	s.Logger.Info("knowledge document compiled", "doc_id", docID, "compiled_id", compiledID)

	// Jev reviews the compile LLM's contradiction claims (drop unconfirmed
	// noise) and sweeps the excerpts for conflicts the LLM missed. Both are
	// single parallel calls and both fail open.
	res.Contradictions = s.confirmContradictions(ctx, title, res.Contradictions)
	res.Contradictions = append(res.Contradictions, s.sweepContradictions(ctx, title, excerpts, res.Contradictions)...)

	if len(res.Contradictions) > 0 {
		items, _ := json.Marshal(res.Contradictions)
		var oldDocID *int32
		first := ""
		for _, c := range res.Contradictions {
			if c.OldDocTitle != "" {
				first = c.OldDocTitle
				break
			}
		}
		if first != "" {
			var id int32
			// Match a real source document only: compiled children carry the
			// source title too, and picking one would point the review at an
			// AI summary instead of the human-authored original.
			if err := s.DB.QueryRow(ctx,
				"SELECT doc_id FROM knowledge_documents WHERE uploaded_by = $1 AND doc_id <> $2 "+
					"AND origin <> 'compiled' AND title ILIKE $3 ORDER BY created_at DESC LIMIT 1",
				userID, docID, "%"+first+"%").Scan(&id); err == nil {
				oldDocID = &id
			}
		}
		if _, err := s.DB.Exec(ctx,
			"INSERT INTO kb_contradictions (user_id, new_doc_id, old_doc_id, items) VALUES ($1, $2, $3, $4::jsonb)",
			userID, docID, oldDocID, string(items)); err != nil {
			s.Logger.Warn("contradiction record failed", "doc_id", docID, "error", err.Error())
		}
	}
	return nil
}

// UploadDocument creates a document (pending) and lets the worker index it.
// sourceURL marks the document as URL-derived (source='url').
func (s *Service) UploadDocument(ctx context.Context, userID int32, title, content, language, category string, tags []string, sourceURL *string) (map[string]any, error) {
	content = NormalizeText(content)
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
		"INSERT INTO knowledge_documents (title, content, language, category, tags, uploaded_by, index_status, source, source_url, origin) "+
			"VALUES ($1, $2, $3, $4, $5::text[], $6, 'pending', $7, $8, $9) "+
			"RETURNING doc_id, title, language, category, chunk_count, source, index_status, index_error, created_at, updated_at, origin, compiled_from, compile_status",
		title, content, lang, category, tags, userID, source, sourceURL, source)
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
	query = NormalizeText(query)
	if topK <= 0 {
		topK = int64(envI("RAG_TOP_K", int(DefaultTopK)))
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
	if hasLexicalScript(query) {
		if rows, err := s.searchTrigram(ctx, userID, query, candidateLimit); err == nil {
			trigram = rows
		}
	}
	if s.Logger != nil {
		s.Logger.Debug("rag search legs", "dense", len(dense), "lexical", len(lexical), "trigram", len(trigram))
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

	// topK is a hard cap. Without it, a clear dense leader skips the rerank and
	// every fused candidate (up to candidateLimit, 4x topK) lands in the grounding
	// prompt — measured at 12+ sources and ~3.1k tokens per Khmer turn on the test
	// KB. fused is already rank-ordered, so truncation degrades gracefully.
	fused = diversifyByDoc(fused, maxChunksPerDoc)
	if int64(len(fused)) > topK {
		fused = fused[:topK]
	}

	out := make([]Source, 0, len(fused))
	for _, c := range fused {
		out = append(out, Source{DocID: c.DocID, Title: c.Title, Content: c.Content, Score: c.Similarity})
	}
	return out, nil
}

// maxChunksPerDoc — cap on how many chunks of one document may enter the
// grounding window. Without it a long, multi-chunk document can occupy every
// slot once the fused list is truncated to topK, crowding out other documents.
const maxChunksPerDoc = 2

// diversifyByDoc keeps rank order but allows at most maxPerDoc chunks per
// document.
func diversifyByDoc(chunks []SearchChunk, maxPerDoc int) []SearchChunk {
	if maxPerDoc <= 0 {
		return chunks
	}
	count := make(map[int32]int, len(chunks))
	out := make([]SearchChunk, 0, len(chunks))
	for _, c := range chunks {
		if count[c.DocID] >= maxPerDoc {
			continue
		}
		count[c.DocID]++
		out = append(out, c)
	}
	return out
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
	scores := s.rerankScores(ctx, query, texts)
	if scores == nil {
		return chunks
	}
	rerankCachePut(key, scores)
	return applyRerankScores(chunks, scores, topK)
}

// rerankScores prefers Jev (one Score question per candidate, all in a single
// parallel batch) and falls back to the fast-model reranker when Jev is
// disabled or unavailable.
func (s *Service) rerankScores(ctx context.Context, query string, texts []string) []float32 {
	if sc, ok := s.rerankScoresJev(ctx, query, texts); ok {
		return sc
	}
	return s.Gemini.RerankChunks(ctx, query, texts)
}

// rerankRelevanceLevels — the ordered scale for passage relevance. Jev Score
// returns a probability-weighted position on it (0..4); ×2.5 maps onto the
// 0-10 scale the Gemini reranker and the rerankMin threshold use.
var rerankRelevanceLevels = []string{
	"Irrelevant: another topic, product, or document",
	"Marginal: shares a word with the query but answers nothing",
	"Partial: related background, does not answer the query",
	"Mostly: answers part of the query",
	"Exact: directly and completely answers the query",
}

// jevRerankBudget bounds the Jev rerank. Like the query rewrite it sits inside
// Search, ahead of generation, so an unbounded call held the turn for the HTTP
// client's 10s timeout. On expiry the caller keeps the fused order (or the
// Gemini rerank, when one is configured) rather than waiting. The default sits
// above the production server's measured 0.8-2.1s spread to api.typesafe.ai;
// tunable so ops can re-measure from the host without a rebuild.
var jevRerankBudget = time.Duration(envI("JEV_RERANK_BUDGET_MS", 3000)) * time.Millisecond

func (s *Service) rerankScoresJev(ctx context.Context, query string, texts []string) ([]float32, bool) {
	if !s.Jev.Enabled() || len(texts) == 0 || len(texts) > 20 {
		return nil, false
	}
	passages := make([]map[string]any, len(texts))
	questions := make(map[string]typesafe.Question, len(texts))
	for i, text := range texts {
		passages[i] = map[string]any{"id": fmt.Sprintf("c%d", i), "text": truncateRunes(text, 400)}
		questions[fmt.Sprintf("c%d", i)] = typesafe.Score(
			"How relevant is passage `passages["+strconv.Itoa(i)+"].text` to `query`?",
			rerankRelevanceLevels)
	}
	// Armed here rather than at function entry: the guards above can return
	// without ever calling Jev.
	ctx, cancel := context.WithTimeout(ctx, jevRerankBudget)
	defer cancel()

	resp, err := s.Jev.Judge(ctx, map[string]any{"query": query, "passages": passages}, questions)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("jev rerank failed; falling back to fast model", "error", err.Error())
		}
		return nil, false
	}
	scores := make([]float32, len(texts))
	for i := range texts {
		v, ok := resp.ScoreValue(fmt.Sprintf("c%d", i))
		if !ok {
			if s.Logger != nil {
				s.Logger.Warn("jev rerank missing a score; falling back to fast model", "passage", i)
			}
			return nil, false
		}
		sc := v * 2.5
		if sc < 0 {
			sc = 0
		}
		if sc > 10 {
			sc = 10
		}
		scores[i] = float32(sc)
	}
	return scores, true
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
	// Honour topK: without this the caller received every candidate above
	// rerankMin (up to rerankWindow=15), inflating the grounding prompt.
	if topK > 0 && int64(len(paired)) > topK {
		paired = paired[:topK]
	}
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
	query = NormalizeText(query)
	// Unspaced scripts (Khmer/CJK) cannot use websearch_to_tsquery: the whole
	// run is one token, so nothing matches. Use the segmented tsquery against
	// content_seg's tsvector, OR-ed with the legacy content expression so chunks written
	// before migration 058 that are not backfilled yet still match.
	termsExpr := "websearch_to_tsquery('simple', $1)"
	termsArg := query
	if tsq := SegmentedTSQuery(query); tsq != "" {
		termsExpr = "to_tsquery('simple', $1)"
		termsArg = tsq
	}
	sql := "WITH search_query AS (SELECT " + termsExpr + " AS terms) " +
		"SELECT kc.chunk_id, kc.doc_id, kc.content, kd.title, " +
		"GREATEST(ts_rank_cd(COALESCE(kc.content_tsv, ''::tsvector), sq.terms), " +
		"ts_rank_cd(to_tsvector('simple', kc.content), sq.terms))::float8 AS similarity " +
		"FROM knowledge_chunks kc " +
		"JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id " +
		"CROSS JOIN search_query sq " +
		"WHERE kd.uploaded_by = $2 AND kd.index_status = 'ready' " +
		"AND (kc.content_tsv @@ sq.terms OR to_tsvector('simple', kc.content) @@ sq.terms) " +
		"ORDER BY similarity DESC, kc.chunk_id ASC LIMIT $3"
	rows, err := s.DB.Query(ctx, sql, termsArg, userID, limit)
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

// searchTrigram — n-gram recall via ILIKE for scripts the simple tsvector
// config cannot segment: CJK bigrams, Khmer trigrams (pg_trgm GIN needs 3+).
func (s *Service) searchTrigram(ctx context.Context, userID int32, query string, limit int64) ([]SearchChunk, error) {
	ngrams := lexicalNgrams(query, 8)
	if len(ngrams) == 0 {
		return nil, nil
	}
	var conditions, scoreParts []string
	args := make([]any, 0, len(ngrams)+2)
	args = append(args, userID, limit)
	for i := range ngrams {
		p := i + 3 // $1 user, $2 limit, ngrams start at $3
		conditions = append(conditions, fmt.Sprintf("kc.content ILIKE $%d", p))
		scoreParts = append(scoreParts, fmt.Sprintf("(CASE WHEN kc.content ILIKE $%d THEN 1 ELSE 0 END)", p))
		args = append(args, "%"+ngrams[i]+"%")
	}
	sql := fmt.Sprintf(
		"SELECT kc.chunk_id, kc.doc_id, kc.content, kd.title, (%s)::float8 AS similarity "+
			"FROM knowledge_chunks kc "+
			"JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id "+
			"WHERE kd.uploaded_by = $1 AND kd.index_status = 'ready' "+
			"AND (%s) "+
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
			// Similarity is only comparable inside one leg (dense cosine 0..1,
			// lexical ts_rank, trigram hit count up to 8), so the cross-leg
			// "keep the larger Similarity" must NOT decide which representation
			// survives: a trigram count of 3 would overwrite the dense hit and
			// clear DenseSim, which the similarity gate and the rerank-skip
			// check depend on. Keep whichever DenseSim exists.
			denseSim := entry.chunk.DenseSim
			if chunk.DenseSim != nil {
				denseSim = chunk.DenseSim
			}
			if chunk.Similarity > entry.chunk.Similarity {
				entry.chunk = chunk
			}
			entry.chunk.DenseSim = denseSim
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
	// turn has nothing to resolve, so skip the extra call. Jev picks among
	// code-built candidates when available; the Gemini free-text rewrite is
	// the fallback.
	effectiveQuery := message
	var rewritten *string
	if len(history) > 0 {
		var decided bool
		rewritten, decided = s.rewriteQueryJev(ctx, message, history)
		if !decided {
			rewritten = s.Gemini.RewriteSearchQuery(ctx, message, history)
		}
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
	// Total injection budget: topK × per-source limit is unbounded above, and
	// every one of those runes is re-billed on every turn. The budget keeps the
	// full per-source allowance while it lasts — so the best-ranked sources are
	// never cut — and trims only the tail, which is cheaper than dropping a
	// whole source (the model still sees that it exists).
	budget := envI("RAG_CONTEXT_BUDGET_RUNES", 4800)
	used := 0
	for i, src := range sources {
		room := groundSourceLimit(src.Content)
		if budget > 0 {
			if used >= budget {
				break
			}
			if room > budget-used {
				room = budget - used
			}
		}
		content := truncateRunes(src.Content, room)
		used += len([]rune(content))
		fmt.Fprintf(&b, "--- Source %d: %s (score %.2f) ---\n%s\n\n", i+1, src.Title, src.Score, content)
	}
	s.logRAGQuery(ctx, userID, sessionID, message, rewritten, len(sources), &topScore, true)
	return GroundingContext{Sources: sources, ContextStr: b.String(), HasMatch: true}
}

// groundSourceLimit — how much of each grounding source reaches the prompt.
// Price lists and tables need more room than prose (a hard cut mid-row makes the
// model hallucinate the rest). Both limits are env-tunable so the source-count /
// source-length tradeoff can be calibrated with rageval.
func groundSourceLimit(content string) int {
	plain := envI("RAG_SOURCE_LIMIT_RUNES", 800)
	table := envI("RAG_TABLE_LIMIT_RUNES", 2000)
	for _, line := range strings.Split(content, "\n") {
		if isTableRow(line) {
			return table
		}
	}
	return plain
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
		// Continue from the emitted chunk's end minus the overlap — advancing by
		// a fixed step would skip [end, start+step) whenever the sentence
		// boundary pulled `end` back, silently losing document content.
		next := end - DefaultChunkOverlap
		if next <= start {
			next = start + 1
		}
		start = next
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

const summaryCols = "kd.doc_id, kd.title, kd.language, kd.category, kd.chunk_count, kd.source, kd.index_status, kd.index_error, kd.created_at, kd.updated_at, kd.origin, kd.compiled_from, kd.compile_status, src.title"
const fullCols = "doc_id, title, content, language, category, tags, chunk_count, source, index_status, index_error, created_at, updated_at, origin, compiled_from, compile_status"

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
		"SELECT "+summaryCols+" FROM knowledge_documents kd "+
			"LEFT JOIN knowledge_documents src ON src.doc_id = kd.compiled_from "+
			"WHERE kd.uploaded_by = $1 ORDER BY kd.created_at DESC LIMIT $2 OFFSET $3",
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
	content = strings.TrimSpace(NormalizeText(content))
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
	if s.KBChanged != nil {
		s.KBChanged(ctx, userID)
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

// backfillSegmentedChunks — fills content_seg/content_tsv for chunks written
// before migration 058. It only touches the lexical columns (embeddings stay
// untouched) and exits once the backlog is empty; new chunks are written with
// the columns already populated.
func (s *Service) backfillSegmentedChunks(ctx context.Context) {
	const batchSize = 200
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		rows, err := s.DB.Query(ctx,
			"SELECT chunk_id, content FROM knowledge_chunks WHERE content_tsv IS NULL ORDER BY chunk_id LIMIT $1",
			batchSize)
		if err == nil {
		} else {
			s.Logger.Warn("segmented backfill query failed", "error", err.Error())
			return
		}
		type legacyChunk struct {
			id      int32
			content string
		}
		var batch []legacyChunk
		for rows.Next() {
			var c legacyChunk
			if scanErr := rows.Scan(&c.id, &c.content); scanErr == nil {
				batch = append(batch, c)
			}
		}
		rows.Close()
		if len(batch) == 0 {
			return
		}
		for _, c := range batch {
			seg := SegmentForSearch(c.content)
			_, _ = s.DB.Exec(ctx,
				"UPDATE knowledge_chunks SET content_seg = $1, content_tsv = to_tsvector('simple', $1) "+
					"WHERE chunk_id = $2 AND content_tsv IS NULL",
				seg, c.id)
		}
		s.Logger.Info("segmented legacy chunks backfilled", "count", len(batch))
	}
}
