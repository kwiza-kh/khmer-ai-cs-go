package api

import (
	"strings"
	"testing"
)

// The audit's P1-4 root cause: the handlers for reaction-CSAT and comment→DM
// existed, but the OAuth subscription never asked for the fields that carry
// those events, so they could never fire. Pin the field sets the subscription
// uses, and that both keep every baseline field a connected channel relies on.
func TestMetaSubscriptionFieldsCoverConsumedEvents(t *testing.T) {
	if !strings.HasPrefix(metaMessengerFields, metaWebhookBaselineFields) {
		t.Fatalf("Messenger fields must start from the baseline set: %q", metaMessengerFields)
	}
	if !strings.HasPrefix(metaInstagramFields, metaWebhookBaselineFields) {
		t.Fatalf("Instagram fields must start from the baseline set: %q", metaInstagramFields)
	}
	for name, fields := range map[string]string{
		"messenger": metaMessengerFields,
		"instagram": metaInstagramFields,
	} {
		for _, want := range []string{"messages", "messaging_postbacks", "message_deliveries", "message_reads", "message_reactions"} {
			if !strings.Contains(fields, want) {
				t.Errorf("%s subscription is missing %q: %s", name, want, fields)
			}
		}
	}
	// Page comments arrive as `feed`; Instagram comments as `comments`. The
	// webhook handlers branch on exactly those field names.
	if !strings.Contains(metaMessengerFields, ",feed") {
		t.Errorf("Messenger must subscribe feed (Page comments): %s", metaMessengerFields)
	}
	if !strings.Contains(metaInstagramFields, ",comments") {
		t.Errorf("Instagram must subscribe comments: %s", metaInstagramFields)
	}
	if metaMessengerFields == metaInstagramFields {
		t.Error("Page comments use feed and IG comments use comments; the two must differ")
	}
}
