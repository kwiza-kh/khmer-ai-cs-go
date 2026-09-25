package main

// Unit tests for the metric definitions, the expect/doc_id matching semantics
// and the ranking itself.
//
// EVERYTHING HERE IS FAKE: hand-built vectors, hand-built rank lists. No test in
// this package touches the network, the database or the clock — the real
// measurement is expensive and runs against production, so the machinery around
// it has to be provably correct before it is pointed at anything.
//
// The tests are written to fail on a plausible WRONG implementation, not to
// restate the right one: recall@k is pinned at the boundary (rank k vs k+1),
// MRR is pinned at the value of a non-zero rank, and the doc-matching tests pin
// the two ways a per-document label is easy to get wrong (a non-expected document
// counting as a hit, and a duplicate document taking two positions).

import (
	"testing"
)

// ─── ranking ─────────────────────────────────────────────────────────────────

func TestCosineAndRanking(t *testing.T) {
	// Three documents whose relationships to the query are unambiguous, so the
	// expected ORDER is not a matter of taste:
	//   d1 = the query direction        → similarity 1
	//   d2 = half the query direction   → similarity 1 (scale-invariant)
	//   d3 = orthogonal                 → similarity 0
	//   d4 = opposite                   → similarity -1
	query := []float32{1, 0}
	corpus := []corpusChunk{
		{chunkID: 1, docID: 10, stored: []float32{1, 0}},
		{chunkID: 2, docID: 20, stored: []float32{2, 0}},
		{chunkID: 3, docID: 30, stored: []float32{0, 1}},
		{chunkID: 4, docID: 40, stored: []float32{-1, 0}},
	}
	ranked := rankChunks(query, corpus, func(c corpusChunk) []float32 { return c.stored })

	if len(ranked) != 4 {
		t.Fatalf("ranked %d chunks, want 4", len(ranked))
	}
	if ranked[3].DocID != 40 {
		t.Errorf("worst-ranked document = %d, want 40 (the opposite direction)", ranked[3].DocID)
	}
	if ranked[0].DocID != 10 {
		t.Errorf("best-ranked document = %d, want 10 (exact match, tie-broken by chunk_id)", ranked[0].DocID)
	}
	// d1 and d2 are both similarity 1; the tie must break by ascending chunk_id,
	// or two runs of the same experiment could disagree.
	if ranked[1].DocID != 20 {
		t.Errorf("second-ranked document = %d, want 20", ranked[1].DocID)
	}
	if got := ranked[2].DocID; got != 30 {
		t.Errorf("third-ranked document = %d, want 30 (orthogonal beats opposite)", got)
	}
	if ranked[3].Sim >= ranked[2].Sim {
		t.Errorf("similarities not descending: %.3f then %.3f", ranked[2].Sim, ranked[3].Sim)
	}
}

func TestCosineEdgeCases(t *testing.T) {
	if got := cosine(nil, nil); got != 0 {
		t.Errorf("cosine of empty vectors = %v, want 0", got)
	}
	if got := cosine([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("cosine of mismatched widths = %v, want 0 (never a silent hit)", got)
	}
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("cosine against a zero vector = %v, want 0", got)
	}
	if got := cosine([]float32{1, 1}, []float32{1, 1}); got < 0.9999 {
		t.Errorf("cosine of a vector with itself = %v, want 1", got)
	}
}

// TestDocRankingCollapsesDuplicateDocuments pins the per-document semantics: a
// document with several matching chunks occupies ONE position, and the position
// is its best chunk's.
func TestDocRankingCollapsesDuplicateDocuments(t *testing.T) {
	ranked := []rankedChunk{
		{ChunkID: 1, DocID: 7, Sim: 0.9},
		{ChunkID: 2, DocID: 7, Sim: 0.8}, // same document again
		{ChunkID: 3, DocID: 9, Sim: 0.7},
		{ChunkID: 4, DocID: 7, Sim: 0.6},
	}
	got := docRanking(ranked)
	want := []int32{7, 9}
	if len(got) != len(want) {
		t.Fatalf("doc ranking = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("doc ranking = %v, want %v", got, want)
		}
	}
}

// ─── expect / doc_id matching semantics ──────────────────────────────────────

