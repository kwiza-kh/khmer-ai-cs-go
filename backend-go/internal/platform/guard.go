package platform

import (
	"context"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/typesafe"
)

// ReplyGuard is the pre/post-delivery semantic audit of one AI reply. The
// regex nets (StripSourceMarkers, ReplyClaimsHandoff) catch literal patterns;
// Jev catches their paraphrases.
type ReplyGuard struct {
	PromisesHandoff bool
	LeaksSources    bool
	UnsafeClaim     bool
}

// guardBudget bounds the audit: on the platform path it delays delivery by
// at most this much, and a dead Jev degrades to "no extra checks" instead of
// holding the customer's answer hostage.
const guardBudget = 1500 * time.Millisecond

// GuardReply asks Jev three yes/no checks about a reply. ok=false when Jev is
// disabled, unavailable, or incomplete — callers then keep only their regex
// safety nets.
func (p *Pipeline) GuardReply(ctx context.Context, reply string) (ReplyGuard, bool) {
	if !p.Jev.Enabled() {
		return ReplyGuard{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, guardBudget)
	defer cancel()

	resp, err := p.Jev.Judge(ctx, map[string]any{"assistant_reply": truncateStr(reply, 800)},
		map[string]typesafe.Question{
			"promises_handoff": typesafe.Noul(
				"Does `assistant_reply` tell or imply to the customer that a human agent will take over, " +
					"contact them, or continue this conversation — even without literally saying 'transfer'?"),
			"leaks_sources": typesafe.Noul(
				"Does `assistant_reply` mention sources, citations, reference numbers, document or file names, " +
					"or markers like (Source 1)?"),
			"unsafe_claim": typesafe.Noul(
				"Does `assistant_reply` commit to a specific price, discount, delivery date, stock quantity, " +
					"legal outcome, or structural/medical guarantee that a store employee would have to confirm first?"),
		})
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("jev reply guard failed; keeping regex nets only", "error", err.Error())
		}
		return ReplyGuard{}, false
	}
	bar := envFloat("JEV_GUARD_MIN", 0.70)
	// A false "promises handoff" verdict creates a bogus handoff request, so
	// it sits behind a higher bar than the advisory checks (live false
	// positive on a plain greeting at 0.70, 2026-09-21).
	handoffBar := envFloat("JEV_GUARD_HANDOFF_MIN", 0.85)
	handoff, okH := resp.NoulValue("promises_handoff")
	leaks, okL := resp.NoulValue("leaks_sources")
	unsafe, okU := resp.NoulValue("unsafe_claim")
	if !okH || !okL || !okU {
		return ReplyGuard{}, false
	}
	return ReplyGuard{
		PromisesHandoff: handoff >= handoffBar,
		LeaksSources:    leaks >= bar,
		UnsafeClaim:     unsafe >= bar,
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

// alertUnsafeClaim pages the tenant once per session per 10 minutes when the
// guard sees an unconfirmed commitment (price, delivery date, stock, legal).
// The reply still goes out — a human reviews it in the inbox afterwards.
func (p *Pipeline) alertUnsafeClaim(ctx context.Context, userID int32, sessionID string) {
	if p.Redis != nil {
		if ok, err := p.Redis.IncrWindow(ctx, "jev-guard-alert:"+sessionID, 1, 10*time.Minute); err == nil && !ok {
			return
		}
	}
	if p.Logger != nil {
		p.Logger.Warn("jev guard: reply makes an unconfirmed commitment", "session_id", sessionID)
	}
	p.notifyUser(ctx, userID, "warning", "AI 回复包含待确认承诺",
		"Jev 标记该回复做出了需店员确认的承诺（价格/交期/库存等），请在收件箱检查该会话。", sessionID)
}

// AlertUnsafeClaim is alertUnsafeClaim for the web-chat path, whose streamed
// reply can no longer be edited — the owner review is the only remedy there.
func (p *Pipeline) AlertUnsafeClaim(ctx context.Context, userID int32, sessionID string) {
	p.alertUnsafeClaim(ctx, userID, sessionID)
}
