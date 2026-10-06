package rag

import (
	"context"
	"fmt"
	"os"
	"slices"

	"khmer-ai-cs-go/internal/gemini"
)

// Eval helpers exported for cmd/rageval: retrieval quality and threshold
// calibration over a labeled query set. Not used by the request path.

type EvalCase struct {
	Query  string  `json:"query"`
	Expect []int32 `json:"expect"`
}

type LegMetrics struct {
	Name     string  `json:"name"`
	Recall5  int     `json:"recall5"`
	Recall10 int     `json:"recall10"`
	MRR      float64 `json:"mrr"`
}

type SweepConfig struct {
	Floor float64 `json:"floor"`
	Ratio float64 `json:"ratio"`
	Skip  float64 `json:"skip"`
}

type SweepRow struct {
	Config        SweepConfig `json:"config"`
	Recall5       int         `json:"recall5"`
	Recall10      int         `json:"recall10"`
	MRR           float64     `json:"mrr"`
	AvgSources    float64     `json:"avg_sources"`
	AvgCandidates float64     `json:"avg_candidates"`
	RerankRuns    int         `json:"rerank_runs"`
	RerankSkips   int         `json:"rerank_skips"`
}

type SweepReport struct {
	Cases int          `json:"cases"`
	Limit int          `json:"limit"`
	Legs  []LegMetrics `json:"legs"`
	Rows  []SweepRow   `json:"rows"`
}

type evalGathered struct {
	expect  []int32
	dense   []SearchChunk
	lexical []SearchChunk
	trigram []SearchChunk
}

// EvalRetrieval gathers each query's dense/lexical/trigram candidates once,
// reports per-leg metrics, then replays RRF fusion + the relative-similarity
// gate in memory for every SweepConfig. Rerank is deliberately skipped so the
// sweep isolates retrieval gating (and costs one embedding pass, not N).
func (s *Service) EvalRetrieval(ctx context.Context, userID int32, cases []EvalCase, configs []SweepConfig, limit int64) (*SweepReport, error) {
	if limit <= 0 {
		limit = DefaultTopK
	}
	candidateLimit := searchCandidateLimit(limit)
	if int64(rerankWindow) > candidateLimit {
		candidateLimit = int64(rerankWindow)
	}
	gs := make([]evalGathered, 0, len(cases))
	for _, c := range cases {
		g := evalGathered{expect: c.Expect}
		vec, err := s.Gemini.GenerateQueryEmbedding(ctx, c.Query)
		if err == nil {
			rows, derr := s.searchDense(ctx, userID, gemini.FormatVector(vec), candidateLimit)
			if derr == nil {
				g.dense = rows
			}
		} else if s.Logger != nil {
			s.Logger.Warn("eval dense leg failed", "query", c.Query, "error", err.Error())
		}
		rows, lerr := s.searchLexical(ctx, userID, c.Query, candidateLimit)
		if lerr == nil {
			g.lexical = rows
		}
		if hasLexicalScript(NormalizeText(c.Query)) {
			tr, terr := s.searchTrigram(ctx, userID, c.Query, candidateLimit)
			if terr == nil {
				g.trigram = tr
			}
		}
		gs = append(gs, g)
	}

	rep := &SweepReport{Cases: len(cases), Limit: int(limit)}
	rep.Legs = []LegMetrics{
		legMetrics("dense", gs, limit, func(g evalGathered) []SearchChunk { return g.dense }),
		legMetrics("lexical", gs, limit, func(g evalGathered) []SearchChunk { return g.lexical }),
		legMetrics("trigram", gs, limit, func(g evalGathered) []SearchChunk { return g.trigram }),
	}
	for _, cfg := range configs {
		row := SweepRow{Config: cfg}
		totalSources, totalCandidates := 0, 0
		h5, h10 := 0, 0
		mrr := 0.0
		for _, g := range gs {
			fused := fuseSearchResults(g.dense, g.lexical, g.trigram)
			fused = gateFused(fused, cfg.Floor, cfg.Ratio)
			totalCandidates += len(fused)
			fused = diversifyByDoc(fused, maxChunksPerDoc)
			if int64(len(fused)) > limit {
				fused = fused[:limit]
			}
			totalSources += len(fused)
			if rerankSkipped(g, fused, cfg.Skip) {
				row.RerankSkips++
			} else {
				row.RerankRuns++
			}
			a, b, m := rankScore(g.expect, chunkDocIDs(fused))
			h5 += a
			h10 += b
			mrr += m
		}
		row.Recall5, row.Recall10, row.MRR = h5, h10, mrr/float64(len(gs))
		row.AvgSources = float64(totalSources) / float64(len(gs))
		row.AvgCandidates = float64(totalCandidates) / float64(len(gs))
		rep.Rows = append(rep.Rows, row)
	}
	return rep, nil
}

