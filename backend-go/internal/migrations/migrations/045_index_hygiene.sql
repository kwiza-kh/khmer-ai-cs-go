-- 045 — index hygiene from the full-codebase audit.
--
-- Added: indexes on hot query paths that had none.
-- Dropped: exact duplicates that only cost write throughput.

-- knowledge_chunks: every re-index / document delete / FK cascade scans by
-- doc_id, and there was no index for it.
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_doc
    ON knowledge_chunks (doc_id);

-- platform_configs: 011 dropped the (user_id, platform) unique key and no
-- replacement was added; the settings list and the per-platform webhook
-- resolvers both filter by user_id / platform with is_active.
CREATE INDEX IF NOT EXISTS idx_platform_configs_user
    ON platform_configs (user_id);
CREATE INDEX IF NOT EXISTS idx_platform_configs_platform_active
    ON platform_configs (platform) WHERE is_active = true;

-- notifications: the bell lists "my unread, newest first" per render.
CREATE INDEX IF NOT EXISTS idx_notifications_user_created
    ON notifications (user_id, created_at DESC);

-- users: Google sign-in looks up lower(email); the plain UNIQUE(email) index
-- cannot serve that predicate.
CREATE INDEX IF NOT EXISTS idx_users_lower_email
    ON users (lower(email));

-- Redundant duplicates removed (identical definitions, double write cost).
DROP INDEX IF EXISTS idx_chunks_content_trgm;              -- == idx_knowledge_chunks_content_trgm
DROP INDEX IF EXISTS uq_platform_configs_active_telegram_bot_token_hash; -- == idx_telegram_bot_token_hash_active
