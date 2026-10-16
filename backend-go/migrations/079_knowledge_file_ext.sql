-- 079 — knowledge_documents.file_ext: the document library shows every file's
-- real type ("PDF", "DOCX", "XLSX", …) with a per-type icon, but a file upload
-- strips the extension from the title (SVN-003-order-quotation), so the type
-- was unrecoverable from the row. Uploads now record it here: lowercase, no
-- leading dot. Legacy rows stay NULL and the UI falls back to the title/source
-- heuristic it always had.
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS file_ext VARCHAR(16);
