// Command embedcmp — a READ-ONLY A/B measurement of retrieval quality, built to
// answer exactly one decision question:
//
//	"After moving embeddings from AI Studio to Vertex, how much retrieval
//	 quality do we lose?"
//
// THE PREMISE (measured with cmd/vertexprobe; not re-derived here)
//
// Production embeds through AI Studio with task conditioning: documents at index
// time with RETRIEVAL_DOCUMENT, queries at search time with RETRIEVAL_QUERY. The
// platform's :predict does not honour the task field — gemini-embedding-001,
// text-embedding-005 and text-multilingual-embedding-002 all answer every
// spelling (parameters.task_type / taskType / instances[].task_type) with
// vectors bit-identical to the no-argument baseline. So the migration silently
// removes the query/document asymmetry the model was trained for:
//
//	path A (today)  queries : AI Studio, RETRIEVAL_QUERY
//	                documents: knowledge_chunks.embedding as stored
//	                           (AI Studio, RETRIEVAL_DOCUMENT)
//	path B (after)  queries : Vertex :predict, no task field
//	                documents: recomputed in memory (Vertex :predict, no task field)
//
// # WHY BOTH PATHS MUST BE INTERNALLY CONSISTENT — the reason this tool exists
//
// A retrieval score comes from ONE query convention compared against ONE
// document convention. Mixing the two sides across vendors or conventions
// measures a pairing the migration never ships:
//
//   - Vertex queries against AI-Studio document vectors (or the reverse) score a
//     CROSS-VENDOR pair: different endpoint, different treatment of the task
//     field, and no guarantee that the two returned vectors even live in the
//     same space. A vector-space mismatch fails far harder than a lost task
//     hint, so that number is a worst case, not a migration estimate.
//   - The honest measurement therefore keeps each path self-consistent, so the
//     ONLY variable between A and B is the embedding convention as a whole:
//     A = "everything AI Studio, task-conditioned", B = "everything Vertex,
//     unconditional". Whatever recall delta is left is attributable to the
//     migration rather than to a harness mistake.
//   - Path A also reuses the vectors already in the column instead of
//     recomputing them, because those ARE the production artifacts: path A is
//     then not an approximation of today's retrieval, it is today's retrieval.
//
// READ-ONLY. See the contract at the top of retrieve.go: SELECT-only statements
// behind a mechanical guard, a read-only session on the connection, and no write
// of any kind — path B's document vectors live in memory for the duration of the
// run. The tool prints that guarantee in its own output so a reader of a saved
// report knows it too.
//
// Usage (on the server; needs DATABASE_URL, and PLATFORM_CREDENTIAL_KEY if
// model_configs.api_key is sealed):
//
//	set -a; . /opt/khmer-ai-cs/.env-go; set +a
//	./embedcmp -eval /root/khmer-deploy/rag_eval.json -user 7 -limit 5 \
//	    -vertex-project gen-lang-client-0354228918 -vertex-region asia-southeast1 \
//	    -vertex-sa /opt/khmer-ai-cs/vertex-sa.json
//
// GEMINI_API_BASE MUST STAY SET for path A. This deployment reaches AI Studio
// through the Cloudflare relay precisely because Google geo-blocks the host's
// egress IP, while the Vertex migration instructions say to CLEAR that variable
// once the platform endpoint is in use. Clearing it does not affect path B — it
// only breaks path A, which would then look like "the measurement cannot run"
// instead of "the measurement is comparing a different endpoint". The probe's
// error message says so, and the printed `path A` line names the base actually
// used, so a saved report shows which route produced its numbers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/security"
)

// defaultSAFile is where the service-account key lives on the deployment host.
// Used only as the LAST fallback for -vertex-sa (after the flag, then
// GEMINI_VERTEX_SA_FILE in the environment), so an operator running the tool
// where the key is elsewhere is never silently pointed at this path — the flag
// help states it, and the gemini package reports an unreadable key as an error
// rather than falling back to AI Studio.
const defaultSAFile = "/opt/khmer-ai-cs/vertex-sa.json"

// probeText is embedded once through each service before the measurement starts.
// It is not an eval query (the eval set is Khmer, this is not), so it cannot
// disturb the 5-minute query-embedding cache the measured queries will use.
const probeText = "embedcmp liveness probe / សាកល្បង"

