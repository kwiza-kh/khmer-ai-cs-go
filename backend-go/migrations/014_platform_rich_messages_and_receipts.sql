-- Keep the exact outbound rich-message contract in the durable queue. A retry
-- must reproduce the original provider request rather than silently falling
-- back to plain text.
ALTER TABLE platform_outbox
    ADD COLUMN IF NOT EXISTS payload JSONB;

-- Provider delivery webhooks can arrive immediately after Send API accepts a
-- message, before the worker writes provider_message_id to platform_outbox.
-- Retain those receipts and apply them as soon as the outbox row is linked.
CREATE TABLE IF NOT EXISTS platform_delivery_receipts (
    receipt_id BIGSERIAL PRIMARY KEY,
    config_id INT NOT NULL REFERENCES platform_configs(config_id) ON DELETE CASCADE,
    provider_message_id VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    failure_detail TEXT,
    applied_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('delivered', 'read', 'failed')),
    UNIQUE(config_id, provider_message_id, status, occurred_at)
);

CREATE INDEX IF NOT EXISTS idx_platform_delivery_receipts_pending
    ON platform_delivery_receipts (config_id, provider_message_id, occurred_at)
    WHERE applied_at IS NULL;