func TestBestRankMatchesOnlyExpectedDocumentIDs(t *testing.T) {
	// The label is per DOCUMENT: any chunk of an expected document retrieves it,
	// and a chunk of any other document never does, however high it ranks.
	rankedDocs := []int32{4, 8, 15, 16, 23}
	expect := []int32{15, 16}

	if got := bestRank(rankedDocs, expect); got != 3 {
		t.Errorf("bestRank = %d, want 3 (document 15 is the first expected one)", got)
	}
	if got := bestRank(rankedDocs, []int32{99}); got != 0 {
		t.Errorf("bestRank for an absent document = %d, want 0 (a near-miss document is NOT a hit)", got)
	}
	if got := bestRank(rankedDocs, nil); got != 0 {
		t.Errorf("bestRank with no expectation = %d, want 0", got)
	}
	// A document at the very end is still a hit — the label does not care about
	// rank, only the caller's k does.
	if got := bestRank(rankedDocs, []int32{23}); got != 5 {
		t.Errorf("bestRank for the last document = %d, want 5", got)
	}
}

func TestRecallAtIsStrictAtTheBoundary(t *testing.T) {
	// The boundary is the whole point: rank k counts, k+1 does not.
	//
	// The composition below is the production one — measure() turns a ranking
	// into bestRank, metricsFor turns a rank into recall — so this test exercises
	// both halves of the real path rather than a test-only convenience.
	rankedDocs := []int32{1, 2, 3, 4, 5, 6}
	cases := []struct {
		name   string
		expect []int32
		k      int
		want   bool
	}{
		{"at the boundary", []int32{5}, 5, true},
		{"one past the boundary", []int32{6}, 5, false},
		{"one past, at its own k", []int32{6}, 6, true},
		{"first position", []int32{1}, 5, true},
		{"absent entirely", []int32{42}, 5, false},
		{"second expectation is the one that ranks", []int32{42, 3}, 5, true},
		{"k of zero is never a hit", []int32{1}, 0, false},
		{"negative k is never a hit", []int32{1}, -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rank := bestRank(rankedDocs, tc.expect)
			if got := hitAtK(rank, tc.k); got != tc.want {
				t.Errorf("hitAtK(bestRank(%v), %d) = %v, want %v", tc.expect, tc.k, got, tc.want)
			}
			// recall@k must also agree with the reciprocal rank the diff table
			// prints for the same case: a hit inside k, and nothing else.
			if inTop := reciprocalRank(rank) > 0; inTop != (rank > 0) {
				t.Errorf("reciprocalRank(%d) is inconsistent with the rank", rank)
			}
		})
	}
}

// TestHitAtKOverRawRanks pins the same definitions at the level metricsFor uses
// them, including the "absent" encoding (rank 0) that a ranking cannot express.
func TestHitAtKOverRawRanks(t *testing.T) {
	cases := []struct {
		rank int
		k    int
		want bool
	}{
		{1, 5, true},
		{5, 5, true},
		{6, 5, false},
		{10, 10, true},
		{11, 10, false},
		{0, 5, false},   // absent: never a hit, at any k
		{0, 999, false}, // ... not even at an absurd k
		{3, 0, false},   // k must be positive
		{-1, 5, false},  // a negative rank is not a rank
	}
	for _, tc := range cases {
		if got := hitAtK(tc.rank, tc.k); got != tc.want {
			t.Errorf("hitAtK(rank=%d, k=%d) = %v, want %v", tc.rank, tc.k, got, tc.want)
		}
	}
}

func TestReciprocalRank(t *testing.T) {
	cases := []struct {
		rank int
		want float64
	}{
		{1, 1.0},
		{3, 1.0 / 3.0},
		{4, 0.25},
		{0, 0}, // a miss contributes 0 to MRR, it is not skipped
	}
	for _, tc := range cases {
		got := reciprocalRank(tc.rank)
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("reciprocalRank(%d) = %v, want %v", tc.rank, got, tc.want)
		}
	}
}

// ─── path metrics ────────────────────────────────────────────────────────────

