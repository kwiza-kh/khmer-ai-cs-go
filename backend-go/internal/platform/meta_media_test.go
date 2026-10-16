package platform

import (
	"context"
	"testing"
)

// Messenger and Instagram send media as message.attachment{type,payload.url}
// (Send API docs). The top-level `type`/`image:{link}` body this used to build is
// the Zalo/LINE shape and Graph answers it with error 100 — nobody noticed while
// media deliveries were zero in production (2026-10-16).
func TestMetaMessageBodyUsesAttachmentShape(t *testing.T) {
	for _, platform := range []string{"meta", "instagram"} {
		t.Run(platform, func(t *testing.T) {
			body := buildMetaMessageBody(&SendRequest{
				Platform:    platform,
				RecipientID: "psid-1",
				Kind:        "media",
				MediaType:   "image",
				MediaURL:    "https://cdn.example.com/p.png",
				Text:        "yes we have it",
			})
			if _, topLevelType := body["type"]; topLevelType {
				t.Fatalf("body still carries the top-level type: %+v", body)
			}
			recipient, _ := body["recipient"].(map[string]any)
			if recipient["id"] != "psid-1" {
				t.Fatalf("recipient = %+v", recipient)
			}
			message, _ := body["message"].(map[string]any)
			attachment, _ := message["attachment"].(map[string]any)
			if attachment["type"] != "image" {
				t.Fatalf("attachment = %+v, want type image", attachment)
			}
			payload, _ := attachment["payload"].(map[string]any)
			if payload["url"] != "https://cdn.example.com/p.png" {
				t.Fatalf("payload = %+v, want the picture URL", payload)
			}
			// No caption field exists on this shape: putting the reply there would
			// silently drop it (the capability table reports CaptionLimit 0).
			if _, hasCaption := payload["caption"]; hasCaption {
				t.Fatal("Messenger/Instagram attachments have no caption; the text must be its own message")
			}
			if _, hasText := message["text"]; hasText {
				t.Fatal("an attachment message must not also carry text")
			}
			if captionFits("yes we have it", CapabilitiesFor(platform)) {
				// captionFits must say no, so channel_meta.Send delivers the text as
				// its own message instead of losing it in a field the API ignores.
				t.Fatalf("%s must report CaptionLimit 0: its attachment has no caption field", platform)
			}
		})
	}
}

// The channel-level contract that matters to a customer: a product photo and the
// answer it belongs to. Two calls — the attachment, then the text.
func TestMetaChannelSendsImageThenText(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"m-1"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	ch := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "meta", AccessToken: "tok", PageID: "page-1"}}
	id, err := ch.Send(context.Background(), ChannelMessage{
		RecipientID: "psid-1", Kind: "media", MediaType: "image",
		MediaURL: "https://cdn.example.com/p.png",
		Content:  "yes, 189 USD",
	})
	if err != nil {
		t.Fatalf("send image: %v", err)
	}
	calls := rs.recorded()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want the attachment and then the text", len(calls))
	}
	if body := calls[0].Body; body["type"] != nil {
		t.Fatalf("first body = %+v, want message.attachment", body)
	}
	second, _ := calls[1].Body["message"].(map[string]any)
	if got, _ := second["text"].(string); got != "yes, 189 USD" {
		t.Fatalf("second message text = %q, want the reply", got)
	}
	if id != "" && id != "m-1" {
		t.Fatalf("id = %q", id)
	}
}

// A picture that IS the whole reply (text-to-image) goes out as one attachment,
// with no empty text message trailing it.
func TestMetaChannelSendsBareImageWhenNothingToSay(t *testing.T) {
	rs := newRecordingServer(t, okJSON(`{"message_id":"m-2"}`))
	overrideBase(t, &metaGraphBase, rs.srv)

	ch := &metaChannel{p: &Pipeline{}, cfg: &configCred{Platform: "instagram", AccessToken: "tok", InstagramBusiness: "ig-1"}}
	if _, err := ch.Send(context.Background(), ChannelMessage{
		RecipientID: "igsid-1", Kind: "media", MediaType: "image",
		MediaURL: "https://cdn.example.com/render.png",
	}); err != nil {
		t.Fatalf("send image: %v", err)
	}
	if calls := rs.recorded(); len(calls) != 1 {
		t.Fatalf("calls = %d, want exactly one attachment", len(calls))
	}
}
