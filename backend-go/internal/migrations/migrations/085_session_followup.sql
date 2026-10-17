-- ============================================
-- 085 — one post-resolution follow-up per session.
--
-- Sessions on a ProactiveSend channel (Telegram/LINE/Zalo) can be nudged once
-- after they were resolved: "anything else?". followup_sent_at is the idempotency
-- key, set in the same transaction that queues the message, so a restart between
-- the two cannot send it twice.
-- ============================================

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS followup_sent_at TIMESTAMPTZ;

-- The sweep reads only unresolved-after-resolution sessions that were never
-- followed up.
CREATE INDEX IF NOT EXISTS idx_sessions_followup_due
    ON sessions (resolved_at)
    WHERE status = 'resolved' AND followup_sent_at IS NULL;
