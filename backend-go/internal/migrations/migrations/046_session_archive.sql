-- 046 — conversation archiving.
--
-- Archiving is the primary way to clear a conversation out of the inbox:
-- the data stays intact (messages, handoffs, billing history) and the row is
-- simply hidden from the default list, so it can be restored at any time.
-- Hard deletion remains available to admins as a separate, confirmed action.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

-- The inbox lists "not archived" by default; a partial index keeps that scan
-- cheap as the archive grows.
CREATE INDEX IF NOT EXISTS idx_sessions_active_unarchived
    ON sessions (user_id, is_test, archived_at) WHERE archived_at IS NULL;
