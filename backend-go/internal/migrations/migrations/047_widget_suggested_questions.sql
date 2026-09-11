-- 047 — Tidio-style opening question chips: each embed token carries its own
-- suggested questions, served by GET /widget/config and rendered by the
-- public widget before the visitor types anything.
ALTER TABLE widget_tokens ADD COLUMN IF NOT EXISTS suggested_questions TEXT[] NOT NULL DEFAULT '{}';
