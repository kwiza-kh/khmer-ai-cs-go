package gemini

import (
	"strings"
	"testing"
)

func TestDetectLanguageByScript(t *testing.T) {
	if got := DetectLanguage("你好, 请问"); got != "zh" {
		t.Errorf("Chinese text → %q, want zh", got)
	}
	if got := DetectLanguage("សួស្តី"); got != "km" {
		t.Errorf("Khmer text → %q, want km", got)
	}
	if got := DetectLanguage("hello there"); got != "en" {
		t.Errorf("English text → %q, want en", got)
	}
	if got := DetectLanguage("1234 !!"); got != "" {
		t.Errorf("digit-only text → %q, want empty", got)
	}
	if got := DetectLanguage("sok sabay បាទ"); got != "km" {
		t.Errorf("mixed Khmer/latin → %q, want km (Khmer script present)", got)
	}
}

func TestStripSourceMarkers(t *testing.T) {
	cases := map[string]string{
		"price is $15 [Source 1].":        "price is $15.",
		"[Source 2] the answer":           "the answer",
		"no markers here":                 "no markers here",
		"see [source 3] and [Source 12].": "see and.",
		"[Source 1]":                      "",
		// Paren and full-width variants.
		"**eps-16kg**：15 美元 (Source 2)":            "**eps-16kg**：15 美元",
		"基础版：99 美元（Source 1）。":                     "基础版：99 美元。",
		"含高棉语支持 (Source 1, Source 2)。":              "含高棉语支持。",
		"价格见 (Source 1、Source 2)。":                 "价格见。",
		"contact us (see brochure)":                 "contact us (see brochure)",
		"价格表\n\n产品价格表":                              "价格表\n\n产品价格表",
		"15 美元 (source 3) [Source 4]（Source 5）结束": "15 美元 结束",
	}
	for in, want := range cases {
		if got := StripSourceMarkers(in); got != want {
			t.Errorf("StripSourceMarkers(%q) = %q, want %q", in, got, want)
		}
	}
	// Markdown bold must survive; bracketed non-source labels must survive.
	if got := StripSourceMarkers("**bold** [Note 1]"); !strings.Contains(got, "[Note 1]") {
		t.Errorf("non-source bracket labels must survive: %q", got)
	}
}

func TestFormatVector(t *testing.T) {
	got := FormatVector([]float32{0.1, 1.23456789})
	if !strings.HasPrefix(got, "[0.100000,") || !strings.HasSuffix(got, "]") {
		t.Errorf("FormatVector = %s", got)
	}
}

func TestMockEmbeddingDeterministic768(t *testing.T) {
	a, b := MockEmbedding(), MockEmbedding()
	if len(a) != 768 || len(b) != 768 {
		t.Fatal("mock embedding must be 768-dim")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("mock embedding must be deterministic")
		}
	}
}

func TestParseScoreArray(t *testing.T) {
	reply := "Here you go: [{\"i\":1,\"score\":8},{\"i\":2,\"score\":1}] thanks"
	scores := parseScoreArray(reply, 2)
	if scores == nil {
		t.Fatal("scores must parse")
	}
	if scores[0] != 8 || scores[1] != 1 {
		t.Fatalf("scores = %v", scores)
	}
	if got := parseScoreArray("no json here", 2); got != nil {
		t.Fatal("garbage must return nil")
	}
	if got := parseScoreArray("[{\"i\":9,\"score\":5}]", 2); got != nil {
		t.Fatal("out-of-range-only entries must return nil")
	}
}

func TestNormalizeModelName(t *testing.T) {
	if got := NormalizeModelName("models/gemini-2.5-flash"); got != "gemini-2.5-flash" {
		t.Errorf("normalize = %q", got)
	}
	if got := NormalizeModelName("gemini-2.5-flash"); got != "gemini-2.5-flash" {
		t.Errorf("plain name must pass through: %q", got)
	}
}
