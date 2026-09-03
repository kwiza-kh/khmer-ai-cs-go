-- ============================================
-- 026 — marketing campaigns (F5) + identity linking (F6).
-- F5: scheduled WhatsApp-template campaigns; the worker turns due campaigns
--     into outbound deliveries (template payloads are 24h-window exempt).
-- F6: customer_profiles can be linked (merged identity); customer_360 shows
--     all linked channel identities of a customer.
-- ============================================

ALTER TABLE customer_profiles ADD COLUMN IF NOT EXISTS linked_to BIGINT REFERENCES customer_profiles(profile_id);

CREATE TABLE IF NOT EXISTS marketing_campaigns (
    campaign_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id),       -- tenant owner
    name VARCHAR(120) NOT NULL DEFAULT '',
    platform VARCHAR(16) NOT NULL DEFAULT 'whatsapp',
    config_id INT NOT NULL,
    template_name VARCHAR(512) NOT NULL,
    template_language VARCHAR(16) NOT NULL DEFAULT 'km',
    body_params JSONB NOT NULL DEFAULT '[]',
    recipient_filter VARCHAR(32) NOT NULL DEFAULT 'all',  -- all | tagged
    tag_filter VARCHAR(64),
    scheduled_at TIMESTAMPTZ NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'scheduled',      -- scheduled | sending | done | failed | cancelled
    sent_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_campaigns_due ON marketing_campaigns (status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_campaigns_user ON marketing_campaigns (user_id);
