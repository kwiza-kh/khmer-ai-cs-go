// Command evalgen — generate a Khmer-centric retrieval eval set from the
// tenant's own knowledge base, using the deployment's chat model.
//
// WHY THIS EXISTS: a retrieval A/B needs queries whose correct answer is a
// KNOWN document. Hand-writing Khmer questions is error-prone and cannot cover
// 43 documents; sampling real customer messages covers what customers actually
// ask but does not name the expected document. This tool closes both gaps: for
// each document it asks the fast model for questions a customer would ask whose
// answer lives in that document, in Khmer, Chinese and English, and writes them
// as an eval set with `expect: [doc_id]`.
//
// THE MODEL'S ANSWER IS A CLAIM, NOT GROUND TRUTH. Two guards keep a bad
// question out of the measurement:
//
//  1. The prompt requires the answer to be present in the excerpt (a question
//     about something the document does not say would mark a good retriever
//     wrong).
//  2. Self-check filtering: the generated questions are embedded and ranked
//     against the corpus by the CALLER's normal retrieval, and a question whose
//     own source document does not come out on top at k<=3 is dropped as
//     ambiguous or unanswerable — unless -keep-all is passed. This doubles as
//     the cheapest possible sanity check of the whole corpus: a document no
//     question can retrieve is itself a signal.
//
// READ-ONLY against the database, like the rest of this toolkit.
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

var (
	flagUser        = flag.Int("user", 1, "tenant id: knowledge_documents.uploaded_by")
	flagPerDoc      = flag.Int("per-doc", 3, "questions to request per document (spread across languages)")
	flagLangs       = flag.String("langs", "km,zh,en", "comma-separated languages to generate (km,zh,en)")
	flagOut         = flag.String("out", "khmer_queries.json", "output eval set path")
	flagModel       = flag.String("model", "", "chat model (default: GEMINI_FAST_MODEL, else gemini-3.8-flash)")
	flagDocs        = flag.String("docs", "", "comma-separated doc ids to restrict to (default: all ready docs)")
	flagKeepAll     = flag.Bool("keep-all", false, "keep every generated question, skipping the self-retrieval check")
	flagFilter      = flag.Int("filter-k", 3, "self-check cutoff: keep a question only if its source doc is in the dense top-k")
	flagConcurrency = flag.Int("concurrency", 4, "parallel generation calls")
	flagTimeout     = flag.Duration("timeout", 90*time.Second, "per-generation-call timeout")
)

type doc struct {
	ID      int32
	Title   string
	Lang    string
	Content string
}

type question struct {
	Query  string  `json:"query"`
	Expect []int32 `json:"expect"`
	Lang   string  `json:"lang,omitempty"`
	Note   string  `json:"note,omitempty"`
}

