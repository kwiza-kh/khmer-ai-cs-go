-- A cancelled automated reply is not a provider failure and must never be
-- retried. It remains visible to operators as an audit record.
ALTER TABLE platform_outbox
    DROP CONSTRAINT IF EXISTS platform_outbox_status_check;

ALTER TABLE platform_outbox
    ADD CONSTRAINT platform_outbox_status_check
    CHECK (status IN ('pending', 'processing', 'sent', 'failed', 'cancelled'));
