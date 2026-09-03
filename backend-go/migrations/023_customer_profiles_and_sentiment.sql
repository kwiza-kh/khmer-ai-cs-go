-- ============================================
-- 023 — customer profiles + session sentiment.
-- F1: AI-generated session summaries + customer 360 view.
-- F2: real-time sentiment flags on sessions (red-alert for angry customers).
-- Tenant-scoped: a "customer profile" belongs to the owning user (tenant).
-- ============================================

-- Sentiment + summary on sessions (F1 + F2).
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS sentiment VARCHAR(16) NOT NULL DEFAULT 'neutral';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS sentiment_at TIMESTAMPTZ;

-- Customer 360: one row per (tenant, channel, platform_user_id) so a customer
-- who appears on WhatsApp + web + LINE is unified into a single profile.
CREATE TABLE IF NOT EXISTS customer_profiles (
    profile_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id),          -- tenant owner
    platform VARCHAR(16) NOT NULL,                           -- channel of first sighting
    platform_user_id VARCHAR(100) NOT NULL,                  -- channel id (whatsapp number etc.)
    display_name VARCHAR(200) NOT NULL DEFAULT '',
    phone VARCHAR(50),
    email VARCHAR(150),
    tags TEXT[] NOT NULL DEFAULT '{}',
    total_sessions INT NOT NULL DEFAULT 0,
    total_messages INT NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMPTZ,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, platform, platform_user_id)
);

CREATE INDEX IF NOT EXISTS idx_customer_profiles_user ON customer_profiles (user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_sentiment ON sessions (user_id, sentiment);
