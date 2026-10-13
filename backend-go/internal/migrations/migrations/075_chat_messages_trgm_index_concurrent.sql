-- Moved here from 064, which created the same index inside the migration
-- runner's transaction. This is the one index in that file on a table that
-- grows with every message, and on a busy deployment its build can hold a
-- SHARE lock (blocking every insert into chat_messages) for minutes.
--
-- The runner executes a file containing CONCURRENTLY statement by statement
-- outside a transaction (see applyOne in internal/migrations/migrations.go),
-- so this build never takes that lock. On a database that already ran 064 the
-- index exists and this is a no-op.
--
-- The DROP comes first because a CONCURRENTLY build that is cancelled (deploy
-- restart, connection loss) leaves an INVALID index behind: it is visible to
-- IF NOT EXISTS, so the retry used to skip it, the version was recorded as
-- applied, and the planner never used it again. Dropping whatever is there
-- makes the retry build a valid one. Same reason every future CONCURRENTLY
-- migration should carry its own DROP.
DROP INDEX CONCURRENTLY IF EXISTS idx_chat_messages_content_trgm;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_chat_messages_content_trgm
    ON chat_messages USING GIN (content gin_trgm_ops);
