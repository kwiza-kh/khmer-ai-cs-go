-- ============================================
-- 024 — multi-agent teams (F4).
-- Users with role 'admin' are team owners; new role 'agent' joins the tenant.
-- Agent teams table scopes agents to a tenant; skill groups for auto-assign.
-- ============================================

CREATE TABLE IF NOT EXISTS agent_teams (
    team_id BIGSERIAL PRIMARY KEY,
    owner_user_id INT NOT NULL REFERENCES users(user_id),   -- tenant owner (admin)
    agent_user_id INT NOT NULL REFERENCES users(user_id),   -- agent member
    display_name VARCHAR(100) NOT NULL DEFAULT '',
    skills TEXT[] NOT NULL DEFAULT '{}',                    -- e.g. {sales, support}
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (owner_user_id, agent_user_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_teams_owner ON agent_teams (owner_user_id);
