package main

// Ranking metrics and the per-query A/B diff.
//
// Everything in this file is PURE: slices of ints and floats in, numbers out.
// No database, no network, no clock — which is what lets the unit tests pin the
// definitions down with fake vectors instead of burning API calls.
//
// The metric definitions here differ from cmd/rageval in exactly one place, and
// it is deliberate. rageval's rankScore returns "recall@10 = 1" whenever the
// expected document appears ANYWHERE in the ranked list, because its dense leg
// is already truncated to candidateLimit (= 4*topK, capped at 40, so 20 for
// topK=5) and floored. This tool ranks the WHOLE corpus, so recall@k here is the
// strict definition — the expected document is within the top k — and recall@10
// is therefore not comparable to rageval's recall@10 column. recall@5 and MRR
// agree between the two, which is what lets the recorded baseline
// (dense recall@5 37/45, MRR 0.655) be reproduced as a sanity check.

// caseResult is one eval query under both conventions. Ranks are 1-based
// positions of the best-matching expected document in that path's document
// ranking; 0 means "no expected document was retrieved at all".
type caseResult struct {
	Index int
	Query string
	RankA int
	RankB int
	TopA  int32   // best-ranked document's id, path A
	TopB  int32   // best-ranked document's id, path B
	SimA  float64 // cosine of the best-ranked CHUNK, path A
	SimB  float64
}

// pathMetrics summarizes one path over the whole eval set.
type pathMetrics struct {
	Name     string
	Cases    int
	Recall5  int
	Recall10 int
	// MRR is the mean reciprocal rank over ALL cases, misses counting as 0 —
	// the same denominator rageval uses, so the two are comparable.
	MRR float64
	// MeanTop1 is the mean cosine similarity of each query's best chunk. It is
	// NOT a quality metric; it is here because the similarity SCALE matters to
	// the production gate (RAG_SIMILARITY_FLOOR / _RATIO are absolute numbers
	// calibrated on AI Studio similarities), and a convention that shifts the
	// scale can empty the dense leg even when the ranking is unchanged.
	MeanTop1 float64
	// BelowFloor counts queries whose best similarity is under the production
	// similarity floor — i.e. queries whose dense leg would return nothing.
	BelowFloor int
	// BeyondCandidateWindow counts cases this path could not have been credited
	// for by cmd/rageval's dense leg, whose SQL is truncated to
	// 4*topK (capped at 40) rows: rank 0, or a rank deeper than that window.
	// It is the reconciliation number for "my MRR is higher than the recorded
	// baseline".
	BeyondCandidateWindow int
}

// ragevalCandidateDepth is 4*topK capped at 40 (rag.searchCandidateFactor /
// maxSearchCandidates) evaluated at the baseline's topK=5. Hard-coded because it
// describes ANOTHER tool's window, not a knob of this one.
const ragevalCandidateDepth = 20

// metricsFor computes one path's metrics. pick selects that path's rank and
// top-1 similarity from a case, so the same code scores both paths and neither
// can drift from the other.
func metricsFor(name string, cases []caseResult, pick func(caseResult) (rank int, topSim float64), floor float64) pathMetrics {
	m := pathMetrics{Name: name, Cases: len(cases)}
	for _, c := range cases {
		rank, sim := pick(c)
		if hitAtK(rank, 5) {
			m.Recall5++
		}
		if hitAtK(rank, 10) {
			m.Recall10++
		}
		m.MRR += reciprocalRank(rank)
		if rank == 0 || rank > ragevalCandidateDepth {
			m.BeyondCandidateWindow++
		}
		m.MeanTop1 += sim
		if sim < floor {
			m.BelowFloor++
		}
	}
	if len(cases) > 0 {
		m.MRR /= float64(len(cases))
		m.MeanTop1 /= float64(len(cases))
	}
	return m
}

