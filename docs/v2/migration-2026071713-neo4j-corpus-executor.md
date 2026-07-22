# Migration 2026071713: Read-Only Neo4j Corpus Executor

## Scope

Adds the first migration executor unit for the V2 legacy-corpus migration:

- a read-only Neo4j `SourceFragment` adapter that must be explicitly enabled
- bounded cursor pagination over non-retracted legacy fragments
- durable PostgreSQL corpus item, source map, checkpoint, error, and exclusion
  writes
- a migration executor service that submits each valid legacy source fragment
  through `RememberV2`
- an internal bounded-page executor called by the migration supervisor

The executor does not write Neo4j, create Neo4j indexes, run GDS/APOC, dual-write
normal requests, or add Neo4j fallback reads to V2 recall, trace, search, or
evaluation paths.

## Architecture Contract

The target architecture treats PostgreSQL with pgvector as the only durable
authority. Neo4j is migration input only. This implementation follows that
contract by reading legacy fragments through `ExecuteRead`, converting them to
V2 remember evidence, and recording all progress in PostgreSQL migration tables.

Legacy claims and facts are carried only as proposal hints and legacy metadata.
They do not directly copy tier, lifecycle state, active edges, or semantic graph
authority into V2. The normal V2 reviewer/verifier/lifecycle pipeline remains
responsible for durable semantic state.

## Ownership And Isolation

Each submitted fragment gets a fresh request context built from the legacy
fragment's original `team_id` and `owner_profile_id` only after PostgreSQL
confirms that exact owner profile belongs to the team. Mismatches are recorded
as cutover-blocking exclusions before any V2 actor context or remember request
is created. The operator/control caller cannot choose the migrated team or
owner through request payload, headers, or control-route identity.

Migration bookkeeping uses PostgreSQL `migration` transaction mode when the RLS
helper exposes it. Normal profile and team transactions cannot read or mutate
the migration progress tables.

## Resumability

The executor is intentionally one page at a time. In current production wiring,
the background migration supervisor calls it under a PostgreSQL advisory leader
lock; operators do not call it through the control API.

For each page it:

1. loads the latest running migration and cursor checkpoint
2. reads a bounded Neo4j page
3. upserts a pending corpus item before `RememberV2`
4. records the ingest source map and submission processing state
5. records exclusions and typed errors for invalid or failed items
6. advances the cursor checkpoint after the page

Retry upserts preserve existing non-pending outcomes, so a restarted executor
skips items already submitted or excluded instead of duplicating work.

Items inside a page are processed sequentially. The supervisor advances through
additional bounded pages only after durable outcomes and checkpoints are
visible, which avoids concurrent owner-scope, provider, and RLS pressure.

`RunOnce` may return page counters with an error after processing has started.
Those counters describe work reached by that call; durable corpus outcomes and
the last committed checkpoint are authoritative for operator inspection and
retry. The private control route returns the error rather than presenting the
partial counters as a successful page.

## Wiring Boundary

Forced migration boot constructs the executor only for migration maintenance
mode. The private control portal exposes status, preflight, start, pause, and
resume; it does not register `run-once` or manual `cutover` routes. Fresh V2
boot and post-cutover active V2 boot do not read Neo4j or keep a fallback
reader.

Each submission preserves the original team/profile owner and carries a typed
internal migration actor derived from the durable migration run ID. Migration
does not require or impersonate a team-scoped API credential.

## Verification

Focused checks:

```bash
go test ./internal/storage/neo4j -run 'TestLegacyCorpus|TestNewLegacyCorpus' -count=1
go test ./internal/service/migrationexecutor -run TestRunOnce -count=1
go test ./internal/repository -run TestV2MigrationExecutorRepositoryPersistsProgressAndStats -count=1
go test ./internal/service/migrationsupervisor -count=1
go test ./internal/http -run TestControlPortalV2Migration -count=1
```

Before production migration, run the repository checks against a real
non-superuser PostgreSQL role so forced RLS policies are exercised outside the
unit-test fixture.
