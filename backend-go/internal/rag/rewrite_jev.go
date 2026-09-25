package rag

import (
	"context"
	"fmt"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/typesafe"
)

// jevRewriteBudget bounds the query rewrite. It runs inside Search, ahead of
// both retrieval and generation, so before this knob existed a stalled Jev held
// the whole turn for the HTTP client's 10s timeout instead of degrading. On
// expiry the caller falls back to the legacy Gemini rewrite. The default sits
// above the production server's measured 0.8-2.1s spread to api.typesafe.ai;
// tunable so ops can re-measure from the host without a rebuild.
var jevRewriteBudget = time.Duration(envI("JEV_REWRITE_BUDGET_MS", 3000)) * time.Millisecond

// rewriteQueryJev resolves follow-up phrasing into a searchable query without
// free-text generation: code builds the candidates (raw message; previous
// customer turn's keywords carried forward; the message's own segmented
// keywords), Jev picks the one most likely to hit the knowledge base, and the
// caller gets typed certainty instead of trusting generated prose.
//
// Returns (nil, true) when Jev actively decided the original query is best,
// (query, true) when it picked another candidate, and (nil, false) when Jev
// is disabled or unavailable — the caller then falls back to the legacy
// Gemini rewrite.
func (s *Service) rewriteQueryJev(ctx context.Context, message string, history []gemini.HistoryItem) (*string, bool) {
	if !s.Jev.Enabled() {
		return nil, false
	}
	candidates := []string{message}
	if prev := lastCustomerTurn(history); prev != "" {
		if kw := searchKeywordsOf(prev); kw != "" {
			candidates = append(candidates, kw+" "+message)
		}
	}
	if kw := searchKeywordsOf(message); kw != "" && kw != message {
		candidates = append(candidates, kw)
	}
	if len(candidates) == 1 {
		return nil, true
	}
	state := map[string]any{"customer_message": truncateRunes(message, 300), "candidates": candidates}
	if prev := lastCustomerTurn(history); prev != "" {
		state["previous_customer_message"] = truncateRunes(prev, 300)
	}
	criteria := make(map[string]string, len(candidates))
	questions := map[string]typesafe.Question{}
	for i, c := range candidates {
		state[fmt.Sprintf("q%d", i)] = c
		criteria[fmt.Sprintf("q%d", i)] = fmt.Sprintf("Candidate: %s", truncateRunes(c, 120))
	}
	questions["pick"] = typesafe.Choice(
		"Which candidate is the best search query for finding the answer to `customer_message` in this store's "+
			"knowledge base? A good query keeps the concrete product names and question intent; carry-over context "+
			"from `previous_customer_message` helps when `customer_message` refers back to it.",
		criteria)
	// Armed here rather than at function entry: the candidate checks above can
	// return without ever calling Jev.
	ctx, cancel := context.WithTimeout(ctx, jevRewriteBudget)
	defer cancel()

	resp, err := s.Jev.Judge(ctx, state, questions)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("jev query rewrite failed; falling back", "error", err.Error())
		}
		return nil, false
	}
	pick, _, ok := resp.ChoiceValue("pick")
	if !ok {
		return nil, false
	}
	for i := range candidates {
		if pick == fmt.Sprintf("q%d", i) {
			if i == 0 {
				return nil, true
			}
			return &candidates[i], true
		}
	}
	return nil, false
}

func lastCustomerTurn(history []gemini.HistoryItem) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[i].Content
		}
	}
	return ""
}

// searchKeywordsOf distills a text to its segmented search tokens (the same
// representation the lexical index uses).
func searchKeywordsOf(text string) string {
	return strings.TrimSpace(SegmentForSearch(text))
}
