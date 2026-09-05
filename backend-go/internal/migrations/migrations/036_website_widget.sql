-- ============================================
-- 036 — website chat widget.
-- A tenant creates one or more embed tokens; the public widget endpoints are
-- authenticated solely by the token (never a kcs_ API key), so a leaked page
-- source cannot expose the full admin API. Tokens are revocable and scoped to
-- chat + history + feedback for that tenant.
-- ============================================

CREATE TABLE IF NOT EXISTS widget_tokens (
    token_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL DEFAULT 'Website',
    token VARCHAR(64) NOT NULL UNIQUE,
    is_active BOOLEAN NOT NULL DEFAULT true,
    allowed_origins TEXT[] NOT NULL DEFAULT '{}',   -- empty = any origin
    theme VARCHAR(16) NOT NULL DEFAULT 'light',     -- light | dark | auto
    primary_color VARCHAR(9) NOT NULL DEFAULT '#4f46e5',
    greeting_km TEXT,
    greeting_en TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_widget_tokens_user ON widget_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_widget_tokens_token ON widget_tokens(token);

-- 'web' platform is now persisted on sessions for widget conversations.
ALTER TYPE platform_type ADD VALUE IF NOT EXISTS 'web';
