ALTER TABLE platform_configs
    ADD COLUMN IF NOT EXISTS bot_token_hash VARCHAR(64);

CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_telegram_bot_token_hash
    ON platform_configs (bot_token_hash)
    WHERE is_active = true AND platform = 'telegram' AND bot_token_hash IS NOT NULL AND bot_token_hash <> '';
