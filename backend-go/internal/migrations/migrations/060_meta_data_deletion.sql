-- Meta Data Deletion Request Callback — audit trail (Meta Platform Terms §3(d)(i)).
--
-- One row per verified deletion request. `confirmation_code` is what Meta shows
-- the data subject and what our own status page looks up, so a retry of the
-- same request MUST yield the same code: UNIQUE (source, meta_app_scoped_id)
-- makes the insert idempotent and the handler returns the existing code.
--
-- `status` carries the honest outcome:
--   completed  — records matching the supplied id were removed
--   unresolved — signature was valid but nothing here is keyed by that id
--                (Meta sends an app-scoped id; end customers are keyed by
--                page-scoped PSID/IGSID/wa_id). Needs the manual path.
--   failed     — the delete transaction rolled back; nothing was removed
CREATE TABLE IF NOT EXISTS deletion_requests (
    request_id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    confirmation_code      TEXT        NOT NULL,
    source                 TEXT        NOT NULL,
    meta_app_scoped_id     TEXT        NOT NULL,
    matched_config_id      INTEGER     REFERENCES platform_configs(config_id) ON DELETE SET NULL,
    status                 TEXT        NOT NULL DEFAULT 'pending',
    detail                 TEXT        NOT NULL DEFAULT '',
    sessions_deleted       INTEGER     NOT NULL DEFAULT 0,
    messages_deleted       INTEGER     NOT NULL DEFAULT 0,
    inbound_events_deleted INTEGER     NOT NULL DEFAULT 0,
    query_logs_deleted     INTEGER     NOT NULL DEFAULT 0,
    requested_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at           TIMESTAMPTZ,
    CONSTRAINT deletion_requests_source_subject_key UNIQUE (source, meta_app_scoped_id)
);

CREATE INDEX IF NOT EXISTS deletion_requests_code_idx
    ON deletion_requests (confirmation_code);

CREATE INDEX IF NOT EXISTS deletion_requests_status_idx
    ON deletion_requests (status, requested_at DESC);
