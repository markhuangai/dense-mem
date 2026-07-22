# Migration 2026071909: V2 Migration Control Plane

## Scope

Added PostgreSQL state for the V2 legacy-corpus migration protocol in the
`v2.1.1` migration release line:

- migration runs, corpus items, source maps, checkpoints, errors, exclusions,
  gate results, and operator actions
- compatibility and cutover markers
- a private control-portal API for status, backup confirmation, start, pause,
  and resume commands during the forced migration release
- guided migration and cleanup control-portal modes in the `v2.1.1` release
  line

The original migration did not read legacy corpus rows, execute the official
migration, write a cutover marker, or switch active remember/recall/trace
authority. Issue #95 removed the runtime migration executor and control routes;
normal boot no longer links the legacy graph adapter.

## Boot Boundary

The historical PR that introduced this migration left production behavior
v1-active. After #94 and #95, normal boot requires a compatible cutover marker
and uses PostgreSQL V2 authority only.

## Private Controls

In the `v2.1.1` migration release, the control API was registered only under
the existing token-protected private control listener:

```text
GET  /control/api/v2/migration
POST /control/api/v2/migration/preflight
POST /control/api/v2/migration/start
POST /control/api/v2/migration/pause
POST /control/api/v2/migration/resume
```

In the latest `v2.1.1` migration release, preflight approval required one
operator confirmation that recoverable artifacts already existed for both
databases:

```json
{
  "backups_confirmed": true,
  "reason": "operator confirmed external database backups"
}
```

Dense-Mem did not create, inspect, verify, restore, or record references to
these backups. The run stored only safe confirmation metadata:

- `operator_backup_confirmation=true`
- `postgres_backup_confirmed=true`
- `neo4j_backup_confirmed=true`
- `confirmation_scope=operator`
- `backup_verification=not_performed`

The active migration-release contract was
`dense-mem.v2.1.migration-control.v3`. Existing
`dense-mem.v2.1.migration-control.v1` and
`dense-mem.v2.1.migration-control.v2` runs were not auto-executed by the
supervisor; operators had to renew backup confirmation through the guided UI
before migration could resume.

Operator actions were persisted in PostgreSQL with bounded metadata and no
credential fields.

After #95, these routes and the migration portal are no longer registered.
Operators with legacy Neo4j configuration must run the latest `v2.1.1` release
to complete migration before upgrading to the cleanup release.

Historical `v2.1.1-rc.12` operator sequence:

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

## RLS And Isolation Impact

The new control tables are forced-RLS tables. Policies allow only `system` and
`migration` transaction modes; request/profile modes cannot select migration
state directly. Public MCP and browser routes do not receive a migration mode
selector.

Redis and process memory are not canonical for the control plane. During the
migration release, restarted processes reconstructed state from PostgreSQL runs,
checkpoints, operator actions, and markers. After #95, normal application
processes only use the compatible cutover marker as boot evidence.

## Rollback Boundary

Backup confirmation records that an operator confirmed PostgreSQL and Neo4j
backups already existed outside Dense-Mem. It does not prove the backups exist,
that Dense-Mem created them, or that either artifact was restore-tested. Gate
output and portal copy in the migration release kept that weaker rollback
assurance visible.

The historical down migration drops migration-control tables and markers. After
production migration progress exists, rollback must be treated as an operator
decision because checkpoint, error, exclusion, gate, and audit history would be
lost. Normal application code does not delete accepted evidence or semantic
audit lineage as part of rollback.

## Verification

Historical focused checks for the `v2.1.1` migration release:

```bash
go test ./cmd/server/... ./internal/config/... ./internal/repository/... ./internal/http/... -count=1
./scripts/ci-check.sh
```

After #95, use the cleanup branch validation instead: run the repository checks,
verify no migration control routes are registered, and boot a marked database
without any legacy Neo4j configuration.
