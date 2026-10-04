package rag

import "testing"

// The rerank gate as a table. The row that matters is topDense == nil: it is the
// turn whose embedding hop failed, and reranking there spends up to 8s of the
// reply path's budget on a lexical-only candidate set that the auxiliary pass has
// the least to improve (and which it then filters down, observed avg_sources
// 5.00 -> 2.69 at RAG_RERANK_SKIP=0.90, 2026-10-04).
func TestShouldRerank(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	const skip = 0.60

	for _, tc := range []struct {
		name      string
		topDense  *float64
		topAgrees bool
		want      bool
	}{
		// No vector leg at all: never rerank. Before this rule the condition was
		// `!leaderClear && !signalsAgree`, both of which are false when topDense is
		// nil only because they are guarded on it — so the rerank ran.
		{"dense leg empty", nil, false, false},
		{"dense leg empty but legs agree", nil, true, false},

		// A confident dense leader is already good enough — that is what skip is for.
		{"leader clear", f(0.80), false, false},
		{"leader exactly at the bar", f(0.60), false, false},

		// Weak dense leader, no agreement: rerank (the case the pass exists for).
		{"weak leader, no agreement", f(0.40), false, true},
		{"weak leader, below the agree bar", f(0.55), true, true},

		// Weak-but-not-terrible leader AND both legs agreeing on it: skip.
		{"agreement at the bar", f(0.60), true, false},
		{"agreement above the bar", f(0.70), true, false},

		// A skip bar looser than the agree bar must not make agreement rerank more.
		{"agree bar independent of skip", f(0.62), true, false},
	} {
		if got := shouldRerank(tc.topDense, tc.topAgrees, skip); got != tc.want {
			t.Errorf("%s: shouldRerank(%v, %v, %.2f) = %v, want %v",
				tc.name, derefOrNil(tc.topDense), tc.topAgrees, skip, got, tc.want)
		}
	}
}

func derefOrNil(p *float64) any {
	if p == nil {
		return "nil"
	}
	return *p
}

// A zero skip bar means "every dense leader is clear", which is how production
// RAG_RERANK_SKIP=0.60 behaves on this corpus (0 reranks) — but it must still not
// rerank when there is no leader to judge.
func TestShouldRerankWithAnAggressiveSkipStillGuardsNil(t *testing.T) {
	if shouldRerank(nil, false, 0) {
		t.Fatal("a zero skip bar must not make an absent dense leg rerankable")
	}
	zero := 0.0
	if shouldRerank(&zero, false, 0) {
		t.Fatal("a dense leader at 0.0 is 'clear' under a 0 skip bar; nothing to rerank")
	}
}
