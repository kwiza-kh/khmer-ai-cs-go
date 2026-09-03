-- Keep the WhatsApp business account selected through Embedded Signup so the
-- server can subscribe the correct application and show a stable connection.
ALTER TABLE platform_configs
    ADD COLUMN IF NOT EXISTS whatsapp_business_account_id TEXT NOT NULL DEFAULT '';

-- Media references remain internal work data until they have been copied to
-- the tenant-scoped private object store.
ALTER TABLE platform_inbound_events
    ADD COLUMN IF NOT EXISTS media_json JSONB;
