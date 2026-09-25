package platform

import (
	"regexp"
	"testing"
	"time"
)

// These tests cover the delivery-receipt, stale-lock, Telegram-linking and
// background-lane defects fixed together. They are all pure-logic assertions on
// purpose: nothing in this package's test suite has a database, and every one of
// these bugs was invisible to the type checker — a wrong column name, a
// discarded error, a prefix that did not match, a constant that was too small.

// TestOutboxProviderStatusNeverWritesQueueStatus pins the exact defect behind
// the delivered/read feature being silently dead.
//
// platform_outbox.status is the LOCAL queue state machine and migration 016
// pins it to ('pending','processing','sent','failed','cancelled'), so the old
// "SET status = 'delivered'" failed the CHECK constraint (23514) on every call —
// and because the error was thrown away, the read side (SUM over
// provider_status = 'read') just reported zero forever. The receipt must go to
// provider_status / delivered_at / read_at.
func TestOutboxProviderStatusNeverWritesQueueStatus(t *testing.T) {
	if !regexp.MustCompile(`provider_status\s*=\s*\$1`).MatchString(outboxProviderStatusSQL) {
		t.Fatalf("receipt must write provider_status: %s", outboxProviderStatusSQL)
	}
	for _, col := range []string{"delivered_at", "read_at"} {
		if !regexp.MustCompile(col + `\s*=\s*CASE`).MatchString(outboxProviderStatusSQL) {
			t.Fatalf("%s must be filled from the receipt state: %s", col, outboxProviderStatusSQL)
		}
	}
	// \b does not fire inside "provider_status" (_ is a word character), so this
	// matches only an assignment to the bare queue-state column.
	if bareStatusAssignment.MatchString(outboxProviderStatusSQL) {
		t.Fatalf("receipt must NEVER assign the queue-state column (CHECK 23514): %s", outboxProviderStatusSQL)
	}
	// The "sent" path is the other half of the same rule: its guard has to be
	// made of provider_status, because status can never hold delivered/read.
	if !regexp.MustCompile(`provider_status IS DISTINCT FROM 'read'`).MatchString(outboxSentSQL) {
		t.Fatalf("a late 'sent' receipt must not roll back a read receipt: %s", outboxSentSQL)
	}
}

var bareStatusAssignment = regexp.MustCompile(`\bstatus\s*=`)

// TestWhatsAppOccurredAt — occurred_at is NOT NULL and drives the replay order,
// so a receipt replayed after the outbox row is linked must still carry the
// provider's own moment. Cloud API sends unix seconds as a JSON string; some
// shapes (and our own tests) produce a number.
func TestWhatsAppOccurredAt(t *testing.T) {
	want := time.Unix(1735689600, 0) // 2025-01-01T00:00:00Z
	if got := whatsAppOccurredAt(map[string]any{"timestamp": "1735689600"}); !got.Equal(want) {
		t.Errorf("string timestamp = %v, want %v", got, want)
	}
	if got := whatsAppOccurredAt(map[string]any{"timestamp": float64(1735689600)}); !got.Equal(want) {
		t.Errorf("numeric timestamp = %v, want %v", got, want)
	}
	// A missing or unusable timestamp must still yield a usable value (the
	// column is NOT NULL); "now" is the honest fallback.
	for _, bad := range []any{nil, "", "not-a-number", float64(0)} {
		got := whatsAppOccurredAt(map[string]any{"timestamp": bad})
		if got.IsZero() {
			t.Errorf("timestamp %#v produced a zero time", bad)
		}
	}
}

