-- Search quality follow-up (2026-09 audit): indexes backing the analytics
-- dashboards' agent-performance and timeline queries, which previously
-- filtered chat_messages by created_at and sessions by assigned_agent_id
-- with no matching index (full scans per agent row).
CREATE INDEX IF NOT EXISTS idx_messages_created_at
    ON chat_messages (created_at);
CREATE INDEX IF NOT EXISTS idx_messages_role_created
    ON chat_messages (role, created_at);
CREATE INDEX IF NOT EXISTS idx_sessions_assigned_agent
    ON sessions (assigned_agent_id);
