package platform

import (
	"regexp"
	"sort"
	"sync"
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

// TestCriticalLaneOverflowCountsAndPages — the never-drop lane spills rather
// than dropping an owner alert, but the spill is bounded (overflowSem) and past
// both caps the task is dropped AND paged: the first drop pages, then every
// criticalLaneOverflowPageAt-th, so the overload stays visible instead of
// silently growing goroutines. The droppable lane still drops without a count.
func TestCriticalLaneOverflowCountsAndPages(t *testing.T) {
	// Zero the queue-for-a-slot wait: this test reproduces a permanently
	// saturated lane, so every call would otherwise sit out the full wait.
	oldWait := criticalLaneWait
	criticalLaneWait = 0
	defer func() { criticalLaneWait = oldWait }()

	var mu sync.Mutex
	var pages []int64
	criticalLaneOverflowPage.Store(func(overflow int64) {
		mu.Lock()
		pages = append(pages, overflow)
		mu.Unlock()
	})
	// atomic.Value cannot hold nil, so leave a benign hook behind rather than
	// trying to restore "unset".
	defer criticalLaneOverflowPage.Store(func(int64) {})

	// A full local channel reproduces the saturated lane without touching the
	// package-level criticalSem that other tests (and the real workers) use, and
	// the package-level overflow tier is filled so the spill has nowhere to go.
	full := make(chan struct{}, 1)
	full <- struct{}{}
	for i := 0; i < cap(overflowSem); i++ {
		overflowSem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(overflowSem); i++ {
			<-overflowSem
		}
	}()

	// The counter is process-wide and monotonic, but the pattern asserted below
	// ("first drop pages, then every threshold") is only observable from a known
	// origin — a second run of this test in the same process would start at 32 and
	// see one page instead of two. Start at zero, the state a fresh process has,
	// and hand the previous value back.
	prevOverflow := criticalLaneOverflow.Load()
	criticalLaneOverflow.Store(0)
	defer criticalLaneOverflow.Store(prevOverflow)

	before := criticalLaneOverflow.Load()
	for range criticalLaneOverflowPageAt {
		spawnAsync(full, func() {}, false)
	}
	// The spill decision now happens in the spawned goroutine — the caller is
	// never made to wait for a slot — so poll instead of asserting synchronously.
	//
	// Poll for the PAGES, not just the counter: dropCriticalTask increments the
	// counter before it calls the page hook, so a counter at the threshold says
	// nothing about the hook having run yet. Reading pages right after the count
	// check panicked here on 2026-10-16 (index out of range [0] with length 0):
	// the gap is wide enough to be deterministic on Windows, where 32 goroutines
	// contend for the same stderr fd before reaching the hook.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(pages)
		mu.Unlock()
		if n >= 2 && criticalLaneOverflow.Load()-before >= criticalLaneOverflowPageAt {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := criticalLaneOverflow.Load() - before; got != criticalLaneOverflowPageAt {
		t.Fatalf("overflow count = %d, want %d", got, criticalLaneOverflowPageAt)
	}
	mu.Lock()
	recorded := append([]int64(nil), pages...)
	mu.Unlock()
	n := len(recorded)
	// Two pages for 32 drops: the first drop always pages (a burst of 1–31 would
	// otherwise be log-only), and every criticalLaneOverflowPageAt-th after that
	// keeps a spike that lasts hours from going silent.
	if n != 2 {
		t.Fatalf("expected a page on the first drop and at the threshold, got %d (%v)", n, recorded)
	}
	// The two pages are written by different dropping goroutines, so their ORDER
	// is not defined: the goroutine that took overflow=32 can reach the hook
	// before the one that took overflow=1 does. Compare as a set — asserting
	// pages[0]==1 made this flake roughly one run in five (2026-10-16).
	sort.Slice(recorded, func(i, j int) bool { return recorded[i] < recorded[j] })
	if want := before + 1; recorded[0] != want {
		t.Errorf("first page reported overflow=%d, want %d", recorded[0], want)
	}
	if want := before + criticalLaneOverflowPageAt; recorded[1] != want {
		t.Errorf("threshold page reported overflow=%d, want %d", recorded[1], want)
	}

	// The best-effort lane keeps dropping in silence: no work, no count, no page.
	countBefore := criticalLaneOverflow.Load()
	spawnAsync(full, func() { t.Error("droppable lane must drop when saturated") }, true)
	if criticalLaneOverflow.Load() != countBefore {
		t.Error("a dropped classification must not be counted as critical-lane overflow")
	}
	mu.Lock()
	n = len(pages)
	mu.Unlock()
	if n != 2 {
		t.Errorf("droppable lane must not page, got %d page(s)", n)
	}
}
