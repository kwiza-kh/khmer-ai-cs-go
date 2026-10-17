-- 081 — turn_metrics: one row per inbound turn with its wall time and the stages
-- that actually cost time.
--
-- The Messenger voice-note stall of 2026-10-16 was invisible: platform_inbound_events
-- carried created_at/processed_at (a total) and nothing else, so "which phase ate
-- the minute" could not be answered without deploying new logging. The per-turn
-- log line answered it once; this table keeps the answer queryable over time —
-- p95 by platform, the stages that moved after a change — without a log pipeline.
--
-- No foreign key to platform_inbound_events on purpose: cmd/retention prunes that
-- table after 30 days, and aggregate timings are useful longer than the events.
-- No foreign key to users either, for the same reason; the row holds a tenant id
-- (user_id) and a platform, never message content.
CREATE TABLE IF NOT EXISTS turn_metrics (
  event_id    BIGINT PRIMARY KEY,
  user_id     INTEGER NOT NULL,
  platform    platform_type NOT NULL,
  media_kind  VARCHAR(16) NOT NULL DEFAULT '',
  total_ms    INTEGER NOT NULL,
  slow_stages JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_turn_metrics_created_at ON turn_metrics (created_at);
CREATE INDEX IF NOT EXISTS idx_turn_metrics_user_created ON turn_metrics (user_id, created_at DESC);
