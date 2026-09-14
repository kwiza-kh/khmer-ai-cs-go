-- ============================================
-- 058 — segmented lexical search for unspaced scripts (Khmer, CJK).
--
-- 010 built the lexical index on to_tsvector('simple', content). 'simple'
-- neither segments words nor knows Khmer: a whole Khmer phrase (no inter-word
-- spaces) becomes ONE token, so the lexical leg never recalls. Verified on
-- PostgreSQL 17: to_tsvector('simple', 'ជំនួយការអតិថិជន') yields a single
-- token. 032 added an ILIKE bigram fallback for CJK only; Khmer (U+1780-17FF)
-- is not in isCJK, so its second leg was dead too.
--
-- This adds an app-segmented representation:
--   content_seg — space-separated token stream: plain words plus character
--                 n-grams for unspaced scripts (CJK bigrams, Khmer trigrams),
--                 produced by rag.SegmentForSearch.
--   content_tsv — to_tsvector('simple', content_seg), GIN-indexed below.
--
-- Both index and query side use the same Go segmentation, so the tsquery
-- matches. Legacy rows have content_tsv NULL; a background pass backfills
-- them without touching embeddings (searchLexical also ORs the old content
-- expression during the transition). Nullable columns keep the previous
-- binary working inside a rollback window.
-- ============================================

ALTER TABLE knowledge_chunks ADD COLUMN IF NOT EXISTS content_seg TEXT;
ALTER TABLE knowledge_chunks ADD COLUMN IF NOT EXISTS content_tsv TSVECTOR;

COMMENT ON COLUMN knowledge_chunks.content_seg IS
    'App-generated token stream for lexical search: words + CJK bigrams + Khmer trigrams (rag.SegmentForSearch).';
COMMENT ON COLUMN knowledge_chunks.content_tsv IS
    'to_tsvector(''simple'', content_seg), GIN-indexed. Backfilled in the background; NULL on legacy rows.';

-- NOTE: plain CREATE INDEX — the runner executes each migration inside a
-- transaction, so CONCURRENTLY is unavailable. It takes a SHARE lock on
-- knowledge_chunks for the build; chunk writes wait. Run during a quiet
-- period on a large table.
CREATE INDEX IF NOT EXISTS idx_knowledge_chunks_content_seg_tsv
    ON knowledge_chunks USING GIN (content_tsv);
