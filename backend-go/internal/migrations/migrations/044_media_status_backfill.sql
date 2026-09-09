-- 044 — backfill media metadata for rows written before processing_status
-- existed. Those rows have no media_url, so the inbox kept requesting a
-- signed URL that could only 404. Marking them unavailable stops the retry
-- loop and shows the correct "media unavailable" state.
UPDATE chat_messages
SET metadata = jsonb_set(metadata, '{platform_media,processing_status}', '"unavailable"')
WHERE metadata -> 'platform_media' IS NOT NULL
  AND metadata -> 'platform_media' -> 'processing_status' IS NULL
  AND (media_url IS NULL OR media_url = '');
