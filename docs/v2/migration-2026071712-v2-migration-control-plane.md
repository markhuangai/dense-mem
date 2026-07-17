# Migration 2026071712: V2 Migration Control Plane

## Scope

Adds dormant PostgreSQL state for the V2 legacy-corpus migration protocol:

- migration runs, corpus items, source maps, checkpoints, errors, exclusions,
  gate results, and operator actions
- compatibility and cutover markers
- a private control-portal API for status, preflight approval, start, pause,
  and resume commands
- a boot readiness check gated by `V2_LEGACY_MIGRATION_REQUIRED`

This migration does not read Neo4j corpus rows, execute the official migration,
write a cutover marker, or switch active remember/recall/trace authority.

## Boot Gate

Default production behavior stays v1-active. `V2_LEGACY_MIGRATION_REQUIRED`
defaults to `false`, so the new migration readiness check is optional unless an
operator explicitly enables the legacy migration boot gate.

When `V2_LEGACY_MIGRATION_REQUIRED=true`, dormant V2 readiness fails until the
migration control service reports either `not_required` or `cut_over`. This
models the wiki maintenance contract without putting existing v1 deployments
into maintenance by default.

## Private Controls

The control API is registered only under the existing token-protected private
control listener:

```text
GET  /control/api/v2/migration
POST /control/api/v2/migration/preflight
POST /control/api/v2/migration/start
POST /control/api/v2/migration/pause
POST /control/api/v2/migration/resume
POST /control/api/v2/migration/run-once
```

Preflight approval requires a backup reference plus verified PostgreSQL restore
and Neo4j snapshot checks. Operator actions are persisted in PostgreSQL with
bounded metadata and no credential fields.

`run-once` is available only when a migration executor service is explicitly
injected. If the executor is not wired, the private route returns service
unavailable instead of starting implicit migration work during normal boot.

## RLS And Isolation Impact

The new control tables are forced-RLS tables. Policies allow only `system` and
`migration` transaction modes; request/profile modes cannot select migration
state directly. Public MCP and browser routes do not receive a migration mode
selector.

Redis and process memory are not canonical for the control plane. Restarted
processes reconstruct state from PostgreSQL runs, checkpoints, operator
actions, and markers.

## Rollback Boundary

The down migration drops only the dormant migration-control tables and markers.
Before the official migration executor writes production progress, rollback
discards rehearsal/control-plane state only. After production migration runs
exist, rollback must be treated as an operator decision because checkpoint,
error, exclusion, and audit history would be lost.

## Verification

Focused checks:

```bash
go test ./internal/service/migrationcontrol ./internal/config ./cmd/server ./internal/http ./internal/repository -count=1
go test ./cmd/server/... ./internal/config/... ./internal/repository/... ./internal/http/... -count=1
./scripts/ci-check.sh
```

PostgreSQL integration checks should be run with a non-superuser
`DATABASE_URL` before production use so migration/system RLS policies are
tested against real database roles.
