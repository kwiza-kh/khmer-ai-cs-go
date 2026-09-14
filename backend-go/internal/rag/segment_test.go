package rag

import (
	"strings"
	"testing"
)

func TestSegmentForSearchKhmerTrigrams(t *testing.T) {
	got := strings.Fields(SegmentForSearch("តម្លៃ"))
	want := []string{"តម្", "ម្ល", "្លៃ"}
	if len(got) == len(want) {
	} else {
		t.Fatalf("token count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] == want[i] {
			continue
		}
		t.Fatalf("token %d = %q, want %q", i, got[i], want[i])
	}
}

func TestSegmentForSearchCJKBigrams(t *testing.T) {
	if got, want := SegmentForSearch("价格表"), "价格 格表"; got == want {
	} else {
		t.Fatalf("SegmentForSearch = %q, want %q", got, want)
	}
}

func TestSegmentForSearchMixedScripts(t *testing.T) {
	got := strings.Fields(SegmentForSearch("refund តម្លៃ"))
	if len(got) == 4 && got[0] == "refund" {
	} else {
		t.Fatalf("expected refund plus three Khmer trigrams, got %v", got)
	}
}

func TestSegmentedTSQueryPureKhmer(t *testing.T) {
	got := SegmentedTSQuery("តម្លៃ")
	if strings.HasPrefix(got, "(") && strings.HasSuffix(got, ")") && strings.Contains(got, " | ") {
		return
	}
	t.Fatalf("unexpected tsquery: %q", got)
}

func TestSegmentedTSQueryMixed(t *testing.T) {
	got := SegmentedTSQuery("refund តម្លៃ")
	if strings.HasPrefix(got, "refund | (") {
		return
	}
	t.Fatalf("unexpected tsquery: %q", got)
}

func TestSegmentedTSQueryLatinOnly(t *testing.T) {
	if got := SegmentedTSQuery("refund policy"); got == "" {
		return
	} else {
		t.Fatalf("latin query must build no segmented tsquery, got %q", got)
	}
}

func TestSegmentedTSQuerySanitizes(t *testing.T) {
	got := SegmentedTSQuery("hello, តម្លៃ")
	if strings.HasPrefix(got, "hello | (") {
		return
	}
	t.Fatalf("punctuation must be stripped: %q", got)
}
