package rag

import "testing"

func floatPtr(v float64) *float64 { return &v }

func TestFusePreservesDenseSim(t *testing.T) {
	dense := []SearchChunk{{ChunkID: 7, DocID: 1, Similarity: 0.82, DenseSim: floatPtr(0.82)}}
	trigram := []SearchChunk{{ChunkID: 7, DocID: 1, Similarity: 5}}
	fused := fuseSearchResults(dense, nil, trigram)
	if len(fused) != 1 {
		t.Fatalf("fused len = %d, want 1", len(fused))
	}
	if fused[0].DenseSim == nil {
		t.Fatalf("DenseSim was cleared by a non-dense duplicate")
	}
	if *fused[0].DenseSim != 0.82 {
		t.Fatalf("DenseSim = %v, want 0.82", *fused[0].DenseSim)
	}
}

func TestFuseRRFRanksSharedChunksFirst(t *testing.T) {
	dense := []SearchChunk{
		{ChunkID: 1, Similarity: 0.9, DenseSim: floatPtr(0.9)},
		{ChunkID: 2, Similarity: 0.8, DenseSim: floatPtr(0.8)},
	}
	lexical := []SearchChunk{{ChunkID: 2, Similarity: 0.3}, {ChunkID: 3, Similarity: 0.2}}
	fused := fuseSearchResults(dense, lexical, nil)
	if len(fused) == 0 || fused[0].ChunkID != 2 {
		t.Fatalf("shared chunk 2 must rank first, got %+v", fused)
	}
}
