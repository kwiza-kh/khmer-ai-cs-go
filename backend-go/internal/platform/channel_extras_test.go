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

// B3: the inbox composer's "document" attachment used to reach Telegram as a
// text-only message — the file was silently dropped. It now goes out as a
// document, with the reply as its caption.
func TestTelegramChannelSendsDocumentByURL(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"ok":true,"result":{"message_id":31}}`))
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "chat-1",
		Kind:        "media",
		MediaType:   "document",
		MediaURL:    "https://cdn.example/price-list.pdf",
		Content:     "**Here** is the list",
		ReplyTo:     55,
	})
	if err != nil {
		t.Fatalf("send document: %v", err)
	}
	if id != "chat-1:31" {
		t.Fatalf("provider id = %q", id)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Path != "/botT/sendDocument" {
		t.Fatalf("calls = %+v, want one sendDocument", calls)
	}
	if calls[0].Body["document"] != "https://cdn.example/price-list.pdf" {
		t.Errorf("document = %v", calls[0].Body["document"])
	}
	if calls[0].Body["caption"] != "<b>Here</b> is the list" {
		t.Errorf("caption = %v, want the HTML conversion", calls[0].Body["caption"])
	}
	rp, ok := calls[0].Body["reply_parameters"].(map[string]any)
	if !ok || rp["message_id"] != float64(55) {
		t.Errorf("reply_parameters = %v, want message_id 55", calls[0].Body["reply_parameters"])
	}
}

func TestTelegramChannelSendsVideoByURL(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"ok":true,"result":{"message_id":32}}`))
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	if _, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "chat-1",
		Kind:        "media",
		MediaType:   "video",
		MediaURL:    "https://cdn.example/demo.mp4",
		Content:     "demo",
	}); err != nil {
		t.Fatalf("send video: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 1 || calls[0].Path != "/botT/sendVideo" {
		t.Fatalf("calls = %+v, want one sendVideo", calls)
	}
}

// B1: an agent reply quotes the customer's message, on the first chunk only.
func TestTelegramChannelQuotesOnlyTheFirstChunk(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"ok":true,"result":{"message_id":40}}`))
	overrideBase(t, &telegramBase, rs.srv)

	ch := &telegramChannel{p: &Pipeline{}, cfg: &configCred{Platform: "telegram", BotToken: "T"}}
	if _, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "chat-1",
		Kind:        "text",
		Content:     strings.Repeat("x", 4097),
		ReplyTo:     71,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 chunks", len(calls))
	}
	if rp, ok := calls[0].Body["reply_parameters"].(map[string]any); !ok || rp["message_id"] != float64(71) {
		t.Errorf("first chunk reply_parameters = %v, want message_id 71", calls[0].Body["reply_parameters"])
	}
	if _, has := calls[1].Body["reply_parameters"]; has {
		t.Error("later chunks must not repeat the quote")
	}
}

// B3: the composer calls a PDF "document"; the Send API calls it "file".
func TestMetaChannelMapsDocumentToFile(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"m_2"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	ch := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "meta", AccessToken: "tok", PageID: "PAGE-1"}}
	if _, err := ch.Send(context.Background(), ChannelMessage{
		Delivery:    &outboundDelivery{},
		RecipientID: "psid-1",
		Kind:        "media",
		MediaType:   "document",
		MediaURL:    "https://cdn.example/price-list.pdf",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := rs.recorded()[0].Body["message"].(map[string]any)
	att := msg["attachment"].(map[string]any)
	if att["type"] != "file" {
		t.Fatalf("attachment type = %v, want file (document is not a Send API type)", att["type"])
	}
}

func TestMediaCapabilitiesMatchTheSendBranches(t *testing.T) {
	for _, tc := range []struct {
		platform string
		kind     MediaKinds
		want     bool
	}{
		{"telegram", MediaVideo, true},
		{"telegram", MediaFile, true},
		{"meta", MediaVideo, true},
		{"meta", MediaFile, true},
		{"instagram", MediaVideo, true},
		{"whatsapp", MediaVideo, true},
		{"whatsapp", MediaFile, true},
		{"zalo", MediaVideo, false},
		{"line", MediaImage, false},
		{"web", MediaVideo, false},
	} {
		got := CapabilitiesFor(tc.platform).Media.Supports(tc.kind)
		if got != tc.want {
			t.Errorf("%s supports %v = %v, want %v", tc.platform, tc.kind, got, tc.want)
		}
	}
}

// C1: only a new comment is a funnel entry — likes, edits, deletes and our own
// public replies must not produce a private message.
func TestMetaCommentTarget(t *testing.T) {
	cases := []struct {
		name     string
		change   map[string]any
		wantID   string
		wantFrom string
		wantOK   bool
	}{
		{
			"page comment added",
			map[string]any{"field": "feed", "value": map[string]any{
				"item": "comment", "verb": "add", "comment_id": "c1",
				"from": map[string]any{"id": "psid-9"},
			}},
			"c1", "psid-9", true,
		},
		{
			"page like is not a comment",
			map[string]any{"field": "feed", "value": map[string]any{
				"item": "like", "verb": "add", "comment_id": "c2",
			}},
			"", "", false,
		},
		{
			"comment removed",
			map[string]any{"field": "feed", "value": map[string]any{
				"item": "comment", "verb": "remove", "comment_id": "c3",
			}},
			"", "", false,
		},
		{
			"instagram comment",
			map[string]any{"field": "comments", "value": map[string]any{
				"id": "ig-c1", "text": "how much?",
				"from": map[string]any{"id": "igsid-9"},
			}},
			"ig-c1", "igsid-9", true,
		},
		{
			"no id",
			map[string]any{"field": "comments", "value": map[string]any{"text": "hi"}},
			"", "", false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, from, ok := metaCommentTarget(tc.change)
			if ok != tc.wantOK || id != tc.wantID || from != tc.wantFrom {
				t.Fatalf("metaCommentTarget = (%q,%q,%v), want (%q,%q,%v)", id, from, ok, tc.wantID, tc.wantFrom, tc.wantOK)
			}
		})
	}
}

func TestSendPrivateReplyUsesTheCommentRecipient(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"m_private"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	client := NewMetaClient("tok", "PAGE-1", "", "")
	id, err := client.SendPrivateReply(context.Background(), "meta", "COMMENT-1", "hello")
	if err != nil {
		t.Fatalf("SendPrivateReply: %v", err)
	}
	if id != "m_private" {
		t.Fatalf("provider id = %q", id)
	}
	call := rs.recorded()[0]
	if call.Path != "/v24.0/PAGE-1/messages" {
		t.Errorf("path = %s", call.Path)
	}
	recipient, _ := call.Body["recipient"].(map[string]any)
	if recipient["comment_id"] != "COMMENT-1" {
		t.Errorf("recipient = %v, want the comment id (a PSID would open the wrong thread)", recipient)
	}
	msg, _ := call.Body["message"].(map[string]any)
	if msg["text"] != "hello" {
		t.Errorf("message text = %v", msg["text"])
	}
}