var (
	flagEval = flag.String("eval", "",
		"eval set path: {\"queries\":[{\"query\":…,\"expect\":[doc_id,…]}]} or a bare [{…}] array (required)")
	flagTenant = flag.Int("user", 1,
		"tenant id to measure: the knowledge_documents.uploaded_by value")
	flagLimit = flag.Int("limit", 5,
		"topK: the cutoff for the hit verdict and the diff table (recall@5/@10 and MRR are always reported)")
	flagMaxChunks = flag.Int("max-chunks", 0,
		"sample at most N chunks by stride (0 = whole corpus; a sampled run is NOT comparable to a full-corpus baseline)")
	flagProgress = flag.Int("progress", 100,
		"print a Vertex corpus-embedding progress line about every N chunks, between 100-chunk batches (0 = silent)")
	flagVertexProject = flag.String("vertex-project",
		firstNonEmpty(os.Getenv("GEMINI_VERTEX_PROJECT"), ""),
		"GCP project id (default: $GEMINI_VERTEX_PROJECT, else the service account's project_id)")
	flagVertexRegion = flag.String("vertex-region",
		firstNonEmpty(os.Getenv("GEMINI_VERTEX_REGION"), "asia-southeast1"),
		"Vertex region (default: $GEMINI_VERTEX_REGION, else asia-southeast1)")
	flagVertexSA = flag.String("vertex-sa",
		firstNonEmpty(os.Getenv("GEMINI_VERTEX_SA_FILE"), defaultSAFile),
		"service-account JSON key (default: $GEMINI_VERTEX_SA_FILE, else "+defaultSAFile+")")
)

func main() {
	flag.Parse()
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "embedcmp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	started := time.Now()

	if strings.TrimSpace(*flagEval) == "" {
		return errors.New("-eval is required")
	}
	if *flagTenant <= 0 {
		return fmt.Errorf("-user must be a positive tenant id, got %d", *flagTenant)
	}
	if *flagLimit <= 0 {
		return fmt.Errorf("-limit must be positive, got %d", *flagLimit)
	}
	if *flagMaxChunks < 0 {
		return fmt.Errorf("-max-chunks cannot be negative, got %d", *flagMaxChunks)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}

	// The eval set is read first: a bad path must cost nothing, and the case
	// count shapes every later step.
	cases, err := loadEvalCases(*flagEval)
	if err != nil {
		return err
	}

	pool, err := openReadOnlyPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	apiKey, keySource, err := resolveStudioKey(ctx, pool)
	if err != nil {
		return err
	}

	corpus, stats, err := loadCorpus(ctx, pool, int32(*flagTenant), gemini.EmbeddingModel)
	if err != nil {
		return err
	}
	if len(corpus) == 0 {
		return fmt.Errorf("no chunks in the dense candidate set for tenant %d (uploaded_by=%d, index_status='ready', embedding_model=%q): "+
			"check -user, or that tenant %d has indexed documents",
			*flagTenant, *flagTenant, gemini.EmbeddingModel, *flagTenant)
	}

	used := corpus
	sampled := false
	if *flagMaxChunks > 0 && *flagMaxChunks < len(corpus) {
		used = sampleChunks(corpus, *flagMaxChunks)
		sampled = true
	}

	// Both services are built here, before any measurement, so a configuration
	// error (missing key, unreadable service account) fails in seconds instead of
	// after the corpus has been embedded.
	studio, vertexSvc, err := buildServices(apiKey, *flagVertexSA, *flagVertexProject, *flagVertexRegion)
	if err != nil {
		return err
	}
	if err := probeServices(ctx, studio, vertexSvc); err != nil {
		return err
	}

	embedCalls, embedTime, err := embedCorpusVertex(ctx, vertexSvc, used, *flagProgress)
	if err != nil {
		return err
	}

	results, err := measure(ctx, studio, vertexSvc, used, cases)
	if err != nil {
		return err
	}

	floor := similarityFloor()
	in := reportInput{
		EvalPath:    *flagEval,
		Tenant:      int32(*flagTenant),
		Limit:       *flagLimit,
		Floor:       floor,
		Model:       gemini.EmbeddingModel,
		Stats:       stats,
		UsedChunks:  len(used),
		RankedDocs:  distinctDocCount(used),
		Sampled:     sampled,
		MaxChunks:   *flagMaxChunks,
		EmbedCalls:  embedCalls,
		EmbedTime:   embedTime,
		TotalTime:   time.Since(started),
		Vertex:      vertexLabel(*flagVertexProject, *flagVertexRegion, *flagVertexSA),
		A:           metricsFor("A", results, func(c caseResult) (int, float64) { return c.RankA, c.SimA }, floor),
		B:           metricsFor("B", results, func(c caseResult) (int, float64) { return c.RankB, c.SimB }, floor),
		Diffs:       diffCases(results, *flagLimit),
		Titles:      titlesOf(corpus),
		EmptyExpect: emptyExpect(cases),
		Unmatched:   unmatchedCases(cases, used),
		KeySource:   keySource,
		StudioBase:  studioBaseLabel(),
	}
	writeReport(os.Stdout, in)
	return nil
}

