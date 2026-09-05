-- ============================================
-- 035 — session summary cache invalidation.
--
-- Summaries were written to sessions.summary once and never invalidated: a
-- conversation that moved on kept showing the stale first summary, and the
-- language it was generated in was unknown. Track when the summary was
-- generated and in which language so it can be regenerated when the
-- transcript grows or the requested language changes.
-- ============================================

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS summary_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS summary_language VARCHAR(8) NOT NULL DEFAULT '';
