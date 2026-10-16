package platform

import (
	"strings"
	"testing"

	"khmer-ai-cs-go/internal/rag"
)

// The product-photo attach decision: the picture is only sent when the retrieval
// evidence points at exactly one product, the reply is a real answer, and the
// channel can carry an image at all.
func TestChooseDocImage(t *testing.T) {
	const (
		mug   = "kb/1/mug.jpg"
		plate = "kb/1/plate.jpg"
	)
	cases := []struct {
		name         string
		sources      []rag.Source
		hasMatch     bool
		canned       bool
		claims       bool
		portal       string
		wantKey      string
		wantAttached bool
	}{
		{
			name: "no sources", sources: nil, hasMatch: true, portal: "telegram",
		},
		{
			name:     "retrieval found nothing to ground on",
			sources:  []rag.Source{{DocID: 1, Score: 0.9, MediaKey: mug}},
			hasMatch: false, portal: "telegram",
		},
		{
			name:     "canned reply is not a grounded answer",
			sources:  []rag.Source{{DocID: 1, Score: 0.9, MediaKey: mug}},
			hasMatch: true, canned: true, portal: "telegram",
		},
		{
			name:     "handoff acknowledgement must not carry a product photo",
			sources:  []rag.Source{{DocID: 1, Score: 0.9, MediaKey: mug}},
			hasMatch: true, claims: true, portal: "telegram",
		},
		{
			name:     "channel cannot send images",
			sources:  []rag.Source{{DocID: 1, Score: 0.9, MediaKey: mug}},
			hasMatch: true, portal: "line",
		},
		{
			name:     "matched document has no picture",
			sources:  []rag.Source{{DocID: 1, Score: 0.9}},
			hasMatch: true, portal: "telegram",
		},
		{
			name:     "one product with a photo",
			sources:  []rag.Source{{DocID: 1, Score: 0.80, MediaKey: mug}},
			hasMatch: true, portal: "telegram",
			wantKey: mug, wantAttached: true,
		},
		{
			name: "the same photo from two chunks of one document is not ambiguity",
			sources: []rag.Source{
				{DocID: 1, Score: 0.81, MediaKey: mug},
				{DocID: 1, Score: 0.80, MediaKey: mug},
			},
			hasMatch: true, portal: "telegram",
			wantKey: mug, wantAttached: true,
		},
		{
			name: "two products at the same confidence: ask, do not guess",
			sources: []rag.Source{
				{DocID: 1, Score: 0.81, MediaKey: mug},
				{DocID: 2, Score: 0.79, MediaKey: plate},
			},
			hasMatch: true, portal: "telegram",
		},
		{
			name: "a clear leader takes the picture",
			sources: []rag.Source{
				{DocID: 1, Score: 0.81, MediaKey: mug},
				{DocID: 2, Score: 0.60, MediaKey: plate},
			},
			hasMatch: true, portal: "telegram",
			wantKey: mug, wantAttached: true,
		},
		{
			name: "a text document between the two does not create ambiguity",
			sources: []rag.Source{
				{DocID: 1, Score: 0.81, MediaKey: mug},
				{DocID: 2, Score: 0.80},
				{DocID: 3, Score: 0.10, MediaKey: plate},
			},
			hasMatch: true, portal: "telegram",
			wantKey: mug, wantAttached: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := chooseDocImage(tc.sources, tc.hasMatch, tc.canned, tc.claims, CapabilitiesFor(tc.portal))
			if ok != tc.wantAttached || key != tc.wantKey {
				t.Fatalf("chooseDocImage = (%q, %v), want (%q, %v)", key, ok, tc.wantKey, tc.wantAttached)
			}
		})
	}
}

// The stage must never panic or abort a turn over a picture; an unknown channel
// means the text reply still leaves.
func TestStageAttachDocImageWithoutChannel(t *testing.T) {
	p := &Pipeline{}
	turn := &inboundTurn{
		Config: &configCred{Platform: "not-a-channel"},
		GroundCtx: rag.GroundingContext{
			HasMatch: true,
			Sources:  []rag.Source{{DocID: 1, Score: 0.9, MediaKey: "kb/1/x.jpg"}},
		},
		Reply: "yes we have it",
	}
	next, err := p.stageAttachDocImage(t.Context(), turn)
	if err != nil || !next {
		t.Fatalf("stage = (%v, %v), want (true, nil)", next, err)
	}
	if turn.AttachMediaKey != "" {
		t.Fatalf("attached %q on an unknown channel", turn.AttachMediaKey)
	}
}

// The stage records the decision on the turn for a channel that can send images.
func TestStageAttachDocImageRecordsKey(t *testing.T) {
	p := &Pipeline{}
	turn := &inboundTurn{
		Config: &configCred{Platform: "telegram", BotToken: "tok"},
		GroundCtx: rag.GroundingContext{
			HasMatch: true,
			Sources:  []rag.Source{{DocID: 7, Score: 0.75, MediaKey: "kb/4/mug.png"}},
		},
		Reply: "yes we have it",
	}
	if _, err := p.stageAttachDocImage(t.Context(), turn); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if turn.AttachMediaKey != "kb/4/mug.png" {
		t.Fatalf("AttachMediaKey = %q, want the matched document's key", turn.AttachMediaKey)
	}
}

// A picture is never worth losing the answer for: the caption rule decides
// between "photo with the reply as its caption" and "photo, then the reply".
func TestCaptionFits(t *testing.T) {
	cases := []struct {
		name    string
		content string
		portal  string
		want    bool
	}{
		{"empty reply is the picture itself", "", "zalo", true},
		{"zalo cannot caption an image", "yes we have it", "zalo", false},
		{"fits on telegram", strings.Repeat("x", 1024), "telegram", true},
		{"one rune over the telegram caption limit", strings.Repeat("x", 1025), "telegram", false},
		// Khmer is many bytes per rune; the limit is runes, not bytes.
		{"1024 Khmer runes still fit", strings.Repeat("រ", 1024), "telegram", true},
		{"1025 Khmer runes do not", strings.Repeat("រ", 1025), "telegram", false},
		{"line has no image path at all", "hi", "line", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := captionFits(tc.content, CapabilitiesFor(tc.portal)); got != tc.want {
				t.Fatalf("captionFits(%q...) = %v, want %v", tc.content[:min(len(tc.content), 8)], got, tc.want)
			}
		})
	}
}