// ─── database (read-only) ────────────────────────────────────────────────────

// openReadOnlyPool opens the pool with READ-ONLY CONTRACT layer 3: the session
// is started with default_transaction_read_only=on, so Postgres itself rejects
// any write this process attempts — including one that somehow bypassed
// assertSelectOnly — with "cannot execute … in a read-only transaction".
//
// It is set as a startup parameter (RuntimeParams) rather than as a `SET`
// statement, because a SET is exactly the kind of non-SELECT statement the
// layer-2 guard refuses to send.
func openReadOnlyPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// resolveStudioKey finds the AI Studio API key the production service uses.
//
// It mirrors cmd/rageval: the default model_configs row first (sealed at rest,
// opened with PLATFORM_CREDENTIAL_KEY), then GEMINI_API_KEY. Using production's
// own key matters because path A is meant to BE production — an ad-hoc key could
// point at a different quota, project or relay.
//
// The row's model/system prompt/max_tokens are ignored: embedding_model is a
// constant in this codebase (gemini.EmbeddingModel), and this tool never chats.
func resolveStudioKey(ctx context.Context, pool *pgxpool.Pool) (key, source string, err error) {
	if stored, _, _, _, _, _, ok := gemini.LoadDefaultConfig(ctx, pool); ok {
		// A failure to open a sealed value yields the input, matching the
		// server's behaviour (the sealer passes unrecognised values through).
		//
		// No usable sealer is NOT fatal here: the column may hold a legacy
		// plaintext key, which is exactly the case DecryptOrKeep exists for and
		// the case where failing closed would refuse a key the server itself
		// accepts. A value that is really ciphertext then reaches the API and is
		// rejected by the liveness probe, with a message that says so.
		key := strings.TrimSpace(stored)
		if sealer, serr := security.NewSealer(os.Getenv("PLATFORM_CREDENTIAL_KEY")); serr == nil {
			key = strings.TrimSpace(sealer.DecryptOrKeep(stored))
		}
		if key != "" {
			return key, "model_configs (default row)", nil
		}
	}
	if env := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); env != "" {
		return env, "GEMINI_API_KEY", nil
	}
	return "", "", errors.New("no AI Studio API key: model_configs has no default row with a usable api_key, " +
		"and GEMINI_API_KEY is unset — path A cannot run without the key production uses")
}

// ─── providers ───────────────────────────────────────────────────────────────

// buildServices returns one gemini.Service per convention.
//
// WHY THE ENVIRONMENT IS STAGED: the gemini package resolves its transport ONCE,
// in New, from GEMINI_PROVIDER (and the GEMINI_VERTEX_* variables) — which is the
// right design for a server process that is either on AI Studio or on Vertex, and
// the reason this comparison can be made at all without copying endpoint or
// authentication logic: each Service captures a different provider, and both keep
// working side by side afterwards. The alternative — reimplementing one of the
// two transports here — is exactly what this tool must not do, because a
// hand-rolled second client is a second thing that can be wrong.
//
// GEMINI_PROVIDER is FORCED to studio for the first call. On a host that has
// already been migrated (GEMINI_PROVIDER=vertex), path A must still measure the
// pre-migration convention — that is what "path A" means. The staged values are
// restored before returning, so nothing later in the process sees a modified
// environment.
func buildServices(apiKey, saFile, project, region string) (studio, vertexSvc *gemini.Service, err error) {
	restore := stageEnv()
	defer restore()

	setenv("GEMINI_PROVIDER", "studio")
	studio = gemini.New(apiKey, gemini.EmbeddingModel, 0)
	if !studio.IsConfigured() {
		// On the studio path an empty key means mock mode, and mock mode would
		// silently compare a constant template vector against real Vertex
		// vectors. Refuse rather than measure a fiction.
		return nil, nil, errors.New("path A has no API key: the AI Studio service would run in mock mode")
	}

	setenv("GEMINI_PROVIDER", "vertex")
	if saFile != "" {
		setenv("GEMINI_VERTEX_SA_FILE", saFile)
	}
	if project != "" {
		setenv("GEMINI_VERTEX_PROJECT", project)
	}
	if region != "" {
		setenv("GEMINI_VERTEX_REGION", region)
	}
	// Validate before New: the package's startup check reports WHICH part of the
	// vertex configuration is wrong (unreadable file / malformed JSON / bad
	// private key / no project), which is the difference between a one-line fix
	// and an afternoon. It reads only the key file — no network.
	if err := gemini.ValidateProviderConfig(); err != nil {
		return nil, nil, fmt.Errorf("vertex configuration: %w", err)
	}
	vertexSvc = gemini.New("", gemini.EmbeddingModel, 0)
	if !vertexSvc.IsConfigured() {
		return nil, nil, errors.New("vertex service did not configure (no service account?)")
	}
	return studio, vertexSvc, nil
}

