-- ============================================
-- 053 — Telegram (OIDC) sign-in.
--
-- Telegram Login returns no e-mail address. Its OIDC id_token carries `sub`,
-- `preferred_username`, `name`, `picture` and — only with the `phone` scope —
-- `phone_number`. users.email was NOT NULL UNIQUE, so a Telegram-only account
-- could not be created at all; the only way around it would be synthesising a
-- fake address, which then leaks into the user list, the team view and every
-- CSV export.
--
-- Making it nullable mirrors what 042 already did for password_hash when
-- Google sign-in landed. Postgres treats NULLs as distinct in a UNIQUE index,
-- so any number of e-mail-less accounts coexist cleanly.
--
-- Every read of users.email in the api package now COALESCEs to '' — a NULL
-- scanned into a Go string is a runtime error, not a zero value.
--
-- telegram_sub is the `sub` claim, the stable identifier for the account.
-- Telegram @usernames are mutable and can be recycled, so they must never be
-- used as an identity key.
--
-- Note the interaction with findOrCreateGoogleUser: it looks accounts up by
-- lower(email), and NULL never equals anything, so e-mail-less accounts are
-- simply invisible to that path — which is the desired behaviour. Google and
-- Telegram identities for one person stay separate unless linked explicitly.
-- ============================================

ALTER TABLE users ALTER COLUMN email DROP NOT NULL;

ALTER TABLE users ADD COLUMN IF NOT EXISTS telegram_sub VARCHAR(64);

COMMENT ON COLUMN users.telegram_sub IS
    'OIDC subject from Telegram Login. Stable across username changes.';

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_telegram_sub
    ON users (telegram_sub) WHERE telegram_sub IS NOT NULL;
