-- ============================================
-- 051 — per-channel webhook routing identity.
--
-- Meta, WhatsApp and Telegram resolve an inbound webhook to a tenant by a
-- provider-side id (page_id / phone_number_id / a SHA-256 of the webhook
-- secret). LINE and Zalo had nothing to match on, so resolveLineConfig and
-- resolveZaloConfig fell back to "the first active config of this platform" —
-- with two LINE merchants connected, every event was verified against the
-- wrong tenant's secret and rejected, and the second merchant never received
-- a single message.
--
-- Both providers do carry a stable identity in every webhook:
--   LINE — the top-level "destination" field, the bot's own userId
--   Zalo — the OA id the event was delivered to
--
-- Stored in one column rather than one per provider, because the routing
-- lookup is identical for both (and for any channel added later): match the
-- identity in the payload to a config. page_id / instagram_business_id /
-- whatsapp_business_account_id keep their existing per-provider columns.
--
-- Populated at connect/verify time from the provider API. Rows left blank
-- (channels connected before this migration) are adopted by the resolver on
-- the first webhook — but only when exactly one active config exists for that
-- platform, so an ambiguous deployment is refused rather than misrouted.
-- ============================================

ALTER TABLE platform_configs ADD COLUMN IF NOT EXISTS channel_identity VARCHAR(128) NOT NULL DEFAULT '';

COMMENT ON COLUMN platform_configs.channel_identity IS
    'Provider-side id used to route inbound webhooks (LINE destination / bot userId, Zalo OA id). Empty = not yet learned.';

-- Routing lookup: platform + identity must identify at most one active config.
-- Partial so the many rows with an empty identity do not collide.
CREATE UNIQUE INDEX IF NOT EXISTS uq_platform_configs_channel_identity
    ON platform_configs (platform, channel_identity)
    WHERE channel_identity <> '' AND is_active;
