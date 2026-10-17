package platform

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A formatting problem must cost the bold, not the message: when Telegram refuses
// the entities, the same chunk goes out again as plain text.
func TestTelegramChannelFallsBackToPlainTextWhenHTMLIsRejected(t *testing.T) {
	rs := newRecordingServer(t, func(r *http.Request, n int) (int, map[string]string, string) {
		if n == 1 {
			return http.StatusBadRequest, map[string]string{"Content-Type": "application/json"},
				`{"ok":false,"description":"Bad Request: can't parse entities: Unsupported start tag"}`
		}
		return http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"ok":true,"result":{"message_id":9}}`
	})
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "chat-1",
		Kind:        "text",
		Content:     "**hi** there",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id != "chat-1:9" {
		t.Fatalf("provider id = %q", id)
	}
	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2 (formatted, then plain)", len(calls))
	}
	if calls[0].Body["parse_mode"] != "HTML" {
		t.Errorf("first attempt parse_mode = %v, want HTML", calls[0].Body["parse_mode"])
	}
	if calls[0].Body["text"] != "<b>hi</b> there" {
		t.Errorf("first attempt text = %q", calls[0].Body["text"])
	}
	if _, ok := calls[1].Body["parse_mode"]; ok {
		t.Errorf("fallback must not carry a parse_mode, got %v", calls[1].Body["parse_mode"])
	}
	if calls[1].Body["text"] != "hi there" {
		t.Errorf("fallback text = %q, want flattened markdown", calls[1].Body["text"])
	}
}

// Messenger renders the CSAT pair as quick replies, on the last chunk, so the
// choices sit under the whole answer.
func TestMetaChannelSendsCSATQuickRepliesOnTheLastChunk(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"m_1"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	ch := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "meta", AccessToken: "tok", PageID: "PAGE-1"}}
	_, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{LastMessageID: 77, Payload: map[string]any{"feedback": true}},
		RecipientID: "psid-1",
		Kind:        "text",
		Content:     strings.Repeat("a", 2001), // two chunks at the 2000-rune limit
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2 chunks", len(calls))
	}
	first := calls[0].Body["message"].(map[string]any)
	if _, has := first["quick_replies"]; has {
		t.Error("the first chunk must not carry the rating pair")
	}
	last := calls[1].Body["message"].(map[string]any)
	qr, ok := last["quick_replies"].([]any)
	if !ok || len(qr) != 2 {
		t.Fatalf("last chunk quick_replies = %v, want the 👍/👎 pair", last["quick_replies"])
	}
	up := qr[0].(map[string]any)
	if up["title"] != "👍" || up["payload"] != "fb:77:1" {
		t.Errorf("up vote = %v, want title 👍 payload fb:77:1", up)
	}
	down := qr[1].(map[string]any)
	if down["payload"] != "fb:77:-1" {
		t.Errorf("down vote payload = %v", down["payload"])
	}
}

// No feedback flag, no quick replies: a plain answer stays plain.
func TestMetaChannelWithoutFeedbackHasNoQuickReplies(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"m_1"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	ch := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "meta", AccessToken: "tok", PageID: "PAGE-1"}}
	if _, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{LastMessageID: 77},
		RecipientID: "psid-1",
		Kind:        "text",
		Content:     "hello",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := rs.recorded()[0].Body["message"].(map[string]any)
	if _, has := msg["quick_replies"]; has {
		t.Error("a reply without the feedback flag grew rating buttons")
	}
}

// The non-text shapes a customer can send must reach the pipeline as text instead
// of being dropped.
func TestTelegramTextMarker(t *testing.T) {
	cases := []struct {
		name    string
		message map[string]any
		want    string
	}{
		{"sticker", map[string]any{"sticker": map[string]any{"emoji": "😀"}}, "[Customer sent a sticker 😀]"},
		{"sticker without emoji", map[string]any{"sticker": map[string]any{}}, "[Customer sent a sticker]"},
		{
			"location",
			map[string]any{"location": map[string]any{"latitude": 11.5564, "longitude": 104.9282, "name": "Shop", "address": "St 1"}},
			"[Customer shared a location: Shop St 1] https://maps.google.com/?q=11.556400,104.928200",
		},
		{
			"contact",
			map[string]any{"contact": map[string]any{"first_name": "Sok", "last_name": "Dara", "phone_number": "+85512345678"}},
			"[Customer shared a contact: Sok Dara +85512345678]",
		},
		{"plain text has no marker", map[string]any{"text": "hi"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := telegramTextMarker(tc.message); got != tc.want {
				t.Fatalf("telegramTextMarker = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetMessengerProfileKeepsAnExistingMenu(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"data":[{"persistent_menu":[{"locale":"default"}]}]}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	client := NewMetaClient("tok", "PAGE-1", "", "")
	msg, err := client.SetMessengerProfile(context.Background())
	if err != nil {
		t.Fatalf("SetMessengerProfile: %v", err)
	}
	if msg != "existing Messenger menu kept" {
		t.Fatalf("message = %q, want the keep-existing sentence", msg)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Method != http.MethodGet {
		t.Fatalf("calls = %+v, want a single GET and no write", calls)
	}
}

func TestSetMessengerProfileInstallsMenuWhenAbsent(t *testing.T) {
	rs := newRecordingServer(t, func(r *http.Request, n int) (int, map[string]string, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"data":[]}`
		}
		return http.StatusOK, map[string]string{"Content-Type": "application/json"}, `{"result":"success"}`
	})
	overrideBase(t, &metaGraphBase, rs.srv)

	client := NewMetaClient("tok", "PAGE-1", "", "")
	msg, err := client.SetMessengerProfile(context.Background())
	if err != nil {
		t.Fatalf("SetMessengerProfile: %v", err)
	}
	if !strings.Contains(msg, "installed") {
		t.Fatalf("message = %q", msg)
	}
	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want a GET then a POST", len(calls))
	}
	post := calls[1]
	if post.Method != http.MethodPost || !strings.HasSuffix(post.Path, "/PAGE-1/messenger_profile") {
		t.Fatalf("write call = %+v", post)
	}
	if _, ok := post.Body["persistent_menu"]; !ok {
		t.Error("the write body has no persistent_menu")
	}
	if gs, ok := post.Body["get_started"].(map[string]any); !ok || gs["payload"] != "GET_STARTED" {
		t.Error("the write body must include get_started: Meta rejects a menu without it (#100)")
	}
	if _, ok := post.Body["greeting"]; !ok {
		t.Error("the write body has no greeting")
	}
}
