-- ============================================
-- 087 — password-reset tokens (forgot-password and operator-issued links).
--
-- WHY HASH-ONLY: the row stores the SHA-256 hex digest of a 32-byte random
-- token, never the token itself. A database read (backup, replica, logged
-- statement, SQL injection) then yields nothing that can be pasted into the
-- reset form. The plaintext lives only in the e-mail body or in the operator's
-- response and must never be logged.
--
-- WHY ONE-TIME: a link is redeemable while used_at IS NULL and expires_at is
-- in the future. Confirming a reset stamps used_at on every outstanding row of
-- that user in the same transaction that changes the password, so a link
-- observed earlier (mailbox, browser history, shoulder) dies with the new
-- credential.
--
-- WHY TWO TTLs: a self-service e-mail link lives 30 minutes — it travels
-- through a third party and is normally opened immediately. An
-- operator-issued link lives 24 hours because it is handed over out of band
-- (chat, phone) and the person may only act the next day; it is still bounded,
-- because an unbounded reset link is a permanent backdoor.
--
-- created_by records the platform admin for an operator-issued link; NULL means
-- the user requested it themselves. ON DELETE SET NULL keeps the token row when
-- the admin account is removed later — the audit_logs row remains the durable
-- record of who acted.
-- ============================================

CREATE TABLE IF NOT EXISTS password_reset_tokens (
    token_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ NULL,
    created_by INT NULL REFERENCES users(user_id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- The throttled-request and confirm paths always look up one user's newest
-- tokens; the admin path additionally invalidates them by user.
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_user_created
    ON password_reset_tokens (user_id, created_at DESC);

-- Retention (cmd/retention) and expiry checks scan by time.
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_expires
    ON password_reset_tokens (expires_at);
