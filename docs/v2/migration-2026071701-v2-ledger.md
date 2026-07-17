# Migration 2026071701: V2 Ledger Foundation

## Scope

Adds dormant V2 PostgreSQL ledger tables for semantic team/profile projections,
knowledge ingests, evidence sources and revisions, immutable evidence
fragments, evidence security history, quarantine state, placement workflow, and
placement outcomes.

The migration does not route active v1 remember, recall, trace, or placement
traffic to these tables.

## Lock And Rewrite Analysis

- Existing tables are not rewritten.
- The only existing-table change is a unique index on
  `team_profiles(team_id, id)` so V2 projection rows can use composite
  team/profile foreign keys.
- New tables, indexes, policies, and triggers are additive.
- Backfill inserts only non-secret projection IDs from existing `teams` and
  `team_profiles`; it does not modify those authority rows.

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

The down migration drops only the V2 ledger objects added here and the composite
`team_profiles(team_id, id)` helper index. It does not delete or alter v1
authority data. Rolling back after writing V2 ledger rows discards those dormant
rows, so production rollback after V2 writes should be treated as a data-loss
boundary unless the rows are exported first.

## Verification

- Run goose up/down on an empty database.
- Run goose up against a database containing existing teams and team profiles,
  then verify projection counts and absence of auth-row changes.
- Run V2 ledger repository tests with a non-superuser `DATABASE_URL` role to
  prove RLS, owner mutation denial, cross-team non-disclosure, idempotency, and
  source revision compare-and-set behavior.
