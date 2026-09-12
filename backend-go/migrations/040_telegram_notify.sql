-- 040 — Telegram bot for owner notifications (new customer messages,
-- handoff requests, SLA alerts). One bot per tenant; bot_token is stored
-- Sealer-encrypted (same envelope as platform credentials).
CREATE TABLE IF NOT EXISTS telegram_notify_settings (
    user_id         INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    bot_token_enc   TEXT NOT NULL,
    chat_id         VARCHAR(64) NOT NULL DEFAULT '',
    chat_title      VARCHAR(128) NOT NULL DEFAULT '',
    notify_messages BOOLEAN NOT NULL DEFAULT TRUE,
    notify_handoff  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