// TestStaleLockCoversWorstCaseProcessing pins the arithmetic that made the old
// 2-minute stale lock unsafe.
//
// Nothing refreshes locked_at while an event is being processed, so an event
// still being handled looks abandoned the moment the lock ages past
// staleLockMinutes — another worker claims it and the same customer message is
// answered (and billed) twice. The inbound path's worst case is
// gemini.postWithRetry: 3 attempts against an http.Client with a 60s timeout,
// plus 400ms and 800ms of backoff between them.
func TestStaleLockCoversWorstCaseProcessing(t *testing.T) {
	worstCase := 3*60*time.Second + 1200*time.Millisecond

	if time.Duration(staleLockMinutes)*time.Minute <= worstCase {
		t.Fatalf("staleLockMinutes=%d is inside the worst-case processing time (%v): "+
			"a live event would be stolen and processed twice", staleLockMinutes, worstCase)
	}
	// The premise of the fix, asserted so this test fails loudly if the Gemini
	// retry budget ever changes: the OLD value really did undercut the worst
	// case, i.e. this was a real defect and not a hypothetical one.
	if 2*time.Minute >= worstCase {
		t.Fatalf("premise changed: 2 minutes no longer undercuts the worst case (%v) — "+
			"re-derive both the constant and this test", worstCase)
	}
}

// TestChatFallbackOnlyInPrivateChats — the chat_id fallback in
// lookupLinkedUser matched telegram_notify_settings.chat_id without checking who
// sent the update. In a private chat that id is the sender's own Telegram id
// (so the match is sound); in a group it belongs to the room, which meant any
// member of a group the bot had been added to could press 接管/解决 on a handoff
// notification and act on the MERCHANT's session.
func TestChatFallbackOnlyInPrivateChats(t *testing.T) {
	if !chatFallbackAllowed("private") {
		t.Error("private chats must keep the fallback: chat_id IS the user id there")
	}
	for _, chatType := range []string{"group", "supergroup", "channel", "", "unknown"} {
		if chatFallbackAllowed(chatType) {
			t.Errorf("chat type %q must NOT resolve an account from the chat id", chatType)
		}
	}
}

// TestCriticalLaneOverflowCountsAndPages — the never-drop lane runs unbounded on
// purpose (losing quota accounting or an owner alert is worse than a goroutine
// spike), so the only thing to verify is that the spike is now VISIBLE: counted,
// logged and paged once per threshold crossing — and that the droppable lane
// still drops without being counted.
func TestCriticalLaneOverflowCountsAndPages(t *testing.T) {
	var pages []int64
	// The page is invoked synchronously from spawnAsync's overflow branch, which
	// this test calls on its own goroutine.
	criticalLaneOverflowPage.Store(func(overflow int64) { pages = append(pages, overflow) })
	// atomic.Value cannot hold nil, so leave a benign hook behind rather than
	// trying to restore "unset".
	defer criticalLaneOverflowPage.Store(func(int64) {})

	// A full local channel reproduces the saturated lane without touching the
	// package-level criticalSem that other tests (and the real workers) use.
	full := make(chan struct{}, 1)
	full <- struct{}{}

	before := criticalLaneOverflow.Load()
	for i := 0; i < criticalLaneOverflowPageAt; i++ {
		spawnAsync(full, func() {}, false)
	}
	if got := criticalLaneOverflow.Load() - before; got != criticalLaneOverflowPageAt {
		t.Fatalf("overflow count = %d, want %d", got, criticalLaneOverflowPageAt)
	}
	if len(pages) != 1 {
		t.Fatalf("expected exactly 1 page per threshold crossing, got %d (%v)", len(pages), pages)
	}
	if want := before + criticalLaneOverflowPageAt; pages[0] != want {
		t.Errorf("page reported overflow=%d, want %d", pages[0], want)
	}

	// The best-effort lane keeps dropping in silence: no work, no count, no page.
	countBefore := criticalLaneOverflow.Load()
	spawnAsync(full, func() { t.Error("droppable lane must drop when saturated") }, true)
	if criticalLaneOverflow.Load() != countBefore {
		t.Error("a dropped classification must not be counted as critical-lane overflow")
	}
	if len(pages) != 1 {
		t.Errorf("droppable lane must not page, got %v", pages)
	}
}
