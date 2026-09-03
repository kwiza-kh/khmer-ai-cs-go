-- ============================================
-- 030 — enterprise agent platform.
-- SLA policies + breaches, routing rules, macros, RBAC roles, TOTP 2FA,
-- outbound webhook subscriptions + delivery log, message-level notes,
-- and session intent/confidence/PII columns.
-- All statements are idempotent (IF NOT EXISTS / ADD COLUMN IF NOT EXISTS).
-- ============================================

-- ------------------------------------------------------------
-- 1. SLA policies + breaches
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sla_policies (
    sla_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    first_response_secs INT NOT NULL,
    resolution_secs INT,
    business_hours_only BOOLEAN NOT NULL DEFAULT false,
    priority VARCHAR(16) NOT NULL DEFAULT 'normal',   -- normal | high | all
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sla_breaches (
    breach_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    sla_id INT REFERENCES sla_policies(sla_id) ON DELETE SET NULL,
    breach_type VARCHAR(32) NOT NULL,                  -- first_response | resolution
    breached_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved BOOLEAN NOT NULL DEFAULT false,
    resolved_at TIMESTAMPTZ,
    UNIQUE (session_id, breach_type)
);
CREATE INDEX IF NOT EXISTS idx_sla_breaches_user_open ON sla_breaches (user_id, resolved, breached_at DESC);
CREATE INDEX IF NOT EXISTS idx_sla_breaches_session ON sla_breaches (session_id);

-- ------------------------------------------------------------
-- 2. Routing rules
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS routing_rules (
    rule_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    -- JSON conditions: {"channel": "...", "language": "...", "intent": "...",
    --                   "skills": [...], "sentiment": "negative"}
    conditions JSONB NOT NULL DEFAULT '{}',
    target_type VARCHAR(16) NOT NULL DEFAULT 'agent',  -- agent | queue | round_robin
    target_agent_id INT REFERENCES users(user_id) ON DELETE SET NULL,
    target_skills TEXT[] NOT NULL DEFAULT '{}',
    priority INT NOT NULL DEFAULT 0,                   -- higher = first match
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_routing_rules_user ON routing_rules (user_id, priority DESC);

-- ------------------------------------------------------------
-- 3. Macros (multi-step canned sequences)
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS macros (
    macro_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    title VARCHAR(120) NOT NULL,
    -- JSON array of steps: [{"content": "...", "delay_ms": 0, "payload": {...}}]
    steps JSONB NOT NULL DEFAULT '[]',
    category VARCHAR(64),
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ------------------------------------------------------------
-- 4. RBAC — custom roles + per-user role assignment
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS roles (
    role_id SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name VARCHAR(64) NOT NULL,
    -- permission slugs, e.g. ["inbox.view","inbox.reply","admin.users","sla.manage"]
    permissions TEXT[] NOT NULL DEFAULT '{}',
    is_system BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, name)
);

CREATE TABLE IF NOT EXISTS user_roles (
    user_role_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    role_id INT NOT NULL REFERENCES roles(role_id) ON DELETE CASCADE,
    UNIQUE (user_id, role_id)
);

-- ------------------------------------------------------------
-- 5. TOTP two-factor authentication
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_totp (
    user_id INT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    secret VARCHAR(128) NOT NULL,                      -- base32 secret
    enabled BOOLEAN NOT NULL DEFAULT false,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ------------------------------------------------------------
-- 6. Message-level notes (agent collaboration)
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS message_notes (
    note_id BIGSERIAL PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    message_id BIGINT REFERENCES chat_messages(message_id) ON DELETE CASCADE,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    body TEXT NOT NULL,
    mentions TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_message_notes_session ON message_notes (session_id, created_at);

-- ------------------------------------------------------------
-- 7. Outbound webhook subscriptions + delivery log
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    subscription_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    url VARCHAR(500) NOT NULL,
    events TEXT[] NOT NULL DEFAULT '{}',
    secret VARCHAR(200),
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    delivery_id BIGSERIAL PRIMARY KEY,
    subscription_id INT NOT NULL REFERENCES webhook_subscriptions(subscription_id) ON DELETE CASCADE,
    event VARCHAR(64) NOT NULL,
    status_code INT,
    success BOOLEAN NOT NULL DEFAULT false,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_sub ON webhook_deliveries (subscription_id, created_at DESC);

-- ------------------------------------------------------------
-- 8. Session intent / confidence / PII columns
-- ------------------------------------------------------------
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS intent VARCHAR(64);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS confidence FLOAT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS pii_redacted BOOLEAN NOT NULL DEFAULT false;
