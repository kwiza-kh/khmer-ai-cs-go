-- ============================================
-- 084 — keep the provider's message id for every inbound event.
--
-- The outbox already stores the provider id of what WE sent, which is how a
-- delivery receipt finds its row. The customer's own message id had no column:
-- platform_inbound_events.external_id is the dedupe key, and for Telegram that is
-- the update_id, not the message_id — so "reply to that message" (Telegram
-- reply_parameters) had nothing to point at.
-- ============================================

ALTER TABLE platform_inbound_events ADD COLUMN IF NOT EXISTS provider_message_id TEXT;

-- The agent-reply quote reads the newest inbound message of one session.
CREATE INDEX IF NOT EXISTS idx_inbound_events_session_provider_msg
    ON platform_inbound_events (session_id, processed_at DESC)
    WHERE provider_message_id IS NOT NULL;