// hitAtK is the recall@k decision for ONE case: was the expected document within
// the top k? bestRank is 1-based, and 0 means absent.
//
// This function is THE definition of recall@k in this tool — metricsFor, the
// tests and the notes in the report all refer to it, so there is no second
// implementation to drift. k <= 0 is false by construction rather than by luck,
// and "absent" can never be counted as a hit at any k.
func hitAtK(bestRank, k int) bool {
	return k > 0 && bestRank > 0 && bestRank <= k
}

// reciprocalRank is a case's contribution to MRR: 1/rank, and exactly 0 for an
// absent document — a miss is averaged in as 0, never dropped from the
// denominator, which is what keeps MRR comparable between tools and eval sets of
// different sizes.
func reciprocalRank(bestRank int) float64 {
	if bestRank <= 0 {
		return 0
	}
	return 1 / float64(bestRank)
}

// bestRank returns the 1-based position of the first expected document in a
// document ranking, or 0 when none of them is present.
//
// The matching semantics are the ones the eval set is written in: `expect` holds
// DOCUMENT ids, the ranking holds document ids, and a hit is a document that the
// tenant actually labelled as an answer. Two consequences worth stating, because
// both are easy to get wrong:
//
//   - a chunk from an expected document counts even if it is not the chunk a
//     human would have picked — the label is per document, so any chunk of that
//     document retrieves it;
//   - a chunk from a NON-expected document never counts, however similar its
//     text looks, and a duplicate document id in the ranking cannot occupy two
//     positions (docRanking has already collapsed them).
func bestRank(rankedDocs []int32, expect []int32) int {
	if len(expect) == 0 {
		return 0
	}
	for i, docID := range rankedDocs {
		for _, want := range expect {
			if docID == want {
				return i + 1
			}
		}
	}
	return 0
}

// Verdicts for the per-query diff.
//
// The sev* constants below ARE the print order (ascending = worst first) and are
// grouped so that citation-affecting changes come before uncited reshuffling —
// see diffCases.
const (
	verdictFellOut  = "FELL OUT" // was cited (top-K), is not any more
	verdictLost     = "LOST"     // was somewhere in the ranking, now absent entirely
	verdictDropped  = "dropped"  // cited both times, ranked worse now
	verdictEntered  = "ENTERED"  // was uncited, is cited now
	verdictImproved = "improved" // cited both times, ranked better now
	verdictShifted  = "shifted"  // uncited both times, order moved down
	verdictGained   = "GAINED"   // was absent from the ranking, now present but still uncited
)

// Severity = table order. The gaps are deliberate: a new verdict can be slotted
// between two classes without renumbering everything.
const (
	sevFellOut  = 0
	sevLost     = 10
	sevDropped  = 20
	sevEntered  = 30
	sevImproved = 40
	sevShifted  = 50
	sevGained   = 60
)

// queryDiff is one query whose outcome changed between the two paths. Queries
// with the same verdict-pair and the same rank are not listed at all: the table
// is meant to be short enough to read, and "nothing changed" is the summary
// line, not 40 identical rows.
type queryDiff struct {
	Case    caseResult
	Verdict string
	// Delta is rankB - rankA, the number of places the expected document moved.
	// It is only defined when BOTH paths retrieved it; for a document that
	// vanished or appeared, a "delta" would be a fabricated magnitude, and the
	// Verdict is what carries the change. Positive = worse.
	Delta int
	sev   int
	// dropKey orders rows inside a severity group: how far the query fell, in
	// units that stay comparable across verdicts (a document that left the
	// ranking counts as having fallen past the deepest position).
	dropKey int
}

