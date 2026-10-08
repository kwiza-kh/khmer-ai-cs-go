package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The scheduler owns three things worth pinning: the two accepted schedule forms,
// the next-occurrence arithmetic (including the wall clock a daily job means), and
// the runner's claim/record behaviour.

func TestParseScheduleAcceptsTheTwoForms(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		wantEvery time.Duration
		wantDaily string
	}{
		{"every:30s", 30 * time.Second, ""},
		{"every:15m", 15 * time.Minute, ""},
		{"every:6h", 6 * time.Hour, ""},
		{" every:24h ", 24 * time.Hour, ""},
		{"daily@08:00", 0, "08:00"},
		{"daily@8:5", 0, "08:05"},
		{"daily@23:59", 0, "23:59"},
	} {
		got, err := ParseSchedule(tc.raw)
		if err != nil {
			t.Fatalf("ParseSchedule(%q): %v", tc.raw, err)
		}
		if got.Every != tc.wantEvery || got.Daily != tc.wantDaily {
			t.Errorf("ParseSchedule(%q) = %+v, want every=%v daily=%q", tc.raw, got, tc.wantEvery, tc.wantDaily)
		}
	}
}

func TestParseScheduleRejectsGarbage(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "* * * * *", "0 8 * * *", "every:", "every:0s", "every:abc",
		"daily:", "daily@24:00", "daily@08:60", "daily@8", "weekly@monday",
	} {
		if _, err := ParseSchedule(raw); err == nil {
			t.Errorf("ParseSchedule(%q) must fail", raw)
		}
	}
	// The error has to name the accepted forms: an operator typing cron needs to
	// be told what this deployment accepts.
	_, err := ParseSchedule("* * * * *")
	if err == nil || !strings.Contains(err.Error(), "every:") || !strings.Contains(err.Error(), "daily@") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestScheduleNextInterval(t *testing.T) {
	from := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s, _ := ParseSchedule("every:15m")
	next, err := s.Next(from, time.UTC)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := from.Add(15 * time.Minute); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

// A daily job means the wall clock where the merchant is, not UTC: the digest is
// "08:00 Phnom Penh" in the deployment notes.
func TestScheduleNextDailyUsesTheLocation(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Phnom_Penh")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s, _ := ParseSchedule("daily@08:00")

	// 00:30 UTC is 07:30 in Phnom Penh → the next 08:00 is the same local day.
	from := time.Date(2026, 10, 3, 0, 30, 0, 0, time.UTC)
	next, err := s.Next(from, loc)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := time.Date(2026, 10, 3, 8, 0, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next.UTC(), want.UTC())
	}

	// 07:00 UTC is 14:00 local → already past today's 08:00, so tomorrow.
	from = time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)
	next, err = s.Next(from, loc)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := time.Date(2026, 10, 4, 8, 0, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next.UTC(), want.UTC())
	}

	// Exactly at the scheduled instant counts as "already happened": a job must
	// not re-fire for the minute it just ran in.
	at := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC) // 08:00 local
	next, err = s.Next(at, loc)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if want := time.Date(2026, 10, 4, 8, 0, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("next = %s, want the following day %s", next.UTC(), want.UTC())
	}
}

// fakeStore is an in-memory Store. Kept deliberately dumb: the manager's
// behaviour is what is under test.
type fakeStore struct {
	jobs      []Job
	claimed   map[int64]int
	refuse    map[int64]bool
	finishes  []string
	lastNext  map[int64]time.Time
	inserted  *Job
	deletedID int64
}

func newFakeStore(jobs ...Job) *fakeStore {
	return &fakeStore{jobs: jobs, claimed: map[int64]int{}, refuse: map[int64]bool{}, lastNext: map[int64]time.Time{}}
}

func (f *fakeStore) Due(_ context.Context, now time.Time, limit int) ([]Job, error) {
	var out []Job
	for _, j := range f.jobs {
		if j.Enabled && !j.NextRun.After(now) && len(out) < limit {
			out = append(out, j)
		}
	}
	return out, nil
}

func (f *fakeStore) Claim(_ context.Context, jobID int64, _ time.Time) (bool, error) {
	if f.refuse[jobID] {
		return false, nil
	}
	f.claimed[jobID]++
	return true, nil
}

func (f *fakeStore) Finish(_ context.Context, job Job, status, lastErr string, next time.Time) error {
	f.finishes = append(f.finishes, status+":"+lastErr)
	f.lastNext[job.ID] = next
	return nil
}

