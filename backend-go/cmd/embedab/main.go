// Command embedab — a READ-ONLY side-by-side measurement of the retrieval
// quality of two embedding models on ONE corpus, built to answer exactly one
// decision question:
//
//	"Does gemini-embedding-2 retrieve the Khmer knowledge base better than
//	 gemini-embedding-001 — and does the switch break the absolute similarity
//	 floor the production gate is calibrated on?"
//
// # WHAT IS COMPARED
//
// Three variants run over the SAME chunks and the SAME queries:
//
//	ge1   gemini-embedding-001 via :predict, no task field — the production
//	      convention on the Vertex platform today (the platform ignores task
//	      fields; measured 2026-09-25/2026-10-09).
//	ge2p  gemini-embedding-2 via :embedContent with the documented prompt
//	      prefixes ("task: search result | query: …" / "title: none | text: …").
//	ge2n  gemini-embedding-2 with prefixes disabled — the control that isolates
//	      the prefix's own contribution from the model's.
//
// Nothing else varies: same transport (whatever GEMINI_PROVIDER says — it must
// be vertex on the production host), same texts, same order, same query set.
// Both models are recomputed from text in this run rather than reusing stored
// vectors, because the stored vectors are 001 artifacts and re-using them would
// compare a fresh model against a possibly stale corpus.
//
// READ-ONLY: the same three-layer contract as cmd/embedcmp — SELECT-only SQL
// behind a mechanical guard, a session with default_transaction_read_only=on,
// and no write of any kind. Vectors live in memory and in the local cache file;
// the database is never touched.
//
// Usage (on the server):
//
//	set -a; . /opt/khmer-ai-cs/.env-go; set +a
//	./embedab -eval /root/khmer-deploy/khmer_queries.json -user 1 \
//	    -cache /root/khmer-deploy/embedab-cache.json -out /root/khmer-deploy/embedab-report.json
//
// The cache makes a rerun (or a fourth variant) cheap: every (variant, text)
// vector is written once and reused, keyed by a hash of the exact bytes sent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/textutil"
)

// ─── flags ───────────────────────────────────────────────────────────────────

var (
	flagEval = flag.String("eval", "",
		"eval set: {\"queries\":[{\"query\":…,\"expect\":[doc_id,…],\"lang\":\"km\"}]} or a bare array (required)")
	flagTenant = flag.Int("user", 1,
		"tenant id to measure: the knowledge_documents.uploaded_by value")
	flagVariants = flag.String("variants", "ge1,ge2p,ge2n",
		"comma-separated variants: ge1, ge2p (prefixes), ge2n (no prefixes)")
	flagConcurrency = flag.Int("concurrency", 6,
		"parallel embedding calls (one text per call)")
	flagLimit = flag.Int("limit", 3,
		"topK used for the boundary of the head of the report (recall@1/5/10 and MRR are always reported)")
	flagFloor = flag.Float64("floor", 0.35,
		"production RAG_SIMILARITY_FLOOR to test both variants against")
	flagCache = flag.String("cache", "embedab-cache.json",
		"vector cache file (created/appended; makes reruns cheap)")
	flagOut = flag.String("out", "",
		"also write the raw report as JSON to this path")
	flagWarmBudget = flag.Duration("warm-budget", 90*time.Second,
		"budget for the pre-measurement keep-warm call (a cold connection would fail queries on the 5s production budget)")
	flagProgress = flag.Int("progress", 100, "print progress every N chunk embeddings (0 = silent)")
)

func main() {
	flag.Parse()
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "embedab:", err)
		os.Exit(1)
	}
}

// ─── the corpus ──────────────────────────────────────────────────────────────

type chunk struct {
	chunkID int32
	docID   int32
	title   string
	content string
	// storedModel is the embedding_model recorded on the document — a data
	// point about the corpus, not an input to anything.
	storedModel string
}

// corpusSQL loads every chunk of the tenant's READY documents, regardless of
// the model its stored vector came from: both variants are recomputed from text
// in this run, so the stored model is an annotation, not a filter (production's
// searchDense filters on it; this tool replaces that filter with "recompute").
const corpusSQL = `SELECT kc.chunk_id, kc.doc_id, kd.title, kc.content, COALESCE(kd.embedding_model, '')
FROM knowledge_chunks kc
JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id
WHERE kd.uploaded_by = $1
  AND kd.index_status = 'ready'
ORDER BY kc.chunk_id`

