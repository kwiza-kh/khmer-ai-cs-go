-- ============================================
-- 021 — tenant-scoped settings.
-- Business hours and canned responses become per-user (user_id), so every
-- registered customer manages their own schedule / quick-replies for their
-- platform channels. Existing global rows are backfilled to the bootstrap
-- admin (user_id = 1).
-- ============================================

-- Add the owner column to both tables.
ALTER TABLE business_hours ADD COLUMN IF NOT EXISTS user_id INT REFERENCES users(user_id);
ALTER TABLE canned_responses ADD COLUMN IF NOT EXISTS user_id INT REFERENCES users(user_id);

-- Backfill existing rows to the bootstrap admin so no data is lost.
UPDATE business_hours SET user_id = 1 WHERE user_id IS NULL;
UPDATE canned_responses SET user_id = 1 WHERE user_id IS NULL;

-- Make it mandatory.
ALTER TABLE business_hours ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE canned_responses ALTER COLUMN user_id SET NOT NULL;

-- The old UNIQUE(weekday, platform) is global — replace it with a per-user
-- uniqueness so two tenants can both configure Monday 09:00-18:00.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'business_hours'::regclass AND conname = 'business_hours_weekday_platform_key'
    ) THEN
        ALTER TABLE business_hours DROP CONSTRAINT business_hours_weekday_platform_key;
    END IF;
END$$;

-- Per-user uniqueness: (user_id, weekday, platform) — NULL platform stays the
-- global (platform-agnostic) rule for that user.
CREATE UNIQUE INDEX IF NOT EXISTS uq_business_hours_user_weekday_platform
    ON business_hours (user_id, weekday, platform)
    WHERE platform IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_business_hours_user_weekday
    ON business_hours (user_id, weekday)
    WHERE platform IS NULL;

-- Tenant-scoped query indexes.
CREATE INDEX IF NOT EXISTS idx_business_hours_user ON business_hours (user_id);
CREATE INDEX IF NOT EXISTS idx_canned_responses_user ON canned_responses (user_id);
