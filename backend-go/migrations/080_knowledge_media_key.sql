-- 080 — knowledge_documents.media_key: an uploaded picture is not only OCR'd into
-- text any more, it is kept so the answer can send the picture itself.
--
-- The merchant's product photo is the thing a customer asks about ("do you have
-- this?"), and the reply is only complete when it carries the photo back. The row
-- stores the R2 object key (not a URL): keys survive a public-base-URL change, and
-- every reader already resolves keys through storager2.PublicOrPresigned like
-- platform media and avatars do. NULL for text/URL documents and for image uploads
-- that happened before this migration (their bytes were never kept).
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS media_key TEXT;
