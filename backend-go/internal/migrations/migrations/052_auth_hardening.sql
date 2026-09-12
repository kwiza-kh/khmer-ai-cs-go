-- ============================================
-- 052 — auth hardening: session revocation + encrypted TOTP secrets.
--
-- 1. users.token_version
--
--    JWTs are stateless and were never revocable: changing a password,
--    demoting a role or disabling 2FA left every previously issued token
--    valid until it expired (24h by default). The migration adds a version
--    that is stamped into each new token and compared on every request. The
--    auth middleware already reads users.is_active for its disabled-account
--    check, so the comparison rides along in that same query at no extra
--    round trip. Bumping the column invalidates every outstanding session for
--    that user.
--
-- 2. user_totp.secret widened for encryption-at-rest
--
--    Secrets were stored in plaintext, so a database read (backup, replica,
--    log of a query) handed over a working second factor. They are now sealed
--    with the same AES-256-GCM sealer used for channel credentials
--    (PLATFORM_CREDENTIAL_KEY). A sealed value is base64url(nonce||ct||tag)
--    with an "enc:v1:" prefix — 71 characters for a 20-byte base32 secret,
--    against a VARCHAR(128) limit. Widened to 255 for headroom.
--
--    Existing plaintext rows keep working: the sealer passes unrecognised
--    values through unchanged, and each secret is re-written in sealed form
--    the next time it is read or rotated.
-- ============================================

ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version INT NOT NULL DEFAULT 0;

COMMENT ON COLUMN users.token_version IS
    'Bumped to invalidate every issued JWT for this user (password change, role change, 2FA change).';

ALTER TABLE user_totp ALTER COLUMN secret TYPE VARCHAR(255);
