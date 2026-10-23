-- ============================================
-- 088 — outbound webhook delivery queue.
--
-- webhook_deliveries has existed since 030 as an audit shape, but nothing ever
-- wrote to it: the subscription CRUD had no delivery worker at all. Delivery
-- has to survive a restart between "the event happened" and "the merchant's
-- server answered", so the event is persisted first and claimed afterwards.
--
-- One row per (event, interested subscription), fanned out at emit time by a
-- single INSERT ... SELECT against webhook_subscriptions. The filter is copied
-- onto the row by that statement, so a subscription that is deleted or disabled
-- between emit and send cannot half-consider the event at send time.
--
-- State machine:
--   pending → delivering → success
--                       ↘ pending (retry, attempts < cap)
--                       ↘ failed  (terminal, attempts reached cap)
--
-- attempts is incremented when a row is CLAIMED, not when it is sent: a worker
-- that dies mid-request still consumed an attempt, which is what stops a
-- poisoned payload from being retried forever.
-- ============================================

ALTER TABLE webhook_deliveries
    ADD COLUMN IF NOT EXISTS user_id INT REFERENCES users(user_id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS event_id TEXT,
    ADD COLUMN IF NOT EXISTS payload JSONB,
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS response_body TEXT;

-- Tenant binding lives on the row: the worker drains a queue that spans
-- tenants, so every follow-up statement (state updates, history reads)
-- binds user_id instead of trusting the subscription join. Rows written
-- before this column existed take their tenant from the subscription.
UPDATE webhook_deliveries d
   SET user_id = s.user_id
  FROM webhook_subscriptions s
 WHERE d.subscription_id = s.subscription_id AND d.user_id IS NULL;

-- Idempotent fan-out: re-running the same business event (for example an
-- inbound event redelivered by the provider) must not deliver twice to the same
-- subscriber. event_id is generated once per emitted envelope.
CREATE UNIQUE INDEX IF NOT EXISTS uq_webhook_deliveries_event
    ON webhook_deliveries (subscription_id, event_id)
    WHERE event_id IS NOT NULL;

-- Worker claim: due rows, oldest first. Partial so terminal history never sits
-- in front of the queue scan.
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_due
    ON webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';
