-- 068: PayPal payments (one-time purchase of a 30-day paid plan).
--
-- tenant_billing has carried plan / quota / usage and a 30-day cycle since the
-- beginning, but nothing could put money in: every registered tenant got "free"
-- and platform staff changed plans by hand. This table is the missing record —
-- one row per provider order, so a capture is idempotent (PayPal's capture
-- response and its webhook can both arrive), the amount actually paid is
-- provable, and a lapsed payment traces back to what was bought.
--
-- paid_until is deliberately separate from cycle_start/cycle_end: the billing
-- cycle is a 30-day usage window that rolls on its own, while paid_until is the
-- date the tenant stops being entitled to a paid plan. Conflating them would
-- either reset usage on a renewal or hand out a plan for free on a quiet cycle.

CREATE TABLE IF NOT EXISTS payments (
    payment_id          BIGSERIAL PRIMARY KEY,
    user_id             INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    provider            TEXT NOT NULL DEFAULT 'paypal',
    provider_order_id   TEXT NOT NULL,
    provider_capture_id TEXT,
    plan                TEXT NOT NULL,
    amount              NUMERIC(12,2) NOT NULL,
    currency            TEXT NOT NULL,
    -- created → captured, or failed. The move is one conditional UPDATE, which
    -- is what makes a capture/webhook pair extend the plan exactly once.
    status              TEXT NOT NULL DEFAULT 'created',
    detail              JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS payments_order_idx ON payments (provider, provider_order_id);
CREATE UNIQUE INDEX IF NOT EXISTS payments_capture_idx ON payments (provider_capture_id) WHERE provider_capture_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS payments_user_idx ON payments (user_id, created_at DESC);

-- NULL = never paid (free). A capture sets it to now()+30d, extending from the
-- existing value when a tenant renews before the current period ends.
ALTER TABLE tenant_billing ADD COLUMN IF NOT EXISTS paid_until TIMESTAMPTZ;

-- The expiry sweep looks for lapsed paid plans; the partial index keeps that
-- scan cheap on a table where most rows have never paid.
CREATE INDEX IF NOT EXISTS tenant_billing_paid_until_idx ON tenant_billing (paid_until) WHERE paid_until IS NOT NULL;
