-- Knowledge search quality & performance (2026-09).
-- The CJK bigram fallback path runs several `content ILIKE '%bi%'` predicates;
-- without a trigram index every query scanned the tenant's chunks.
CREATE INDEX IF NOT EXISTS idx_chunks_content_trgm
    ON knowledge_chunks USING GIN (content gin_trgm_ops);
