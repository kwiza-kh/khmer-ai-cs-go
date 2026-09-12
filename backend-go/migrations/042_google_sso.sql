-- 042 — Google (OIDC) sign-in. A user may authenticate either with a password
-- or through Google, so password_hash becomes nullable and the Google subject
-- (sub) is stored for lookup.
ALTER TABLE users ADD COLUMN IF NOT EXISTS google_sub VARCHAR(64);
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_google_sub
    ON users (google_sub) WHERE google_sub IS NOT NULL;
