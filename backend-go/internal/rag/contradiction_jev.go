package rag

import (
	"context"
	"fmt"

	"khmer-ai-cs-go/internal/typesafe"
)

// kbExcerpt is one existing-KB excerpt used by the contradiction check.
type kbExcerpt struct{ Title, Content string }

// confirmContradictions asks Jev to confirm each contradiction the compile
// LLM listed — that single call is prone to over-flagging, and every false
// item costs a human review. Fail-open: any Jev problem keeps every item
// (a noisy review queue beats a silently dropped knowledge conflict).
func (s *Service) confirmContradictions(ctx context.Context, newTitle string, items []compileContradiction) []compileContradiction {
	if !s.Jev.Enabled() || len(items) == 0 {
		return items
	}
	state := map[string]any{"new_document_title": newTitle}
	questions := make(map[string]typesafe.Question, len(items))
	for i, it := range items {
		state[fmt.Sprintf("c%d", i)] = map[string]string{
			"new_claim": it.NewClaim, "old_claim": it.OldClaim, "old_doc_title": it.OldDocTitle,
		}
		questions[fmt.Sprintf("c%d", i)] = typesafe.Noul(
			"Read `c" + fmt.Sprintf("%d", i) + "`. Do its new_claim and old_claim give DIFFERENT specific values " +
				"(prices, quantities, dates, specifications) for the SAME item or policy? Answer yes only when both " +
				"statements address the same item and at least one stated value differs. Identical values, or " +
				"statements about unrelated topics, are no.")
	}
	resp, err := s.Jev.Judge(ctx, state, questions)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("jev contradiction confirm failed; keeping all items", "error", err.Error())
		}
		return items
	}
	bar := envF64("JEV_CONTRADICTION_MIN", 0.60)
	kept := make([]compileContradiction, 0, len(items))
	for i, it := range items {
		if v, ok := resp.NoulValue(fmt.Sprintf("c%d", i)); ok && v < bar {
			if s.Logger != nil {
				s.Logger.Info("jev dropped a compile contradiction as unconfirmed", "old_doc", it.OldDocTitle, "noul", v)
			}
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

// sweepContradictions runs one Jev Noul per existing excerpt (all in a single
// parallel call) to catch conflicts the compile LLM missed. Flagged pairs land
// in the review queue as low-severity pointers; humans read the actual docs.
func (s *Service) sweepContradictions(ctx context.Context, newTitle string, excerpts []kbExcerpt, already []compileContradiction) []compileContradiction {
	if !s.Jev.Enabled() || len(excerpts) == 0 {
		return nil
	}
	flagged := map[string]bool{}
	for _, it := range already {
		if it.OldDocTitle != "" {
			flagged[it.OldDocTitle] = true
		}
	}
	state := map[string]any{"new_document_title": newTitle}
	questions := make(map[string]typesafe.Question, len(excerpts))
	pending := make([]kbExcerpt, 0, len(excerpts))
	for _, ex := range excerpts {
		if flagged[ex.Title] {
			continue
		}
		state[fmt.Sprintf("e%d", len(pending))] = map[string]string{"title": ex.Title, "excerpt": ex.Content}
		questions[fmt.Sprintf("e%d", len(pending))] = typesafe.Noul(
			"Does the NEW DOCUMENT contradict this existing document on any concrete fact (price, date, policy, specification)?")
		pending = append(pending, ex)
	}
	if len(pending) == 0 {
		return nil
	}
	resp, err := s.Jev.Judge(ctx, state, questions)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("jev contradiction sweep failed; no extra items", "error", err.Error())
		}
		return nil
	}
	bar := envF64("JEV_CONTRADICTION_MIN", 0.60)
	var found []compileContradiction
	for i, ex := range pending {
		v, ok := resp.NoulValue(fmt.Sprintf("e%d", i))
		if !ok || v < bar {
			continue
		}
		found = append(found, compileContradiction{
			NewClaim:    "(Jev sweep — verify against the new document)",
			OldClaim:    "(see existing document)",
			OldDocTitle: ex.Title,
			Severity:    "low",
		})
	}
	return found
}