func TestMetricsForRecallAndMRR(t *testing.T) {
	// Six cases with the ranks 1, 2, 5, 11, 25 and a miss, chosen so every metric
	// has a value a wrong implementation would not produce:
	//   recall@5  = ranks 1, 2, 5              → 3/6
	//   recall@10 = ranks 1, 2, 5              → 3/6  (11 and 25 are past the cut)
	//   MRR       = (1 + 1/2 + 1/5 + 1/11 + 1/25 + 0) / 6
	cases := []caseResult{
		{Index: 0, RankA: 1, RankB: 0, SimA: 0.9},
		{Index: 1, RankA: 2, RankB: 1, SimA: 0.8},
		{Index: 2, RankA: 5, RankB: 3, SimA: 0.5},
		{Index: 3, RankA: 11, RankB: 12, SimA: 0.2},
		{Index: 4, RankA: 25, RankB: 24, SimA: 0.05},
		{Index: 5, RankA: 0, RankB: 0, SimA: 0.1},
	}
	pickA := func(c caseResult) (int, float64) { return c.RankA, c.SimA }

	got := metricsFor("A", cases, pickA, 0.35)

	if got.Cases != 6 {
		t.Fatalf("Cases = %d, want 6", got.Cases)
	}
	if got.Recall5 != 3 {
		t.Errorf("Recall5 = %d, want 3 (ranks 1, 2, 5)", got.Recall5)
	}
	if got.Recall10 != 3 {
		t.Errorf("Recall10 = %d, want 3 (ranks 11 and 25 are past the cut)", got.Recall10)
	}
	wantMRR := (1.0 + 0.5 + 0.2 + 1.0/11.0 + 1.0/25.0 + 0) / 6
	if diff := got.MRR - wantMRR; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("MRR = %v, want %v", got.MRR, wantMRR)
	}
	// Mean top-1 similarity is a scale diagnostic, not a quality metric — but it
	// must still be a mean over ALL cases, including misses.
	wantTop1 := (0.9 + 0.8 + 0.5 + 0.2 + 0.05 + 0.1) / 6
	if diff := got.MeanTop1 - wantTop1; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("MeanTop1 = %v, want %v", got.MeanTop1, wantTop1)
	}
	if got.BelowFloor != 3 {
		t.Errorf("BelowFloor = %d, want 3 (0.2, 0.05 and 0.1 are below 0.35)", got.BelowFloor)
	}
	// Rank 25 and the miss fall outside rageval's dense window (rank > 20); ranks
	// 1, 2, 5 and 11 are inside it. Getting this wrong is what would make the two
	// tools' MRR disagree on the same corpus.
	if got.BeyondCandidateWindow != 2 {
		t.Errorf("BeyondCandidateWindow = %d, want 2 (rank 25 and the miss)", got.BeyondCandidateWindow)
	}
}

func TestMetricsForEmptyCaseSet(t *testing.T) {
	// A zero-case run must not divide by zero — it prints 0/0 rather than NaN.
	got := metricsFor("A", nil, func(c caseResult) (int, float64) { return c.RankA, c.SimA }, 0.35)
	if got.Cases != 0 || got.MRR != 0 || got.MeanTop1 != 0 {
		t.Errorf("empty case set produced %+v, want all zero", got)
	}
}

func TestMetricsForPerfectAndWorstRuns(t *testing.T) {
	perfect := []caseResult{{RankA: 1, SimA: 1}, {RankA: 1, SimA: 1}}
	got := metricsFor("A", perfect, func(c caseResult) (int, float64) { return c.RankA, c.SimA }, 0.35)
	if got.Recall5 != 2 || got.Recall10 != 2 || got.MRR != 1 {
		t.Errorf("perfect run = %+v, want 2/2, 2/2, MRR 1", got)
	}

	worst := []caseResult{{RankA: 0, SimA: 0.1}, {RankA: 0, SimA: 0.1}}
	got = metricsFor("B", worst, func(c caseResult) (int, float64) { return c.RankA, c.SimA }, 0.35)
	if got.Recall5 != 0 || got.Recall10 != 0 || got.MRR != 0 {
		t.Errorf("worst run = %+v, want 0/2, 0/2, MRR 0", got)
	}
	if got.BeyondCandidateWindow != 2 {
		t.Errorf("worst run BeyondCandidateWindow = %d, want 2 (both misses)", got.BeyondCandidateWindow)
	}
}

// ─── per-query diff ──────────────────────────────────────────────────────────

func TestDiffCasesClassifiesEveryKindOfChange(t *testing.T) {
	cases := []caseResult{
		{Index: 0, RankA: 1, RankB: 0},  // cited, now nowhere: FELL OUT
		{Index: 1, RankA: 12, RankB: 0}, // uncited, now nowhere: LOST
		{Index: 2, RankA: 1, RankB: 3},  // cited both times, worse: dropped
		{Index: 3, RankA: 0, RankB: 4},  // appeared inside the cutoff: ENTERED
		{Index: 4, RankA: 8, RankB: 2},  // from uncited to cited: ENTERED
		{Index: 5, RankA: 4, RankB: 1},  // cited both times, better: improved
		{Index: 6, RankA: 2, RankB: 2},  // unchanged
		{Index: 7, RankA: 0, RankB: 0},  // missed by both
		{Index: 8, RankA: 9, RankB: 14}, // uncited both times, moved down: shifted
		{Index: 9, RankA: 0, RankB: 30}, // appeared, still uncited: GAINED
	}
	diffs := diffCases(cases, 5)

	if len(diffs) != 8 {
		t.Fatalf("diff rows = %d, want 8 (the unchanged rank 2→2 and the double miss are not differences): %+v", len(diffs), diffs)
	}
	wantOrder := []struct {
		index   int
		verdict string
	}{
		{0, verdictFellOut},
		{1, verdictLost},
		{2, verdictDropped},
		{3, verdictEntered},
		{4, verdictEntered},
		{5, verdictImproved},
		{8, verdictShifted},
		{9, verdictGained},
	}
	for i, want := range wantOrder {
		if diffs[i].Case.Index != want.index || diffs[i].Verdict != want.verdict {
			t.Errorf("row %d = case %d/%s, want case %d/%s (table must print worst first)",
				i, diffs[i].Case.Index, diffs[i].Verdict, want.index, want.verdict)
		}
	}
}

