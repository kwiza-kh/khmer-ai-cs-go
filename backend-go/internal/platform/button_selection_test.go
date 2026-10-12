package platform

import "testing"

// The button-selection marker and its router exemption.
//
// 2026-10-12: an operator sent a single reply button titled "1" over Messenger,
// the customer tapped it, and Jev routed the bare "1" as junk at p=0.90 — exactly
// the default JEV_ROUTE_JUNK_MIN — so the turn was silenced: no generation, no
// delivery, no owner ping. The customer saw a button that did nothing. The marker
// is what carries "this is an option the store offered" into the router.

func TestButtonSelectionIsNeverSilenced(t *testing.T) {
	sel := ButtonSelection("1")
	if !IsButtonSelection(sel) {
		t.Fatalf("ButtonSelection(%q) = %q is not recognised by IsButtonSelection", "1", sel)
	}
	// The exact production tuple: junk route, p at the threshold.
	if _, _, silent := RouteDecision(RouteJunk, 0.90, sel); silent {
		t.Error("a tapped button was silenced as junk — the 2026-10-12 incident is back")
	}
	if _, _, silent := RouteDecision(RouteJunk, 0.99, sel); silent {
		t.Error("a tapped button was silenced as junk at high confidence")
	}
	// Chit-chat: the canned template answers "thanks for your message", which is
	// not an answer to the option the customer chose.
	if _, skip, _ := RouteDecision(RouteSmallTalk, 0.99, sel); skip {
		t.Error("a tapped button took the chit-chat shortcut")
	}
	// Control: the same text WITHOUT the marker is still junk, which is what
	// makes the marker — not the text — the thing doing the work.
	if _, _, silent := RouteDecision(RouteJunk, 0.90, "1"); !silent {
		t.Error("control: a bare \"1\" should still be routed junk")
	}
}

// The exemption covers absence-of-request verdicts only. A button the operator
// labelled "talk to a human" is still a human request.
func TestButtonSelectionStillEscalatesOnHandoffRoute(t *testing.T) {
	if esc, _, _ := RouteDecision(RouteHandoff, 0.90, ButtonSelection("转人工")); !esc {
		t.Error("a tapped handoff button stopped escalating")
	}
}

func TestIsButtonSelection(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{ButtonSelection("查询价格"), true},
		{"  " + ButtonSelection("x"), true},
		{ButtonSelection(""), true}, // marker with no label: still a tap
		{"[Button selected]", true}, // the bare tag as well
		{"查询价格", false},
		{"[Button sent] x", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsButtonSelection(tc.in); got != tc.want {
			t.Errorf("IsButtonSelection(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestMetaQuickReplyLabel(t *testing.T) {
	cases := []struct {
		name   string
		msg    map[string]any
		want   string
		wantOK bool
	}{
		{"title wins over payload", map[string]any{"text": "查询价格", "quick_reply": map[string]any{"payload": "price"}}, "查询价格", true},
		{"payload when the title is missing", map[string]any{"quick_reply": map[string]any{"payload": "price"}}, "price", true},
		{"blank title and payload", map[string]any{"text": "  ", "quick_reply": map[string]any{"payload": "  "}}, "", false},
		{"plain typed text is not a tap", map[string]any{"text": "你好"}, "", false},
		{"no message at all", map[string]any{}, "", false},
	}
	for _, tc := range cases {
		got, ok := metaQuickReplyLabel(tc.msg)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: metaQuickReplyLabel = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestMetaPostbackLabel(t *testing.T) {
	label, payload, ok := metaPostbackLabel(map[string]any{
		"postback": map[string]any{"title": "Talk to a human", "payload": "human"},
	})
	if !ok || label != "Talk to a human" || payload != "human" {
		t.Errorf("postback with title = (%q, %q, %v)", label, payload, ok)
	}
	// A title-less postback still has to reach the pipeline: the payload is the
	// only thing identifying the choice.
	label, payload, ok = metaPostbackLabel(map[string]any{
		"postback": map[string]any{"payload": "human"},
	})
	if !ok || label != "human" || payload != "human" {
		t.Errorf("postback without title = (%q, %q, %v)", label, payload, ok)
	}
	if _, _, ok := metaPostbackLabel(map[string]any{"message": map[string]any{}}); ok {
		t.Error("a plain message was read as a postback")
	}
	if _, _, ok := metaPostbackLabel(map[string]any{"postback": map[string]any{"title": "  "}}); ok {
		t.Error("an empty postback was accepted")
	}
}

func TestWhatsAppReplyLabel(t *testing.T) {
	cases := []struct {
		name    string
		msgType string
		msg     map[string]any
		want    string
		wantOK  bool
	}{
		{"template quick reply", "button", map[string]any{"button": map[string]any{"text": "Yes", "payload": "yes"}}, "Yes", true},
		{"quick reply without text falls back to payload", "button", map[string]any{"button": map[string]any{"payload": "yes"}}, "yes", true},
		{"interactive button reply", "interactive", map[string]any{"interactive": map[string]any{"button_reply": map[string]any{"id": "b1", "title": "Price"}}}, "Price", true},
		{"interactive button reply without title", "interactive", map[string]any{"interactive": map[string]any{"button_reply": map[string]any{"id": "b1"}}}, "b1", true},
		{"list reply", "interactive", map[string]any{"interactive": map[string]any{"list_reply": map[string]any{"id": "l1", "title": "Option A"}}}, "Option A", true},
		{"interactive without a reply", "interactive", map[string]any{"interactive": map[string]any{}}, "", false},
		{"a text message is not a tap", "text", map[string]any{"text": map[string]any{"body": "hi"}}, "", false},
	}
	for _, tc := range cases {
		got, ok := whatsAppReplyLabel(tc.msgType, tc.msg)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: whatsAppReplyLabel = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}
