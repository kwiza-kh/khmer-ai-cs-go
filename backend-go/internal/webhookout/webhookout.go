// Package webhookout is the outbound (merchant-facing) webhook event layer.
//
// The subscription CRUD lives in internal/api; this package owns what an event
// is and how it reaches the delivery queue. It deliberately carries no HTTP
// client: emission is one database write that callers run next to the business
// write, and later a worker in internal/api claims the rows and POSTs them.
//
// Why a queue table instead of sending inline: the merchant's server can be
// down, slow, or behind a firewall, and that must never delay a customer reply
// or a handoff notification. A row in webhook_deliveries is the durable record
// that the event happened; the worker turns it into an HTTP request and retries
// with backoff until it succeeds or the attempt cap is reached.
package webhookout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SupportedEvents are the events an outbound webhook subscription can select.
// Deliberately a small, honest set: every name here has a real emission site.
// The subscription UI advertises exactly this list, so a merchant cannot
// subscribe to an event that never fires (the old UI offered seven, four of
// which had no producer).
const (
	EventSessionCreated   = "session.created"
	EventHandoffRequested = "handoff.requested"
	EventSessionResolved  = "session.resolved"
)

// SupportedEvents lists the event names in display order.
var SupportedEvents = []string{
	EventSessionCreated,
	EventHandoffRequested,
	EventSessionResolved,
}

// ValidEvent reports whether name is a real event this build emits. Empty is
// NOT a valid event: an empty filter means "all events" on a subscription, but
// it is never a thing the platform emits.
func ValidEvent(name string) bool {
	for _, e := range SupportedEvents {
		if e == name {
			return true
		}
	}
	return false
}

// Envelope is the JSON body a subscriber receives.
//
// event_id is stable across retries of the same delivery (it is written to the
// row's event_id column), so a receiver can dedupe on it. created_at is the
// emission time, not the delivery time — a retry delivered an hour later must
// not look like a fresh event.
type Envelope struct {
	Event     string         `json:"event"`
	EventID   string         `json:"event_id"`
	CreatedAt time.Time      `json:"created_at"`
	Data      map[string]any `json:"data"`
}

// NewEnvelope builds the envelope and its exact wire bytes. The bytes are
// inserted into the queue as jsonb, so the worker signs and sends the same
// document the receiver reads.
func NewEnvelope(event string, data map[string]any) (Envelope, []byte, error) {
	if !ValidEvent(event) {
		return Envelope{}, nil, fmt.Errorf("webhookout: unsupported event %q", event)
	}
	if data == nil {
		data = map[string]any{}
	}
	env := Envelope{
		Event:     event,
		EventID:   newEventID(),
		CreatedAt: time.Now().UTC(),
		Data:      data,
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return Envelope{}, nil, fmt.Errorf("webhookout: encode event: %w", err)
	}
	return env, payload, nil
}

// Emit fans one event out to every active subscription of tenantID whose filter
// accepts it (an empty filter means all supported events), then returns.
//
// The fan-out is a single INSERT ... SELECT, so a subscription created or
// deleted concurrently cannot produce a half-written batch. ON CONFLICT DO
// NOTHING makes a re-emitted envelope idempotent against the
// (subscription_id, event_id) unique index.
//
// A tenant with no subscriptions writes nothing and returns nil: "nobody is
// listening" is not an error. Callers log a non-nil error and carry on; a
// webhook consumer being unable to receive must never fail the business write
// that produced the event (fail open on the auxiliary path).
func Emit(ctx context.Context, pool *pgxpool.Pool, tenantID int32, event string, data map[string]any) error {
	if pool == nil {
		return fmt.Errorf("webhookout: no database")
	}
	env, payload, err := NewEnvelope(event, data)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO webhook_deliveries
		     (subscription_id, user_id, event, event_id, payload, status, attempts, next_attempt_at, created_at)
		 SELECT s.subscription_id, s.user_id, $2::text, $3, $4::jsonb, 'pending', 0, NOW(), NOW()
		   FROM webhook_subscriptions s
		  WHERE s.user_id = $1
		    AND s.is_active
		    AND (cardinality(s.events) = 0 OR $2::text = ANY(s.events))
		 ON CONFLICT DO NOTHING`,
		tenantID, env.Event, env.EventID, string(payload))
	if err != nil {
		return fmt.Errorf("webhookout: queue %s: %w", event, err)
	}
	return nil
}

// newEventID returns a random RFC 4122 version-4 UUID string. It duplicates the
// tiny helper in internal/platform instead of importing it: internal/platform
// imports this package for emission, and a shared helper would close that
// dependency cycle.
func newEventID() string {
	b := make([]byte, 16)
	// crypto/rand.Read never returns an error on Go >= 1.24 (it panics when no
	// entropy source exists); the error return is documented as always nil.
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
