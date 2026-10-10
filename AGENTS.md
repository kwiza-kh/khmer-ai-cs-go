# AGENTS.md — working notes for an AI agent (or a new human) in this repo

The project is **RelayChat** (customer-service platform): a Go backend
(`backend-go/`), a Next.js frontend (`frontend/`), PostgreSQL 17 + pgvector and
Redis. Production runs on one host behind Cloudflare — see
`deploy-khmer-ai-cs/SKILL.md` before touching anything that ships.

## The two rules that bite people

1. **Migrations are a forward-only, mirrored set.** A new file goes in *both*
   `backend-go/internal/migrations/migrations/` and `backend-go/migrations/`;
   `go test ./internal/migrations/` asserts the two directories match and that
   every file is a `.sql` with a numeric prefix. There is no down migration.
2. **SQL the compiler cannot see.** `internal/sqlcheck` prepares every SQL string
   in the Go source against a live schema (`PREPARE` only, inside a rolled-back
   transaction, so pointing it at production is safe). It *skips* when
   `DATABASE_URL` is unset — so run it with `SQLCHECK_REQUIRED=1` in any release
   step, or the gate silently passes. Two production incidents came from exactly
   this class of bug (`message_quota`, `sla_priority`).

## Commands

```bash
cd backend-go
go build ./... && go vet ./... && go test ./...
gofmt -l internal cmd                     # the working tree is CRLF; check a LF copy
set -a; . ./.env-go; set +a; SQLCHECK_REQUIRED=1 go test ./internal/sqlcheck/
```

```bash
cd frontend
npm run build            # NEXT_PUBLIC_API_URL must be set: it is baked into the bundle
```

## Layout worth knowing

| Path | What lives there |
|---|---|
| `backend-go/internal/platform` | channels: `capabilities.go` (what a channel *is*), `channel*.go` (what it *does*), `inbound_stages.go` (one stage per decision), `webhooks.go` + `webhook_routes.go` |
| `backend-go/internal/gemini` | the model client: provider failover, call budgets, history shape |
| `backend-go/internal/llm` | generation provider router: the default row's `provider` decides Gemini, Claude or DeepSeek (§二十 / §二十二) |
| `backend-go/internal/anthropic` | the Claude client (Anthropic API) and its verified model catalog |
| `backend-go/internal/deepseek` | the DeepSeek client (OpenAI-format chat completions) and its verified model catalog |
| `backend-go/internal/scheduler` | DB-backed jobs (migration 066) |
| `backend-go/internal/persona` | persona resolution, most specific binding wins |
| `backend-go/internal/api/member_permissions.go` | seat permissions: the owner→member grant matrix, the `CurrentUser.Tenant()/Can()` resolution, and its owner-only write endpoint |
| `backend-go/internal/sqlcheck` | the SQL gate described above |

## Conventions this codebase actually follows

- **A decision belongs in data, not in a `switch`.** Channel differences live in
  the capability table; a missing capability is a bug, not a branch.
- **A seat is not a tenant.** An active `agent_teams` row is resolved (middleware)
  to the owner's tenant plus an explicit permission set: tenant-data handlers
  filter on `user.Tenant()` and gate with `requirePermission`. A surface that is
  not wired for permissions stays `tenantAdminOnly` — a member gets a clean 403,
  never an empty page or a write that lands under their own user_id.
- **A failure that a customer would feel must be either retried or recorded.**
  Several comments in this tree exist because one of those was missing.
- **Fail open on auxiliary paths, fail loud on the money path.** Moderation and
  the reply guard skip a broken strategy; a broken credential stops the turn.
- **Tests run or they do not exist.** `go test ./...` must be green before a
  push; new behaviour ships with a test that would fail without it.
- **Tenant data is bound in the statement, or under a funnel.** Every query
  that touches a tenant table names its tenant column (`user_id`,
  `uploaded_by`, `owner_user_id`, …) or runs after `ensureSessionAccess` /
  `ensureConfigOwner`. `internal/tenantscope` enforces that statically (and
  needs no database, so it cannot skip the way the `DATABASE_URL`-gated
  isolation tests do); reviewed exceptions carry their reason in
  `internal/tenantscope/exceptions.go`.
- Comments explain *why*, and name the incident or the measurement when there is
  one. Do not restate the code.

## Deployment

`deploy-khmer-ai-cs/` is the runbook (SSH, cross-compile, upload, migrate,
restart, verify). Ports: backend `:8081`, frontend `:3001`, and `wms.service`
shares the host — do not touch it.
