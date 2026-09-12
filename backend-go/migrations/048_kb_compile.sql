-- 048 — ingest-time "compile" stage (llm-wiki pattern): every indexed source
-- document gets an LLM-compiled FAQ/summary child document that participates
-- in normal hybrid retrieval, plus a contradiction review queue fed at
-- ingest time when the new document clashes with existing KB facts.
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS origin VARCHAR(20) NOT NULL DEFAULT 'manual';
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS compiled_from INT REFERENCES knowledge_documents(doc_id) ON DELETE CASCADE;
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS compile_status VARCHAR(20) NOT NULL DEFAULT 'none';

CREATE INDEX IF NOT EXISTS idx_kd_compiled_from
    ON knowledge_documents (compiled_from)
    WHERE compiled_from IS NOT NULL;

CREATE TABLE IF NOT EXISTS kb_contradictions (
    contradiction_id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    new_doc_id INT NOT NULL REFERENCES knowledge_documents(doc_id) ON DELETE CASCADE,
    old_doc_id INT REFERENCES knowledge_documents(doc_id) ON DELETE SET NULL,
    items JSONB NOT NULL DEFAULT '[]',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_kbc_user_status
    ON kb_contradictions (user_id, status, created_at DESC);
