package platform

import (
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/gemini"
)

func TestSplitPlatformTextShort(t *testing.T) {
	got := SplitPlatformText("hello", 100)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("short text must stay whole: %v", got)
	}
}

func TestSplitPlatformTextLong(t *testing.T) {
	text := strings.Repeat("word ", 1000) // ~5000 runes
	got := SplitPlatformText(text, 4096)
	if len(got) < 2 {
		t.Fatalf("long text must split, got %d chunk(s)", len(got))
	}
	joined := strings.Join(got, "")
	// No runes lost.
	if len([]rune(joined)) != len([]rune(text)) {
		t.Fatalf("split lost runes: %d vs %d", len([]rune(joined)), len([]rune(text)))
	}
}

func TestHandoffAcknowledgementLanguages(t *testing.T) {
	if handoffAcknowledgement("en") == "" || handoffAcknowledgement("zh") == "" || handoffAcknowledgement("km") == "" {
		t.Fatal("all language acks must be non-empty")
	}
	if handoffAcknowledgement("xx") != handoffAcknowledgement("km") {
		t.Fatal("unknown language must fall back to Khmer")
	}
}

// The handoff contract, pinned from both ends.
//
// The prompt requires an exact Khmer sentence for a committed transfer, and
// ReplyClaimsHandoff is what decides whether an agent actually gets paged. On
// 2026-10-04 the sentence's subject was rewritten from "ភ្នាក់ងារមនុស្ស" (a literal
// "human agent", which the reply-quality judge twice called translated English)
// to "បុគ្គលិករបស់យើង". That edit touches a path where a mismatch is SILENT: the
// customer is told a human is coming, no agent is notified, and nothing errors.
// So both ends are asserted against the one constant the prompt interpolates.
func TestKhmerHandoffSentenceIsStillMatchedByTheDetector(t *testing.T) {
	if !ReplyClaimsHandoff(gemini.KhmerHandoffSentence) {
		t.Fatalf("the required Khmer handoff sentence is no longer detected as a claim: %q",
			gemini.KhmerHandoffSentence)
	}
	full := HandoffAcknowledgement("km")
	if !ReplyClaimsHandoff(full) {
		t.Fatalf("the canned Khmer acknowledgement is no longer detected as a claim: %q", full)
	}
	// The prompt's few-shot example is the sentence too — a customer reading what
	// the AI was taught to write must see the same string the matcher knows.
	if !strings.Contains(gemini.DefaultSystemPrompt, gemini.KhmerHandoffSentence) {
		t.Fatal("the system prompt no longer contains the sentence the matcher keys on")
	}
	// And the calque must not come back: it is what the judge flagged, and the
	// canned copy is the one string a human would have to edit by hand.
	if strings.Contains(full, "ភ្នាក់ងារមនុស្ស") {
		t.Errorf("the handoff copy is back to the literal 'human agent' rendering: %q", full)
	}
}

