package platform

import (
	"context"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/config"
	"khmer-ai-cs-go/internal/textutil"
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
var routeBudget = config.EnvMillis("JEV_ROUTE_BUDGET_MS", 4*time.Second)

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

	resp, err := p.Jev.Judge(ctx, map[string]any{"customer_message": textutil.Ellipsize(msg, 600)},
		map[string]typesafe.Question{
			"route": typesafe.Choice(
				"What does `customer_message` need from this store's customer service?",
				map[string]string{
					RouteKBQuestion:  "A product or service question answerable from the store knowledge base",
					RouteSmallTalk:   "Greeting, thanks, or pure pleasantries with NO question in it — a message that asks for something (the company itself, a product, a price, delivery, or the assistant) is a product question, not chit-chat",
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
	case v >= config.EnvFloat("JEV_ROUTE_URGENT_MIN", 1.5):
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
func routeDecision(route string, prob float64, content string) (escalate, skipGround, silent bool) {
	if route == RouteHandoff && prob >= config.EnvFloat("JEV_ROUTE_HANDOFF_MIN", 0.80) {
		return true, false, false
	}
	if route == RouteSmallTalk && prob >= config.EnvFloat("JEV_ROUTE_CHITCHAT_MIN", 0.80) {
		// A question is never chit-chat.
		//
		// skipGround means "the router is confident this carries no request, so
		// answer from the fixed template without retrieval". Measured 2026-10-04:
		// "你是谁" was routed small_talk, the shortcut fired, and the customer got
		// "谢谢您的消息！有任何问题随时告诉我。" with tokens_used=0 and
		// model_name=smalltalk-template — a real question (answerable from the
		// company-profile document) answered by a line whose whole premise is that
		// there is no question. The router is a model and will be wrong sometimes;
		// the shortcut now has to survive one veto.
		//
		// The junk route below is deliberately NOT given the same treatment: spam and
		// ads do contain question marks, so "looks like a question" is not evidence
		// against silence, and wrongly silencing a customer is the worse failure.
		if !LooksLikeQuestion(content) {
			return false, true, false
		}
	}
	// 0.90 default, not 0.85: the documented Jev accuracy on Khmer is lower
	// than on English (jeveval -mode khmer), and a wrongly-silenced Khmer
	// customer is a worse outcome than one wasted generation. Raise the bar
	// until there is Khmer-route accuracy data justifying a lower one.
	if route == RouteJunk && prob >= config.EnvFloat("JEV_ROUTE_JUNK_MIN", 0.90) {
		// A question is never silenced either — and this veto matters more than the
		// chit-chat one, because silence is the router's only irreversible action:
		// no reply, no generation, no delivery. The customer sees a bot that ignored
		// them, and the threshold above is the only thing standing in the way.
		//
		// Evidence (2026-10-04, SQL over chat_messages): two real Telegram questions —
		// "你是什么模型" and "你好 你有哪些产品" (2026-09-26) — are the only customer
		// messages in the database with no reply of any kind, and silence is the one
		// path that produces that. The cost of the veto is a generation for spam that
		// happens to contain a question mark; the code above already states the
		// trade-off it wants ("a wrongly-silenced customer is a worse outcome than one
		// wasted generation").
		if LooksLikeQuestion(content) {
			return false, false, false
		}
		return false, false, true
	}
	return false, false, false
}

// RouteDecision is routeDecision for callers outside the platform package
// (the web widget applies the same switches on its own reply path). content is the
// customer's message — the chit-chat veto needs it, and skipping it would mean the
// widget kept answering questions with the pleasantries template.
func RouteDecision(route string, prob float64, content string) (escalate, skipGround, silent bool) {
	return routeDecision(route, prob, content)
}

// questionSubstrings — markers that say "this message asks something". Khmer and
// Chinese do not put spaces between words, so those are substring tests: "ណា" also
// matches "ណាស់" and "几" also matches "几乎", and that is the intended direction.
var questionSubstrings = []string{
	"?", "？",
	// Khmer
	"អ្វី", "ណា", "ប៉ុន្មាន", "ដែរ", "ទេ", "ឬ",
	// Chinese
	"谁", "什么", "什麼", "哪", "多少", "几", "怎", "如何", "为什么", "為什麼",
	"吗", "嗎", "呢", "是不是", "有没有", "有沒有", "能不能", "请问", "請問",
}

// questionWords — English interrogatives, unambiguous wherever they appear.
var questionWords = []string{"who", "whose", "whom", "what", "which", "when", "where", "why", "how"}

// questionOpeners — auxiliaries that only signal a question as the FIRST word
// ("Can you…", "Do you…"), which keeps "this is great" out of the veto.
var questionOpeners = []string{
	"can", "could", "do", "does", "did", "is", "are", "was", "were",
	"will", "would", "should", "have", "has", "any", "may",
}

// LooksLikeQuestion reports whether a customer message asks something.
//
// Deliberately over-inclusive, because it is only ever used as a VETO on the
// chit-chat shortcut: a false positive costs one paid generation where the template
// was free, while a false negative is a real question answered with "thanks for
// your message" (the 2026-10-04 "你是谁" case this exists to prevent).
func LooksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lowered := strings.ToLower(trimmed)
	if textutil.ContainsAny(lowered, questionSubstrings) {
		return true
	}
	fields := strings.Fields(lowered)
	if len(fields) == 0 {
		return false
	}
	first := strings.Trim(fields[0], `.,!;:"'()[]{}`)
	for _, opener := range questionOpeners {
		if first == opener {
			return true
		}
	}
	for _, f := range fields {
		word := strings.Trim(f, `.,!;:"'()[]{}`)
		for _, qw := range questionWords {
			if word == qw {
				return true
			}
		}
	}
	return false
}
