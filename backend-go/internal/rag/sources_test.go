package rag

import (
	"encoding/json"
	"testing"
)

// The mapping from ranked chunks to the sources the pipeline sees must carry
// every field the answer depends on — the product-photo attach step reads
// MediaKey here, and a drop is invisible to the database-bound retrieval tests
// (2026-10-16: the first production smoke of that feature returned sources with
// no media_key, which would have silently disabled it).
func TestSourcesFromChunksCarriesFields(t *testing.T) {
	chunks := []SearchChunk{
		{ChunkID: 1, DocID: 7, Title: "SANYO rice cooker", Content: "PRODUCT: 1.8L", Similarity: 0.82, MediaKey: "kb/3/abc.png"},
		{ChunkID: 2, DocID: 8, Title: "warranty policy", Content: "12 months", Similarity: 0.41},
	}
	sources := sourcesFromChunks(chunks)
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	first := sources[0]
	if first.DocID != 7 || first.Title != "SANYO rice cooker" || first.Content != "PRODUCT: 1.8L" || first.Score != 0.82 {
		t.Fatalf("source = %+v, want the chunk's own fields", first)
	}
	if first.MediaKey != "kb/3/abc.png" {
		t.Fatalf("MediaKey = %q, want the document's stored picture key", first.MediaKey)
	}
	if sources[1].MediaKey != "" {
		t.Fatalf("a text document must carry no key, got %q", sources[1].MediaKey)
	}

	// sources_json is what the inbox and the turn see, so the key has to survive
	// serialisation too (omitempty keeps text sources clean).
	blob, err := json.Marshal([]Source{{DocID: 7, MediaKey: "kb/3/abc.png"}, {DocID: 8}})
	if err != nil {
		t.Fatal(err)
	}
	var back []map[string]any
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if back[0]["media_key"] != "kb/3/abc.png" {
		t.Fatalf("media_key lost in JSON: %s", blob)
	}
	if _, present := back[1]["media_key"]; present {
		t.Fatalf("a source without a picture must not carry the field: %s", blob)
	}
}
