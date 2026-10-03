-- 066: DB-backed scheduled jobs.
--
-- The background work in cmd/server was a fixed list of `a.loop(ctx, every, fn)`
-- calls compiled into the binary: changing a cadence, or scheduling anything at
-- all per tenant, meant a code change and a deploy. This table makes a job a row —
-- type, schedule, payload, and the bookkeeping the runner needs to claim it
-- exactly once even with several processes racing.
--
-- Borrowed from AstrBot's CronJobManager (core/cron/manager.py: jobs live in the
-- database, sync_from_db on start, add / update / delete / list plus "run now",
-- and a job kind that runs a turn rather than only notifying).

CREATE TABLE IF NOT EXISTS scheduled_jobs (
    job_id      SERIAL PRIMARY KEY,
    -- NULL = platform-level (operator) job; otherwise the tenant that owns it.
    user_id     INT REFERENCES users(user_id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    job_type    TEXT NOT NULL,
    -- every:<duration> | daily@HH:MM   — see internal/scheduler.ParseSchedule
    schedule    TEXT NOT NULL,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_run_at TIMESTAMPTZ,
    last_status TEXT,
    last_error  TEXT,
    run_count   INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The runner's only query: enabled jobs whose next_run_at has passed.
CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_due ON scheduled_jobs (enabled, next_run_at);
