-- Inbox search + retention DELETE indexes (2026-09).
--
-- WHY (6a) chat_messages.content
-- The inbox search box feeds one predicate with five ILIKEs across four tables
-- (see listInbox in internal/api/inbox_handlers.go); the only one with no
-- usable index is `cm_s.content ILIKE '%q%'`. Before this, a GIN trigram index
-- existed on exactly two text columns in the whole schema — sessions.title
-- (004) and knowledge_chunks.content (032/038) — so every inbox search with a
-- term that was not a session title fell back to a sequential scan of every
-- chat_messages row on the platform, not just the caller's: the predicate
-- inside EXISTS is correlated by session_id, but the planner is free to choose
-- a full index scan when it expects the ILIKE to be selective.
--
-- Caveat, deliberate: a trigram index cannot serve patterns shorter than three
-- characters. `q` of one or two chars still scans; that is a UI-level limit
-- (the search box is a free-text field), not something this index can fix.
--
-- WHY (6b) the retention predicates
-- cmd/retention/main.go deletes with `WHERE ctid IN (SELECT ctid FROM <table>
-- WHERE <time predicate> LIMIT n)`. Each of those predicates is currently
-- unindexed in a way that forces a full scan:
--
--   * notifications/rag_query_logs/platform_oauth_sessions DO have indexes that
--     mention the timestamp column, but it is never the leading column
--     (user_id, created_at DESC / user_id, expires_at DESC), and a btree
--     cannot be used for `created_at < now() - interval '30 days'` without the
--     leading column.
--   * platform_inbound_events has (status, next_attempt_at, created_at) — same
--     problem, and the rule additionally filters status='completed', which is
--     not the indexed status class (that index is partial on
--     pending/processing).
--   * platform_outbox.sent_at is not indexed at all; the outbox is the largest
--     table on a busy deployment, so the opt-in --outbox-days rule was a
--     sequential scan of it, every run.
--
-- LOCKING NOTE: plain CREATE INDEX takes a SHARE lock that blocks writes to the
-- table for the whole build. Every table here is empty or small on a fresh
-- deploy and the migration runner holds one transaction per file, so this is
-- acceptable — but on a large production database build the trgm index on
-- chat_messages separately and CONCURRENTLY (outside the migration runner),
-- because that table grows with every message and the build can take minutes.
-- CREATE INDEX CONCURRENTLY cannot run inside the migration transaction.

CREATE INDEX IF NOT EXISTS idx_chat_messages_content_trgm
    ON chat_messages USING GIN (content gin_trgm_ops);

-- Retention: the four always-on rules plus the opt-in outbox rule.
CREATE INDEX IF NOT EXISTS idx_notifications_created_at
    ON notifications (created_at);

CREATE INDEX IF NOT EXISTS idx_rag_query_logs_created_at
    ON rag_query_logs (created_at);

-- Partial on the status the rule actually deletes: completed events are the
-- only removable ones, and a partial index keeps the (much smaller) pending /
-- processing working set out of it.
CREATE INDEX IF NOT EXISTS idx_platform_inbound_events_completed_created_at
    ON platform_inbound_events (created_at)
    WHERE status = 'completed';

-- Partial because the rule only ever deletes delivered rows; unsent rows have
-- sent_at IS NULL and an entry for them would be dead weight in the index.
CREATE INDEX IF NOT EXISTS idx_platform_outbox_sent_at
    ON platform_outbox (sent_at)
    WHERE sent_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_platform_oauth_sessions_expires_at
    ON platform_oauth_sessions (expires_at);
