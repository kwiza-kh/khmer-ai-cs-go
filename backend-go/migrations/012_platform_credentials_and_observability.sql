ALTER TABLE platform_configs
    ADD COLUMN IF NOT EXISTS webhook_secret_hash VARCHAR(64);

-- Existing credentials are migrated by the application after it is given the
-- encryption key. This hash preserves Telegram's secure constant-time lookup.
CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_telegram_secret_hash
    ON platform_configs (webhook_secret_hash)
    WHERE is_active = true AND platform = 'telegram' AND webhook_secret_hash IS NOT NULL AND webhook_secret_hash <> '';
