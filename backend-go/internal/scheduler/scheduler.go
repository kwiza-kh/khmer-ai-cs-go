// Package scheduler runs jobs that live in the database.
//
// Platform maintenance (campaign dispatch, SLA scan, billing reset, health
// watchdog, retention) keeps its fixed loops in internal/api/tasks.go: those
// cadences are properties of the deployment. What this package adds is the other
// half — a job a tenant or the operator can create, change or disable without a
// deploy, which is what AstrBot's CronJobManager provides
// (astrbot/core/cron/manager.py: sync_from_db, add_basic_job, add_active_job,
// update_job, delete_job, list_jobs, get_next_run_time, run_job_now).
//
// The manager never decides what a job does: it looks the job's type up in a
// handler registry. That keeps this package free of database schema knowledge
// beyond its own table, and makes the whole runner testable with a fake store.
package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Job is one row of scheduled_jobs.
type Job struct {
	ID       int64
	UserID   *int32 // nil = platform-level
	Name     string
	Type     string
	Schedule string
	Payload  map[string]any
	Enabled  bool
	NextRun  time.Time
	LastRun  *time.Time

	LastStatus string
	LastError  string
	RunCount   int
}

// Handler runs one job. An error is recorded on the row (last_status='error',
// last_error) and does not stop the other due jobs.
type Handler func(ctx context.Context, job Job) error

// Store is the persistence the manager needs. PostgresStore implements it for
// production; tests use an in-memory fake.
type Store interface {
	// Due returns enabled jobs whose next_run_at is at or before now.
	Due(ctx context.Context, now time.Time, limit int) ([]Job, error)
	// Claim atomically takes a job if it is still due, so two runners cannot run
	// the same occurrence. False means somebody else won.
	Claim(ctx context.Context, jobID int64, now time.Time) (bool, error)
	// Finish records the outcome and schedules the next occurrence.
	Finish(ctx context.Context, job Job, status, lastErr string, next time.Time) error

	Insert(ctx context.Context, job Job) (int64, error)
	Update(ctx context.Context, job Job) error
	Delete(ctx context.Context, jobID int64) error
	List(ctx context.Context, userID *int32) ([]Job, error)
}

// Schedule is a parsed schedule string.
type Schedule struct {
	// Every is set for "every:<duration>" (15m, 6h, 30s).
	Every time.Duration
	// Daily is "HH:MM" for "daily@HH:MM".
	Daily string
}

// ParseSchedule reads the two forms this platform needs:
//
//	every:30s | every:15m | every:6h | every:24h   — a fixed interval
//	daily@08:00                                     — once a day, in the manager's location
//
// A cron expression is deliberately NOT accepted: the interval and daily forms
// cover every job this deployment has, and a half-implemented cron parser that
// silently mis-schedules a billing reset is worse than refusing the string.
func ParseSchedule(raw string) (Schedule, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Schedule{}, fmt.Errorf("scheduler: empty schedule")
	}
	if rest, ok := strings.CutPrefix(s, "every:"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return Schedule{}, fmt.Errorf("scheduler: bad interval %q: %w", raw, err)
		}
		if d < time.Second {
			return Schedule{}, fmt.Errorf("scheduler: interval %q is below one second", raw)
		}
		return Schedule{Every: d}, nil
	}
	if rest, ok := strings.CutPrefix(s, "daily@"); ok {
		hh, mm, err := parseClock(strings.TrimSpace(rest))
		if err != nil {
			return Schedule{}, fmt.Errorf("scheduler: bad daily time %q: %w", raw, err)
		}
		return Schedule{Daily: fmt.Sprintf("%02d:%02d", hh, mm)}, nil
	}
	return Schedule{}, fmt.Errorf("scheduler: unrecognised schedule %q (want every:<duration> or daily@HH:MM)", raw)
}

func parseClock(v string) (int, int, error) {
	parts := strings.Split(v, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want HH:MM")
	}
	hh, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || hh < 0 || hh > 23 {
		return 0, 0, fmt.Errorf("hour out of range")
	}
	mm, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || mm < 0 || mm > 59 {
		return 0, 0, fmt.Errorf("minute out of range")
	}
	return hh, mm, nil
}

// Next returns the first occurrence strictly after from. loc decides the wall
// clock for a daily schedule (a digest at 08:00 means 08:00 where the merchant
// is, not 08:00 UTC); an interval ignores it.
func (s Schedule) Next(from time.Time, loc *time.Location) (time.Time, error) {
	if s.Every > 0 {
		return from.Add(s.Every), nil
	}
	if s.Daily == "" {
		return time.Time{}, fmt.Errorf("scheduler: schedule has neither interval nor daily time")
	}
	if loc == nil {
		loc = time.UTC
	}
	hh, mm, err := parseClock(s.Daily)
	if err != nil {
		return time.Time{}, err
	}
	local := from.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, loc)
	if !next.After(from) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// Manager runs due jobs through a registry of handlers.
type Manager struct {
	store    Store
	handlers map[string]Handler
	loc      *time.Location
	batch    int
	// now is a seam so tests do not depend on the wall clock.
	now func() time.Time
}

// NewManager builds a manager. A nil store yields a manager that does nothing,
// so callers do not need a nil check on a deployment that has no database.
func NewManager(store Store, loc *time.Location) *Manager {
	if loc == nil {
		loc = time.UTC
	}
	return &Manager{
		store:    store,
		handlers: map[string]Handler{},
		loc:      loc,
		batch:    50,
		now:      time.Now,
	}
}

// Register binds a job type to its handler. Registering the same type twice
// replaces the handler — the last registration wins, which is what a test wants
// and what a plugin-style override would want.
func (m *Manager) Register(jobType string, h Handler) {
	if m == nil || jobType == "" || h == nil {
		return
	}
	m.handlers[jobType] = h
}

func (m *Manager) NextFor(job Job) time.Time {
	s, err := ParseSchedule(job.Schedule)
	if err != nil {
		// An unparsable schedule must not hot-loop: back off an hour and leave
		// the error on the row.
		return m.now().Add(time.Hour)
	}
	next, err := s.Next(m.now(), m.loc)
	if err != nil {
		return m.now().Add(time.Hour)
	}
	return next
}

// RunDue executes every job that is due and returns how many ran. A job with no
// handler is recorded as skipped rather than retried forever.
func (m *Manager) RunDue(ctx context.Context) int {
	if m == nil || m.store == nil {
		return 0
	}
	now := m.now()
	jobs, err := m.store.Due(ctx, now, m.batch)
	if err != nil {
		return 0
	}
	ran := 0
	for _, job := range jobs {
		if err := m.runOne(ctx, job, now); err != nil {
			continue
		}
		ran++
	}
	return ran
}

// runOne claims and executes one job.
func (m *Manager) runOne(ctx context.Context, job Job, now time.Time) error {
	claimed, err := m.store.Claim(ctx, job.ID, now)
	if err != nil || !claimed {
		return fmt.Errorf("not claimed")
	}
	h, ok := m.handlers[job.Type]
	if !ok {
		// Unknown type: record it once, advance, and never call anything.
		return m.store.Finish(ctx, job, "skipped", "no handler registered for type "+job.Type, m.NextFor(job))
	}
	runErr := h(ctx, job)
	status, detail := "ok", ""
	if runErr != nil {
		status, detail = "error", runErr.Error()
	}
	if err := m.store.Finish(ctx, job, status, detail, m.NextFor(job)); err != nil {
		return err
	}
	return nil
}