func TestDiffCasesDeltaIsOnlySetWhenBothPathsFoundIt(t *testing.T) {
	// Delta is rankB - rankA, and it is only meaningful when both sides retrieved
	// the document. A vanished document has no "delta" — the verdict carries that.
	cases := []caseResult{
		{Index: 0, RankA: 1, RankB: 4},
		{Index: 1, RankA: 2, RankB: 0},
	}
	diffs := diffCases(cases, 5)
	if len(diffs) != 2 {
		t.Fatalf("diff rows = %d, want 2", len(diffs))
	}
	if diffs[0].Verdict != verdictFellOut || diffs[0].Delta != 0 {
		t.Errorf("vanished document: verdict=%s delta=%d, want FELL OUT with no delta",
			diffs[0].Verdict, diffs[0].Delta)
	}
	if diffs[1].Verdict != verdictDropped || diffs[1].Delta != 3 {
		t.Errorf("dropped document: verdict=%s delta=%d, want dropped with delta 3",
			diffs[1].Verdict, diffs[1].Delta)
	}
}

func TestDiffCasesOrdersFallsByDepth(t *testing.T) {
	// Within "dropped" (both paths CITED the document), the query that fell
	// further comes first: the top of the table is where the damage is.
	//
	// Case 3 fell further in absolute places (10→20) but was never cited on
	// either side, so it must sort BELOW every cited regression — otherwise an
	// uncited reshuffle would head a table about lost citations.
	cases := []caseResult{
		{Index: 0, RankA: 1, RankB: 2},   // cited both, -1 place
		{Index: 1, RankA: 1, RankB: 5},   // cited both, -4 places
		{Index: 2, RankA: 3, RankB: 5},   // cited both, -2 places
		{Index: 3, RankA: 10, RankB: 20}, // uncited on both sides, -10 places
	}
	diffs := diffCases(cases, 5)
	got := make([]int, 0, len(diffs))
	for _, d := range diffs {
		got = append(got, d.Case.Index)
	}
	want := []int{1, 2, 0, 3}
	if len(got) != len(want) {
		t.Fatalf("diff rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("diff order = %v, want %v", got, want)
		}
	}
	if diffs[3].Verdict != verdictShifted {
		t.Errorf("uncited move classified as %q, want %q", diffs[3].Verdict, verdictShifted)
	}
}

func TestCountVerdictsKeepsUncitedMovesOutOfRegressions(t *testing.T) {
	diffs := diffCases([]caseResult{
		{Index: 0, RankA: 1, RankB: 0},  // regressed: FELL OUT
		{Index: 1, RankA: 9, RankB: 0},  // regressed: LOST
		{Index: 2, RankA: 1, RankB: 4},  // regressed: dropped
		{Index: 3, RankA: 0, RankB: 2},  // improved: ENTERED (cited now)
		{Index: 4, RankA: 4, RankB: 1},  // improved: better rank
		{Index: 5, RankA: 8, RankB: 12}, // uncited reshuffle: NOT a regression
		{Index: 6, RankA: 0, RankB: 30}, // uncited appearance: NOT an improvement
	}, 5)
	counts := countVerdicts(diffs)
	if counts.Regressed != 3 {
		t.Errorf("Regressed = %d, want 3", counts.Regressed)
	}
	if counts.Improved != 2 {
		t.Errorf("Improved = %d, want 2", counts.Improved)
	}
	if counts.Uncited != 2 {
		t.Errorf("Uncited = %d, want 2 (a 8→12 move must not be reported as damage)", counts.Uncited)
	}
}

// TestDiffCasesIsDeterministic guards the property the table relies on: the same
// input prints the same rows in the same order, run after run.
func TestDiffCasesIsDeterministic(t *testing.T) {
	cases := []caseResult{
		{Index: 0, RankA: 2, RankB: 4},
		{Index: 1, RankA: 2, RankB: 4},
		{Index: 2, RankA: 1, RankB: 4},
	}
	first := diffCases(cases, 5)
	second := diffCases(cases, 5)
	if len(first) != len(second) {
		t.Fatalf("row counts differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Case.Index != second[i].Case.Index {
			t.Fatalf("row %d differs between runs: %d vs %d", i, first[i].Case.Index, second[i].Case.Index)
		}
	}
}
