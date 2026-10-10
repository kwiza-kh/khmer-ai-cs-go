package replyscore

// The judge: the half of the rubric that needs a model. It lives here, beside the
// deterministic checks, because every tool that grades replies must grade them the
// same way — jeveval runs it over the serving provider, claudeeval runs it per arm
// of a provider comparison — and two copies of a rubric drift. A drifted rubric
// reports a model change as a quality change.
//
// What the judge adds to the string checks is the part no string check can see: is
// this actually Khmer, is the register consistent, does it read like a person, is it
// chat-shaped. Its numbers are a trend across runs, never a verdict on their own;
// the prompt is exported so a human can audit what it was asked.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// JudgeVerdict is one judged reply: four axes of 0-3, a reason, and their total.
type JudgeVerdict struct {
	Language int    `json:"language"` // 0-3: is it the asked-for language, cleanly
	Register int    `json:"register"` // 0-3: one consistent polite register
	Natural  int    `json:"natural"`  // 0-3: reads like a person, not translated English
	Format   int    `json:"format"`   // 0-3: chat-shaped (no markdown/citations)
	Reason   string `json:"reason"`   // one short sentence
	Total    int    `json:"total"`    // Language+Register+Natural+Format
}

// MaxJudgeTotal is the full rubric total. A harness gate compares against it.
const MaxJudgeTotal = 12

// Available reports whether the judge actually scored this reply. An unscored case
// is a failed case in the harnesses: a gate that cannot fail is not a gate, and the
// old one failed only LOW scores, so a reply nobody looked at could be reported as
// passing (2026-10-04).
func (v JudgeVerdict) Available() bool { return v.Total > 0 }

// Ask is one model call: a prompt, a budget, and the answer text with ok=false when
// the call did not answer. It is the fast model's GenerateFast, so the judge is not
// the model under test.
type Ask func(ctx context.Context, prompt string, timeout time.Duration) (string, bool)

// Judge scores one reply through ask. A failed call, or an answer that is not the
// expected JSON, yields an unavailable verdict with the reason attached. One retry
// is allowed on a FAILED CALL: the harnesses count an unscored case as a failed one,
// and a single 45s timeout (measured: one in ~90 judge calls) must not read as a
// quality failure of the model under test. An unparseable answer is not retried —
// that is the judge model's own output, not a transport failure.
func Judge(ctx context.Context, ask Ask, c Case, reply string) JudgeVerdict {
	if ask == nil {
		return JudgeVerdict{Reason: "judge unavailable"}
	}
	text, ok := ask(ctx, JudgePrompt(c, reply), 45*time.Second)
	if !ok {
		text, ok = ask(ctx, JudgePrompt(c, reply), 45*time.Second)
	}
	if !ok {
		return JudgeVerdict{Reason: "judge unavailable"}
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return JudgeVerdict{Reason: "judge returned no JSON"}
	}
	var v JudgeVerdict
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return JudgeVerdict{Reason: "judge JSON unparseable"}
	}
	// Clamp each axis: a judge that answers 7 must not inflate a 12-point average.
	v.Language = clampAxis(v.Language)
	v.Register = clampAxis(v.Register)
	v.Natural = clampAxis(v.Natural)
	v.Format = clampAxis(v.Format)
	v.Total = v.Language + v.Register + v.Natural + v.Format
	return v
}

func clampAxis(n int) int {
	if n < 0 {
		return 0
	}
	if n > 3 {
		return 3
	}
	return n
}

// JudgePrompt is the exact question the judge model is asked. Exported so the
// rubric's wording can be audited and pinned by a test.
func JudgePrompt(c Case, reply string) string {
	return "You are auditing ONE customer-service reply from a Cambodian EPS/insulation supplier.\n" +
		"The customer wrote in " + c.Language + ". The reply must be in " + c.Language + ".\n\n" +
		"Question: " + c.Question + "\n" +
		"Reply: " + reply + "\n\n" +
		"Score the reply on four axes, each 0-3:\n" +
		"- language: 3 = fluent, idiomatic " + c.Language + "; 0 = wrong language or unreadable\n" +
		"- register: 3 = one consistent polite customer-service register; 0 = mixed/cold\n" +
		"- natural: 3 = reads like a Cambodian salesperson wrote it; 0 = word-for-word translation\n" +
		"- format: 3 = clean chat text; 0 = markup, citations, invisible junk, wall of text\n" +
		// The house chat convention, stated because it is a convention and not a
		// judgment call: without it the judge systematically penalises the list
		// style the product asks for (measured: it flagged '- ' bullets as markdown
		// in 4 of the first 23 cases). It is NOT told which pronoun to expect —
		// that disagreement is a finding for a native speaker, not something to
		// suppress.
		"Note: this product's chat convention allows plain paragraphs and list lines starting with '- ' or '• '. " +
		"Those are NOT formatting failures; only **bold**, ## headings, tables, code fences and citation markers are.\n" +
		"Answer with JSON only: {\"language\":n,\"register\":n,\"natural\":n,\"format\":n,\"reason\":\"<one short sentence>\"}"
}
