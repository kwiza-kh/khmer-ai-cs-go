-- A provider account must belong to exactly one active tenant. Application
-- checks prevent normal duplicates; these indexes close the concurrent-write
-- race that would otherwise make webhook routing ambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_page_identity
    ON platform_configs (platform, page_id)
    WHERE is_active = true
      AND platform IN ('meta', 'whatsapp')
      AND page_id IS NOT NULL
      AND page_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_active_instagram_identity
    ON platform_configs (instagram_business_id)
    WHERE is_active = true
      AND platform = 'instagram'
      AND instagram_business_id IS NOT NULL
      AND instagram_business_id <> '';

-- Four workers can process different customers at once. This index supports
-- the per-customer ordering predicate used when they claim inbound events.
CREATE INDEX IF NOT EXISTS idx_platform_inbound_events_conversation_order
    ON platform_inbound_events (config_id, platform_user_id, event_id)
    WHERE status IN ('pending', 'processing');
