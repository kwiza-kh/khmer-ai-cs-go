package main

// Tests for the eval-set parsing, the corpus sampling and the two small display
// helpers. All pure: a temp file at most, never the network.

import (
	"khmer-ai-cs-go/internal/textutil"
	"os"
	"path/filepath"
	"testing"
)

func writeTempEval(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eval.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp eval set: %v", err)
	}
	return path
}

func TestLoadEvalCasesAcceptsBothShapes(t *testing.T) {
	// The wrapped shape is what rag_eval.json uses and cmd/rageval documents.
	wrapped := `{"queries":[{"query":"តើប្រាក់ខែបើកថ្ងៃណា?","expect":[3,4]},{"query":"second","expect":[7]}]}`
	cases, err := loadEvalCases(writeTempEval(t, wrapped))
	if err != nil {
		t.Fatalf("wrapped shape: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("wrapped shape: %d cases, want 2", len(cases))
	}
	if cases[0].Query != "តើប្រាក់ខែបើកថ្ងៃណា?" || len(cases[0].Expect) != 2 || cases[0].Expect[0] != 3 {
		t.Errorf("wrapped shape: first case decoded as %+v", cases[0])
	}

	// A bare array is what an exported or hand-written file tends to be, and
	// rejecting it would cost a round trip to the server to discover.
	bare := `[{"query":"a","expect":[1]},{"query":"b","expect":[2]}]`
	cases, err = loadEvalCases(writeTempEval(t, bare))
	if err != nil {
		t.Fatalf("bare shape: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("bare shape: %d cases, want 2", len(cases))
	}
}

func TestLoadEvalCasesRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"empty file":        "",
		"no cases":          `{"queries":[]}`,
		"unknown key":       `{"items":[{"query":"a","expect":[1]}]}`,
		"empty query":       `{"queries":[{"query":"   ","expect":[1]}]}`,
		"truncated json":    `{"queries":[{"query":"a"`,
		"expect is not ids": `{"queries":[{"query":"a","expect":["doc-1"]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadEvalCases(writeTempEval(t, body)); err == nil {
				t.Errorf("loadEvalCases accepted %q", body)
			}
		})
	}
	if _, err := loadEvalCases(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Error("loadEvalCases accepted a missing file")
	}
}

func TestEmptyExpect(t *testing.T) {
	cases := []evalCase{
		{Query: "a", Expect: []int32{1}},
		{Query: "b"},
		{Query: "c", Expect: []int32{}},
		{Query: "d", Expect: []int32{2}},
	}
	got := emptyExpect(cases)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("emptyExpect = %v, want [1 2]", got)
	}
}

func TestUnmatchedCases(t *testing.T) {
	corpus := []corpusChunk{{chunkID: 1, docID: 5}, {chunkID: 2, docID: 5}, {chunkID: 3, docID: 9}}
	cases := []evalCase{
		{Query: "in corpus", Expect: []int32{9}},
		{Query: "one of two is present", Expect: []int32{99, 5}},
		{Query: "absent", Expect: []int32{42}},
		{Query: "no label at all", Expect: nil}, // reported by emptyExpect, not here
	}
	got := unmatchedCases(cases, corpus)
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("unmatchedCases = %v, want [2] (a guaranteed miss in both paths)", got)
	}
}

func TestSampleChunksKeepsWholeCorpusSpread(t *testing.T) {
	corpus := make([]corpusChunk, 0, 100)
	for i := 0; i < 100; i++ {
		corpus = append(corpus, corpusChunk{chunkID: int32(i + 1), docID: int32(i/10 + 1)})
	}

	if got := sampleChunks(corpus, 0); len(got) != 100 {
		t.Errorf("max=0 kept %d chunks, want all 100", len(got))
	}
	if got := sampleChunks(corpus, 200); len(got) != 100 {
		t.Errorf("max>len kept %d chunks, want all 100", len(got))
	}

	got := sampleChunks(corpus, 10)
	if len(got) != 10 {
		t.Fatalf("max=10 kept %d chunks, want 10", len(got))
	}
	// The whole corpus must stay REPRESENTED: taking the first 10 would leave
	// documents 2..10 with no chunks at all, and every query answered by them
	// would be a miss for both paths for a purely mechanical reason.
	docs := map[int32]bool{}
	for _, c := range got {
		docs[c.docID] = true
	}
	if len(docs) < 10 {
		t.Errorf("stride sample covers only %d documents, want 10", len(docs))
	}
	// And it must stay deterministic — sampling is not part of the variable.
	again := sampleChunks(corpus, 10)
	for i := range got {
		if got[i].chunkID != again[i].chunkID {
			t.Fatalf("sampling is not deterministic at %d: %d vs %d", i, got[i].chunkID, again[i].chunkID)
		}
	}
}

func TestOneLine(t *testing.T) {
	// A query carrying a newline must not break the table's one-row-per-query
	// layout. It stays ONE row with several columns, not a row with several
	// columns and no name.
	if got := textutil.OneLine("a\nb\tc", 64); got != "a b c" {
		t.Errorf("oneLine = %q, want %q", got, "a b c")
	}
	// Truncation counts RUNES: a Khmer string cut at 4 must stay valid UTF-8 and
	// keep its combining marks attached to their base characters.
	khmer := "តើប្រាក់ខែ"
	got := textutil.OneLine(khmer, 4)
	if len([]rune(got)) != 4 {
		t.Errorf("oneLine truncation = %q (%d runes), want 4 runes", got, len([]rune(got)))
	}
	if got != string([]rune(khmer)[:3])+"…" {
		t.Errorf("oneLine truncated to %q, want the first 3 runes plus an ellipsis", got)
	}
	if got := textutil.OneLine("short", 64); got != "short" {
		t.Errorf("oneLine shortened a short string: %q", got)
	}
	if got := textutil.OneLine("", 64); got != "" {
		t.Errorf("oneLine of empty = %q", got)
	}
}

func TestRankLabel(t *testing.T) {
	// The diff table prints 0 as "miss": printing "0" would read as a rank, and
	// there is no rank zero.
	if got := rankLabel(0); got != "miss" {
		t.Errorf("rankLabel(0) = %q, want \"miss\"", got)
	}
	if got := rankLabel(3); got != "3" {
		t.Errorf("rankLabel(3) = %q, want \"3\"", got)
	}
}

func TestIndexListCapsItself(t *testing.T) {
	long := make([]int, 40)
	for i := range long {
		long[i] = i
	}
	got := indexList(long)
	if len(got) > 200 {
		t.Errorf("indexList printed %d characters for 40 indices, want a capped list", len(got))
	}
	if indexList(nil) != "" {
		t.Errorf("indexList(nil) = %q, want empty", indexList(nil))
	}
}

func TestDistinctDocCountAndTitles(t *testing.T) {
	corpus := []corpusChunk{
		{chunkID: 1, docID: 7, title: "pricing"},
		{chunkID: 2, docID: 7, title: "pricing"},
		{chunkID: 3, docID: 8, title: "clinic hours"},
	}
	if got := distinctDocCount(corpus); got != 2 {
		t.Errorf("distinctDocCount = %d, want 2", got)
	}
	titles := titlesOf(corpus)
	if titles[7] != "pricing" || titles[8] != "clinic hours" {
		t.Errorf("titlesOf = %v", titles)
	}
}
