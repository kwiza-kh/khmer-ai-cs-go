-- System-prompt version history, so a bad edit can be rolled back.
--
-- The system prompt is the single most behaviour-defining knob in the product:
-- one bad edit silently changes every customer reply, and until now the
-- previous text was simply gone — model_configs.system_prompt holds only the
-- current value.
--
-- Semantics that the table depends on:
--
--   * APPEND-ONLY. A rollback writes a NEW row; history is never rewritten or
--     deleted. That keeps the rollback itself auditable and lets it be undone.
--
--   * '' means "no override — the built-in default in code is in effect". That
--     is a meaningful, healthy state (see gemini.FromPartsFull), NOT a missing
--     value, which is why the column is NOT NULL with an empty default rather
--     than nullable. Storing NULL here would make "cleared" and "never set"
--     indistinguishable in the history.
--
--   * source records how the row came to be:
--       baseline   — captured when this migration ran
--       admin_edit — saved from the admin model page
--       rollback   — restored from an earlier version
--
-- No foreign key from model_configs to this table: the history must outlive
-- any single current value, and ON DELETE CASCADE from the config is the only
-- direction that makes sense.
CREATE TABLE IF NOT EXISTS model_prompt_versions (
    version_id    BIGSERIAL PRIMARY KEY,
    config_id     INTEGER NOT NULL REFERENCES model_configs(config_id) ON DELETE CASCADE,
    system_prompt TEXT        NOT NULL DEFAULT '',
    source        TEXT        NOT NULL DEFAULT 'admin_edit',
    changed_by    INTEGER     REFERENCES users(user_id) ON DELETE SET NULL,
    note          TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS model_prompt_versions_config_idx
    ON model_prompt_versions (config_id, version_id DESC);

-- Baseline: one row per existing config, so the very first edit already has
-- something to roll back to. Without this the feature is useless exactly when
-- it is first needed.
INSERT INTO model_prompt_versions (config_id, system_prompt, source, note)
SELECT c.config_id, COALESCE(c.system_prompt, ''), 'baseline', '迁移 063 记录'
FROM model_configs c
WHERE NOT EXISTS (
    SELECT 1 FROM model_prompt_versions v WHERE v.config_id = c.config_id
);
