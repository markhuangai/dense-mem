# Migration 2026071703: V2 Ledger Foundation

## Scope

Adds dormant V2 PostgreSQL ledger tables for semantic team/profile projections,
knowledge ingests, evidence sources and revisions, immutable evidence
fragments, evidence security history, quarantine state, placement workflow, and
placement outcomes.

The migration does not route active v1 remember, recall, trace, or placement
traffic to these tables.

## Lock And Rewrite Analysis

- Existing tables are not rewritten.
- `2026071702_team_profiles_team_id_id_unique_index.sql` creates the
  `team_profiles(team_id, id)` helper index concurrently and outside a
  transaction so profile writes are not blocked by the index build. If the
  concurrent build fails, PostgreSQL can leave an invalid
  `idx_team_profiles_team_id_id_unique` index behind; drop or reindex that
  invalid index before rerunning the migration. The migration omits
  `IF NOT EXISTS` so retry fails visibly instead of accepting an invalid helper
  index.
- New tables, indexes, policies, and triggers are additive.
- Backfill inserts only non-secret projection IDs from existing `teams` and
  `team_profiles`; it does not modify those authority rows.
- Projection rows cascade when unused authority team/profile rows are deleted.
  Once V2 ledger rows reference a projection, those ledger-to-projection
  references remain restrictive and preserve ledger history.

## RLS Impact

- All new tenant-owned V2 tables have forced RLS.
- Normal profile transactions can read same-team rows but can only insert or
  update rows owned by `app.current_profile_id`.
- Team-mode transactions are reserved for explicit team-local workers.
- System and migration modes are separate RLS modes and are not request
  selectable.

## WAL And Runtime Impact

- Fresh installs create empty V2 tables.
- Upgrades emit WAL for new catalogs, new indexes, and projection backfill rows.
- No V1 runtime table is rewritten, and no V1 runtime query path changes.
- Placement and evidence workers are not started by this migration.

## Rollback

The ledger down migration drops only the V2 ledger objects added here. The
helper-index down migration drops only the composite `team_profiles(team_id, id)`
index. Neither down migration deletes or alters v1 authority data. Rolling back
after writing V2 ledger rows discards those dormant rows, so production rollback
after V2 writes should be treated as a data-loss boundary unless the rows are
exported first.

## Verification

- Run goose up/down on an empty database, including the separate helper-index
  migration.
- Run goose up against a database containing existing teams and team profiles,
  then verify projection counts and absence of auth-row changes.
- Run V2 ledger repository tests with a non-superuser `DATABASE_URL` role to
  prove RLS, owner mutation denial, cross-team non-disclosure, idempotency, and
  source revision compare-and-set behavior.
