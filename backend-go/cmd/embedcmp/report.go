package main

// Rendering. One function writes the whole report to an io.Writer, so main
// stays wiring and the formatting stays testable.

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// reportInput is everything the report needs, gathered by main. It is a plain
// struct rather than a set of arguments because the alternative is a
// fifteen-parameter function whose call site is unreadable.
type reportInput struct {
	EvalPath   string
	Tenant     int32
	Limit      int
	Floor      float64
	Model      string
	Stats      corpusStats
	UsedChunks int
	RankedDocs int
	Sampled    bool
	MaxChunks  int
	EmbedCalls int
	EmbedTime  time.Duration
	TotalTime  time.Duration
	Vertex     string
	// KeySource records WHERE path A's AI Studio key came from (the default
	// model_configs row, or GEMINI_API_KEY). Provenance matters for a measurement
	// that claims to BE production retrieval: a key from a different project or
	// relay is a different service.
	KeySource string
	// StudioBase is the endpoint path A actually called. It is printed because
	// this deployment routes AI Studio through a relay (GEMINI_API_BASE), and a
	// report whose numbers came from a direct call instead cannot be compared
	// with one whose numbers came through the relay.
	StudioBase  string
	A, B        pathMetrics
	Diffs       []queryDiff
	Titles      map[int32]string
	EmptyExpect []int
	Unmatched   []int
}

// oneLine makes arbitrary text safe to print on one table row: control
// characters (a query in the eval set may carry newlines) become spaces, and the
// result is truncated to max runes — counted in RUNES, not bytes, so truncating
// a Khmer query cannot split a code point and print mojibake.
func oneLine(s string, max int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, s)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	runes := []rune(cleaned)
	if max > 0 && len(runes) > max {
		if max == 1 {
			return "…"
		}
		return string(runes[:max-1]) + "…"
	}
	return cleaned
}

// writeReport prints the run header, the two summaries and the diff table.
func writeReport(w io.Writer, in reportInput) {
	printHeader(w, in)
	printSummary(w, in)
	printDiffs(w, in)
	printNotes(w, in)
}

func printHeader(w io.Writer, in reportInput) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	p("embedcmp — retrieval quality under two embedding conventions (AI Studio+task  vs  Vertex+no task)")
	p("")
	p("READ-ONLY  every statement this run sends is a SELECT, and the connection is opened with")
	p("           default_transaction_read_only=on, so Postgres itself would reject a write.")
	p("           Path B's document vectors are computed in memory and are NEVER stored.")
	p("")
	p("eval set : %s   cases=%d", in.EvalPath, in.A.Cases)
	if len(in.EmptyExpect) > 0 {
		p("           %d case(s) have an empty expect list and count as misses in BOTH paths: %s",
			len(in.EmptyExpect), indexList(in.EmptyExpect))
	}
	p("tenant   : user_id=%d   topK(limit)=%d   similarity floor=%.2f (diagnostic only — it never filters)",
		in.Tenant, in.Limit, in.Floor)
	p("corpus   : %d chunks / %d documents in the dense candidate set", in.Stats.Chunks, in.Stats.Docs)
	p("           filter: uploaded_by=%d AND index_status='ready' AND embedding_model=%q",
		in.Tenant, in.Model)
	p("           skipped: %d chunk(s) with a NULL vector", in.Stats.NoVector)
	if in.Stats.DocsTotal > 0 {
		p("           tenant documents: %d total, %d ready; %d ready with another embedding_model → %d unreachable by dense search",
			in.Stats.DocsTotal, in.Stats.DocsReady, in.Stats.DocsOtherCell,
			in.Stats.DocsNotReady+in.Stats.DocsOtherCell)
	}
	if in.Sampled {
		p("sampling : -max-chunks=%d → %d chunks kept by stride (NOT comparable to a full-corpus baseline)",
			in.MaxChunks, in.UsedChunks)
	} else {
		p("sampling : none (full corpus)")
	}
	p("path A   : queries  = AI Studio RETRIEVAL_QUERY via %s", in.StudioBase)
	p("           key      = %s", in.KeySource)
	p("           documents= knowledge_chunks.embedding as stored (AI Studio, RETRIEVAL_DOCUMENT)")
	p("path B   : queries  = Vertex :predict, NO task field")
	p("           documents= recomputed in memory this run (Vertex :predict, NO task field)")
	p("vertex   : %s", in.Vertex)
	p("           corpus embedding: %d chunks in %d :predict call(s), %s",
		in.UsedChunks, in.EmbedCalls, in.EmbedTime.Round(time.Millisecond))
	p("")
}

