ALTER TABLE knowledge_documents
    ALTER COLUMN embedding_model SET DEFAULT 'gemini-embedding-001';

DELETE FROM knowledge_chunks
WHERE doc_id IN (
    SELECT doc_id
    FROM knowledge_documents
    WHERE embedding_model = 'text-embedding-004'
);

UPDATE knowledge_documents
SET embedding_model = 'gemini-embedding-001',
    index_status = 'pending',
    index_error = '',
    chunk_count = 0,
    last_embedded_at = NULL
WHERE embedding_model = 'text-embedding-004';
