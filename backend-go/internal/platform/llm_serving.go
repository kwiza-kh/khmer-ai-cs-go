package platform

import "khmer-ai-cs-go/internal/llm"

// serving is the generation client for the provider the default row selects. Reply
// generation and the other text generation go through it. The Gemini-only capabilities
// (the turn judge, vision, speech) keep using p.Gemini.
//
// With no router wired, which is how the tests build a Pipeline, it answers with the
// Gemini client: the behaviour every existing deployment has.
func (p *Pipeline) serving() llm.Model {
	if p.LLM != nil {
		return p.LLM.Model()
	}
	return p.Gemini
}
