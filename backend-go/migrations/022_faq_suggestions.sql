-- ============================================
-- 022 — AI FAQ suggestions (self-learning knowledge base).
-- Periodically, the backend aggregates repeated customer questions that the
-- AI could not answer confidently and (with Gemini) drafts suggested answers.
-- Admins/owners review: accept → becomes a knowledge document; dismiss → hidden.
-- Tenant-scoped by user_id.
-- ============================================

CREATE TABLE IF NOT EXISTS faq_suggestions (
    suggestion_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id),
    question TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    frequency INT NOT NULL DEFAULT 1,
    status VARCHAR(16) NOT NULL DEFAULT 'pending', -- pending | added | dismissed
    source VARCHAR(32) NOT NULL DEFAULT 'aggregation', -- aggregation | manual
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_faq_suggestions_user_status
    ON faq_suggestions (user_id, status, frequency DESC);