func legMetrics(name string, gs []evalGathered, limit int64, pick func(evalGathered) []SearchChunk) LegMetrics {
	m := LegMetrics{Name: name}
	for _, g := range gs {
		h5, h10, mrr := rankScore(g.expect, chunkDocIDs(pick(g)))
		m.Recall5 += h5
		m.Recall10 += h10
		m.MRR += mrr
	}
	if len(gs) > 0 {
		m.MRR = m.MRR / float64(len(gs))
	}
	_ = limit
	return m
}

func rankScore(expect, ranked []int32) (int, int, float64) {
	best := 0
	for i, id := range ranked {
		if slices.Contains(expect, id) {
			best = i + 1
			break
		}
	}
	if best == 0 {
		return 0, 0, 0
	}
	h5 := 0
	if best <= 5 {
		h5 = 1
	}
	return h5, 1, 1.0 / float64(best)
}

func chunkDocIDs(chunks []SearchChunk) []int32 {
	seen := make(map[int32]bool, len(chunks))
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

// EvalPipeline runs the production Search path for each case - including the
// LLM rerank decision driven by RAG_RERANK_SKIP / RAG_RERANK_MIN - and reports
// ranking quality plus the average number of returned sources. Run it with
// different RAG_RERANK_SKIP values to see what reranking actually buys.
func (s *Service) EvalPipeline(ctx context.Context, userID int32, cases []EvalCase, limit int64) (LegMetrics, float64, error) {
	m := LegMetrics{Name: "pipeline"}
	total := 0
	for _, c := range cases {
		srcs, err := s.Search(ctx, userID, c.Query, limit)
		if err != nil {
			continue
		}
		ids := make([]int32, 0, len(srcs))
		for _, src := range srcs {
			ids = append(ids, src.DocID)
		}
		total += len(srcs)
		if os.Getenv("RAGEVAL_VERBOSE") != "" {
			fmt.Fprintf(os.Stderr, "  sources=%d query=%s\n", len(srcs), c.Query)
		}
		h5, h10, mrr := rankScore(c.Expect, dedupeIDs(ids))
		m.Recall5 += h5
		m.Recall10 += h10
		m.MRR += mrr
	}
	if len(cases) > 0 {
		m.MRR = m.MRR / float64(len(cases))
	}
	return m, float64(total) / float64(len(cases)), nil
}

func dedupeIDs(ids []int32) []int32 {
	seen := make(map[int32]bool, len(ids))
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// rerankSkipped mirrors Search's rerank-skip rule: a clear dense leader, or
// dense and lexical agreeing on the same leader with a solid score.
func rerankSkipped(g evalGathered, fused []SearchChunk, skip float64) bool {
	var topDense *float64
	for _, c := range fused {
		if c.DenseSim != nil {
			if topDense == nil || *c.DenseSim > *topDense {
				v := *c.DenseSim
				topDense = &v
			}
		}
	}
	if topDense == nil {
		return false
	}
	top := *topDense
	if top >= skip {
		return true
	}
	topAgrees := len(g.dense) > 0 && len(g.lexical) > 0 && g.dense[0].ChunkID == g.lexical[0].ChunkID
	return topAgrees && top >= 0.60
}

// gateFused replays Search's relative-similarity gate with explicit knobs.
func gateFused(fused []SearchChunk, floor, ratio float64) []SearchChunk {
	var topDense *float64
	for _, c := range fused {
		if c.DenseSim != nil {
			if topDense == nil || *c.DenseSim > *topDense {
				v := *c.DenseSim
				topDense = &v
			}
		}
	}
	if topDense == nil {
		return fused
	}
	minDense := *topDense * ratio
	if floor > minDense {
		minDense = floor
	}
	kept := make([]SearchChunk, 0, len(fused))
	for _, c := range fused {
		if c.DenseSim == nil || *c.DenseSim >= minDense {
			kept = append(kept, c)
		}
	}
	return kept
}
