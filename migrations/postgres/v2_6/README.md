# v2.6 migrations

The v2.6 migration history is one ordered Goose stream across the release
directories. Existing migration files remain immutable; follow-up schema
changes use a new ordered migration and validate the current catalog before
changing it.

`20260913010001_detach_migration_control.sql` is the release boundary for
issue #278. It preserves the eight `v2_migration_*` control tables and every
`v2_compatibility_markers` row, but removes retired lineage references from
the active schema. The migration has no automatic rollback: restore a verified
pre-detachment backup and schema, or use a separately reviewed forward recovery
migration after all application nodes are stopped.

Issue #279 is a separate destructive retirement. It may drop the eight control
tables only after this detached release is deployed to every node, the release
has passed its current-main rehearsal, a backup/restore rehearsal is verified,
and the coordinated-stop and catalog preflight gates succeed. A closed issue or
an applied Goose version does not satisfy those operational gates.

Production execution also requires a separate recorded authorization before
the replacement release starts. The authorization names the approved commit,
the detached-release deployment receipt, the current-main rehearsal and
backup/restore evidence, and the planned coordinated stop. The migration
requires that approval in the latest `v2_migration_runs` row, a non-empty
`backup_reference`, passing evidence for the detached-release, current-main,
backup/restore, coordinated-stop, and catalog-preflight gates, and a recorded
`retire_migration_control` operator action. Without those records, Goose fails
before dropping a table.

Ordinary server startup deliberately does not execute this destructive
boundary. The maintenance process first runs the normal ordered migrations,
stages the preflight records through the authorized table-owner/system path
while the retained audit tables are still writable, and then invokes the
explicit migration-control retirement runner. The retirement version remains
pending in normal startup until that maintenance action succeeds.

The production image exposes the maintenance action through the server binary.
With every application instance stopped, run `/app/server
migration-control-retirement` once from the deployed image (for example,
`docker compose run --rm --no-deps server /app/server
migration-control-retirement`). The command first applies pending ordered
runtime migrations, then uses the recorded preflight and operator evidence for
the destructive boundary.

After stopping every API, worker, and demo instance that shares the database,
the operator runs the ordered Goose stream once from the authorized
maintenance process while all instances remain stopped. A rolling
`docker compose up` is not sufficient. The operator records the eight table
definitions and row counts, the compatibility marker identifiers and contents,
and the preserved catalog before the stop. After the replacement starts,
verify that the migration completed, all eight tables and only their owned
objects are absent, the marker and canonical tables are unchanged, and health,
Remember, recall, trace, workers, and RLS checks pass.

The former `repository/TestAuthorityRepositoryRetainsMigrationAuditReadOnly`
baseline case belonged to #278's retained-table boundary. #279 retires that
symbol with the tables; its surviving operation-log restart regression is
registered as `repository/TestAuthorityRepositoryOperationLogRetentionSurvivesRepositoryRestart`.

Recovery is a verified backup restoration or a separately reviewed forward
recovery migration after all instances are stopped. Recreating empty tables
cannot restore discarded migration evidence and is not a rollback.

`20261011125000_ontology_community_projections.sql` adds resumable derived
topic projections without canonical backfill or history rewriting.
Dependency expansion shares a 100-row turn budget and persists its own cursor;
dependency-only turns retain the membership cursor and publication fingerprint.
Team reads may select only the nonsecret ontology mode flag from app_config; configuration
writes and all other settings remain system-only. New tables
use forced RLS, team/shared-space constraints, and generation fences. Small
source counters make publication and current reads reject changed dependencies;
canonical triggers never expand memberships. Adding the nullable topic column
and replacing the status constraint require a brief table lock with a five-second
lock-wait timeout. Constraint validation scans existing records in a separate
transaction that permits community reads and writes. The scoped current
logical-ID index is built concurrently before the stronger legacy index is
dropped concurrently; no historical rows are rewritten. A failed concurrent
index build requires separately authorized removal of its invalid derived index
before retrying. Down refuses any topic projection history, including incomplete
builds. Production recovery preserves that history and rolls forward; an empty
test installation can use Down.

## Ontology budget retirement (#617)

`20261023020000_ontology_budget_retirement.sql` requires old application instances
to be paused or disabled, drained, and stopped. Running batches, live preparation
or projection leases, and unreconciled attempts block the migration. Recover
unfinished work before retrying; a timeout with durably recorded unavailable
usage is terminal and does not require inventing usage or clearing reservations.

The migration adds nullable per-call sizing observations without rewriting the
attempt heap. Constraint validation scans operational tables; lock acquisition
is bounded to five seconds. Existing forced RLS remains in force, and migration
authority is transaction-local. It requeues budget-deferred coordination records
without changing eligibility, pending order, canonical Knowledge, semantic
failures, ambiguity, immutable assessments, historical windows, grants, or usage.
Legacy pending/running scheduling commands close with an audit record. All
changes commit together; a failed migration rolls back and can be retried.

New writers record durable attempt identity, input estimates, and output capacity,
then record terminal reported usage once. They create no spending reservations.
Database guards reject legacy grants, reservations, budget windows, and deferred
states. Existing startup schema validation rejects older binaries after cutover.
Old spending-setting rows remain inert and unchanged. New model dispatch requires
an explicitly configured `ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS` value.

The first valid window uses saved model/context/concurrency settings immediately
and ends at the retained schedule boundary; ordinary next-window activation then
continues. Historical conservative reservations never fund or block new work.
Missing configuration leaves control status and configuration available.

Down is supported only while the operational installation remains empty.
Populated retirement requires disabling or pausing maintenance and rolling
forward. Never restart an incompatible binary or rewrite historical accounting
to make rollback appear safe.
