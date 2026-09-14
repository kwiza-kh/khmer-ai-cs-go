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
