package api

import "testing"

// The batch endpoint hands the model a numbered list and expects a JSON array
// back. Models sometimes wrap it in prose or fences, and occasionally merge
// entries — the parser must degrade predictably so callers can fall back.
func TestParseTranslationArray(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  int
		first string
	}{
		{"plain", `["a","b","c"]`, 3, "a"},
		{"fenced", "```json\n[\"你好\",\"再见\"]\n```", 2, "你好"},
		{"with prose", "Here you go:\n[\"x\",\"y\"]\nHope that helps!", 2, "x"},
		{"khmer", `["សួស្តី","អរគុណ"]`, 2, "សួស្តី"},
		{"escaped newline", `["line1\nline2"]`, 1, "line1\nline2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseTranslationArray(c.reply, c.want)
			if len(got) != c.want {
				t.Fatalf("len = %d (%v), want %d", len(got), got, c.want)
			}
			if got[0] != c.first {
				t.Fatalf("first = %q, want %q", got[0], c.first)
			}
		})
	}

	// Non-JSON replies fall back to line splitting only when the count matches.
	if got := parseTranslationArray("one\ntwo\nthree", 3); len(got) != 3 {
		t.Fatalf("line fallback: %v", got)
	}
	if got := parseTranslationArray("just one line", 3); got != nil {
		t.Fatalf("mismatched count must return nil, got %v", got)
	}
	if got := parseTranslationArray("", 1); got != nil {
		t.Fatalf("empty reply must return nil, got %v", got)
	}
}

func TestResolveTranslateTarget(t *testing.T) {
	// Explicit valid target wins.
	if got := resolveTranslateTarget("សួស្តី", "en"); got != "en" {
		t.Errorf("explicit target = %q", got)
	}
	// Unset target: Khmer source → Chinese for the (Chinese-speaking) agent.
	if got := resolveTranslateTarget("តម្លៃប៉ុន្មាន", ""); got != "zh" {
		t.Errorf("khmer source default = %q, want zh", got)
	}
	// Unset target, non-Khmer source → Khmer (the customer's likely language).
	if got := resolveTranslateTarget("how much is it", ""); got != "km" {
		t.Errorf("non-khmer source default = %q, want km", got)
	}
	// Unknown code is treated as unset rather than rejected.
	if got := resolveTranslateTarget("hello", "xx"); got != "km" {
		t.Errorf("unknown target = %q, want km", got)
	}
}

func TestTranslateCacheKeyStable(t *testing.T) {
	a := translateCacheKey("hello", "km")
	b := translateCacheKey("hello", "km")
	if a != b {
		t.Fatal("cache key must be deterministic")
	}
	if a == translateCacheKey("hello", "zh") {
		t.Fatal("different targets must key differently")
	}
	if a == translateCacheKey("hello!", "km") {
		t.Fatal("different text must key differently")
	}
}
