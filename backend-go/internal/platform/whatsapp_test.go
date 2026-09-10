package platform

import (
	"encoding/json"
	"testing"
)

// WhatsApp Cloud API requires its own envelope (messaging_product + to +
// type). Sending the Messenger shape made every non-template reply fail with
// HTTP 400 — these tests lock the contract in.
func TestBuildWhatsAppBody(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		body := buildMetaMessageBody(&SendRequest{
			Platform: "whatsapp", RecipientID: "85512345678", Kind: "text", Text: "hello",
		})
		if body["messaging_product"] != "whatsapp" {
			t.Fatalf("messaging_product missing: %v", body)
		}
		if body["to"] != "85512345678" || body["type"] != "text" {
			t.Fatalf("envelope wrong: %v", body)
		}
		text, ok := body["text"].(map[string]any)
		if !ok || text["body"] != "hello" {
			t.Fatalf("text object wrong: %v", body["text"])
		}
		if _, bad := body["recipient"]; bad {
			t.Fatalf("Messenger recipient leaked into WhatsApp body: %v", body)
		}
		if _, bad := body["message"]; bad {
			t.Fatalf("Messenger message leaked into WhatsApp body: %v", body)
		}
	})

	t.Run("media", func(t *testing.T) {
		body := buildMetaMessageBody(&SendRequest{
			Platform: "whatsapp", RecipientID: "8551", Kind: "media",
			MediaURL: "https://cdn/x.jpg", MediaType: "image", Text: "caption",
		})
		if body["type"] != "image" {
			t.Fatalf("type = %v", body["type"])
		}
		img, _ := body["image"].(map[string]any)
		if img["link"] != "https://cdn/x.jpg" || img["caption"] != "caption" {
			t.Fatalf("image object wrong: %v", img)
		}
	})

	t.Run("buttons", func(t *testing.T) {
		body := buildMetaMessageBody(&SendRequest{
			Platform: "whatsapp", RecipientID: "8551", Kind: "buttons", Text: "pick",
			Buttons: [][2]string{{"Yes", "yes"}, {"No", "no"}},
		})
		if body["type"] != "interactive" {
			t.Fatalf("type = %v", body["type"])
		}
		interactive, _ := body["interactive"].(map[string]any)
		action, _ := interactive["action"].(map[string]any)
		btns, _ := action["buttons"].([]map[string]any)
		if len(btns) != 2 {
			t.Fatalf("buttons = %v", action["buttons"])
		}
	})

	t.Run("template", func(t *testing.T) {
		body := buildMetaMessageBody(&SendRequest{
			Platform: "whatsapp", RecipientID: "8551", Kind: "template",
			TemplateName: "order_update", TemplateLanguage: "en_US", TemplateBodyParams: []string{"A1"},
		})
		tpl, _ := body["template"].(map[string]any)
		if tpl["name"] != "order_update" {
			t.Fatalf("template wrong: %v", tpl)
		}
		lang, _ := tpl["language"].(map[string]any)
		if lang["code"] != "en_US" {
			t.Fatalf("language wrong: %v", lang)
		}
	})
}

// Messenger/Instagram keep their own envelope — the WhatsApp branch must not
// leak into them.
func TestBuildMessengerBodyUnchanged(t *testing.T) {
	body := buildMetaMessageBody(&SendRequest{
		Platform: "meta", RecipientID: "psid-1", Kind: "text", Text: "hi",
	})
	if _, ok := body["messaging_product"]; ok {
		t.Fatalf("WhatsApp field leaked into Messenger body: %v", body)
	}
	recipient, _ := body["recipient"].(map[string]any)
	if recipient["id"] != "psid-1" {
		t.Fatalf("recipient wrong: %v", body)
	}
	msg, _ := body["message"].(map[string]any)
	if msg["text"] != "hi" {
		t.Fatalf("message wrong: %v", body)
	}
	// Serialisable JSON (Graph API rejects non-JSON values).
	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
