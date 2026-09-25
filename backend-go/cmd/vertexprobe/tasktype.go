package main

// Task-type probe for the platform's :predict embedding path.
//
// This is the one question a 200 status cannot answer. gemini-embedding-001 is
// task-conditioned: the AI Studio path sends taskType=RETRIEVAL_DOCUMENT when
// indexing and taskType=RETRIEVAL_QUERY when searching, and the 621 chunks in
// the knowledge base were embedded that way. If the platform accepts a
// task-type parameter but silently ignores it, the vectors come back
// well-formed and the wrong width is impossible to notice — retrieval simply
// gets worse with no error and no log.
//
// So each spelling is embedded AND its vector compared against the untasked
// baseline:
//
//	404/400            → the spelling is not supported
//	200 + same vector  → accepted but IGNORED (the dangerous case)
//	200 + other vector → it took effect
//
// The QUERY-vs-DOCUMENT pair is included because identical vectors there would
// mean the parameter is decorative even when it changes the bytes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// got is one variant's outcome: the vector when one came back, else why not.
type got struct {
	v    []float64
	note string
}

type taskVariant struct {
	label string
	body  map[string]any
}

func (p *probe) runTaskTypeProbe(ctx context.Context, model, text string) int {
	const width = 768
	inst := []map[string]any{{"content": text}}
	params := func(m map[string]any) map[string]any {
		out := map[string]any{"outputDimensionality": width}
		for k, v := range m {
			out[k] = v
		}
		return out
	}

	variants := []taskVariant{
		{label: "baseline (no task type)", body: map[string]any{"instances": inst, "parameters": params(nil)}},
		{label: "parameters.task_type=QUERY", body: map[string]any{"instances": inst, "parameters": params(map[string]any{"task_type": "RETRIEVAL_QUERY"})}},
		{label: "parameters.taskType=QUERY", body: map[string]any{"instances": inst, "parameters": params(map[string]any{"taskType": "RETRIEVAL_QUERY"})}},
		{label: "parameters.task_type=DOCUMENT", body: map[string]any{"instances": inst, "parameters": params(map[string]any{"task_type": "RETRIEVAL_DOCUMENT"})}},
		{label: "parameters.taskType=DOCUMENT", body: map[string]any{"instances": inst, "parameters": params(map[string]any{"taskType": "RETRIEVAL_DOCUMENT"})}},
		{label: "instances[].task_type=QUERY", body: map[string]any{"instances": []map[string]any{{"content": text, "task_type": "RETRIEVAL_QUERY"}}, "parameters": params(nil)}},
	}

	fmt.Printf("\n── task-type probe on %s ──────────────────────────\n", model)
	results := make([]got, len(variants))
	var baseline []float64

	for i, v := range variants {
		status, resp, err := p.post(ctx, p.modelBase+"/"+model+":predict", v.body)
		switch {
		case err != nil:
			results[i].note = "error: " + err.Error()
		case status != http.StatusOK:
			results[i].note = summarize(status, resp)
		default:
			vec := predictVector(resp)
			if len(vec) == 0 {
				results[i].note = fmt.Sprintf("HTTP 200 but no vector found (dim=%s)", dimensions(resp))
			} else {
				results[i].v = vec
				results[i].note = fmt.Sprintf("dim=%d", len(vec))
			}
		}
		if i == 0 {
			baseline = results[i].v
			// Control: different models have different weights, so the baseline
			// vector MUST differ across models. Printing its head is what tells a
			// real "task type ignored" apart from "every request hit one model".
			if n := len(baseline); n >= 4 {
				fmt.Printf("  [control] %s baseline head: %.6f %.6f %.6f %.6f\n", model,
					baseline[0], baseline[1], baseline[2], baseline[3])
			}
		}
	}

	// Report, classifying each row against the baseline.
	effective := 0
	for i, v := range variants {
		verdict := v.label
		switch {
		case results[i].v == nil:
			verdict = "UNSUPPORTED/FAILED"
		case i == 0:
			verdict = "baseline"
		case sameVector(results[i].v, baseline):
			verdict = "ACCEPTED BUT IGNORED"
		default:
			verdict = "EFFECTIVE"
			effective++
		}
		fmt.Printf("%-34s %-24s %s\n", v.label, results[i].note, verdict)
	}

	// The QUERY/DOCUMENT pair: if a spelling is effective, these two must differ
	// from each other, or the parameter changes bytes without changing meaning.
	for _, key := range []string{"task_type", "taskType"} {
		q := findVariant(results, variants, "parameters."+key+"=QUERY")
		d := findVariant(results, variants, "parameters."+key+"=DOCUMENT")
		if q == nil || d == nil {
			continue
		}
		if sameVector(q, d) {
			fmt.Printf("\n⚠ %s: QUERY and DOCUMENT vectors are IDENTICAL — the parameter does not condition the embedding.\n", key)
		} else {
			fmt.Printf("\n✓ %s: QUERY and DOCUMENT vectors differ — the parameter conditions the embedding.\n", key)
		}
	}

	if effective == 0 {
		fmt.Println("\nNo task-type spelling changed the vector. Migrating as-is would silently lose the")
		fmt.Println("query/document asymmetry the knowledge base was built with.")
		return 1
	}
	fmt.Printf("\n%d spelling(s) actually condition the embedding.\n", effective)
	return 0
}

func findVariant(results []got, variants []taskVariant, label string) []float64 {
	for i, v := range variants {
		if v.label == label {
			return results[i].v
		}
	}
	return nil
}

func sameVector(a, b []float64) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// predictVector digs the first embedding vector out of a :predict response.
func predictVector(resp []byte) []float64 {
	var v map[string]any
	if json.Unmarshal(resp, &v) != nil {
		return nil
	}
	preds, _ := v["predictions"].([]any)
	if len(preds) == 0 {
		return nil
	}
	p0, _ := preds[0].(map[string]any)
	emb, _ := p0["embeddings"].(map[string]any)
	vals, _ := emb["values"].([]any)
	if len(vals) == 0 {
		if direct, ok := p0["embeddings"].([]any); ok {
			vals = direct
		}
	}
	out := make([]float64, 0, len(vals))
	for _, n := range vals {
		if f, ok := n.(float64); ok {
			out = append(out, f)
		}
	}
	return out
}