// stageEnv saves the provider-related environment and returns a restore func.
func stageEnv() func() {
	names := []string{"GEMINI_PROVIDER", "GEMINI_VERTEX_SA_FILE", "GEMINI_VERTEX_PROJECT", "GEMINI_VERTEX_REGION"}
	saved := make(map[string]*string, len(names))
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok {
			value := v
			saved[n] = &value
			continue
		}
		saved[n] = nil
	}
	return func() {
		for n, v := range saved {
			if v == nil {
				_ = os.Unsetenv(n)
				continue
			}
			_ = os.Setenv(n, *v)
		}
	}
}

func setenv(name, value string) { _ = os.Setenv(name, value) }

// probeServices embeds one fixed text through each service and rejects the two
// ways a "successful" call can still be useless.
//
// WHY A PROBE: both failure modes below are silent — the run completes, prints a
// full report, and the numbers are meaningless or fake. A measurement tool that
// can quietly measure the wrong thing is worse than one that fails, so this is
// checked before the corpus is embedded rather than diagnosed afterwards.
//
//   - 768 dims: knowledge_chunks is vector(768). The gemini package already
//     rejects a wrong width per call, so this only asserts that the guard is in
//     play for the service we hold.
//   - not the mock vector: gemini.MockEmbedding is the deterministic placeholder
//     returned when a service is unconfigured. It must never appear in a
//     comparison against real vectors.
func probeServices(ctx context.Context, studio, vertexSvc *gemini.Service) error {
	probes := []struct {
		name string
		svc  *gemini.Service
		// hint is appended to a FAILURE. Both transports fail with errors that
		// name the symptom and not the deployment fact that explains it, and the
		// operator running this is usually one env var away from a working run.
		hint string
	}{
		{
			name: "path A (AI Studio)",
			svc:  studio,
			hint: "path A embeds through " + studioBaseLabel() + ". If that is the default endpoint, note that this " +
				"deployment reaches AI Studio through GEMINI_API_BASE (the Cloudflare relay) because Google " +
				"geo-blocks the server's egress IP — and the Vertex migration instructions say to CLEAR that " +
				"variable, which breaks path A (not path B). Keep it set for this comparison.",
		},
		{
			name: "path B (Vertex)",
			svc:  vertexSvc,
			hint: "check GEMINI_VERTEX_REGION (a wrong region 404s every call) and that the service account belongs " +
				"to the project whose vectors are being compared.",
		},
	}
	for _, p := range probes {
		vec, err := p.svc.GenerateQueryEmbedding(ctx, probeText)
		if err != nil {
			return fmt.Errorf("%s: probe embedding failed, so the measurement cannot start: %w\n    hint: %s",
				p.name, err, p.hint)
		}
		if len(vec) != expectedDims {
			return fmt.Errorf("%s: probe returned %d dimensions, want %d", p.name, len(vec), expectedDims)
		}
		if isMockVector(vec) {
			return fmt.Errorf("%s: probe returned gemini.MockEmbedding — the service is in mock mode, so any report would be fiction", p.name)
		}
	}
	return nil
}

