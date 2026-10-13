-- audit_logs had no index on created_at, while cmd/retention's opt-in
-- -audit-days rule deletes `WHERE created_at < now() - interval '…'` in
-- 5000-row batches. Without the index every batch is a sequential scan of a
-- table that only grows, so the purge gets slower exactly as it matters more.
--
-- CONCURRENTLY because this table is large on a live deployment; the DROP
-- first is the retry-after-an-interrupted-build rule (see 075): an invalid
-- index from a cancelled build is visible to IF NOT EXISTS and would be
-- skipped forever.
DROP INDEX CONCURRENTLY IF EXISTS idx_audit_logs_created_at;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_logs_created_at
    ON audit_logs (created_at);
