-- Tenant isolation backstop (Row-Level Security) for the two tables that hold
-- per-customer content without an owner column of their own.
--
-- WHY ONLY THESE TWO
-- chat_messages and knowledge_chunks are the only tenant-content tables whose
-- scoping requires a JOIN (to sessions / knowledge_documents) instead of a
-- plain WHERE user_id column. Missing that join in one query is exactly the
-- cross-tenant leak class this project has shipped before — the 2026-09-12
-- audit found five live holes of this shape. Every other tenant table carries
-- user_id directly, where the omission is visible in the same statement.
--
-- HOW TENANCY IS DECIDED
-- The session GUC `app.user_id`, read through app_tenant_id() (defined below).
-- No Go code sets it yet — the wiring plan lives in docs/DEVELOPMENT.md 「十」 —
-- so the policies are deliberately FAIL-OPEN while it is unset:
--
--   * the deploy flow runs migrate-go BEFORE the new binary starts, and
--     rolling the binary back to a GUC-unaware build must keep working;
--     a fail-closed policy would break every query of the old binary;
--   * the platform pipeline and the background workers are cross-tenant by
--     design and must keep seeing everything.
--
-- From the first transaction that sets the GUC, the policy flips from no-op
-- to enforced: every SELECT/UPDATE/DELETE and INSERT on these two tables is
-- constrained to rows whose session/document belongs to that user — even in
-- a query that forgot its WHERE clause.
--
-- ⚠️ WHY app_tenant_id() MUST STAY VOLATILE — do not inline current_setting
-- back into the policies, and do not mark the function STABLE:
--
-- Under the extended protocol (everything the app runs — pgx statement
-- caching), the planner hoists subplan expressions that do not reference the
-- outer row into InitPlans evaluated once at executor start. Inlining
-- `current_setting('app.user_id', true)::int` inside the policy's EXISTS put
-- that cast in an InitPlan: with the GUC in its post-transaction reset state
-- ('' — NOT NULL; a pooled connection that served one tenant transaction
-- reads '' forever after) the whole table errored with
-- `invalid input syntax for type integer: ""` before the OR's short-circuit
-- could fire. psql (simple protocol, custom plans) hides the bug completely,
-- which is why it survived three rounds of manual verification.
-- VOLATILE forbids folding and InitPlan hoisting: the function is called per
-- row, after the short-circuit, so unset means the EXISTS is never evaluated.
-- Cost when unset: one function call per row (~sub-µs); when set, the EXISTS
-- rides idx_messages_session / idx_knowledge_chunks_doc, and every existing
-- access path to these tables is already session- or doc-scoped.
--
-- KNOWN LIMITS (accepted, not oversold)
--   * superusers and roles with BYPASSRLS bypass row security
--     unconditionally. cmd/server logs a warning at startup when connected
--     as one; connect as the dedicated non-superuser role for this to apply.
--   * TRUNCATE is not policy-governed (nothing truncates these tables).
--   * fail-open means a code path that deliberately avoids the GUC behaves
--     exactly as before 061. This is the scaffolding for enforcement, not
--     enforcement yet.
--   * the VOLATILE qual is parallel-unsafe by default: full-scan analytical
--     queries against these two tables will not parallelize below it. The
--     current access paths are point/small scans, so this is acceptable.
--
-- ROLLBACK SAFETY
-- Pure policy DDL plus two helper functions. An older binary never sets the
-- GUC, so it always takes the fail-open branch and sees today's behavior —
-- downgrading stays a supported operation. CASCADE deletes via FK actions are
-- RI-triggered and bypass row security, so deletion flows are unaffected.

-- Reads the requesting tenant for RLS policies. NULL = "no tenant context in
-- this transaction" (GUC unset, or reset by a finished SET LOCAL — the '' vs
-- NULL distinction is exactly why the NULLIF lives here and callers never
-- cast the raw setting). VOLATILE is load-bearing, see above.
CREATE FUNCTION app_tenant_id() RETURNS int
    LANGUAGE sql VOLATILE
    RETURN NULLIF(current_setting('app.user_id', true), '')::int;

-- Wiring helper for the future tenant-scoped executor: transaction-local by
-- construction (is_local = true). NEVER set the GUC with is_local = false on
-- a pooled connection — the identity would leak into the next borrower.
CREATE FUNCTION app_set_tenant(pid int) RETURNS void
    LANGUAGE sql VOLATILE
    RETURN set_config('app.user_id', pid::text, true);

ALTER TABLE chat_messages ENABLE ROW LEVEL SECURITY;
-- The connecting role is the table owner; without FORCE the owner would
-- silently bypass every policy below.
ALTER TABLE chat_messages FORCE ROW LEVEL SECURITY;

CREATE POLICY chat_messages_tenant_isolation ON chat_messages
    USING (
        app_tenant_id() IS NULL
        OR EXISTS (
            SELECT 1 FROM sessions s
            WHERE s.session_id = chat_messages.session_id
              AND s.user_id = app_tenant_id()
        )
    )
    WITH CHECK (
        app_tenant_id() IS NULL
        OR EXISTS (
            SELECT 1 FROM sessions s
            WHERE s.session_id = chat_messages.session_id
              AND s.user_id = app_tenant_id()
        )
    );

ALTER TABLE knowledge_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_chunks FORCE ROW LEVEL SECURITY;

CREATE POLICY knowledge_chunks_tenant_isolation ON knowledge_chunks
    USING (
        app_tenant_id() IS NULL
        OR EXISTS (
            SELECT 1 FROM knowledge_documents d
            WHERE d.doc_id = knowledge_chunks.doc_id
              AND d.uploaded_by = app_tenant_id()
        )
    )
    WITH CHECK (
        app_tenant_id() IS NULL
        OR EXISTS (
            SELECT 1 FROM knowledge_documents d
            WHERE d.doc_id = knowledge_chunks.doc_id
              AND d.uploaded_by = app_tenant_id()
        )
    );
