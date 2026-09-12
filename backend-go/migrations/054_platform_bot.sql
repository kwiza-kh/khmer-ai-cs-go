-- ============================================
-- 054 — platform bot: merchant linking + support inbox.
--
-- 1. telegram_notify_settings.bot_token_enc becomes nullable.
--
--    040 made every merchant store their own encrypted bot token, which forced
--    each one through BotFather before they could receive a single
--    notification — create a bot, copy a token, paste it, message the bot, wait
--    for a getUpdates poll to discover the chat id. Five steps, one of which
--    assumes the merchant knows what a bot token is.
--
--    With the operator bot, a merchant taps one deep link and is done. A NULL
--    token now means "deliver through the platform bot"; a non-NULL one keeps
--    the old per-merchant behaviour, so existing setups are untouched and no
--    forced migration is needed.
--
--    It also removes the per-tenant getUpdates polling: that loop ran every 12
--    seconds and issued one Telegram call per configured merchant, so the load
--    scaled linearly with the customer count. Platform-bot deliveries are
--    push-driven by a single webhook.
--
-- 2. platform_support_messages — merchants messaging the operator bot.
--
--    There is no ticketing system, and the merchants already live in Telegram.
--    Storing the messages here (rather than only forwarding them) keeps a
--    record when the operator is asleep, which for this timezone is most of
--    the working day elsewhere.
-- ============================================

ALTER TABLE telegram_notify_settings ALTER COLUMN bot_token_enc DROP NOT NULL;

ALTER TABLE telegram_notify_settings ADD COLUMN IF NOT EXISTS linked_at TIMESTAMPTZ;

-- Announcements are opt-OUT: a platform broadcast reaches every merchant, and
-- a merchant who does not want them must be able to say so. Defaulting to TRUE
-- keeps existing behaviour for the tenant-bot path, which never received
-- platform broadcasts at all.
ALTER TABLE telegram_notify_settings ADD COLUMN IF NOT EXISTS notify_announcements BOOLEAN NOT NULL DEFAULT TRUE;

COMMENT ON COLUMN telegram_notify_settings.bot_token_enc IS
    'Per-merchant bot token. NULL = deliver through the platform bot.';

CREATE TABLE IF NOT EXISTS platform_support_messages (
    message_id      BIGSERIAL PRIMARY KEY,
    -- The merchant who wrote in. NULL when the sender has no linked account
    -- (an unknown Telegram user found the bot), which is kept rather than
    -- discarded so the operator can still see and answer it.
    user_id         INTEGER REFERENCES users(user_id) ON DELETE SET NULL,
    telegram_user_id BIGINT NOT NULL,
    chat_id         VARCHAR(64) NOT NULL,
    username        VARCHAR(128) NOT NULL DEFAULT '',
    display_name    VARCHAR(128) NOT NULL DEFAULT '',
    body            TEXT NOT NULL,
    -- Who answered and when, so the operator can see what is still open.
    replied_at      TIMESTAMPTZ,
    replied_by      BIGINT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_platform_support_open
    ON platform_support_messages (created_at DESC) WHERE replied_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_platform_support_user
    ON platform_support_messages (user_id, created_at DESC);
