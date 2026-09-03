-- ============================================
-- 027 — platform super-admin role (enum + rollup view).
-- The actual admin upgrade lives in 028 (PG forbids using a newly-added enum
-- value inside the same transaction that added it).
-- ============================================

-- Add the new enum value (PG 12+ allows this inside a DO block).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_enum
        WHERE enumtypid = 'user_role'::regtype AND enumlabel = 'platform_admin'
    ) THEN
        ALTER TYPE user_role ADD VALUE 'platform_admin';
    END IF;
END$$;

-- Per-tenant usage rollup view used by the platform dashboard (idempotent).
DROP VIEW IF EXISTS tenant_overview;
CREATE VIEW tenant_overview AS
SELECT
    u.user_id,
    u.username,
    u.email,
    u.role::text AS role,
    u.is_active,
    u.created_at,
    COALESCE(b.plan, 'free') AS plan,
    COALESCE(b.messages_used, 0)::bigint AS messages_used,
    COALESCE(b.monthly_message_quota, 500)::bigint AS message_quota,
    COALESCE(b.docs_used, 0)::bigint AS docs_used,
    COALESCE(b.monthly_doc_quota, 20)::bigint AS doc_quota,
    (SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.user_id) AS total_sessions,
    (SELECT COUNT(*) FROM chat_messages m JOIN sessions s ON s.session_id = m.session_id
        WHERE s.user_id = u.user_id) AS total_messages,
    (SELECT COUNT(*) FROM knowledge_documents k WHERE k.uploaded_by = u.user_id) AS total_documents
FROM users u
LEFT JOIN tenant_billing b ON b.user_id = u.user_id;
