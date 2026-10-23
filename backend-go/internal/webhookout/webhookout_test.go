package webhookout

import (
	"context"
	"encoding/json"
	"testing"
)

func TestValidEvent(t *testing.T) {
	for _, e := range SupportedEvents {
		if !ValidEvent(e) {
			t.Fatalf("supported event %q reported invalid", e)
		}
	}
	for _, e := range []string{"", "message.received", "session.created ", "SESSION.CREATED", "handoff.assigned"} {
		if ValidEvent(e) {
			t.Errorf("event %q must not be accepted — it has no emission site", e)
		}
	}
}

func TestNewEnvelopeCarriesStableIdentity(t *testing.T) {
	data := map[string]any{"session_id": "s-1", "platform": "telegram"}
	const eventID = "11111111-1111-4111-8111-111111111111"
	env, payload, err := NewEnvelope(EventSessionCreated, eventID, data)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if env.Event != EventSessionCreated {
		t.Fatalf("event = %q", env.Event)
	}
	if env.EventID != eventID {
		t.Fatalf("event_id = %q, want %q", env.EventID, eventID)
	}
	if env.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if decoded["event"] != EventSessionCreated || decoded["event_id"] != env.EventID {
		t.Fatalf("payload identity does not match envelope: %s", payload)
	}
	if _, ok := decoded["created_at"].(string); !ok {
		t.Fatalf("created_at missing from payload: %s", payload)
	}
}

func TestNewEnvelopeRejectsUnsupportedEvent(t *testing.T) {
	if _, _, err := NewEnvelope("message.received", "evt-1", nil); err == nil {
		t.Fatal("unsupported event must not build an envelope")
	}
}

func TestNewEnvelopeRequiresEventID(t *testing.T) {
	if _, _, err := NewEnvelope(EventSessionCreated, "", nil); err == nil {
		t.Fatal("an envelope without an event id must be rejected")
	}
}

func TestNewEnvelopeNilDataBecomesObject(t *testing.T) {
	_, payload, err := NewEnvelope(EventSessionResolved, "evt-1", nil)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if _, ok := decoded["data"].(map[string]any); !ok {
		t.Fatalf("nil data must encode as {} (a receiver indexer should not see null): %s", payload)
	}
}

func TestEmitWithoutDatabaseFailsLoudly(t *testing.T) {
	if err := Emit(context.Background(), nil, 1, EventSessionCreated, "evt-1", nil); err == nil {
		t.Fatal("emitting without a database must return an error")
	}
}
