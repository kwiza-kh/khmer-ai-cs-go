package main

// A smoke test for the report as a whole: it renders a full report from fake
// data and asserts the numbers a reader would act on actually appear.
//
// It is not a golden-file test on purpose — the wording of a header is not a
// contract, but "the ROW A recall@5 the tool computed is the one it printed" is,
// and a report that silently drops a column or prints NaN would be worse than no
// report at all.

import (
	"strings"
	"testing"
	"time"
)

func sampleReport() reportInput {
	cases := []caseResult{
		{Index: 0, Query: "តើប្រាក់ខែបើកថ្ងៃណា?", RankA: 1, RankB: 3, TopA: 12, TopB: 31, SimA: 0.71, SimB: 0.68},
		{Index: 1, Query: "clinic hours", RankA: 2, RankB: 0, TopA: 8, TopB: 9, SimA: 0.66, SimB: 0.40},
		{Index: 2, Query: "delivery fee", RankA: 0, RankB: 4, TopA: 3, TopB: 5, SimA: 0.30, SimB: 0.55},
	}
	floor := 0.35
	return reportInput{
		EvalPath:   "/root/khmer-deploy/rag_eval.json",
		Tenant:     7,
		Limit:      5,
		Floor:      floor,
		Model:      "gemini-embedding-001",
		Stats:      corpusStats{Chunks: 615, Docs: 65, NoVector: 2, DocsTotal: 66, DocsReady: 65, DocsNotReady: 1},
		UsedChunks: 615,
		RankedDocs: 65,
		EmbedCalls: 7,
		EmbedTime:  3 * time.Second,
		TotalTime:  9 * time.Second,
		Vertex:     "project=gen-lang-client-0354228918 region=asia-southeast1 sa=/opt/khmer-ai-cs/vertex-sa.json",
		KeySource:  "model_configs (default row)",
		// The server's real situation: AI Studio is reached through the relay,
		// which is why the header has to name the base it called.
		StudioBase: "https://gateway.ai.cloudflare.com/v1/acct/gemini-relay-gw/google-ai-studio/v1beta (GEMINI_API_BASE)",
		A:          metricsFor("A", cases, func(c caseResult) (int, float64) { return c.RankA, c.SimA }, floor),
		B:          metricsFor("B", cases, func(c caseResult) (int, float64) { return c.RankB, c.SimB }, floor),
		Diffs:      diffCases(cases, 5),
		Titles:     map[int32]string{12: "ប្រាក់ខែ", 31: " payroll policy ", 8: "clinic hours", 9: "opening times", 3: "delivery", 5: "fees"},
		Unmatched:  []int{2},
	}
}

func TestWriteReportRendersBothPathsAndTheDiff(t *testing.T) {
	var sb strings.Builder
	writeReport(&sb, sampleReport())
	out := sb.String()

	// Logged so that `go test -v -run TestWriteReport` prints the whole artifact:
	// the report IS the deliverable, and reading it should not require a database,
	// a service account and a live API. (Also the only way to eyeball the column
	// alignment without spending a real measurement.)
	t.Log("\n" + out)

	for _, want := range []string{
		"READ-ONLY",                        // the guarantee is stated in the output, not only in the source
		"default_transaction_read_only=on", // ... and so is the mechanism
		"NEVER stored",                     // path B's vectors are not persisted
		"cases=3",                          // the eval set size
		"user_id=7",                        // the tenant actually measured
		"615 chunks / 65 documents",        // the corpus actually measured
		"model_configs (default row)",      // where path A's key came from
		"GEMINI_API_BASE",                  // ... and which AI Studio route it called (the relay on this deployment)
		"2/3 ( 67%)",                       // the recall@5 both paths score here (ranks 1&2 vs 3&4)
		"0.500",                            // A's MRR: (1 + 1/2 + 0) / 3
		"0.194",                            // B's MRR: (1/3 + 0 + 1/4) / 3 — a different number, or the paths were not measured separately
		"FELL OUT",                         // case 1 went from rank 2 to nowhere
		"clinic hours",                     // ... and the legend names the documents involved
		"2 regressed, 1 improved",          // the summary counts the citation-affecting rows
		"invisible@20",                     // the reconciliation column with rageval
		"WARNING 1 case(s) expect a document that has no chunk in the corpus",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not mention %q\n----- report -----\n%s", want, out)
		}
	}

	// The unchanged case must not be listed as a difference: the table is meant
	// to be read.
	if strings.Contains(out, "unchanged rank") {
		t.Errorf("report leaked an internal note\n%s", out)
	}
	if strings.Contains(out, "NaN") || strings.Contains(out, "%!") {
		t.Errorf("report contains a formatting failure\n%s", out)
	}
}

func TestWriteReportHandlesNoDifferences(t *testing.T) {
	in := sampleReport()
	in.Diffs = nil
	var sb strings.Builder
	writeReport(&sb, in)
	out := sb.String()
	if !strings.Contains(out, "every query landed on the same rank") {
		t.Errorf("an identical A/B run must say so explicitly:\n%s", out)
	}
}

func TestWriteReportMarksSamplingAndEmptyExpect(t *testing.T) {
	in := sampleReport()
	in.Sampled = true
	in.MaxChunks = 100
	in.UsedChunks = 100
	in.EmptyExpect = []int{4, 5}
	in.Stats.Chunks = 615
	var sb strings.Builder
	writeReport(&sb, in)
	out := sb.String()
	if !strings.Contains(out, "NOT comparable to a full-corpus baseline") {
		t.Errorf("a sampled run must warn that its numbers are not comparable:\n%s", out)
	}
	if !strings.Contains(out, "empty expect list") {
		t.Errorf("cases that count as misses in both paths must be disclosed:\n%s", out)
	}
}