type corpusStats struct {
	Chunks       int
	Docs         int
	StoredModels map[string]int
	DocModels    map[string]int
}

func loadCorpus(ctx context.Context, pool *pgxpool.Pool, tenant int32) ([]chunk, corpusStats, error) {
	rows, err := pool.Query(ctx, corpusSQL, tenant)
	if err != nil {
		return nil, corpusStats{}, fmt.Errorf("load corpus: %w", err)
	}
	defer rows.Close()
	var out []chunk
	stats := corpusStats{StoredModels: map[string]int{}, DocModels: map[string]int{}}
	docSeen := map[int32]bool{}
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.chunkID, &c.docID, &c.title, &c.content, &c.storedModel); err != nil {
			return nil, corpusStats{}, err
		}
		out = append(out, c)
		stats.StoredModels[c.storedModel]++
		if !docSeen[c.docID] {
			docSeen[c.docID] = true
			stats.DocModels[c.storedModel]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, corpusStats{}, err
	}
	stats.Chunks = len(out)
	stats.Docs = len(docSeen)
	return out, stats, nil
}

// ─── variants ────────────────────────────────────────────────────────────────

type variant struct {
	name     string // flag spelling, e.g. "ge2p"
	label    string // report label
	model    string
	prefixes bool
	// titleAware: the DOCUMENT text is conditioned here, by this tool, with the
	// chunk's real title ("title: <title> | text: <content>") instead of the
	// service's generic document prefix. The service cannot do this today — it
	// embeds chunks without their title — so this variant measures whether
	// wiring titles through is worth the change. Queries keep the documented
	// query prefix.
	titleAware bool
	svc        *gemini.Service
}

func buildVariants(names []string) ([]variant, error) {
	var out []variant
	for _, name := range names {
		v := variant{name: strings.TrimSpace(name)}
		switch v.name {
		case "ge1":
			v.label = "GE1 (gemini-embedding-001, :predict, no task field)"
			v.model = gemini.EmbeddingModelGE1
			v.prefixes = false
		case "ge2p":
			v.label = "GE2+prefix (gemini-embedding-2, :embedContent, prefixes)"
			v.model = gemini.EmbeddingModelGE2
			v.prefixes = true
		case "ge2n":
			v.label = "GE2 no-prefix (gemini-embedding-2, :embedContent, bare text)"
			v.model = gemini.EmbeddingModelGE2
			v.prefixes = false
		case "ge2t":
			v.label = "GE2 title-aware (docs: 'title: <real title> | text: …', queries: query prefix)"
			v.model = gemini.EmbeddingModelGE2
			v.prefixes = true
			v.titleAware = true
		default:
			return nil, fmt.Errorf("unknown variant %q (want ge1, ge2p, ge2n or ge2t)", name)
		}
		svc := gemini.New("", "", 0)
		svc.SetEmbeddingModel(v.model)
		svc.SetEmbeddingPrefixes(v.prefixes)
		if v.titleAware {
			// The service would add "title: none | text: …" to documents; this
			// variant replaces that whole string with a real title, so the
			// service's document-side prefix is disabled and the query-side
			// prefix is kept (see documentsFor / the query loop).
			svc.SetEmbeddingPrefixes(false)
		}
		if !svc.IsConfigured() {
			return nil, fmt.Errorf("variant %s: service not configured (provider=%s) — source .env-go first",
				v.name, gemini.CredentialSourceOf())
		}
		v.svc = svc
		out = append(out, v)
	}
	return out, nil
}

// documentsFor builds the exact document texts this variant embeds: raw content
// for every variant except ge2t, which prepends the chunk's real title in the
// documented GE2 idiom.
func (v variant) documentsFor(corpus []chunk) []string {
	out := make([]string, len(corpus))
	for i, c := range corpus {
		if v.titleAware {
			out[i] = "title: " + strings.TrimSpace(c.title) + " | text: " + c.content
		} else {
			out[i] = c.content
		}
	}
	return out
}

// queryPrefixFor is the query-side conditioning this variant's service applies.
// ge2t runs with the service's prefixes OFF (they would double-condition the
// document side it builds itself), so it must prepend the documented query
// idiom here instead.
func (v variant) queryPrefixFor() string {
	if v.titleAware {
		return "task: search result | query: "
	}
	return ""
}

