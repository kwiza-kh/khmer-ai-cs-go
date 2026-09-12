-- ============================================
-- 055 — two-way merchant support relay.
--
-- 054 gave merchants a way to reach the operator (platform_support_messages)
-- but no way back: the operator saw the message and had nothing to answer
-- with. /broadcast reaches everyone, which is the wrong tool for one merchant.
--
-- The reply mechanism is Telegram's own: the alert the operator receives is a
-- message from the bot, so they long-press it and reply. That update carries
-- reply_to_message.message_id, and this table maps that id back to the
-- merchant conversation it came from.
--
-- Keyed on (admin_chat_id, admin_message_id) because Telegram message ids are
-- only unique within a chat — a bare message_id would collide across operators.
-- ============================================

CREATE TABLE IF NOT EXISTS platform_support_relay (
    relay_id           BIGSERIAL PRIMARY KEY,
    -- The operator's chat, and the id of the copy we sent them.
    admin_chat_id      BIGINT NOT NULL,
    admin_message_id   BIGINT NOT NULL,
    -- Where a reply to that copy should go.
    merchant_chat_id   BIGINT NOT NULL,
    -- The stored inbound message this relay carries, so a reply can mark it
    -- answered. Nullable: the row survives if the inbox entry is pruned.
    support_message_id BIGINT REFERENCES platform_support_messages(message_id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (admin_chat_id, admin_message_id)
);

-- The hot path is a single lookup per operator reply.
CREATE INDEX IF NOT EXISTS idx_support_relay_lookup
    ON platform_support_relay (admin_chat_id, admin_message_id);

-- Age index so stale relays can be pruned; a mapping nobody ever replies to
-- would otherwise accumulate forever.
CREATE INDEX IF NOT EXISTS idx_support_relay_created
    ON platform_support_relay (created_at);