// studioBaseLabel names the endpoint path A will actually call, read the same way
// the gemini package reads it (per call, from the environment). It exists so a
// probe failure can tell the operator WHICH host was unreachable — the answer
// differs completely between "the relay is misconfigured" and "the relay is gone
// because the migration removed it".
func studioBaseLabel() string {
	if v := strings.TrimSpace(os.Getenv("GEMINI_API_BASE")); v != "" {
		return v + " (GEMINI_API_BASE)"
	}
	return "https://generativelanguage.googleapis.com/v1beta (GEMINI_API_BASE is unset)"
}

// expectedDims mirrors knowledge_chunks' vector(768) and the gemini package's
// unexported embeddingVectorDimension. Duplicated deliberately: this is an
// assertion about the schema this tool reads, not a parameter it passes.
const expectedDims = 768

func isMockVector(vec []float32) bool {
	mock := gemini.MockEmbedding()
	if len(vec) != len(mock) {
		return false
	}
	for i := range mock {
		if vec[i] != mock[i] {
			return false
		}
	}
	return true
}

// ─── measurement ─────────────────────────────────────────────────────────────

// vertexBatchSize mirrors the batching gemini.GenerateEmbeddings performs
// internally (100 texts per :predict). embedCorpusVertex loops over the corpus
// itself ONLY to be able to print progress between batches; the calls it makes
// are the same calls the library would make for a 615-chunk document, so the
// progress wrapper does not change what is sent to the platform.
const vertexBatchSize = 100

// embedCorpusVertex embeds the whole corpus with Vertex, in memory.
//
// The result is never written anywhere — see the READ-ONLY CONTRACT. A failed
// batch ABORTS the run instead of leaving a gap: a chunk with no vector would
// score 0 against every query, which reads exactly like a quality loss caused by
// the migration. Missing data must never look like bad news.
func embedCorpusVertex(ctx context.Context, svc *gemini.Service, corpus []corpusChunk, progressEvery int) (calls int, elapsed time.Duration, err error) {
	started := time.Now()
	done, lastReport := 0, 0
	for start := 0; start < len(corpus); start += vertexBatchSize {
		end := start + vertexBatchSize
		if end > len(corpus) {
			end = len(corpus)
		}
		texts := make([]string, 0, end-start)
		for _, c := range corpus[start:end] {
			// The EXACT text that produced the stored vector: indexing embeds
			// the chunk content it then stores (rag.indexDocument embeds
			// chunks[i] and writes chunks[i] as content). Embedding anything
			// else — the title, the segmented text, a re-chunked document —
			// would compare two different corpora on top of two conventions.
			texts = append(texts, c.content)
		}
		vecs, verr := svc.GenerateEmbeddings(ctx, texts)
		calls++
		if verr != nil {
			return calls, time.Since(started), fmt.Errorf("vertex corpus embedding failed at chunk %d/%d: %w",
				start, len(corpus), verr)
		}
		if len(vecs) != len(texts) {
			return calls, time.Since(started), fmt.Errorf("vertex corpus embedding returned %d vectors for %d chunks", len(vecs), len(texts))
		}
		for i := range vecs {
			corpus[start+i].vertex = vecs[i]
		}
		done = end
		// Progress is reported between batches: the corpus is embedded in the
		// same 100-text :predict calls gemini.GenerateEmbeddings would make, and
		// splitting them further to print more often would change what is sent
		// to the platform. So a line appears once the next progress point has
		// passed, and always on the last batch.
		if progressEvery > 0 && (done == len(corpus) || done-lastReport >= progressEvery) {
			lastReport = done
			fmt.Fprintf(os.Stderr, "  vertex corpus: %d/%d chunks embedded (%.0f%%) after %s\n",
				done, len(corpus), 100*float64(done)/float64(len(corpus)), time.Since(started).Round(time.Millisecond))
		}
	}
	return calls, time.Since(started), nil
}

