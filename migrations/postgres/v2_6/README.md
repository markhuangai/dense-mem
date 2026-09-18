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
