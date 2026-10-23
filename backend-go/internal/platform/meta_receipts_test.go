package platform

import (
	"strings"
	"testing"
	"time"
)

func TestMetaOccurredAt(t *testing.T) {
	const ms = int64(1700000000123)
	want := time.UnixMilli(ms)
	if got := metaOccurredAt(float64(ms)); !got.Equal(want) {
		t.Errorf("metaOccurredAt(float64) = %v, want %v", got, want)
	}
	if got := metaOccurredAt("1700000000123"); !got.Equal(want) {
		t.Errorf("metaOccurredAt(string) = %v, want %v", got, want)
	}
	// Missing/garbage watermarks fall back to now, never to the zero time (a
	// 1970 receipt would sort before every real one and be dropped by the
	// outbox watermark comparison).
	for _, v := range []any{nil, "", "not-a-number", float64(0)} {
		got := metaOccurredAt(v)
		if got.Before(time.Now().Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
			t.Errorf("metaOccurredAt(%v) = %v, want approximately now", v, got)
		}
	}
}

// Read receipts have no message ids, so they are applied by watermark to every
// linked outbox row at/before it. Pin the guards that make that safe: only rows
// we actually sent, never failed ones, and an idempotent "already read" state.
func TestMetaReadWatermarkSQLIsGuarded(t *testing.T) {
	for _, want := range []string{
		"provider_status = 'read'",
		"provider_status IS NULL OR provider_status IN ('sent', 'delivered')",
		"provider_message_id <> ''",
		"COALESCE(sent_at, created_at) <= $2",
	} {
		if !strings.Contains(metaReadWatermarkSQL, want) {
			t.Errorf("metaReadWatermarkSQL is missing guard %q: %s", want, metaReadWatermarkSQL)
		}
	}
}
