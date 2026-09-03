-- Short-lived, user-bound state for the Meta OAuth callback. The JSON payload
-- is internal only and holds temporary Page access tokens until the user picks
-- which Page and Instagram professional account to connect.
CREATE TABLE IF NOT EXISTS platform_oauth_sessions (
    session_id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    state_hash VARCHAR(64) NOT NULL UNIQUE,
    payload_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at TIMESTAMPTZ NOT NULL,
    oauth_completed_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_platform_oauth_sessions_user_expiry
    ON platform_oauth_sessions (user_id, expires_at DESC);
