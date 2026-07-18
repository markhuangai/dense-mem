# Migration 2026071704: V2 Semantic Ledger

## Scope

Adds the dormant V2 semantic identity and lifecycle ledger:

- versioned `predicate_definitions`
- `entity_records`, `entity_names`, and deterministic `value_records`
- profile-owned `relationship_records`
- append-only observation, verification, support, support-decision, transition,
  resolution, correction, and cross-reference history
- `review_tasks`, `hypotheses`, and the `semantic_edges` read view

The active V1 runtime remains unchanged. This migration only prepares the V2
PostgreSQL authority required by issue #79.

## Lock And Rewrite Analysis

The migration creates new tables, indexes, triggers, policies, and one view. It
does not rewrite existing tenant data. The existing V2 ledger tables touched are
`evidence_sources`, `evidence_fragments`, and `placement_items`.

`evidence_sources` and `evidence_fragments` receive canonical authority checks
for:

```text
authoritative, primary, secondary, inferred, unknown
```

Before changing those checks, the migration scans both tables and aborts with
per-table authority counts if any noncanonical value is present. It does not
rewrite legacy values such as `derived`.

The authority checks are changed with `DROP CONSTRAINT`, `ADD CONSTRAINT ... NOT
VALID`, then `VALIDATE CONSTRAINT`. `NOT VALID` avoids a table rewrite during the
add step. Validation still scans `evidence_sources` and `evidence_fragments`, and
the drop/add steps still take short `ALTER TABLE` locks. Schedule this migration
with that scan and lock profile in mind if dormant V2 evidence tables are already
large.

`evidence_fragments` and `placement_items` also receive owner-aware unique
constraints to support semantic foreign keys. The only active-runtime table
touched is `app_config` for `update_time`.

The seeded predicate definitions are small shared reference rows. Updates and
deletes are blocked by a trigger; future predicate meaning changes must use new
versions or a reviewed migration.

## RLS And Isolation Impact

All tenant-owned semantic tables carry non-null `team_id` and composite
tenant-local foreign keys. Forced RLS allows same-team reads while constraining
ordinary profile writes to `owner_profile_id` or `author_profile_id` where
ownership applies.

`semantic_edges` is a `security_invoker` view over `relationship_records`, so
it inherits the caller's RLS context and exposes only:

```sql
status = 'active'
AND tier IN ('validated_claim', 'fact')
```

Candidates, hypotheses, observations, verifier rationale, reviews, and
transition history are excluded from the active edge view.

## Rollback Boundary

The down migration first checks `evidence_sources` and `evidence_fragments` for
authority values unsupported by 1703:

```text
primary, secondary, derived
```

If canonical-only values such as `authoritative`, `inferred`, or `unknown` are
present, rollback aborts before dropping V2 semantic tables. If the guard passes,
the down migration drops the V2 semantic view and tables, then restores the 1703
authority checks with `NOT VALID` plus validation.

Dropping the semantic tables is destructive for V2 semantic data. This is
acceptable only before V2 semantic writes are used as production authority. After
V2 semantic writes are enabled, rollback must use a forward migration or an
explicit export/import recovery plan.

## Verification

Focused checks used for this migration:

```bash
go test ./internal/domain ./internal/repository ./internal/tools/registry -count=1
DATABASE_URL="postgres://testuser:testpass@127.0.0.1:55432/testdb?sslmode=disable" \
  go test -tags integration ./internal/storage/postgres \
  -run 'TestMigratorRunUp|TestMigratorRunDown|TestV2SemanticLedgerMigration' -count=1
DATABASE_URL="postgres://testuser:testpass@127.0.0.1:55432/testdb?sslmode=disable" \
  go test ./internal/repository -run 'TestV2Ledger|TestV2Semantic' -count=1
```