// diffCases compares the two paths per query and returns only the queries that
// changed, worst first.
//
// limit is the production topK: it is what makes "fell out of the top-5" a
// category, because a document at rank 6 was never cited before and is not
// missed now. Without it, every rank shuffle would look like a regression.
//
// The verdicts split into three classes, and the split is the point. Only the
// first class changes what the customer sees, because production grounds its
// answer in the top `limit` results:
//
//	CITED     FELL OUT, LOST, dropped, ENTERED, improved
//	UNCITED   shifted, GAINED   — moves between two uncited positions
//	NONE      same rank, or a miss in both paths: not a difference at all
//
// Without that separation a 9→14 move between two documents nobody would ever
// cite would sit in the same table (and the same "N regressed" headline) as a
// 1→6 drop out of the grounding window.
func diffCases(cases []caseResult, limit int) []queryDiff {
	out := make([]queryDiff, 0, len(cases))
	for _, c := range cases {
		hitA := c.RankA > 0 && c.RankA <= limit
		hitB := c.RankB > 0 && c.RankB <= limit
		d := queryDiff{Case: c}
		switch {
		case hitA && !hitB:
			// Cited before, not cited now. The single most decision-relevant row.
			d.Verdict = verdictFellOut
			d.sev = sevFellOut
			// A query lost from rank 1 is worse than one lost from rank limit.
			d.dropKey = limit - c.RankA + 1
		case !hitA && hitB:
			d.Verdict = verdictEntered
			d.sev = sevEntered
		case hitA && hitB:
			switch {
			case c.RankB > c.RankA:
				d.Verdict = verdictDropped
				d.sev = sevDropped
				d.Delta = c.RankB - c.RankA
				d.dropKey = d.Delta
			case c.RankB < c.RankA:
				d.Verdict = verdictImproved
				d.sev = sevImproved
				d.Delta = c.RankB - c.RankA
			default:
				continue // same rank: not a difference
			}
		default: // neither path cited it
			switch {
			case c.RankA > 0 && c.RankB == 0:
				// It used to be retrievable somewhere in the corpus and now is
				// not — worse than falling out of the top-5, because no topK
				// would bring it back.
				d.Verdict = verdictLost
				d.sev = sevLost
				d.dropKey = c.RankA
			case c.RankA == 0 && c.RankB > 0:
				d.Verdict = verdictGained
				d.sev = sevGained
			case c.RankA == 0 && c.RankB == 0:
				continue // missed by both: no difference to report
			default:
				d.Verdict = verdictShifted
				d.sev = sevShifted
				d.Delta = c.RankB - c.RankA
				d.dropKey = d.Delta
			}
		}
		out = append(out, d)
	}
	// Worst first, then biggest fall, then the better-ranked query — a stable
	// order, so two runs of the same measurement print the same table.
	sortDiffs(out)
	return out
}

func sortDiffs(diffs []queryDiff) {
	// Insertion sort: the tables this prints are tens of rows, and it keeps the
	// comparator in one place without dragging in a Less closure.
	for i := 1; i < len(diffs); i++ {
		for j := i; j > 0 && diffLess(diffs[j], diffs[j-1]); j-- {
			diffs[j], diffs[j-1] = diffs[j-1], diffs[j]
		}
	}
}

func diffLess(a, b queryDiff) bool {
	if a.sev != b.sev {
		return a.sev < b.sev
	}
	if a.dropKey != b.dropKey {
		return a.dropKey > b.dropKey
	}
	if a.Case.RankA != b.Case.RankA {
		return a.Case.RankA < b.Case.RankA
	}
	return a.Case.Index < b.Case.Index
}

// verdictCounts is the diff table's tally for the summary line.
type verdictCounts struct {
	Regressed int // citations lost or damaged: FELL OUT / LOST / dropped
	Improved  int // citations gained or improved: ENTERED / improved
	Uncited   int // order moved between two uncited positions: shifted / GAINED
}

// countVerdicts classifies the diff rows. Keeping `uncited` out of Regressed is
// deliberate: a document at rank 9 moving to 14 changes no answer, and folding
// it into "N regressed" would report noise as damage.
func countVerdicts(diffs []queryDiff) verdictCounts {
	var c verdictCounts
	for _, d := range diffs {
		switch d.sev {
		case sevFellOut, sevLost, sevDropped:
			c.Regressed++
		case sevEntered, sevImproved:
			c.Improved++
		default:
			c.Uncited++
		}
	}
	return c
}
