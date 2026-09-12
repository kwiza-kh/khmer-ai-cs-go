-- 041 — Telegram bot uniqueness: the cross-tenant dedupe in the save handler
-- ignored lookup errors; with this partial unique index the constraint holds
-- even if the check is bypassed.
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_bot_token_hash_active
    ON platform_configs (bot_token_hash) WHERE platform = 'telegram' AND is_active = true;
