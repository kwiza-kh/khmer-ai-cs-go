-- 083 — sessions.memory_summary: what the assistant remembers beyond the
-- 20-message history window.
--
-- The prompt only ever carried the newest 20 messages (loadHistory), so a
-- constraint stated early ("5 boxes", "deliver before Friday") fell out of the
-- model's view after ten exchanges while staying in the database. sessions.summary
-- could not be reused: it is the merchant-facing 2-3 sentence digest in the
-- merchant's UI language (inbox panel), whereas memory wants the customer's
-- stated facts and commitments, in the conversation's language, refreshed as the
-- thread grows. Mixing them would make one feature's prompt rewrite the other's
-- artifact.
--
-- memory_summary_at is when it was written; memory_summary_count is how many
-- messages it covered, so a refresh can wait for real new material instead of
-- re-summarising after every single message.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS memory_summary TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS memory_summary_at TIMESTAMPTZ;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS memory_summary_count INTEGER NOT NULL DEFAULT 0;
