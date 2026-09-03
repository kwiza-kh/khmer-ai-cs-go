-- RAG upgrade: trigram recall for CJK lexical search, HNSW vector index,
-- URL source tracking for freshness sweeps, and retrieval telemetry that
-- powers the knowledge-gap report.

-- CJK text has no spaces, so tsquery('simple') tokens whole sentences and the
-- lexical path never recalls. Trigram GIN accelerates the ILIKE bigram path.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_content_trgm
    ON knowledge_chunks USING GIN (content gin_trgm_ops);

-- Approximate nearest-neighbour index for the dense path (cosine distance).
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_embedding_hnsw
    ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);

-- Persist the origin URL so a freshness sweep can re-fetch and re-index
-- pages whose content changed.
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS source_url TEXT;

-- Retrieval telemetry: every grounding query with hit counts and scores.
-- Zero/low-quality hits are the knowledge-gap report.
CREATE TABLE IF NOT EXISTS rag_query_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    session_id UUID,
    query TEXT NOT NULL,
    rewritten_query TEXT,
    hit_count INT NOT NULL DEFAULT 0,
    top_score REAL,
    used_in_reply BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_rag_query_logs_user_time
    ON rag_query_logs (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_rag_query_logs_zero_hits
    ON rag_query_logs (user_id, hit_count, created_at DESC);
