-- Durable human-support queue. A session can retain resolved request history,
-- but only one pending or assigned request may be open at a time.
CREATE TYPE human_handoff_request_status AS ENUM ('pending', 'assigned', 'resolved');
CREATE TYPE human_handoff_priority AS ENUM ('normal', 'high');
CREATE TYPE human_handoff_trigger AS ENUM ('customer_request', 'negative_feedback', 'ai_decision', 'manual');

CREATE TABLE human_handoff_requests (
    request_id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    session_id UUID NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    status human_handoff_request_status NOT NULL DEFAULT 'pending',
    priority human_handoff_priority NOT NULL DEFAULT 'normal',
    trigger human_handoff_trigger NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    assigned_agent_id INTEGER REFERENCES users(user_id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    assigned_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    resolution_note TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX human_handoff_requests_one_open_session
    ON human_handoff_requests(session_id)
    WHERE status IN ('pending', 'assigned');

CREATE INDEX human_handoff_requests_queue_idx
    ON human_handoff_requests(user_id, status, priority, created_at DESC);
