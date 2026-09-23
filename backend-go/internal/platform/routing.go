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
	// RouteJunk — spam, ads, gibberish: a message that deserves no reply at
	// all. It is deliberately gated behind the highest threshold of the four
	// routes (see routeDecision): wrongly silencing a real customer is the
	// worst failure mode this router has, worse than a wasted generation.
	RouteJunk = "junk"
)

var inboundRouteValues = map[string]bool{
	RouteKBQuestion: true, RouteSmallTalk: true, RouteHandoff: true,
	RouteTransaction: true, RouteJunk: true,
}

// Urgency — the Score verdict rides in the same Judge batch as the route, so
// the "should it be answered / what is it / how urgent" triple costs one call.
// The scale is 3 levels; ScoreValue returns a probability-weighted position
// (0..2) that code thresholds (see urgencyFromScore).
const (
	UrgencyUnknown  = ""
	UrgencyRoutine  = "routine"
	UrgencyElevated = "elevated"
	UrgencyUrgent   = "urgent"
)

// InboundRoute bundles one routing verdict.
type InboundRoute struct {
	Route string
	// Prob is the model's own probability for the chosen route.
	Prob float64
	// Urgency is "routine" | "elevated" | "urgent", or "" (unknown) when Jev
	// is off, timed out, or answered out of vocabulary.
	Urgency string
}

// routeBudget bounds the routing call: it sits on the reply path, so a slow
// or dead Jev must degrade to "route unknown" quickly, never stall the turn.
// 2.5s only just covered the prod server's measured 0.8-2.1s spread to
// api.typesafe.ai with no margin for its tail; raised, and tunable so ops can
// re-measure from the host without a rebuild.
var routeBudget = envMillis("JEV_ROUTE_BUDGET_MS", 4000)

// RouteInbound asks Jev what a message needs — route and urgency, judged in
// one batch — before any retrieval or generation. ok=false when Jev is
// disabled, unavailable, or answers out of vocabulary: every caller then
// keeps today's behaviour unchanged.
func (p *Pipeline) RouteInbound(ctx context.Context, msg string) (InboundRoute, bool) {
	if !p.Jev.Enabled() {
		return InboundRoute{}, false
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
					RouteJunk:        "Spam, advertisement, gibberish, or a solicitation that is not a customer request and deserves no reply",
				}),
			"urgency": typesafe.Score(
				"How urgent is `customer_message` for the store to handle?",
				[]string{
					"Routine: a normal question or chat, no time pressure",
					"Elevated: a problem is brewing — complaint tone, waiting on a delivery, a deadline mentioned",
					"Urgent: money at risk, legal threat, angry customer, or an explicit deadline right now",
				}),
		})
	if err != nil {
		if p.Logger != nil {
			p.Logger.Warn("jev inbound routing failed; keeping default path", "error", err.Error())
		}
		return InboundRoute{}, false
	}
	a, present := resp.Answers["route"]
	if !present || a.Type != "choice" || !inboundRouteValues[a.Choice] {
		return InboundRoute{}, false
	}
	return InboundRoute{
		Route:   a.Choice,
		Prob:    a.Probabilities[a.Choice],
		Urgency: urgencyFromScore(resp),
	}, true
}

// urgencyFromScore thresholds the probability-weighted position on the 3-level
// scale: [0,0.5) routine, [0.5,1.5) elevated, [1.5,2] urgent. The urgent cut
// is env-tunable because it decides handoff queue priority.
func urgencyFromScore(resp *typesafe.Response) string {
	v, ok := resp.ScoreValue("urgency")
	if !ok {
		return UrgencyUnknown
	}
	switch {
	case v >= envFloat("JEV_ROUTE_URGENT_MIN", 1.5):
		return UrgencyUrgent
	case v >= 0.5:
		return UrgencyElevated
	default:
		return UrgencyRoutine
	}
}

// routeDecision turns a routed answer into the three behavioural switches,
// thresholded in code (calibrated knobs, never model prose):
//
//	escalate    — hand the conversation to a human, AI stays silent
//	skipGround — answer without touching the knowledge base (chit-chat)
//	silent     — junk: no reply at all on platform channels
func routeDecision(route string, prob float64) (escalate, skipGround, silent bool) {
	if route == RouteHandoff && prob >= envFloat("JEV_ROUTE_HANDOFF_MIN", 0.80) {
		return true, false, false
	}
	if route == RouteSmallTalk && prob >= envFloat("JEV_ROUTE_CHITCHAT_MIN", 0.80) {
		return false, true, false
	}
	// 0.90 default, not 0.85: the documented Jev accuracy on Khmer is lower
	// than on English (jeveval -mode khmer), and a wrongly-silenced Khmer
	// customer is a worse outcome than one wasted generation. Raise the bar
	// until there is Khmer-route accuracy data justifying a lower one.
	if route == RouteJunk && prob >= envFloat("JEV_ROUTE_JUNK_MIN", 0.90) {
		return false, false, true
	}
	return false, false, false
}

// RouteDecision is routeDecision for callers outside the platform package
// (the web widget applies the same switches on its own reply path).
func RouteDecision(route string, prob float64) (escalate, skipGround, silent bool) {
	return routeDecision(route, prob)
}
