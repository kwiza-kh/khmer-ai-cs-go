-- Semantic reply cache — one row per (tenant, question): a grounded, guarded
-- answer that is served verbatim to the next semantically identical ask,
-- skipping the retrieval wait and generation entirely.
--
-- Scope & lifetime are tenant-level by construction:
--   * user_id mirrors the rest of the tenant model (one tenant = one users
--     row); no RLS backstop here beyond the app-layer WHERE — answers are not
--     customer personal data, they are KB-derived content, but the tenant
--     boundary is kept anyway;
--   * the app drops every row of a tenant whenever its knowledge base changes
--     (rag.Service.KBChanged → replycache.InvalidateTenant): a cached answer
--     was grounded in the old content;
--   * TTL + per-tenant cap are enforced opportunistically on store (see the
--     replycache package) — this table stays small by design.
--
-- The embedding is the same 768-dim Gemini model the chunks use, so a swap of
-- embedding model (see the Phase 4 note in DEVELOPMENT.md) must rebuild this
-- table too — like knowledge_chunks, it stores vectors from the live model.
CREATE TABLE IF NOT EXISTS reply_cache (
    cache_id        BIGSERIAL PRIMARY KEY,
    user_id         INT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    language        VARCHAR(10) NOT NULL DEFAULT 'km',
    query_text      TEXT NOT NULL,
    query_embedding VECTOR(768) NOT NULL,
    answer          TEXT NOT NULL,
    model_name      VARCHAR(100) NOT NULL DEFAULT '',
    hit_count       INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_hit_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_reply_cache_user
    ON reply_cache (user_id);

-- Same HNSW shape as the chunk index (032): nearest-question search on every
-- kb_question turn, small per-tenant row counts.
CREATE INDEX IF NOT EXISTS idx_reply_cache_embedding
    ON reply_cache USING hnsw (query_embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);

CREATE INDEX IF NOT EXISTS idx_reply_cache_created
    ON reply_cache (created_at);
