-- ============================================
-- 089 — drop three objects nothing reads.
--
-- message_notes (030) and session_assignments (001): the note/assignment
-- behaviour they were designed for landed on sessions columns instead, and no
-- Go statement reads either table (tenantscope carried them only as dead
-- bookkeeping). customer_profiles.linked_to (026): cross-channel identity merge
-- was never built and the column has zero references.
--
-- Forward-only: safe to drop because no code, view or foreign key reads them.
-- ============================================

DROP TABLE IF EXISTS message_notes;
DROP TABLE IF EXISTS session_assignments;
ALTER TABLE customer_profiles DROP COLUMN IF EXISTS linked_to;
