package platform

import (
	"context"

	"khmer-ai-cs-go/internal/typesafe"
)

// Inbound routes — the pre-retrieval triage of one customer message. The
// platform pipeline and the web widget share this decision so both entry
// points behave identically (they drifted before, see TurnTrigger's comment).
const (
	RouteKBQuestion  = "kb_question"
	RouteSmallTalk   = "small_talk"
	RouteHandoff     = "handoff_request"
	RouteTransaction = "transaction"
)

var inboundRouteValues = map[string]bool{
	RouteKBQuestion: true, RouteSmallTalk: true, RouteHandoff: true, RouteTransaction: true,
}

// routeBudget bounds the routing call: it sits on the reply path, so a slow
// or dead Jev must degrade to "route unknown" quickly, never stall the turn.
// 2.5s only just covered the prod server's measured 0.8-2.1s spread to
// api.typesafe.ai with no margin for its tail; raised, and tunable so ops can
// re-measure from the host without a rebuild.
var routeBudget = envMillis("JEV_ROUTE_BUDGET_MS", 4000)

// RouteInbound asks Jev which handling path a message needs, before any
// retrieval or generation. The returned probability is the model's own
// probability for the chosen option; callers threshold it in code.
// ok=false when Jev is disabled, unavailable, or answers out of vocabulary —
// every caller then keeps today's behaviour unchanged.
func (p *Pipeline) RouteInbound(ctx context.Context, msg string) (route string, prob float64, ok bool) {
	if !p.Jev.Enabled() {
		return "", 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, routeBudget)
	defer cancel()

	resp, err := p.Jev.Judge(ctx, map[string]any{"customer_message": truncateStr(msg, 600)},
		map[string]typesafe.Question{
			"route": typesafe.Choice(
				"What does `customer_message` need from this store's customer service?",
				map[string]string{
					RouteKBQuestion:  "A product or service question answerable from the store knowledge base",
					RouteSmallTalk:   "Greeting, thanks, or chit-chat with no request in it",
					RouteHandoff:     "Asks to talk to a human agent, or rejects the AI assistant",
					RouteTransaction: "Order, booking, payment, or delivery arrangement that needs store action",
				}),
		})
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("jev inbound routing failed; keeping default path", "error", err.Error())
		}
		return "", 0, false
	}
	a, present := resp.Answers["route"]
	if !present || a.Type != "choice" || !inboundRouteValues[a.Choice] {
		return "", 0, false
	}
	return a.Choice, a.Probabilities[a.Choice], true
}

// routeDecision turns a routed answer into the two behavioural switches,
// thresholded in code (calibrated knobs, never model prose).
func routeDecision(route string, prob float64) (escalate, skipGround bool) {
	if route == RouteHandoff && prob >= envFloat("JEV_ROUTE_HANDOFF_MIN", 0.80) {
		return true, false
	}
	if route == RouteSmallTalk && prob >= envFloat("JEV_ROUTE_CHITCHAT_MIN", 0.80) {
		return false, true
	}
	return false, false
}

// RouteDecision is routeDecision for callers outside the platform package
// (the web widget applies the same switches on its own reply path).
func RouteDecision(route string, prob float64) (escalate, skipGround bool) {
	return routeDecision(route, prob)
}
