package main

import (
	"math"
	"testing"
)

// The metrics are the product of this tool, so they are pinned here with hand-
// computed expectations rather than "whatever the code says".

func TestCosineBasics(t *testing.T) {
	if got := cosine([]float32{1, 0}, []float32{1, 0}); math.Abs(got-1) > 1e-9 {
		t.Errorf("identical vectors: %v, want 1", got)
	}
	if got := cosine([]float32{1, 0}, []float32{0, 1}); math.Abs(got) > 1e-9 {
		t.Errorf("orthogonal: %v, want 0", got)
	}
	if got := cosine([]float32{1, 0}, []float32{-1, 0}); math.Abs(got+1) > 1e-9 {
		t.Errorf("opposite: %v, want -1", got)
	}
	// Magnitude must not matter (that is the whole point of cosine).
	if got := cosine([]float32{2, 2}, []float32{5, 5}); math.Abs(got-1) > 1e-9 {
		t.Errorf("scaled duplicates: %v, want 1", got)
	}
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Errorf("zero vector: %v, want 0", got)
	}
}

func TestRankQueryRanksDocumentsByBestChunk(t *testing.T) {
	// Three docs: A (two chunks), B, C. Query is closest to A's second chunk's
	// direction, then B, then C.
	corpus := []chunk{{chunkID: 1, docID: 10}, {chunkID: 2, docID: 10}, {chunkID: 3, docID: 20}, {chunkID: 4, docID: 30}}
	query := []float32{1, 0}
	vectors := [][]float32{
		{0, 1},  // A chunk 1: orthogonal
		{1, 1},  // A chunk 2: 45°, the doc's best
		{1, 3},  // B: ~72°
		{-1, 0}, // C: opposite
	}
	rank, topDoc, topSim, hitSim := rankQuery(query, vectors, corpus, []int32{20})
	if rank != 2 {
		t.Errorf("rank of doc 20 = %d, want 2", rank)
	}
	if topDoc != 10 {
		t.Errorf("top doc = %d, want 10 (its best chunk beats B)", topDoc)
	}
	if topSim <= hitSim {
		t.Errorf("top sim %v must exceed hit sim %v here", topSim, hitSim)
	}
	// Best-expected-chunk similarity is doc 20's chunk.
	if want := cosine(query, []float32{1, 3}); math.Abs(hitSim-want) > 1e-9 {
		t.Errorf("hit sim = %v, want %v", hitSim, want)
	}
}

func TestRankQueryMissIsZero(t *testing.T) {
	corpus := []chunk{{docID: 1}}
	rank, _, _, hitSim := rankQuery([]float32{1, 0}, [][]float32{{1, 0}}, corpus, []int32{99})
	if rank != 0 || hitSim != 0 {
		t.Errorf("miss = (%d,%v), want (0,0)", rank, hitSim)
	}
}

func TestRankQueryNilVectorsAreSkipped(t *testing.T) {
	corpus := []chunk{{docID: 1}, {docID: 2}}
	rank, topDoc, _, _ := rankQuery([]float32{1, 0}, [][]float32{nil, {1, 0}}, corpus, []int32{2})
	if rank != 1 || topDoc != 2 {
		t.Errorf("rank/top = %d/%d, want 1/2 (the embedded chunk decides)", rank, topDoc)
	}
}

func TestRecallAndMRR(t *testing.T) {
	ranks := []int{1, 2, 5, 0, 11}
	if got := countWithin(ranks, 1); got != 1 {
		t.Errorf("@1 = %d, want 1", got)
	}
	if got := countWithin(ranks, 5); got != 3 {
		t.Errorf("@5 = %d, want 3", got)
	}
	if got := countWithin(ranks, 10); got != 3 {
		t.Errorf("@10 = %d, want 3 (rank 11 is out)", got)
	}
	// MRR counts every non-miss rank, including one past @10 (rank 11 still
	// contributes 1/11); only the rank-0 miss contributes 0.
	want := (1.0/1 + 1.0/2 + 1.0/5 + 0 + 1.0/11) / 5
	if got := mean(reciprocalRanks(ranks)); math.Abs(got-want) > 1e-9 {
		t.Errorf("MRR = %v, want %v (misses count in the denominator)", got, want)
	}
}

func TestPercentile(t *testing.T) {
	xs := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	// Nearest-rank with int(p*(n-1)): p50 of 10 samples sits at index 4.
	if got := percentile(xs, 0.5); got != 50 {
		t.Errorf("p50 = %v, want 50", got)
	}
	if got := percentile(xs, 0.9); got != 90 {
		t.Errorf("p90 = %v, want 90", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
}

func TestCacheKeySeparatesVariantsAndPrefixModes(t *testing.T) {
	a := cacheKey("ge2p", "gemini-embedding-2", true, "same text")
	b := cacheKey("ge2n", "gemini-embedding-2", false, "same text")
	c := cacheKey("ge1", "gemini-embedding-001", false, "same text")
	if a == b || a == c || b == c {
		t.Fatal("cache keys must differ across variant/model/prefix-mode: a shared key would let one variant reuse another's vectors")
	}
	if a != cacheKey("ge2p", "gemini-embedding-2", true, "same text") {
		t.Error("cache key must be stable for identical inputs")
	}
}

func TestCountBelow(t *testing.T) {
	if got := countBelow([]float64{0.1, 0.35, 0.5}, 0.35); got != 1 {
		t.Errorf("below floor = %d, want 1 (strictly below)", got)
	}
}
