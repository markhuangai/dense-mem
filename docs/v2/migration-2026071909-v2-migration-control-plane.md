# Migration 2026071909: V2 Migration Control Plane

## Scope

Added PostgreSQL state for the V2 legacy-corpus migration protocol in the
`v2.1.1` migration release line:

- migration runs, corpus items, source maps, checkpoints, errors, exclusions,
  gate results, and operator actions
- compatibility and cutover markers
- a private control-portal API for status and operator approval commands during
  the forced migration release

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

Preflight approval required a backup reference plus verified PostgreSQL restore
and historical source snapshot checks. Operator actions were persisted in
PostgreSQL with bounded metadata and no credential fields.

After #95, these routes and the migration portal are no longer registered.
Operators with legacy Neo4j configuration must run the latest `v2.1.1` release
to complete migration before upgrading to the cleanup release.

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

The historical down migration drops only the dormant migration-control tables
and markers. Before the official migration executor writes production progress,
rollback discards rehearsal/control-plane state only. After production migration
runs exist, rollback must be treated as an operator decision because checkpoint,
error, exclusion, and audit history would be lost.

## Verification

Historical focused checks for the `v2.1.1` migration release:

```bash
go test ./cmd/server/... ./internal/config/... ./internal/repository/... ./internal/http/... -count=1
./scripts/ci-check.sh
```

After #95, use the cleanup branch validation instead: run the repository checks,
verify no migration control routes are registered, and boot a marked database
without any legacy Neo4j configuration.
