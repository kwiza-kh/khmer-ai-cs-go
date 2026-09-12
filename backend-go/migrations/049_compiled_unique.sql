-- 049 — exactly one compiled child per source document. The index-worker
-- claim race could compile a source twice; this partial unique index is the
-- hard guarantee (the compiled-doc insert uses ON CONFLICT DO NOTHING).
-- Existing duplicates are collapsed first (keep the earliest child).
DELETE FROM knowledge_documents d
WHERE d.compiled_from IS NOT NULL
  AND d.doc_id NOT IN (
    SELECT MIN(doc_id) FROM knowledge_documents
    WHERE compiled_from IS NOT NULL GROUP BY compiled_from
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_kd_one_compiled_per_source
    ON knowledge_documents (compiled_from)
    WHERE compiled_from IS NOT NULL;
