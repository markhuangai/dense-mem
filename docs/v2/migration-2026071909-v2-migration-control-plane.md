# Migration 2026071909: V2 Migration Control Plane

## Scope

Adds PostgreSQL state for the V2 legacy-corpus migration protocol:

- migration runs, corpus items, source maps, checkpoints, errors, exclusions,
  gate results, and operator actions
- compatibility and cutover markers
- a private control-portal API for status and operator approval commands

This migration does not read legacy corpus rows, execute the official
migration, write a cutover marker, or switch active remember/recall/trace
authority. Issue #95 removed the runtime migration executor and normal boot no
longer links the legacy graph adapter.

## Boot Boundary

The historical PR that introduced this migration left production behavior
v1-active. After #94 and #95, normal boot requires a compatible cutover marker
and uses PostgreSQL V2 authority only.

## Private Controls

The control API is registered only under the existing token-protected private
control listener:

```text
GET  /control/api/v2/migration
POST /control/api/v2/migration/preflight
POST /control/api/v2/migration/start
POST /control/api/v2/migration/pause
POST /control/api/v2/migration/resume
```

Preflight approval requires a backup reference plus verified PostgreSQL restore
and historical source snapshot checks. Operator actions are persisted in
PostgreSQL with bounded metadata and no credential fields. The previous
`run-once` executor route was removed by #95.

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
go test ./internal/service/migrationcontrol ./internal/http ./internal/repository -count=1
go test ./cmd/server/... ./internal/config/... ./internal/repository/... ./internal/http/... -count=1
./scripts/ci-check.sh
```

PostgreSQL integration checks should be run with a non-superuser
`DATABASE_URL` before production use so migration/system RLS policies are
tested against real database roles.
