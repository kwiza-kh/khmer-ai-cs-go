-- Platform integrations are tenant-scoped and may have more than one account
-- of the same channel. The original unique key prevented a customer from
-- connecting two Pages or Telegram bots.
ALTER TABLE platform_configs
    DROP CONSTRAINT IF EXISTS platform_configs_user_id_platform_key;

CREATE TABLE IF NOT EXISTS platform_connection_health (
    config_id INT PRIMARY KEY REFERENCES platform_configs(config_id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'unknown',
    account_name VARCHAR(200),
    detail TEXT,
    checked_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('unknown', 'connected', 'error'))
);

-- An event is first persisted, then processed by a worker. The unique key
-- makes provider webhook retries idempotent per configured integration.
CREATE TABLE IF NOT EXISTS platform_inbound_events (
    event_id BIGSERIAL PRIMARY KEY,
    config_id INT NOT NULL REFERENCES platform_configs(config_id) ON DELETE CASCADE,
    external_id VARCHAR(255) NOT NULL,
    platform platform_type NOT NULL,
    platform_user_id VARCHAR(100) NOT NULL,
    user_display_name VARCHAR(200),
    content TEXT NOT NULL,
    session_id UUID REFERENCES sessions(session_id) ON DELETE SET NULL,
    user_message_id BIGINT REFERENCES chat_messages(message_id) ON DELETE SET NULL,
    model_message_id BIGINT REFERENCES chat_messages(message_id) ON DELETE SET NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    locked_at TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    UNIQUE(config_id, external_id)
);
CREATE INDEX IF NOT EXISTS idx_platform_inbound_events_ready
    ON platform_inbound_events (status, next_attempt_at, created_at);

-- Reply delivery is durable and independently retryable. A persisted chat
-- message creates no more than one outbound delivery record.
CREATE TABLE IF NOT EXISTS platform_outbox (
    delivery_id BIGSERIAL PRIMARY KEY,
    config_id INT NOT NULL REFERENCES platform_configs(config_id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    chat_message_id BIGINT NOT NULL REFERENCES chat_messages(message_id) ON DELETE CASCADE,
    platform platform_type NOT NULL,
    recipient_id VARCHAR(100) NOT NULL,
    content TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    locked_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('pending', 'processing', 'sent', 'failed')),
    UNIQUE(chat_message_id)
);
CREATE INDEX IF NOT EXISTS idx_platform_outbox_ready
    ON platform_outbox (status, next_attempt_at, created_at);
