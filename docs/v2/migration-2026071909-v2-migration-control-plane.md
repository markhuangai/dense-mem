# Migration 2026071909: V2 Migration Control Plane

## Scope

Adds PostgreSQL state for the V2 legacy-corpus migration protocol:

- migration runs, corpus items, source maps, checkpoints, errors, exclusions,
  gate results, and operator actions
- compatibility and cutover markers
- a private control-portal API for status, backup confirmation, start, pause,
  and resume commands
- guided migration and cleanup control-portal modes

As of `v2.1.1-rc.12`, forced legacy migration production boot wires this
control plane with a background supervisor. The supervisor executes migration
pages internally, evaluates hard gates, commits the compatible marker, and
requests a same-binary restart. Operators no longer call a manual page runner
or a manual cutover API.

## Boot Boundary

Production boot is marker-driven:

| State | Boot behavior |
|-------|---------------|
| Compatible V2 marker exists | Main MCP/API data plane starts on PostgreSQL V2. |
| No marker and any `NEO4J_*` config exists | Main data plane stays closed with HTTP 503 and the private control portal enters migration mode. |
| No marker, no `NEO4J_*`, and PostgreSQL has no existing data | Startup writes a fresh compatible marker and starts PostgreSQL V2. |
| No marker, no `NEO4J_*`, and PostgreSQL is nonempty | Startup fails closed. |

Migration mode requires a complete Neo4j source configuration:
`NEO4J_URI`, `NEO4J_USER`, and `NEO4J_PASSWORD`. After the compatible marker is
committed and the process reexecs, partial or stale `NEO4J_*` settings are
allowed only so the control portal can show cleanup instructions while MCP
continues serving from PostgreSQL V2.

## Private Controls

In migration mode the control API registers only the token-protected migration
surface:

```text
GET  /control/api/v2/migration
POST /control/api/v2/migration/preflight
POST /control/api/v2/migration/start
POST /control/api/v2/migration/pause
POST /control/api/v2/migration/resume
```

`POST /control/api/v2/migration/run-once` and
`POST /control/api/v2/migration/cutover` are intentionally not registered.
The supervisor owns bounded page execution and cutover.

Backup confirmation requires one operator confirmation that recoverable
artifacts exist for both databases:

```json
{
  "backups_confirmed": true,
  "reason": "operator confirmed external database backups"
}
```

Dense-Mem does not create, inspect, verify, restore, or record references to
these backups. The run stores only safe confirmation metadata:

- `operator_backup_confirmation=true`
- `postgres_backup_confirmed=true`
- `neo4j_backup_confirmed=true`
- `confirmation_scope=operator`
- `backup_verification=not_performed`

The active contract is `dense-mem.v2.1.migration-control.v3`. Existing
`dense-mem.v2.1.migration-control.v1` and
`dense-mem.v2.1.migration-control.v2` runs are not auto-executed by the
supervisor. The operator must renew backup confirmation through the guided UI,
which upgrades the run to v3 and returns it to `ready` before migration can
resume.

## Operator Sequence

```text
Deploy rc.12 with complete Neo4j source config
  -> /mcp returns 503 and private portal shows only migration mode
  -> operator confirms PostgreSQL and Neo4j backups already exist
  -> operator clicks Confirm and start migration
  -> supervisor runs pages with bounded retries
  -> supervisor evaluates hard gates
  -> supervisor commits compatible marker
  -> process gracefully shuts down and reexecs
  -> MCP/API starts on PostgreSQL V2
  -> cleanup portal appears while any NEO4J_* setting remains
  -> operator removes legacy NEO4J_* env/service/network and recreates deploy
  -> normal control portal appears
```

Transient page failures use the bounded retry sequence
`2s, 5s, 15s, 30s, 60s`. If retries are exhausted, the run moves to
`paused_retryable`; the operator can inspect the error and click Resume.

## RLS And Isolation Impact

The new control tables are forced-RLS tables. Policies allow only `system` and
`migration` transaction modes; request/profile modes cannot select migration
state directly. Public MCP and browser routes do not receive a migration mode
selector.

Redis and process memory are not canonical for the control plane. Restarted
processes reconstruct state from PostgreSQL runs, checkpoints, operator
actions, and markers.

## Rollback Boundary

Backup confirmation records that an operator confirmed PostgreSQL and Neo4j
backups already exist outside Dense-Mem. It does not prove the backups exist,
that Dense-Mem created them, or that either artifact was restore-tested. Gate
output and portal copy must keep that weaker rollback assurance visible.

The down migration drops migration-control tables and markers. After production
migration progress exists, rollback is an operator decision because checkpoint,
error, exclusion, gate, and audit history would be lost. Normal application
code does not delete accepted evidence or semantic audit lineage as part of
rollback.

## Verification

Focused checks:

```bash
go test ./internal/service/migrationcontrol ./internal/service/migrationsupervisor ./internal/http -count=1
go test ./cmd/server/... ./internal/config/... ./internal/repository/... -count=1
npm test --prefix web -- App.test.tsx api.test.ts
./scripts/ci-check.sh
```

PostgreSQL integration checks should be run with a non-superuser
`DATABASE_URL` before production use so migration/system RLS policies are
tested against real database roles.
