-- Lexical retrieval complements vector similarity for product names, SKUs and
-- identifiers. The expression matches lexicalKnowledgeSearchSQL exactly.
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_content_tsv_simple
    ON knowledge_chunks USING GIN (to_tsvector('simple', content));