// A customer may ask for a person using the same plain word the bot now uses.
func TestHumanRequestKeywordMatchesPlainKhmerStaffWords(t *testing.T) {
	for _, msg := range []string{"សុំបុគ្គលិកបន្តិច", "ខ្ញុំចង់និយាយជាមួយបុគ្គលិក", "ភ្នាក់ងារមនុស្ស"} {
		if _, ok := HumanRequestKeyword(msg); !ok {
			t.Errorf("a request for a person must escalate: %q", msg)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	if got := safeFilename("a/b\\c\x00d.txt", "audio", 1); strings.ContainsAny(got, "/\\\x00") {
		t.Fatalf("unsafe chars survived: %q", got)
	}
	if got := safeFilename("", "voice", 7); got != "voice-7.bin" {
		t.Fatalf("empty filename must derive: %q", got)
	}
}

func TestHumanRequestKeywordMatches(t *testing.T) {
	hits := []string{
		"我要人工客服", "转人工", "你好，我想找真人",
		"can I talk to a human?", "connect me to an agent please",
		"I need to speak to staff", "speak to a real person",
		"សុំនិយាយជាមួយមនុស្ស", "សុំភ្នាក់ងារ",
	}
	for _, msg := range hits {
		if _, ok := humanRequestKeyword(msg); !ok {
			t.Errorf("expected escalation for %q", msg)
		}
	}
	misses := []string{
		"how is the weather", "I manage a team", "what is the price", "reagent kit shipping",
		"my order 12345 not delivered yet",
		// Negations: declining a transfer must not re-escalate.
		"不用转人工了", "不需要人工", "不用了，谢谢", "取消转人工",
		"I don't need an agent anymore", "no need for a human, thanks",
	}
	for _, msg := range misses {
		if _, ok := humanRequestKeyword(msg); ok {
			t.Errorf("unexpected escalation for %q", msg)
		}
	}
	// A negation that follows the keyword still escalates.
	if _, ok := humanRequestKeyword("I need a human, don't send me a bot"); !ok {
		t.Errorf("expected escalation for a request preceding its negation")
	}
}

// A question mislabelled small_talk must not lose its no_knowledge_base handoff.
//
// The exception below exists because chit-chat legitimately has no KB answer; the
// bug was that a QUESTION labelled small_talk inherited it. "你是谁" was labelled
// small_talk on 2026-10-04 — if the KB had also missed it, the customer would have
// got an ungrounded guess instead of a human, with nothing logged anywhere.
func TestTurnTriggerSmallTalkExceptionOnlyCoversRealPleasantries(t *testing.T) {
	// Confidence must be under 0.35 for the no_knowledge_base rule to be in play at
	// all: the exception only ever mattered on low-confidence labels.
	smallTalk := gemini.TurnVerdict{Intent: "small_talk", Sentiment: "neutral", Confidence: 0.2}
	// A real pleasantry with no KB match: no handoff, as before.
	if trig, _ := TurnTrigger(smallTalk, false, true, "谢谢"); trig != "" {
		t.Fatalf("a pleasantry must not open a handoff: %q", trig)
	}
	// The same label on a question must not excuse the miss.
	for _, question := range []string{"你是谁", "你们有什么产品", "តើអ្នកជាអ្នកណា?", "who are you"} {
		trig, _ := TurnTrigger(smallTalk, false, true, question)
		if trig != "no_knowledge_base" {
			t.Errorf("question %q mislabelled small_talk must still hand off, got %q", question, trig)
		}
	}
}

func TestTurnTrigger(t *testing.T) {
	hard := []struct {
		intent   string
		wantTrig string
	}{
		{"complaint", "negative_feedback"},
		{"refund", "negative_feedback"},
		{"legal", "negative_feedback"},
		{"customization", "ai_decision"},
		{"bulk_order", "ai_decision"},
	}
	for _, c := range hard {
		v := gemini.TurnVerdict{Intent: c.intent, Sentiment: "neutral", Confidence: 0.9}
		trig, reason := TurnTrigger(v, true, true, "谢谢")
		if trig != c.wantTrig {
			t.Errorf("intent %q: trigger = %q (%s), want %q", c.intent, trig, reason, c.wantTrig)
		}
	}
	// Negative sentiment wins as negative_feedback.
	if trig, _ := TurnTrigger(gemini.TurnVerdict{Intent: "price", Sentiment: "negative", Confidence: 0.9}, true, true, "太贵了"); trig != "negative_feedback" {
		t.Errorf("negative sentiment: trigger = %q", trig)
	}
	// Plain answered question: no escalation.
	if trig, _ := TurnTrigger(gemini.TurnVerdict{Intent: "price", Sentiment: "neutral", Confidence: 0.9}, true, true, "多少钱"); trig != "" {
		t.Errorf("answered price question must not escalate, got %q", trig)
	}
	// No-KB miss with low confidence escalates when docs exist.
	if trig, _ := TurnTrigger(gemini.TurnVerdict{Intent: "other", Sentiment: "neutral", Confidence: 0.2}, false, true, "你们有这个型号吗"); trig != "no_knowledge_base" {
		t.Errorf("no-KB miss: trigger = %q", trig)
	}
	// Same miss without docs: silence.
	if trig, _ := TurnTrigger(gemini.TurnVerdict{Intent: "other", Sentiment: "neutral", Confidence: 0.2}, false, false, "你们有这个型号吗"); trig != "" {
		t.Errorf("no-KB miss without docs must stay silent, got %q", trig)
	}
	// Classifier-recommended escalation.
	if trig, _ := TurnTrigger(gemini.TurnVerdict{Intent: "order_status", Sentiment: "neutral", Confidence: 0.8, Escalate: true}, true, true, "我的订单到哪了"); trig != "ai_decision" {
		t.Errorf("classifier escalate: trigger = %q", trig)
	}
}

func TestReplyClaimsHandoff(t *testing.T) {
	claims := []string{
		"好的，已为您转接人工客服处理您的定制需求。",
		"我已把这次对话转交人工处理。",
		"Connecting you to a human agent now.",
		"I've escalated this to our support team.",
		"ខ្ញុំនឹងប្រគល់ការសន្ទនានេះទៅឱ្យពួកគេ។",
	}
	for _, reply := range claims {
		if !ReplyClaimsHandoff(reply) {
			t.Errorf("expected handoff claim for %q", reply)
		}
	}
	// Offers/questions must NOT trigger — only the customer's consent decides.
	offers := []string{
		"我可以为您转接人工客服，您需要吗？",
		"需要我为您转接人工客服吗？",
		"Would you like me to connect you to a human agent?",
		"如果您需要人工客服，请随时告诉我。",
	}
	for _, reply := range offers {
		if ReplyClaimsHandoff(reply) {
			t.Errorf("offer must not count as a claim: %q", reply)
		}
	}
}
