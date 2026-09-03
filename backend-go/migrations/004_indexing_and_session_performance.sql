-- Durable knowledge-index status plus indexes for the current query paths.
ALTER TABLE knowledge_documents
    ADD COLUMN IF NOT EXISTS index_status VARCHAR(20) NOT NULL DEFAULT 'pending';
ALTER TABLE knowledge_documents
    ADD COLUMN IF NOT EXISTS index_error TEXT NOT NULL DEFAULT '';

-- Preserve already embedded documents and recover work interrupted by a restart.
UPDATE knowledge_documents kd
SET index_status = CASE
    WHEN EXISTS (SELECT 1 FROM knowledge_chunks kc WHERE kc.doc_id = kd.doc_id) THEN 'ready'
    ELSE 'pending'
END
WHERE index_status = 'pending';
UPDATE knowledge_documents
SET index_status = 'pending', index_error = ''
WHERE index_status = 'indexing';

CREATE INDEX IF NOT EXISTS idx_knowledge_documents_index_status
    ON knowledge_documents (index_status, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_sessions_user_status_created
    ON sessions (user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_session_feedback_at
    ON chat_messages (session_id, feedback_at DESC)
    WHERE feedback_rating IS NOT NULL;

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_sessions_title_trgm
    ON sessions USING GIN (title gin_trgm_ops);
