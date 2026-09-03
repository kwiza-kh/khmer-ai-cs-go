-- ============================================
-- 034 — per-user preferences (settings page).
-- language: '' = auto-detect per message; 'km'/'en'/'zh' forces the AI reply
-- language for the whole tenant. notification_pref: all/escalations/none.
-- ============================================

ALTER TABLE users ADD COLUMN IF NOT EXISTS language VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS notification_pref VARCHAR(16) NOT NULL DEFAULT 'all';