func main() {
	flag.Parse()
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "evalgen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}
	pool, err := openReadOnlyPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	docs, err := loadDocs(ctx, pool, int32(*flagUser))
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("no ready documents for tenant %d", *flagUser)
	}
	if *flagDocs != "" {
		want := map[int32]bool{}
		for _, s := range strings.Split(*flagDocs, ",") {
			var id int32
			if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &id); err != nil {
				return fmt.Errorf("bad -docs entry %q: %w", s, err)
			}
			want[id] = true
		}
		kept := docs[:0]
		for _, d := range docs {
			if want[d.ID] {
				kept = append(kept, d)
			}
		}
		docs = kept
		if len(docs) == 0 {
			return errors.New("-docs matched no documents")
		}
	}

	svc := gemini.New("", *flagModel, 0)
	if !svc.IsConfigured() {
		return fmt.Errorf("chat service not configured (provider=%s): source .env-go on the host",
			gemini.CredentialSourceOf())
	}
	model := *flagModel
	if model == "" {
		model = gemini.FastModel
	}
	fmt.Printf("evalgen: %d docs, model=%s, langs=%s, per-doc=%d\n", len(docs), model, *flagLangs, *flagPerDoc)

	langs := strings.Split(*flagLangs, ",")
	for i := range langs {
		langs[i] = strings.TrimSpace(langs[i])
	}

	// Generate, bounded-parallel.
	type result struct {
		doc doc
		qs  []question
		err error
	}
	results := make([]result, len(docs))
	sem := make(chan struct{}, *flagConcurrency)
	var wg sync.WaitGroup
	for i, d := range docs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, d doc) {
			defer wg.Done()
			defer func() { <-sem }()
			qs, err := generateFor(ctx, svc, d, langs, *flagPerDoc, *flagTimeout)
			results[i] = result{doc: d, qs: qs, err: err}
			if err != nil {
				fmt.Printf("  doc %d (%s): %v\n", d.ID, d.Title, err)
			} else {
				fmt.Printf("  doc %d (%s): %d questions\n", d.ID, d.Title, len(qs))
			}
		}(i, d)
	}
	wg.Wait()

	var all []question
	var failed []int32
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, r.doc.ID)
			continue
		}
		all = append(all, r.qs...)
	}
	if len(all) == 0 {
		return fmt.Errorf("no questions generated (%d documents failed)", len(failed))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Expect[0] != all[j].Expect[0] {
			return all[i].Expect[0] < all[j].Expect[0]
		}
		return all[i].Query < all[j].Query
	})

	if !*flagKeepAll {
		kept, dropped, err := selfFilter(ctx, svc, all, *flagFilter)
		if err != nil {
			return err
		}
		fmt.Printf("self-check: kept %d, dropped %d (source doc not in top-%d)\n", len(kept), len(dropped), *flagFilter)
		for _, d := range dropped {
			fmt.Printf("  dropped [%s] %s (doc %d)\n", d.Lang, textutil.Ellipsize(d.Query, 80), d.Expect[0])
		}
		all = kept
	}

	out := struct {
		Meta    map[string]any `json:"meta"`
		Queries []question     `json:"queries"`
	}{Meta: map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"model":        model,
		"tenant":       *flagUser,
		"docs":         len(docs),
		"failed_docs":  failed,
		"note":         "generated from the knowledge base; expect[] is the source document of each question",
	}, Queries: all}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*flagOut, raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %d questions to %s\n", len(all), *flagOut)
	return nil
}

const evalgenPromptTemplate = `You write retrieval evaluation questions for a customer-support knowledge base of a Cambodian construction-materials company (EPS insulation). The customers write in Khmer, Chinese or English.

DOCUMENT (id %d, title: %s):
%s

TASK: write exactly %d questions, as a JSON array, that a real customer would plausibly ask and whose answer is stated IN THE DOCUMENT ABOVE.
Requirements:
- Distribute across these languages: %s (roughly evenly).
- Each question must be answerable from the document ALONE — do not ask about anything the document does not state.
- Vary the phrasing: some questions use the document's own words, some paraphrase, some ask for a specific number/term/process.
- Questions must NOT contain the document title verbatim as the only clue, and must not mention "the document".
- Khmer questions must be natural spoken Khmer (as a Cambodian customer would type), not a machine translation of the Chinese.

Reply with ONLY a JSON array: [{"q": "<question>", "lang": "km|zh|en"}]`

