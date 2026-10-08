package rag

import "khmer-ai-cs-go/internal/llm"

// serving is the text-generation client for the provider the default row selects.
// Embeddings, reranking and query rewriting stay on s.Gemini: Claude has no embedding
// model, and those prompts are Gemini-specific.
//
// With no router wired, which is how the tests build a Service around one Gemini
// client, it answers with the Gemini client.
func (s *Service) serving() llm.Model {
	if s.LLM != nil {
		return s.LLM.Model()
	}
	return s.Gemini
}
