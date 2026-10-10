package replyscore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The judge's parsing and its gate semantics are pinned here: an unscored case is a
// failed case in every harness, and a judge that answers outside 0-3 must not inflate
// an average.

func TestJudgeParsesTheVerdictOutOfSurroundingProse(t *testing.T) {
	var sawPrompt string
	ask := func(_ context.Context, prompt string, _ time.Duration) (string, bool) {
		sawPrompt = prompt
		return "Here is my audit:\n{\"language\":3,\"register\":3,\"natural\":2,\"format\":3,\"reason\":\"natural Khmer\"}\nDone.", true
	}
	v := Judge(context.Background(), ask, Case{Language: "km", Question: "តម្លៃប៉ុន្មាន?"}, "តម្លៃ ៣២ ដុល្លារ")
	if !v.Available() || v.Total != 11 || v.Language != 3 || v.Natural != 2 {
		t.Fatalf("verdict = %+v, want a parsed 11/12", v)
	}
	if v.Reason != "natural Khmer" {
		t.Errorf("reason = %q, want the judge's own sentence", v.Reason)
	}
	// The prompt must carry the question, the reply and the house chat convention:
	// dropping the convention note makes the judge flag the list style the product asks for.
	for _, want := range []string{"តម្លៃប៉ុន្មាន?", "តម្លៃ ៣២ ដុល្លារ", "list lines starting with '- '", "km"} {
		if !strings.Contains(sawPrompt, want) {
			t.Errorf("judge prompt is missing %q", want)
		}
	}
}

func TestJudgeClampsAxesOutsideTheRubric(t *testing.T) {
	ask := func(context.Context, string, time.Duration) (string, bool) {
		return `{"language":7,"register":-2,"natural":3,"format":3,"reason":"out of range"}`, true
	}
	v := Judge(context.Background(), ask, Case{Language: "en"}, "reply")
	if v.Total != 9 || v.Language != 3 || v.Register != 0 {
		t.Fatalf("verdict = %+v, want clamped axes (3/0/3/3)", v)
	}
	if !v.Available() {
		t.Error("a clamped but scored verdict must remain available")
	}
}

func TestAnUnansweredOrUnparseableJudgeIsUnavailableNotAPass(t *testing.T) {
	failing := func(context.Context, string, time.Duration) (string, bool) { return "", false }
	if v := Judge(context.Background(), failing, Case{}, "reply"); v.Available() {
		t.Error("a failed judge call must not score as available")
	} else if v.Reason != "judge unavailable" {
		t.Errorf("reason = %q", v.Reason)
	}
	prose := func(context.Context, string, time.Duration) (string, bool) { return "no json here", true }
	if v := Judge(context.Background(), prose, Case{}, "reply"); v.Available() {
		t.Errorf("unparseable judge output scored as available: %+v", v)
	}
	if v := Judge(context.Background(), nil, Case{}, "reply"); v.Available() {
		t.Error("a nil ask must not score")
	}
}