func (f *fakeStore) Insert(_ context.Context, job Job) (int64, error) {
	f.inserted = &job
	return 42, nil
}

func (f *fakeStore) Update(_ context.Context, job Job) error {
	f.inserted = &job
	return nil
}

func (f *fakeStore) Delete(_ context.Context, jobID int64) error {
	f.deletedID = jobID
	return nil
}

func (f *fakeStore) List(_ context.Context, _ *int32) ([]Job, error) { return f.jobs, nil }

func TestManagerRunsDueJobsAndAdvancesThem(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(
		Job{ID: 1, Name: "digest", Type: "digest", Schedule: "daily@08:00", Enabled: true, NextRun: now.Add(-time.Hour)},
		Job{ID: 2, Name: "later", Type: "digest", Schedule: "every:6h", Enabled: true, NextRun: now.Add(time.Hour)},
	)
	m := NewManager(store, time.UTC)
	m.now = func() time.Time { return now }

	var ran []int64
	m.Register("digest", func(_ context.Context, job Job) error {
		ran = append(ran, job.ID)
		return nil
	})

	if got := m.RunDue(context.Background()); got != 1 {
		t.Fatalf("ran %d jobs, want 1 (only the due one)", got)
	}
	if len(ran) != 1 || ran[0] != 1 {
		t.Fatalf("handlers ran for %v", ran)
	}
	if got := store.finishes; len(got) != 1 || got[0] != "ok:" {
		t.Fatalf("finish records = %v", got)
	}
	// daily@08:00 with the clock at 12:00 → tomorrow 08:00, not 24h from now.
	if next := store.lastNext[1]; !next.Equal(time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("next run = %s, want tomorrow 08:00", next)
	}
}

// A failing handler is recorded and still advances: otherwise a job that always
// fails would be retried on every tick, forever.
func TestManagerRecordsFailureAndDoesNotHotLoop(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(Job{ID: 3, Name: "boom", Type: "boom", Schedule: "every:30s", Enabled: true, NextRun: now})
	m := NewManager(store, time.UTC)
	m.now = func() time.Time { return now }
	m.Register("boom", func(context.Context, Job) error { return errors.New("handler exploded") })

	if got := m.RunDue(context.Background()); got != 1 {
		t.Fatalf("ran %d", got)
	}
	if len(store.finishes) != 1 || !strings.HasPrefix(store.finishes[0], "error:handler exploded") {
		t.Fatalf("finish records = %v", store.finishes)
	}
	if next := store.lastNext[3]; !next.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("next run = %s, want 30s later", next)
	}
}

// A job whose type nobody registered is recorded as skipped exactly once per
// occurrence, and nothing is executed.
func TestManagerSkipsUnknownJobTypes(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(Job{ID: 4, Name: "ghost", Type: "not-registered", Schedule: "every:1m", Enabled: true, NextRun: now})
	m := NewManager(store, time.UTC)
	m.now = func() time.Time { return now }

	if got := m.RunDue(context.Background()); got != 1 {
		t.Fatalf("ran %d", got)
	}
	if len(store.finishes) != 1 || !strings.HasPrefix(store.finishes[0], "skipped:") {
		t.Fatalf("finish records = %v", store.finishes)
	}
	if !strings.Contains(store.finishes[0], "not-registered") {
		t.Fatalf("the record must name the missing type: %v", store.finishes[0])
	}
}

// Two runners must not run the same occurrence: the store refuses the claim and
// the handler is never called.
func TestManagerRespectsALostClaim(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(Job{ID: 5, Name: "shared", Type: "shared", Schedule: "every:1m", Enabled: true, NextRun: now})
	store.refuse[5] = true
	m := NewManager(store, time.UTC)
	m.now = func() time.Time { return now }

	called := false
	m.Register("shared", func(context.Context, Job) error { called = true; return nil })
	if got := m.RunDue(context.Background()); got != 0 {
		t.Fatalf("ran %d jobs after losing the claim", got)
	}
	if called {
		t.Fatal("the handler ran despite the claim being refused")
	}
}

func TestManagerWithoutStoreIsSafe(t *testing.T) {
	var m *Manager
	if got := m.RunDue(context.Background()); got != 0 {
		t.Fatalf("nil manager ran %d jobs", got)
	}
	m.Register("x", func(context.Context, Job) error { return nil })
	if err := NewManager(nil, nil).RunDue(context.Background()); err != 0 {
		t.Fatalf("a store-less manager must be a no-op, got %d", err)
	}
}
