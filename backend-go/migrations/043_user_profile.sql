-- 043 — user profile fields editable in the personal profile section.
ALTER TABLE users ADD COLUMN IF NOT EXISTS display_name VARCHAR(80);
ALTER TABLE users ADD COLUMN IF NOT EXISTS job_title VARCHAR(80);
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone VARCHAR(32);
ALTER TABLE users ADD COLUMN IF NOT EXISTS timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Phnom_Penh';
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url TEXT;
