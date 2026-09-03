-- Provider-level delivery state is separate from the local durable outbox
-- state. The worker's `sent` status means the provider accepted the request;
-- these fields record later provider webhooks such as delivered and read.
ALTER TABLE platform_outbox
    ADD COLUMN IF NOT EXISTS provider_message_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS provider_status VARCHAR(32),
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS read_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_outbox_provider_message
    ON platform_outbox (config_id, provider_message_id)
    WHERE provider_message_id IS NOT NULL AND provider_message_id <> '';
