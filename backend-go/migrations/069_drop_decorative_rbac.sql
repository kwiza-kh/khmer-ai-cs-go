-- 069: drop the decorative RBAC tables.
--
-- roles / user_roles carried a CRUD surface (GET/POST/DELETE /roles, assign,
-- unassign) and a permission vocabulary in the console, but nothing in the
-- codebase ever read them for an authorization decision — they only looked like
-- access control, which is worse than having none. The real boundaries are
-- agent_teams membership (who belongs to whose tenant) plus the tenant-owner
-- checks in internal/api/tenant_owner.go.
--
-- Both tables were empty in production (0 rows each), so this drops no data. If
-- fine-grained permissions are ever needed, they should be designed as a real
-- matrix (vocabulary validated at write time, every route declaring what it
-- requires, tests pinning the decisions) rather than restored from here.

DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS roles;