func generateFor(ctx context.Context, svc *gemini.Service, d doc, langs []string, perDoc int, timeout time.Duration) ([]question, error) {
	prompt := fmt.Sprintf(evalgenPromptTemplate, d.ID, d.Title, textutil.Ellipsize(d.Content, 8000), perDoc, strings.Join(langs, ", "))
	reply, ok := svc.GenerateFastMax(ctx, prompt, timeout, 2048)
	if !ok {
		return nil, errors.New("generation call failed (timeout or model error)")
	}
	trimmed := strings.TrimSpace(reply)
	if i := strings.Index(trimmed, "["); i >= 0 {
		if j := strings.LastIndex(trimmed, "]"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var parsed []struct {
		Q    string `json:"q"`
		Lang string `json:"lang"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("parse generated questions: %w", err)
	}
	var out []question
	for _, p := range parsed {
		q := strings.TrimSpace(p.Q)
		if q == "" {
			continue
		}
		if len([]rune(q)) < 6 {
			continue
		}
		out = append(out, question{Query: q, Expect: []int32{d.ID}, Lang: normalizeLang(p.Lang), Note: d.Title})
	}
	if len(out) == 0 {
		return nil, errors.New("no usable questions in the reply")
	}
	return out, nil
}

func normalizeLang(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "km", "kh", "khmer", "ខ្មែរ":
		return "km"
	case "zh", "cn", "chinese", "中文":
		return "zh"
	case "en", "english":
		return "en"
	default:
		return strings.ToLower(strings.TrimSpace(l))
	}
}

// selfFilter ranks every question against the whole corpus with the client's
// normal retrieval and keeps only those whose source document comes back within
// the top k. It reuses the running service's embeddings, so it also warms the
// query cache the A/B run will use.
func selfFilter(ctx context.Context, svc *gemini.Service, qs []question, k int) ([]question, []question, error) {
	corpus, err := loadCorpusTexts(ctx, svc)
	if err != nil {
		return nil, nil, err
	}
	// Embed the corpus once with the CURRENT model (001 until the switch), so
	// the check reflects production retrieval at the moment of generation.
	chunkVecs, _, err := embedAll(ctx, svc, corpus.texts, 6)
	if err != nil {
		return nil, nil, err
	}
	var kept, dropped []question
	for _, q := range qs {
		vec, err := svc.GenerateQueryEmbedding(ctx, q.Query)
		if err != nil {
			dropped = append(dropped, q)
			continue
		}
		best := map[int32]float64{}
		for i, cv := range chunkVecs {
			if cv == nil {
				continue
			}
			s := cosine(vec, cv)
			if cur, ok := best[corpus.docIDs[i]]; !ok || s > cur {
				best[corpus.docIDs[i]] = s
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
		sort.Slice(list, func(i, j int) bool { return list[i].sim > list[j].sim })
		hit := -1
		for i, e := range list {
			if i >= k {
				break
			}
			if e.doc == q.Expect[0] {
				hit = i
				break
			}
		}
		if hit >= 0 {
			kept = append(kept, q)
		} else {
			dropped = append(dropped, q)
		}
	}
	return kept, dropped, nil
}

type corpusTexts struct {
	texts  []string
	docIDs []int32
}

func loadCorpusTexts(ctx context.Context, svc *gemini.Service) (corpusTexts, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	pool, err := openReadOnlyPool(ctx, dsn)
	if err != nil {
		return corpusTexts{}, err
	}
	defer pool.Close()
	rows, err := pool.Query(ctx,
		`SELECT kc.doc_id, kc.content FROM knowledge_chunks kc
		 JOIN knowledge_documents kd ON kc.doc_id = kd.doc_id
		 WHERE kd.uploaded_by = $1 AND kd.index_status = 'ready'
		 ORDER BY kc.chunk_id`, int32(*flagUser))
	if err != nil {
		return corpusTexts{}, err
	}
	defer rows.Close()
	var out corpusTexts
	for rows.Next() {
		var id int32
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return corpusTexts{}, err
		}
		out.docIDs = append(out.docIDs, id)
		out.texts = append(out.texts, text)
	}
	return out, rows.Err()
}

// embedAll embeds texts with a bounded pool, one call per text (GE2 has no
// batch entry point, so the tool always takes the per-text path; being uniform
// keeps the two models' behaviour identical).
func embedAll(ctx context.Context, svc *gemini.Service, texts []string, conc int) ([][]float32, int, error) {
	out := make([][]float32, len(texts))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	calls := 0
	for i := range texts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			vec, err := svc.GenerateEmbedding(ctx, texts[i])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("embed %d: %w", i, err)
				}
				return
			}
			out[i] = vec
			calls++
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, calls, firstErr
	}
	return out, calls, nil
}

func loadDocs(ctx context.Context, pool *pgxpool.Pool, tenant int32) ([]doc, error) {
	rows, err := pool.Query(ctx,
		`SELECT doc_id, title, COALESCE(language, 'km'), content FROM knowledge_documents
		 WHERE uploaded_by = $1 AND index_status = 'ready' ORDER BY doc_id`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.ID, &d.Title, &d.Lang, &d.Content); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

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
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

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
