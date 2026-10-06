-- 072: per-member permissions.
--
-- 070/071 made a seat real (invite → agent_teams row, seat counting, history),
-- but the data plane never resolved an agent to the owner's tenant: every
-- handler filtered by the CALLER's user_id, so an invited member saw an empty
-- inbox, an empty knowledge base and no channels — and could meanwhile create
-- documents/channels under their own id that the owner would never see.
--
-- Wiring the tenant data plane without a permission model would be worse than
-- leaving it: every member would inherit full tenant powers. So the two ship
-- together: agent_teams.permissions holds the owner's explicit grants for one
-- seat, and the middleware resolves (tenant, permissions) per request.
--
-- '{}' means "no explicit grants" — handlers fall back to the per-key defaults
-- in member_permissions.go, so a seat added before this migration keeps working.
-- Unknown keys are rejected at write time; unknown keys found here are ignored.
ALTER TABLE agent_teams
    ADD COLUMN IF NOT EXISTS permissions JSONB NOT NULL DEFAULT '{}'::jsonb;
