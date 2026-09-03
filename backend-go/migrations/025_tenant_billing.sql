-- ============================================
-- 025 — tenant billing / quotas (F9).
-- Every tenant (users row) gets a billing profile with a plan and monthly
-- usage counters. The backend enforces limits on chat messages and knowledge
-- documents per cycle; the cycle resets via the billing task.
-- ============================================

CREATE TABLE IF NOT EXISTS tenant_billing (
    user_id INT PRIMARY KEY REFERENCES users(user_id),
    plan VARCHAR(32) NOT NULL DEFAULT 'free',            -- free | pro | enterprise
    monthly_message_quota INT NOT NULL DEFAULT 500,
    monthly_doc_quota INT NOT NULL DEFAULT 20,
    messages_used INT NOT NULL DEFAULT 0,
    docs_used INT NOT NULL DEFAULT 0,
    cycle_start TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cycle_end TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + INTERVAL '30 days'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tenant_billing_cycle ON tenant_billing (cycle_end);