// ─── embedding with a cache ──────────────────────────────────────────────────

// embedTexts embeds every text once, in order, using at most `conc` calls in
// flight, consulting and filling the cache. It returns the vectors, per-call
// latencies for the calls that actually reached the network, and the number of
// cache hits. A failure aborts the run: a partially embedded corpus would make
// every metric below describe a corpus nobody has.
func embedTexts(ctx context.Context, v variant, texts []string, conc int, cache *vectorCache, progress int) ([][]float32, []float64, int, error) {
	out := make([][]float32, len(texts))
	misses := make([]int, 0, len(texts))
	for i, text := range texts {
		if cache.has(v.name, v.model, v.prefixes, text) {
			continue
		}
		misses = append(misses, i)
	}
	hits := len(texts) - len(misses)
	fmt.Printf("  [%s] %d texts: %d cached, %d to embed\n", v.name, len(texts), hits, len(misses))
	if len(misses) == 0 {
		for i, text := range texts {
			vec, _ := cache.get(v.name, v.model, v.prefixes, text)
			out[i] = vec
		}
		return out, nil, hits, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	var mu sync.Mutex
	var firstErr error
	latencies := make([]float64, 0, len(misses))
	done := 0
	for _, idx := range misses {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			vec, err := embedWithRetry(ctx, v.svc, texts[i])
			took := time.Since(start)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("variant %s text %d: %w", v.name, i, err)
					cancel()
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			out[i] = vec
			latencies = append(latencies, float64(took.Milliseconds()))
			done++
			if progress > 0 && done%progress == 0 {
				fmt.Printf("  [%s] %d/%d embedded (%dms last)\n", v.name, done, len(misses), took.Milliseconds())
			}
			mu.Unlock()
			cache.put(v.name, v.model, v.prefixes, texts[i], vec, took.Milliseconds())
		}(idx)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, hits, firstErr
	}
	for i, vec := range out {
		if vec == nil {
			// Either a cache get raced a put (impossible: gets happen before the
			// pool starts) or the context died mid-batch. Either way the corpus
			// is incomplete and the numbers would lie.
			return nil, nil, hits, fmt.Errorf("variant %s: vector %d was not produced", v.name, i)
		}
	}
	return out, latencies, hits, nil
}

// embedWithRetry wraps the single-text embed with a small retry for the two
// failures that are known-transient on this endpoint: 429 (quota bursts) and
// 5xx waves that outlived the client's own retry. Everything else propagates.
func embedWithRetry(ctx context.Context, svc *gemini.Service, text string) ([]float32, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 3 * time.Second):
			}
		}
		vec, err := svc.GenerateEmbedding(ctx, text)
		if err == nil {
			return vec, nil
		}
		last = err
		msg := err.Error()
		if !strings.Contains(msg, "429") && !strings.Contains(msg, "500") && !strings.Contains(msg, "503") {
			return nil, err
		}
	}
	return nil, last
}

// ─── eval set ────────────────────────────────────────────────────────────────

type evalCase struct {
	Query  string  `json:"query"`
	Expect []int32 `json:"expect"`
	Lang   string  `json:"lang,omitempty"`
	Note   string  `json:"note,omitempty"`
}

