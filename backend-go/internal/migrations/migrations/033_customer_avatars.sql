-- ============================================
-- 033 — customer avatars.
-- Stores the channel profile photo URL (Meta profile_pic, LINE pictureUrl).
-- Fetched once on first inbound message, refreshed when the customer writes
-- again and the field is empty. WhatsApp/Telegram have no stable public
-- avatar API — the frontend falls back to an initials placeholder.
-- ============================================

ALTER TABLE customer_profiles ADD COLUMN IF NOT EXISTS avatar_url VARCHAR(500);
