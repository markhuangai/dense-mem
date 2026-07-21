# Migration 2026071713: Read-Only Neo4j Corpus Executor

## Scope

Adds the first migration executor unit for the V2 legacy-corpus migration:

- a read-only Neo4j `SourceFragment` adapter that must be explicitly enabled
- bounded cursor pagination over non-retracted legacy fragments
- durable PostgreSQL corpus item, source map, checkpoint, error, and exclusion
  writes
- a migration executor service that submits each valid legacy source fragment
  through `RememberV2`
- a private `POST /control/api/v2/migration/run-once` control action for one
  bounded page of migration work

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
fragment's original `team_id` and `owner_profile_id`. The operator/control
caller cannot choose the migrated team or owner through request payload,
headers, or control-route identity.

Migration bookkeeping uses PostgreSQL `migration` transaction mode when the RLS
helper exposes it. Normal profile and team transactions cannot read or mutate
the migration progress tables.

## Resumability

The executor is intentionally one page at a time:

```text
POST /control/api/v2/migration/run-once
```

For each page it:

1. loads the latest running migration and cursor checkpoint
2. reads a bounded Neo4j page
3. upserts a pending corpus item before `RememberV2`
4. records the ingest source map and submission processing state
5. records exclusions and typed errors for invalid or failed items
6. advances the cursor checkpoint after the page

Retry upserts preserve existing non-pending outcomes, so a restarted executor skips
items already submitted or excluded instead of duplicating work.

## Wiring Boundary

Main boot constructs the executor only when `V2_BOOT_MODE=dormant` and
`V2_LEGACY_MIGRATION_REQUIRED=true`. The token-protected private control portal
authorizes every action; one `run-once` call performs one bounded page and no
implicit migration worker starts during normal boot.

Each submission preserves the original team/profile owner and carries a typed
internal migration actor derived from the durable migration run ID. Migration
does not require or impersonate a team-scoped API credential.

## Verification

Focused checks:

```bash
go test ./internal/storage/neo4j -run 'TestLegacyCorpus|TestNewLegacyCorpus' -count=1
go test ./internal/service/migrationexecutor -run TestRunOnce -count=1
go test ./internal/repository -run TestV2MigrationExecutorRepositoryPersistsProgressAndStats -count=1
go test ./internal/http -run TestControlPortalV2Migration -count=1
```

Before production migration, run the repository checks against a real
non-superuser PostgreSQL role so forced RLS policies are exercised outside the
unit-test fixture.
