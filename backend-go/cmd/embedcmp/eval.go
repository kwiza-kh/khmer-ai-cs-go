package main

// The eval set: parsing, validation, and deterministic corpus sampling.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// evalCase is one labelled query. The JSON shape is cmd/rageval's
// (`{"query": "...", "expect": [doc_id, …]}`), and `expect` holds DOCUMENT ids —
// not chunk ids — which is what makes recall measurable at all: the tenant
// labelled which knowledge-base document answers the question, not which of its
// chunks.
type evalCase struct {
	Query  string  `json:"query"`
	Expect []int32 `json:"expect"`
}

// evalFile accepts the two shapes an eval set arrives in:
//
//	{"queries": [ … ]}   the shape rag_eval.json uses and rageval documents
//	[ … ]                a bare array, which is what a hand-written or exported
//	                     file tends to be
//
// Both are accepted because the failure mode of guessing wrong is a confusing
// "0 cases" run on the production server rather than a clear error, and the cost
// of accepting both is four lines.
type evalFile struct {
	Queries []evalCase `json:"queries"`
	Cases   []evalCase `json:"cases"`
}

// loadEvalCases reads and validates the eval set. It runs BEFORE any database or
// network work so a typo in the path costs nothing.
func loadEvalCases(path string) ([]evalCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read eval set: %w", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("eval set %s is empty", path)
	}

	var cases []evalCase
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(raw, &cases); err != nil {
			return nil, fmt.Errorf("parse eval set %s as an array: %w", path, err)
		}
	} else {
		var doc evalFile
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("parse eval set %s: %w", path, err)
		}
		cases = doc.Queries
		if len(cases) == 0 {
			cases = doc.Cases
		}
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("eval set %s has no cases (expected {\"queries\":[…]} or a bare array)", path)
	}

	for i, c := range cases {
		if strings.TrimSpace(c.Query) == "" {
			return nil, fmt.Errorf("eval set %s: case %d has an empty query", path, i)
		}
	}
	return cases, nil
}

// emptyExpect lists the case indices whose `expect` is empty. They are not an
// error — a case can be a negative control — but they are guaranteed misses in
// BOTH paths, so leaving them in dilutes any recall difference. Reported in the
// run header rather than silently tolerated.
func emptyExpect(cases []evalCase) []int {
	out := make([]int, 0)
	for i, c := range cases {
		if len(c.Expect) == 0 {
			out = append(out, i)
		}
	}
	return out
}

// sampleChunks reduces the corpus to at most max chunks, spread evenly by
// stride.
//
// WHY A STRIDE AND NOT THE FIRST max: chunk ids are assigned in document order,
// so taking the head would silently delete whole documents from the corpus —
// every query whose answer lived in a dropped document would then be a miss for
// BOTH paths, and the recall numbers would fall for a reason that has nothing to
// do with the embedding convention. A stride keeps coverage of the whole corpus
// while shrinking it.
//
// The same sampled slice is used by both paths, so whatever distortion sampling
// introduces, it is common-mode.
func sampleChunks(all []corpusChunk, max int) []corpusChunk {
	if max <= 0 || max >= len(all) {
		return all
	}
	stride := (len(all) + max - 1) / max
	out := make([]corpusChunk, 0, max)
	for i := 0; i < len(all) && len(out) < max; i += stride {
		out = append(out, all[i])
	}
	return out
}
