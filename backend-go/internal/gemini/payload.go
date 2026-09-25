package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Request bodies and response parsing that differ per provider.
//
// The URL is not the only thing that moves between AI Studio and Vertex: the
// embedding METHODS have different body shapes and different response
// envelopes, and the context cache names its model differently. Everything
// shape-related lives here so the two branches of each function sit next to
// each other and can be compared at a glance.

// embedBody builds the body for embedding ONE text.
//
// The two shapes (both measured by cmd/vertexprobe against the real endpoints):
//
//	studio: {"model":"models/gemini-embedding-001","content":{"parts":[{"text":…}]},
//	         "taskType":…,"outputDimensionality":768}
//	vertex: {"instances":[{"content":"…"}],"parameters":{"outputDimensionality":768}}
//
// outputDimensionality is mandatory on both, and for the same reason: the
// model's own default is 3072 while knowledge_chunks is vector(768). Omitting
// it does not fail the call — it returns vectors of the wrong width, which
// pgvector then rejects on insert ("expected 768 dimensions"), or, for a QUERY
// vector, silently compares against a column of a different width.
func (p provider) embedBody(text, taskType string) map[string]any {
	if p.kind == providerVertex {
		// Only the two fields the platform was measured to accept are sent here.
		// taskType has no verified spelling on :predict (the platform's docs use
		// snake_case task_type for some embedding models and camelCase for
		// others), and guessing wrong costs a 400 on EVERY retrieval — a worse
		// failure than the retrieval-quality loss of omitting it. Re-probe with
		// cmd/vertexprobe before adding it back.
		return map[string]any{
			"instances":  []map[string]any{{"content": text}},
			"parameters": map[string]any{"outputDimensionality": embeddingVectorDimension},
		}
	}
	return map[string]any{
		"model":                "models/" + EmbeddingModel,
		"content":              map[string]any{"parts": []map[string]any{{"text": text}}},
		"taskType":             taskType,
		"outputDimensionality": embeddingVectorDimension,
	}
}

// embedBatchBody builds the body for embedding N texts.
//
// Vertex has no :batchEmbedContents at all: the generic :predict takes an
// `instances` array and answers with one prediction per instance, so batching
// survives the migration as a shape change rather than a lost capability —
// which matters, because the per-text fallback in GenerateEmbeddings costs one
// round trip per chunk of an ingested document.
func (p provider) embedBatchBody(texts []string) map[string]any {
	if p.kind == providerVertex {
		instances := make([]map[string]any, len(texts))
		for i, text := range texts {
			instances[i] = map[string]any{"content": text}
		}
		return map[string]any{
			"instances":  instances,
			"parameters": map[string]any{"outputDimensionality": embeddingVectorDimension},
		}
	}
	requests := make([]map[string]any, len(texts))
	for i, text := range texts {
		requests[i] = map[string]any{
			"model":                "models/" + EmbeddingModel,
			"content":              map[string]any{"parts": []map[string]any{{"text": text}}},
			"taskType":             "RETRIEVAL_DOCUMENT",
			"outputDimensionality": embeddingVectorDimension,
		}
	}
	return map[string]any{"requests": requests}
}

// embeddingValues reads vector i out of a single-embedding response.
func (p provider) embeddingValues(v map[string]any, i int) []float64 {
	if p.kind == providerVertex {
		return vertexPredictValues(v, i)
	}
	// Studio's :embedContent answers {"embedding":{"values":[…]}}.
	return digArray(v, "embedding", "values")
}

// vertexPredictValues reads vector i out of a :predict response.
//
// The platform has shipped more than one serialisation for embeddings —
// predictions[i].embeddings.values, predictions[i].values, and a top-level
// embeddings array — which is why the probe carries the same three-way lookup.
// Picking one and being wrong fails every retrieval with "invalid response",
// so all three are accepted (in the order the platform documents them).
func vertexPredictValues(v map[string]any, i int) []float64 {
	if preds, ok := v["predictions"].([]any); ok && i < len(preds) {
		if pm, ok := preds[i].(map[string]any); ok {
			switch emb := pm["embeddings"].(type) {
			case map[string]any:
				if vals := digArray(emb, "values"); vals != nil {
					return vals
				}
			case []any:
				if vals := float64Slice(emb); vals != nil {
					return vals
				}
			}
			if vals := digArray(pm, "values"); vals != nil {
				return vals
			}
		}
	}
	if embs, ok := v["embeddings"].([]any); ok && i < len(embs) {
		if em, ok := embs[i].(map[string]any); ok {
			return digArray(em, "values")
		}
	}
	return nil
}

// float64Slice converts a decoded JSON array of numbers.
func float64Slice(arr []any) []float64 {
	out := make([]float64, 0, len(arr))
	for _, item := range arr {
		n, ok := item.(float64)
		if !ok {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// parseVertexPredictBatch reads n vectors out of one :predict response.
//
// The width is checked per vector, not once for the batch: a truncated or
// padded prediction would otherwise be stored as a valid chunk embedding and
// only surface as bad search results much later.
func parseVertexPredictBatch(respText string, n int) ([][]float32, error) {
	var v map[string]any
	if json.Unmarshal([]byte(respText), &v) != nil {
		return nil, errors.New("batchEmbedContents: invalid response")
	}
	out := make([][]float32, n)
	for i := 0; i < n; i++ {
		values := vertexPredictValues(v, i)
		if len(values) == 0 {
			return nil, fmt.Errorf("batchEmbedContents: empty vector at %d", i)
		}
		if len(values) != embeddingVectorDimension {
			return nil, embeddingWidthError(providerVertex, len(values))
		}
		vec := make([]float32, len(values))
		for j, f := range values {
			vec[j] = float32(f)
		}
		out[i] = vec
	}
	return out, nil
}

// embeddingWidthError explains a vector whose width does not fit the column.
//
// A wrong width is silent upstream — the call succeeds — so the message has to
// carry the consequence: knowledge_chunks is vector(768), and a 3072-dim
// vector is rejected by pgvector on insert or, for a query vector, compared
// against a column it cannot match.
func embeddingWidthError(kind providerKind, got int) error {
	if kind == providerVertex {
		return fmt.Errorf("vertex :predict returned %d dimensions, want %d: parameters.outputDimensionality "+
			"was not honoured (the model's default is 3072 and knowledge_chunks is vector(%d))",
			got, embeddingVectorDimension, embeddingVectorDimension)
	}
	// Unchanged from before the transport split: the studio caller only ever
	// acted on the error being non-nil.
	return errors.New("Gemini returned an invalid embedding")
}

// modelNameFromResource strips the resource prefixes the two platforms use:
// AI Studio answers "models/gemini-2.5-flash", the platform answers
// "publishers/google/models/gemini-2.5-flash". Leaving either on would make
// ListModels return a string that SetModelName then pastes into a URL as a
// doubled path.
func modelNameFromResource(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"publishers/google/models/", "models/"} {
		name = strings.TrimPrefix(name, prefix)
	}
	return name
}