func printSummary(w io.Writer, in reportInput) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	p("SUMMARY — one ranking implementation for both paths, ranked over %d documents in the corpus ranking",
		in.RankedDocs)
	p("          recall@k is strict: the expected document is WITHIN the top k.")
	p("")
	p("  path  embedding convention                             recall@5        recall@10       MRR     mean top1 sim  top1<floor  invisible@20")
	row := func(m pathMetrics, label string) {
		p("  %-5s %-48s %2d/%d (%3.0f%%)  %2d/%d (%3.0f%%)  %.3f   %.3f          %-11d %d",
			m.Name, label,
			m.Recall5, m.Cases, pct(m.Recall5, m.Cases),
			m.Recall10, m.Cases, pct(m.Recall10, m.Cases),
			m.MRR, m.MeanTop1, m.BelowFloor, m.BeyondCandidateWindow)
	}
	row(in.A, "AI Studio, task-conditioned (today)")
	row(in.B, "Vertex, unconditional (after migration)")
	p("  %-5s %-48s %+d (%+.1f pp)  %+d (%+.1f pp)  %+.3f   %+.3f",
		"Δ", "B − A",
		in.B.Recall5-in.A.Recall5, pct(in.B.Recall5, in.A.Cases)-pct(in.A.Recall5, in.A.Cases),
		in.B.Recall10-in.A.Recall10, pct(in.B.Recall10, in.A.Cases)-pct(in.A.Recall10, in.A.Cases),
		in.B.MRR-in.A.MRR, in.B.MeanTop1-in.A.MeanTop1)
	p("")
}

func printDiffs(w io.Writer, in reportInput) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	counts := countVerdicts(in.Diffs)
	if len(in.Diffs) == 0 {
		p("PER-QUERY DIFFERENCES — none: every query landed on the same rank in both paths.")
		p("")
		return
	}
	p("PER-QUERY DIFFERENCES — %d of %d cases changed (worst first): %d regressed, %d improved, %d uncited reorderings.",
		len(in.Diffs), in.A.Cases, counts.Regressed, counts.Improved, counts.Uncited)
	p("                      'regressed' = a citation lost or damaged; 'uncited' = the order moved between two")
	p("                      positions that were never cited, which changes no answer. Unchanged cases are not listed,")
	p("                      and 'miss' = the expected document is nowhere in the ranking.")
	p("")
	p("  %-4s %-10s %-14s %-17s %-10s %s", "#", "verdict", "rank A→B", "rr A→B", "top1 A→B", "query")
	for _, d := range in.Diffs {
		p("  %-4d %-10s %-14s %-17s %-10s %s",
			d.Case.Index, d.Verdict,
			fmt.Sprintf("%s → %s", rankLabel(d.Case.RankA), rankLabel(d.Case.RankB)),
			fmt.Sprintf("%.3f → %.3f", reciprocalRank(d.Case.RankA), reciprocalRank(d.Case.RankB)),
			fmt.Sprintf("%d→%d", d.Case.TopA, d.Case.TopB),
			oneLine(d.Case.Query, 64))
	}
	p("")

	// The doc-id → title legend, so a reader can tell whether a slipped query
	// lost an actual answer or swapped one plausible document for another. Only
	// documents that appear in the table above are listed.
	legend := map[int32]bool{}
	for _, d := range in.Diffs {
		for _, id := range []int32{d.Case.TopA, d.Case.TopB} {
			if id != 0 {
				legend[id] = true
			}
		}
	}
	if len(legend) > 0 {
		ids := make([]int32, 0, len(legend))
		for id := range legend {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		p("  documents named above (top1 A→B):")
		for _, id := range ids {
			p("    %-6d %s", id, oneLine(in.Titles[id], 72))
		}
		p("")
	}
}

func printNotes(w io.Writer, in reportInput) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format+"\n", args...) }

	p("NOTES — read these before quoting a number")
	p("  * recall@10 here is the strict definition. cmd/rageval counts a case as recall@10 whenever the")
	p("    expected document appears ANYWHERE in its (floored, truncated) dense list, so its recall@10 is")
	p("    larger by construction. recall@5 and MRR use the same definition in both tools.")
	p("  * invisible@20 = cases whose expected document is missing or ranked deeper than %d (4×topK, the", ragevalCandidateDepth)
	p("    depth rageval's dense leg can see). Those cases score 0 for rageval and >0 here — which is the")
	p("    only reason this tool's MRR may exceed a rageval-recorded baseline.")
	p("  * mean top1 sim and top1<floor describe the similarity SCALE, not the ranking. Production's")
	p("    RAG_SIMILARITY_FLOOR / _RATIO are absolute values calibrated on path A's scale: if path B's")
	p("    scale is lower, its dense leg empties earlier even where recall is unchanged.")
	if len(in.Unmatched) > 0 {
		p("  * WARNING %d case(s) expect a document that has no chunk in the corpus at all — guaranteed misses", len(in.Unmatched))
		p("    in BOTH paths, which dilutes any recall difference: %s", indexList(in.Unmatched))
	}
	p("  * elapsed %s", in.TotalTime.Round(time.Millisecond))
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

// rankLabel prints a rank, or "miss" for 0 (absent from the ranking). Printing
// 0 would read as "rank zero", which is not a rank.
func rankLabel(rank int) string {
	if rank <= 0 {
		return "miss"
	}
	return fmt.Sprintf("%d", rank)
}

// indexList renders case indices compactly, capped so one bad eval set cannot
// print hundreds of numbers.
func indexList(idx []int) string {
	const cap = 12
	parts := make([]string, 0, cap+1)
	for i, v := range idx {
		if i == cap {
			parts = append(parts, fmt.Sprintf("… (+%d more)", len(idx)-cap))
			break
		}
		parts = append(parts, fmt.Sprintf("%d", v))
	}
	return strings.Join(parts, ", ")
}
