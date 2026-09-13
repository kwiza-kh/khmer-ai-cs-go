-- 057_context_hygiene.sql — context-integrity columns for chat_messages.
--
-- inbound_event_id: platform inbound events are retried up to 5 times and
--   ensureSession re-inserted the customer message on every attempt. A partial
--   unique index makes the persist idempotent per event.
-- cancelled_at: model replies whose outbox delivery was cancelled (session
--   handed off / resolved while queued) never reached the customer but kept
--   polluting future AI history. Marking them lets history loaders skip them.
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS inbound_event_id BIGINT;
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_messages_inbound_event
    ON chat_messages (inbound_event_id)
    WHERE inbound_event_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_chat_messages_session_history
    ON chat_messages (session_id, message_id DESC)
    WHERE role IN ('user', 'model', 'agent');
