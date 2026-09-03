-- ============================================
-- 029 — in-app notifications center.
-- Tenant-scoped: every user gets a notification feed (new sessions, handoff
-- requests, sentiment alerts, quota warnings). The WebSocket inbox hub pushes
-- "inbox.notification" events so the bell updates in real time.
-- ============================================

CREATE TABLE IF NOT EXISTS notifications (
    notification_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    kind VARCHAR(32) NOT NULL DEFAULT 'info',      -- session | handoff | sentiment | quota | system
    title VARCHAR(200) NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    session_id UUID,
    is_read BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_notifications_user_unread
    ON notifications (user_id, is_read, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_user_created
    ON notifications (user_id, created_at DESC);
