# Migration 2026071909: V2 Migration Control Plane

## Scope

Adds dormant PostgreSQL state for the V2 legacy-corpus migration protocol:

- migration runs, corpus items, source maps, checkpoints, errors, exclusions,
  gate results, and operator actions
- compatibility and cutover markers
- a private control-portal API for status, preflight approval, start, pause,
  and resume commands
- internal control services that remain unwired from normal production boot

This migration does not read Neo4j corpus rows, execute the official migration,
write a cutover marker, or switch active remember/recall/trace authority.

## Boot Boundary

Default production behavior stays v1-active in this PR. The server still
requires Neo4j configuration and does not expose an environment switch that can
activate V2 migration services, V2 evaluation data, or a V2-only UAT runtime.
#94 owns the forced migration boot classifier, migration maintenance mode, and
compatible/fresh marker write.

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
#119 leaves the production server unwired, so these routes exist only as inert
control-plane surface until a later cutover branch injects the services.

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
