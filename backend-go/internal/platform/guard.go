package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/textutil"
	"khmer-ai-cs-go/internal/typesafe"
)

// ReplyGuard is the pre/post-delivery semantic audit of one AI reply. The
// regex nets (StripSourceMarkers, ReplyClaimsHandoff) catch literal patterns;
// Jev catches their paraphrases.
type ReplyGuard struct {
	PromisesHandoff bool
	LeaksSources    bool
	UnsafeClaim     bool
	// SupportedBySources is only meaningful when sources were passed in:
	// false means the reply asserts concrete facts the retrieved passages
	// do not back up (hallucination signal).
	SupportedBySources bool
}

// guardBudget bounds the audit: on the platform path it delays delivery by
// at most this much, and a dead Jev degrades to "no extra checks" instead of
// holding the customer's answer hostage.
//
// 1500ms sat BELOW the production server's measured 0.8-2.1s spread to
// api.typesafe.ai, so every slow-but-healthy call silently dropped the
// semantic audit and left only the regex nets. Raised above that spread;
// tunable so ops can re-measure from the host without a rebuild.
var guardBudget = config.EnvMillis("JEV_GUARD_BUDGET_MS", 3*time.Second)

// GuardReply asks Jev three yes/no checks about a reply plus, when knowledge
// sources grounded it, whether every concrete claim is actually backed by
// those passages (citation check). ok=false when Jev is disabled,
// unavailable, or incomplete — callers then keep only their regex safety
// nets. An empty sources slice skips the citation question.
func (p *Pipeline) GuardReply(ctx context.Context, reply string, sources []string) (ReplyGuard, bool) {
	if !p.Jev.Enabled() {
		return ReplyGuard{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, guardBudget)
	defer cancel()

	state := map[string]any{"assistant_reply": textutil.Ellipsize(reply, 800)}
	questions := map[string]typesafe.Question{
		"promises_handoff": typesafe.Noul(
			"Does `assistant_reply` tell or imply to the customer that a human agent will take over, " +
				"contact them, or continue this conversation — even without literally saying 'transfer'?"),
		"leaks_sources": typesafe.Noul(
			"Does `assistant_reply` mention sources, citations, reference numbers, document or file names, " +
				"or markers like (Source 1)?"),
		"unsafe_claim": typesafe.Noul(
			"Does `assistant_reply` commit to a specific price, discount, delivery date, stock quantity, " +
				"legal outcome, or structural/medical guarantee that a store employee would have to confirm first?"),
	}
	if len(sources) > 0 {
		passages := make([]map[string]any, 0, len(sources))
		for i, src := range sources {
			passages = append(passages, map[string]any{"id": fmt.Sprintf("s%d", i), "text": textutil.Ellipsize(src, 500)})
		}
		state["kb_passages"] = passages
		questions["supported_by_sources"] = typesafe.Noul(
			"Is every concrete factual claim in `assistant_reply` (prices, dates, quantities, specifications, policies) " +
				"directly supported by `kb_passages`? Generic politeness, offers to check with staff, and answers to " +
				"greetings do not need support.")
	}
	resp, err := p.Jev.Judge(ctx, state, questions)
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("jev reply guard failed; keeping regex nets only", "error", err.Error())
		}
		return ReplyGuard{}, false
	}
	bar := config.EnvFloat("JEV_GUARD_MIN", 0.70)
	// A false "promises handoff" verdict creates a bogus handoff request, so
	// it sits behind a higher bar than the advisory checks (live false
	// positive on a plain greeting at 0.70, 2026-09-21).
	handoffBar := config.EnvFloat("JEV_GUARD_HANDOFF_MIN", 0.85)
	handoff, okH := resp.NoulValue("promises_handoff")
	leaks, okL := resp.NoulValue("leaks_sources")
	unsafe, okU := resp.NoulValue("unsafe_claim")
	if !okH || !okL || !okU {
		return ReplyGuard{}, false
	}
	// The citation question is only sent when sources grounded the reply; a
	// missing answer there means an incomplete audit, never a false "supported".
	supported, okS := resp.NoulValue("supported_by_sources")
	if len(sources) > 0 && !okS {
		return ReplyGuard{}, false
	}
	return ReplyGuard{
		PromisesHandoff:    handoff >= handoffBar,
		LeaksSources:       leaks >= bar,
		UnsafeClaim:        unsafe >= bar,
		SupportedBySources: len(sources) == 0 || supported >= bar,
	}, true
}

// citationLineMarkers — line-level citation leftovers the token-level
// StripSourceMarkers cannot see ("According to the price list document…").
var citationLineMarkers = []string{
	"(source", "source 1", "source 2", "来源", "ឯកសារ", "according to the document",
	"according to our document", "the document states", "ឯកសារយោង",
}

// StripCitationLines drops whole lines that read as citations. Only applied
// when the Jev guard says a stripped reply still leaks sources.
func StripCitationLines(reply string) string {
	lines := strings.Split(reply, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		lowered := strings.ToLower(strings.TrimSpace(ln))
		drop := false
		for _, m := range citationLineMarkers {
			if strings.Contains(lowered, m) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, ln)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// alertQuality pages the tenant once per session per 10 minutes when a guard
// check flags the reply (unconfirmed commitments, KB-unsupported claims). The
// reply still goes out — a human reviews it in the inbox afterwards.
func (p *Pipeline) alertQuality(ctx context.Context, userID int32, sessionID, title, body string) {
	if p.Redis != nil {
		if ok, err := p.Redis.IncrWindow(ctx, "jev-guard-alert:"+sessionID, 1, 10*time.Minute); err == nil && !ok {
			return
		}
	}
	if p.Logger != nil {
		p.Logger.Warn("jev guard flagged the reply", "session_id", sessionID, "title", title)
	}
	p.notifyUser(ctx, userID, "warning", title, body, sessionID)
}

// AlertQuality is alertQuality for the web-chat path, whose streamed reply
// can no longer be edited — the owner review is the only remedy there.
func (p *Pipeline) AlertQuality(ctx context.Context, userID int32, sessionID, title, body string) {
	p.alertQuality(ctx, userID, sessionID, title, body)
}
