-- Bind external platform users to an integration config, not only a platform.
-- This permits the same Messenger/Telegram user to contact different tenants
-- without sharing a conversation or a knowledge base.
ALTER TABLE platform_user_sessions
    ADD COLUMN IF NOT EXISTS config_id INT REFERENCES platform_configs(config_id) ON DELETE CASCADE;

-- Backfill config_id: link each existing platform_user_sessions row to the
-- platform_configs row owned by the session's owner for that platform.
-- PostgreSQL UPDATE ... FROM does not allow aliasing the target table in the
-- FROM clause, so we join the extra tables in the WHERE clause instead.
UPDATE platform_user_sessions
SET config_id = pc.config_id
FROM sessions s, platform_configs pc
WHERE platform_user_sessions.session_id = s.session_id
  AND pc.user_id = s.user_id
  AND pc.platform = platform_user_sessions.platform
  AND platform_user_sessions.config_id IS NULL;

ALTER TABLE platform_user_sessions
    DROP CONSTRAINT IF EXISTS platform_user_sessions_pkey;

CREATE UNIQUE INDEX IF NOT EXISTS idx_platform_user_sessions_config_user
    ON platform_user_sessions (config_id, platform_user_id)
    WHERE config_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_platform_user_sessions_session
    ON platform_user_sessions (session_id);

CREATE INDEX IF NOT EXISTS idx_platform_configs_active_page
    ON platform_configs (platform, page_id)
    WHERE is_active = true AND page_id IS NOT NULL AND page_id <> '';

CREATE INDEX IF NOT EXISTS idx_platform_configs_active_instagram
    ON platform_configs (platform, instagram_business_id)
    WHERE is_active = true AND instagram_business_id IS NOT NULL AND instagram_business_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_page
    ON platform_configs (platform, page_id)
    WHERE is_active = true AND page_id IS NOT NULL AND page_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_instagram
    ON platform_configs (platform, instagram_business_id)
    WHERE is_active = true AND instagram_business_id IS NOT NULL AND instagram_business_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_telegram_secret
    ON platform_configs (webhook_secret)
    WHERE is_active = true AND platform = 'telegram' AND webhook_secret IS NOT NULL AND webhook_secret <> '';

CREATE INDEX IF NOT EXISTS idx_messages_session_latest
    ON chat_messages (session_id, created_at DESC, message_id DESC);