// measure runs every eval query through both paths and returns the per-query
// outcome. Queries are interleaved (A then B per query, not "all A, then all B")
// so both paths see the same network conditions — a slow window in the middle of
// the run lands on both sides rather than on one.
func measure(ctx context.Context, studio, vertexSvc *gemini.Service, corpus []corpusChunk, cases []evalCase) ([]caseResult, error) {
	out := make([]caseResult, 0, len(cases))
	for i, c := range cases {
		// The query is embedded RAW, unchanged, on both sides. rageval's dense
		// baseline embedded c.Query raw as well, which is what makes the recorded
		// "dense recall@5 37/45, MRR 0.655" a checkable anchor for path A. Any
		// normalisation applied here would have to be applied identically on both
		// sides — so applying none is both simpler and the faithful choice.
		vecA, err := studio.GenerateQueryEmbedding(ctx, c.Query)
		if err != nil {
			return nil, queryEmbedError("A (AI Studio, RETRIEVAL_QUERY)", i, c.Query, err)
		}
		vecB, err := vertexSvc.GenerateQueryEmbedding(ctx, c.Query)
		if err != nil {
			return nil, queryEmbedError("B (Vertex, no task)", i, c.Query, err)
		}

		rankA := rankChunks(vecA, corpus, func(cc corpusChunk) []float32 { return cc.stored })
		rankB := rankChunks(vecB, corpus, func(cc corpusChunk) []float32 { return cc.vertex })
		docsA := docRanking(rankA)
		docsB := docRanking(rankB)

		res := caseResult{
			Index: i,
			Query: c.Query,
			RankA: bestRank(docsA, c.Expect),
			RankB: bestRank(docsB, c.Expect),
		}
		if len(rankA) > 0 {
			res.TopA, res.SimA = rankA[0].DocID, rankA[0].Sim
		}
		if len(rankB) > 0 {
			res.TopB, res.SimB = rankB[0].DocID, rankB[0].Sim
		}
		out = append(out, res)
	}
	return out, nil
}

// queryEmbedError explains a failed query embedding and, crucially, refuses to
// continue. Dropping the case would shrink one side's denominator or silently
// rank it against nothing, and either way the report would show a difference
// that is an infrastructure hiccup, not retrieval quality.
//
// GenerateQueryEmbedding caps this hop at GEMINI_EMBED_BUDGET_MS (default 5000),
// which is deliberately short for the reply path and can be too short for a
// benchmark run over a slow link — so the message names the knob.
func queryEmbedError(path string, i int, query string, err error) error {
	return fmt.Errorf("case %d (%s): path %s query embedding failed: %w\n"+
		"    hint: raise GEMINI_EMBED_BUDGET_MS (default 5000) if this is a timeout; do not ignore it, "+
		"a missing query vector would be reported as a retrieval failure",
		i, oneLine(query, 40), path, err)
}

// titlesOf indexes the corpus titles for the diff table's legend.
func titlesOf(corpus []corpusChunk) map[int32]string {
	out := make(map[int32]string, len(corpus))
	for _, c := range corpus {
		if _, ok := out[c.docID]; !ok {
			out[c.docID] = c.title
		}
	}
	return out
}

func distinctDocCount(corpus []corpusChunk) int {
	seen := make(map[int32]bool, len(corpus))
	for _, c := range corpus {
		seen[c.docID] = true
	}
	return len(seen)
}

// unmatchedCases lists eval cases whose expected documents have no chunk in the
// measured corpus. They are guaranteed misses in both paths, so they push both
// recall numbers down and hide part of the difference between them; the reader
// has to know they are there.
func unmatchedCases(cases []evalCase, corpus []corpusChunk) []int {
	present := make(map[int32]bool, len(corpus))
	for _, c := range corpus {
		present[c.docID] = true
	}
	out := make([]int, 0)
	for i, c := range cases {
		if len(c.Expect) == 0 {
			continue // reported separately
		}
		found := false
		for _, want := range c.Expect {
			if present[want] {
				found = true
				break
			}
		}
		if !found {
			out = append(out, i)
		}
	}
	return out
}

// ─── small helpers ───────────────────────────────────────────────────────────

// similarityFloor reads production's absolute similarity floor for the
// SCALE diagnostic only. It is never used to filter: a fixed floor calibrated on
// AI Studio similarities would penalize whichever convention has the smaller
// absolute similarity, which is a threshold artifact rather than a retrieval
// loss. Reading the same variable production reads keeps the diagnostic honest
// if the deploy retunes it.
func similarityFloor() float64 {
	const fallback = 0.35 // rag.currentThresholds' RAG_SIMILARITY_FLOOR default
	if v := strings.TrimSpace(os.Getenv("RAG_SIMILARITY_FLOOR")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func vertexLabel(project, region, sa string) string {
	if project == "" {
		project = "(project_id from the service account)"
	}
	return fmt.Sprintf("project=%s region=%s sa=%s", project, region, sa)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
