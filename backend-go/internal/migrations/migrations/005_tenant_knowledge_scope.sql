-- Knowledge documents are tenant-owned through uploaded_by. This index serves
-- the per-user list and vector-search prefilter without exposing other users'
-- documents. Legacy rows with a NULL owner remain inaccessible until assigned.
CREATE INDEX IF NOT EXISTS idx_knowledge_documents_owner_status_created
    ON knowledge_documents (uploaded_by, index_status, created_at DESC);