func loadEvalCases(path string) ([]evalCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read eval set: %w", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("eval set %s is empty", path)
	}
	var cases []evalCase
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(raw, &cases); err != nil {
			return nil, fmt.Errorf("parse eval set %s as an array: %w", path, err)
		}
	} else {
		var doc struct {
			Queries []evalCase `json:"queries"`
			Cases   []evalCase `json:"cases"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse eval set %s: %w", path, err)
		}
		cases = doc.Queries
		if len(cases) == 0 {
			cases = doc.Cases
		}
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("eval set %s has no cases", path)
	}
	for i, c := range cases {
		if strings.TrimSpace(c.Query) == "" {
			return nil, fmt.Errorf("eval set %s: case %d has an empty query", path, i)
		}
	}
	return cases, nil
}

// ─── run ─────────────────────────────────────────────────────────────────────

type variantRun struct {
	variant    variant
	corpus     []chunk
	vectors    [][]float32
	chunkCalls int
	hits       int
	latencies  []float64
	queryVecs  [][]float32
	queryErrs  []string
	queryMS    []float64
}

type caseOutcome struct {
	Index    int     `json:"index"`
	Query    string  `json:"query"`
	Lang     string  `json:"lang,omitempty"`
	Expect   []int32 `json:"expect"`
	Rank     int     `json:"rank"`
	TopDoc   int32   `json:"top_doc"`
	TopSim   float64 `json:"top_sim"`
	HitSim   float64 `json:"hit_sim"`
	EmbedErr bool    `json:"embed_err,omitempty"`
}

type variantReport struct {
	Name      string                    `json:"name"`
	Label     string                    `json:"label"`
	Chunks    int                       `json:"chunks"`
	CacheHits int                       `json:"cache_hits"`
	Network   int                       `json:"network_calls"`
	Metrics   map[string]any            `json:"metrics"`
	Latency   map[string]any            `json:"latency_ms"`
	PerLang   map[string]map[string]any `json:"per_lang,omitempty"`
	Outcomes  []caseOutcome             `json:"outcomes"`
	QueryErrs []string                  `json:"query_embed_errors,omitempty"`
}

type report struct {
	Started    time.Time       `json:"started"`
	Tenant     int32           `json:"tenant"`
	EvalPath   string          `json:"eval_path"`
	Cases      int             `json:"cases"`
	Corpus     corpusStats     `json:"corpus"`
	Transport  string          `json:"transport"`
	Region     string          `json:"region"`
	Floor      float64         `json:"floor"`
	Variants   []variantReport `json:"variants"`
	Diffs      []diffLine      `json:"diffs_ge1_vs_ge2p,omitempty"`
	Finished   time.Time       `json:"finished"`
	ElapsedSec float64         `json:"elapsed_sec"`
}

func run(ctx context.Context) error {
	started := time.Now()
	if strings.TrimSpace(*flagEval) == "" {
		return errors.New("-eval is required")
	}
	if *flagTenant <= 0 {
		return fmt.Errorf("-user must be positive, got %d", *flagTenant)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}
	cases, err := loadEvalCases(*flagEval)
	if err != nil {
		return err
	}
	pool, err := openReadOnlyPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	corpus, stats, err := loadCorpus(ctx, pool, int32(*flagTenant))
	if err != nil {
		return err
	}
	if len(corpus) == 0 {
		return fmt.Errorf("no chunks for tenant %d (uploaded_by): is -user right, and is the KB indexed?", *flagTenant)
	}

	variants, err := buildVariants(strings.Split(*flagVariants, ","))
	if err != nil {
		return err
	}
	cache, err := loadCache(*flagCache)
	if err != nil {
		return err
	}

	rep := report{
		Started: started, Tenant: int32(*flagTenant), EvalPath: *flagEval, Cases: len(cases),
		Corpus: stats, Floor: *flagFloor,
		Transport: string(gemini.CredentialSourceOf()), Region: transportRegion(variants),
	}

	fmt.Printf("embedab: tenant=%d chunks=%d docs=%d queries=%d\n", *flagTenant, stats.Chunks, stats.Docs, len(cases))
	fmt.Printf("corpus stored embedding_model: %v\n", stats.StoredModels)
	fmt.Printf("transport=%s region=%s\n", rep.Transport, rep.Region)

	var runs []variantRun
	for _, v := range variants {
		fmt.Printf("\n=== %s ===\n", v.label)
		if err := warm(ctx, v); err != nil {
			fmt.Printf("  warm-up: %v (continuing; query budget failures will be counted)\n", err)
		}
		docs := v.documentsFor(corpus)
		vecs, lat, hits, err := embedTexts(ctx, v, docs, *flagConcurrency, cache, *flagProgress)
		if err != nil {
			return err
		}
		if err := cache.save(*flagCache); err != nil {
			return err
		}
		qr := make([][]float32, len(cases))
		qerrs := make([]string, len(cases))
		qms := make([]float64, 0, len(cases))
		// The query text is conditioned here only for the variants whose
		// service runs with prefixes disabled (ge2t, which conditions the
		// document side itself); everything else goes through the service's own
		// conditionText, so the tool and the service cannot disagree about what
		// a "prefixed query" is except where that is the variable under test.
		queryPrefix := v.queryPrefixFor()
		for i, c := range cases {
			qStart := time.Now()
			vec, err := v.svc.GenerateQueryEmbedding(ctx, queryPrefix+c.Query)
			qms = append(qms, float64(time.Since(qStart).Milliseconds()))
			if err != nil {
				qerrs[i] = err.Error()
				continue
			}
			qr[i] = vec
		}
		runs = append(runs, variantRun{variant: v, corpus: corpus, vectors: vecs, chunkCalls: len(lat), hits: hits,
			latencies: lat, queryVecs: qr, queryErrs: qerrs, queryMS: qms})
	}

	// Score every variant and build the per-variant reports.
	for _, r := range runs {
		vr := scoreVariant(r, cases, corpus, *flagFloor)
		rep.Variants = append(rep.Variants, vr)
		printVariant(vr)
	}
	if len(rep.Variants) >= 2 {
		rep.Diffs = diffRuns(runs[0], runs[1], cases)
		printDiffs(rep.Diffs, runs[0].variant.name, runs[1].variant.name)
	}
	rep.Finished = time.Now()
	rep.ElapsedSec = rep.Finished.Sub(started).Seconds()

	corpusTokens := 0
	for _, c := range corpus {
		corpusTokens += len(c.content) / 4
	}
	printCost(rep, corpusTokens)
	if *flagOut != "" {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*flagOut, raw, 0o600); err != nil {
			return err
		}
		fmt.Printf("\nraw report: %s\n", *flagOut)
	}
	return nil
}

func transportRegion(variants []variant) string {
	if len(variants) == 0 {
		return ""
	}
	return variants[0].svc.Region()
}

// warm runs one real, un-cached embed call per variant so the queries are
// measured on a hot connection. It matters here more than anywhere: the
// production query budget is 5s and the global endpoint measures ~11s cold
// (see warm.go), so a cold variant would report budget failures that say
// nothing about the model. The probe uses the document conditioning on purpose:
// it is the text the corpus embedding will send anyway, so the connection it
// warms is the one the measurement uses.
func warm(ctx context.Context, v variant) error {
	_, err := embedWithRetry(ctx, v.svc, "warm-up probe សាកល្បង")
	return err
}

// ─── scoring ─────────────────────────────────────────────────────────────────

// scoreVariant ranks the whole corpus for each query and computes the metrics.
// Ranks are 1-based document ranks; 0 means "no expected document retrieved".
func scoreVariant(r variantRun, cases []evalCase, corpus []chunk, floor float64) variantReport {
	vr := variantReport{Name: r.variant.name, Label: r.variant.label, Chunks: len(corpus),
		CacheHits: r.hits, Network: r.chunkCalls, Outcomes: []caseOutcome{}}
	for i := range cases {
		if r.queryErrs[i] != "" {
			vr.QueryErrs = append(vr.QueryErrs, fmt.Sprintf("case %d: %s", i, r.queryErrs[i]))
		}
	}
	var ranks []int
	var topSims []float64
	var hitSims []float64
	perLang := map[string]*langAgg{}
	for i, c := range cases {
		out := caseOutcome{Index: i, Query: c.Query, Lang: c.Lang, Expect: c.Expect}
		if r.queryVecs[i] == nil {
			out.EmbedErr = true
			vr.Outcomes = append(vr.Outcomes, out)
			ranks = append(ranks, 0)
			addLang(perLang, c.Lang, 0, 0)
			continue
		}
		rank, topDoc, topSim, hitSim := rankQuery(r.queryVecs[i], r.vectors, corpus, c.Expect)
		out.Rank, out.TopDoc, out.TopSim, out.HitSim = rank, topDoc, topSim, hitSim
		vr.Outcomes = append(vr.Outcomes, out)
		ranks = append(ranks, rank)
		topSims = append(topSims, topSim)
		if hitSim > 0 {
			hitSims = append(hitSims, hitSim)
		}
		addLang(perLang, c.Lang, rank, topSim)
	}
	vr.Metrics = map[string]any{
		"recall@1":    float64(countWithin(ranks, 1)) / float64(len(ranks)),
		"recall@3":    float64(countWithin(ranks, 3)) / float64(len(ranks)),
		"recall@5":    float64(countWithin(ranks, 5)) / float64(len(ranks)),
		"recall@10":   float64(countWithin(ranks, 10)) / float64(len(ranks)),
		"mrr":         mean(reciprocalRanks(ranks)),
		"count@1":     countWithin(ranks, 1),
		"count@3":     countWithin(ranks, 3),
		"count@5":     countWithin(ranks, 5),
		"count@10":    countWithin(ranks, 10),
		"top1_mean":   mean(topSims),
		"top1_p10":    percentile(topSims, 0.10),
		"top1_p25":    percentile(topSims, 0.25),
		"top1_p50":    percentile(topSims, 0.50),
		"hit_mean":    mean(hitSims),
		"below_floor": countBelow(topSims, floor),
		"cases":       len(cases),
	}
	vr.Latency = map[string]any{
		"chunk_p50":   percentile(r.latencies, 0.50),
		"chunk_p90":   percentile(r.latencies, 0.90),
		"query_p50":   percentile(r.queryMS, 0.50),
		"query_p90":   percentile(r.queryMS, 0.90),
		"query_calls": len(r.queryMS),
	}
	vr.PerLang = map[string]map[string]any{}
	for lang, agg := range perLang {
		if lang == "" {
			lang = "(unlabelled)"
		}
		vr.PerLang[lang] = map[string]any{
			"cases":     agg.n,
			"recall@5":  float64(agg.hits5) / float64(agg.n),
			"mrr":       agg.rr / float64(agg.n),
			"top1_mean": agg.simSum / float64(agg.n),
		}
	}
	return vr
}

type langAgg struct {
	n      int
	hits5  int
	rr     float64
	simSum float64
}

func addLang(m map[string]*langAgg, lang string, rank int, topSim float64) {
	a := m[lang]
	if a == nil {
		a = &langAgg{}
		m[lang] = a
	}
	a.n++
	if rank >= 1 && rank <= 5 {
		a.hits5++
	}
	if rank > 0 {
		a.rr += 1 / float64(rank)
	}
	a.simSum += topSim
}

// rankQuery returns the rank of the best expected document, the top document
// and its similarity, and the similarity of the best expected chunk (0 when
// missed). Documents are ranked by their best chunk — the same collapse
// production's source assembly performs.
func rankQuery(query []float32, chunks [][]float32, corpus []chunk, expect []int32) (rank int, topDoc int32, topSim, hitSim float64) {
	best := map[int32]float64{}
	for i, vec := range chunks {
		if vec == nil {
			continue
		}
		sim := cosine(query, vec)
		if cur, ok := best[corpus[i].docID]; !ok || sim > cur {
			best[corpus[i].docID] = sim
		}
	}
	type ds struct {
		doc int32
		sim float64
	}
	list := make([]ds, 0, len(best))
	for d, s := range best {
		list = append(list, ds{d, s})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].sim != list[j].sim {
			return list[i].sim > list[j].sim
		}
		return list[i].doc < list[j].doc
	})
	if len(list) > 0 {
		topDoc, topSim = list[0].doc, list[0].sim
	}
	want := map[int32]bool{}
	for _, d := range expect {
		want[d] = true
	}
	for i, e := range list {
		if want[e.doc] {
			return i + 1, topDoc, topSim, e.sim
		}
	}
	return 0, topDoc, topSim, 0
}

// ─── no-database helper ──────────────────────────────────────────────────────

// openReadOnlyPool mirrors cmd/embedcmp: the session itself refuses writes.
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

// ─── printing ────────────────────────────────────────────────────────────────

func printVariant(vr variantReport) {
	fmt.Printf("\n── %s ──\n", vr.Label)
	fmt.Printf("  recall@1=%.3f recall@3=%.3f recall@5=%.3f recall@10=%.3f  MRR=%.4f\n",
		vr.Metrics["recall@1"], vr.Metrics["recall@3"], vr.Metrics["recall@5"], vr.Metrics["recall@10"], vr.Metrics["mrr"])
	fmt.Printf("  hits: @1=%v @3=%v @5=%v @10=%v of %v\n",
		vr.Metrics["count@1"], vr.Metrics["count@3"], vr.Metrics["count@5"], vr.Metrics["count@10"], vr.Metrics["cases"])
	fmt.Printf("  top1 sim: mean=%.4f p10=%.4f p25=%.4f p50=%.4f | hit sim mean=%.4f | below floor %.2f: %v\n",
		vr.Metrics["top1_mean"], vr.Metrics["top1_p10"], vr.Metrics["top1_p25"], vr.Metrics["top1_p50"],
		vr.Metrics["hit_mean"], *flagFloor, vr.Metrics["below_floor"])
	fmt.Printf("  latency ms: chunk p50=%v p90=%v (%d calls, %d cached) | query p50=%v p90=%v\n",
		vr.Latency["chunk_p50"], vr.Latency["chunk_p90"], vr.Network, vr.CacheHits,
		vr.Latency["query_p50"], vr.Latency["query_p90"])
	for lang, m := range vr.PerLang {
		fmt.Printf("  [%s] n=%v recall@5=%.3f mrr=%.3f top1_mean=%.3f\n",
			lang, m["cases"], m["recall@5"], m["mrr"], m["top1_mean"])
	}
}

type diffLine struct {
	Index int     `json:"index"`
	Query string  `json:"query"`
	Lang  string  `json:"lang,omitempty"`
	RankA int     `json:"rank_ge1"`
	RankB int     `json:"rank_ge2p"`
	Delta int     `json:"delta"` // RankA - RankB; positive = GE2p better
	SimA  float64 `json:"sim_ge1"`
	SimB  float64 `json:"sim_ge2p"`
}

func diffRuns(a, b variantRun, cases []evalCase) []diffLine {
	var out []diffLine
	for i := range cases {
		ra := rankOfCase(a, cases, i)
		rb := rankOfCase(b, cases, i)
		if ra == rb {
			continue
		}
		out = append(out, diffLine{Index: i, Query: cases[i].Query, Lang: cases[i].Lang,
			RankA: ra, RankB: rb, Delta: ra - rb})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Delta > out[j].Delta })
	return out
}

// rankOfCase recomputes one case's rank using the same function scoring uses,
// so the diff cannot disagree with the metrics it is explaining.
func rankOfCase(r variantRun, cases []evalCase, i int) int {
	if r.queryVecs[i] == nil {
		return 0
	}
	rank, _, _, _ := rankQuery(r.queryVecs[i], r.vectors, r.corpus, cases[i].Expect)
	return rank
}

func printDiffs(diffs []diffLine, nameA, nameB string) {
	gains, losses := 0, 0
	for _, d := range diffs {
		if d.Delta > 0 {
			gains++
		} else {
			losses++
		}
	}
	fmt.Printf("\n── per-query changes %s → %s: %d gains, %d losses ──\n", nameA, nameB, gains, losses)
	max := 25
	for i, d := range diffs {
		if i >= max {
			fmt.Printf("  … %d more\n", len(diffs)-max)
			break
		}
		verdict := "better"
		if d.Delta < 0 {
			verdict = "worse"
		}
		fmt.Printf("  %-5s #%d %s | %s → %s | %s\n", verdict, d.Index, ellipsis(d.Query, 60),
			rankStr(d.RankA), rankStr(d.RankB), d.Lang)
	}
}

func rankStr(r int) string {
	if r == 0 {
		return "miss"
	}
	return fmt.Sprintf("#%d", r)
}

func ellipsis(s string, n int) string { return textutil.Ellipsize(s, n) }

// printCost estimates the corpus re-embed cost per variant from the token
// heuristic production uses (chars/4) and the two list rates, so the switch
// decision carries its own price tag.
func printCost(rep report, corpusTokens int) {
	fmt.Printf("\n── cost estimate ──\n")
	fmt.Printf("corpus: ~%d tokens (chars/4 of %d chunks)\n", corpusTokens, rep.Corpus.Chunks)
	rates := map[string]float64{"ge1": 0.15, "ge2p": 0.20, "ge2n": 0.20}
	for _, vr := range rep.Variants {
		rate, ok := rates[vr.Name]
		if !ok {
			rate = 0.20
		}
		fmt.Printf("  %-5s re-embed corpus ≈ $%.4f | per full re-index @rate $%.2f/1M\n",
			vr.Name, float64(corpusTokens)/1e6*rate, rate)
	}
}

// ─── small stats ─────────────────────────────────────────────────────────────

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func countWithin(ranks []int, k int) int {
	n := 0
	for _, r := range ranks {
		if r >= 1 && r <= k {
			n++
		}
	}
	return n
}

func reciprocalRanks(ranks []int) []float64 {
	out := make([]float64, len(ranks))
	for i, r := range ranks {
		if r > 0 {
			out[i] = 1 / float64(r)
		}
	}
	return out
}

func countBelow(xs []float64, floor float64) int {
	n := 0
	for _, x := range xs {
		if x < floor {
			n++
		}
	}
	return n
}
