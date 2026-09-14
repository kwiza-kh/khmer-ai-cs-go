package rag

import "testing"

func TestDiversifyByDoc(t *testing.T) {
	in := []SearchChunk{
		{ChunkID: 1, DocID: 1},
		{ChunkID: 2, DocID: 1},
		{ChunkID: 3, DocID: 1},
		{ChunkID: 4, DocID: 2},
		{ChunkID: 5, DocID: 2},
		{ChunkID: 6, DocID: 3},
	}
	out := diversifyByDoc(in, 2)
	if len(out) != 5 {
		t.Fatalf("len = %d, want 5", len(out))
	}
	want := []int32{1, 2, 4, 5, 6}
	for i := range want {
		if out[i].ChunkID != want[i] {
			t.Fatalf("position %d = chunk %d, want %d", i, out[i].ChunkID, want[i])
		}
	}
	if len(diversifyByDoc(in, 0)) != len(in) {
		t.Fatal("maxPerDoc <= 0 must be a no-op")
	}
}

func TestDefaultTopKEnvOverride(t *testing.T) {
	t.Setenv("RAG_TOP_K", "7")
	if got := envI("RAG_TOP_K", int(DefaultTopK)); got != 7 {
		t.Fatalf("envI RAG_TOP_K = %d, want 7", got)
	}
	if got := envI("RAG_TOP_K_UNSET", int(DefaultTopK)); got != int(DefaultTopK) {
		t.Fatalf("fallback = %d, want %d", got, DefaultTopK)
	}
}

func TestGroundSourceLimitEnv(t *testing.T) {
	t.Setenv("RAG_SOURCE_LIMIT_RUNES", "450")
	t.Setenv("RAG_TABLE_LIMIT_RUNES", "1200")
	if got := groundSourceLimit("plain prose content"); got != 450 {
		t.Fatalf("prose limit = %d, want 450", got)
	}
	if got := groundSourceLimit("| a | b |\n| 1 | 2 |"); got != 1200 {
		t.Fatalf("table limit = %d, want 1200", got)
	}
}
