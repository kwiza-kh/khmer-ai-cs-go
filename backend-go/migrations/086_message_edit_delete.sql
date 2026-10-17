-- ============================================
-- 086 — messages an operator withdrew or rewrote.
--
-- The provider is told first (Telegram editMessageText/deleteMessage); this
-- column records the outcome so the transcript stops showing a message the
-- customer no longer has. The row itself is kept: the audit trail of "what we
-- sent" must survive the correction.
-- ============================================

ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE chat_messages ADD COLUMN IF NOT EXISTS edited_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_chat_messages_edited ON chat_messages (message_id) WHERE edited_at IS NOT NULL;
