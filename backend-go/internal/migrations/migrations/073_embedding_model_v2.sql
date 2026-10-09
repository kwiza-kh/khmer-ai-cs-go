-- 073: the embedding model moves to its successor, gemini-embedding-2.
--
-- WHY NOW: 001 retires no sooner than 2028-05, so this is a quality/headroom
-- move, not a forced one: 002 doubles the input window (8192 vs 2048 tokens),
-- adds modalities, and scores higher on the published multilingual mean. The
-- call shape changes with it — 002 is served by :embedContent on the platform
-- where 001 used :predict (measured 2026-10-09 on the production project), and
-- it has NO batch entry point, so client code embeds its documents one text per
-- call. Vector spaces are INCOMPATIBLE between the two models: a corpus indexed
-- with one cannot be queried with the other.
--
-- This migration does the two things the DATABASE owns; the env switch
-- (GEMINI_EMBEDDING_MODEL, read by the running binary) owns the rest:
--
--   1. the column default follows the model the deployment now embeds with, so
--      a row inserted without an explicit model does not keep advertising 001;
--   2. every document whose vectors are from the previous model is queued for
--      re-embedding. This mirrors the boot-time sweep in
--      rag.Service.SpawnIndexWorkers — which stays authoritative, because it
--      resolves the model from the DEPLOYMENT's configuration and therefore
--      also handles a rollback back to 001 (it would re-queue these same docs).
--      Doing it here as well means the queue is correct the moment the
--      migration lands, not only after the next service start.
--
-- Chunks are deliberately NOT deleted the way 007 deleted text-embedding-004
-- chunks. Every dense read filters on kd.embedding_model = <current model>, so
-- stale-model chunks are unreachable by vector search, while the lexical leg
-- (which never touches embeddings) keeps serving their content during the
-- re-embed window. indexDocument replaces a document's chunks wholesale in one
-- transaction, so the swap is atomic per document.
ALTER TABLE knowledge_documents
    ALTER COLUMN embedding_model SET DEFAULT 'gemini-embedding-2';

UPDATE knowledge_documents
SET index_status = 'pending',
    index_error = ''
WHERE index_status = 'ready'
  AND embedding_model <> ''
  AND embedding_model <> 'gemini-embedding-2';
