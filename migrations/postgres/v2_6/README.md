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
backup/restore evidence, and the planned coordinated stop. Do not start the
replacement release or allow startup migrations to execute until that record
is approved by the maintainer.

The retirement release is applied during a planned stop of every API, worker,
and demo instance that shares the database. Stop all instances before starting
the replacement release; a rolling `docker compose up` is not sufficient. The
operator records the eight table definitions and row counts, the compatibility
marker identifiers and contents, and the preserved catalog before the stop.
After the replacement starts, verify that the migration completed, all eight
tables and only their owned objects are absent, the marker and canonical tables
are unchanged, and health, Remember, recall, trace, workers, and RLS checks
pass.

The former `repository/TestAuthorityRepositoryRetainsMigrationAuditReadOnly`
baseline case belonged to #278's retained-table boundary. #279 retires that
symbol with the tables; its surviving operation-log restart regression is
registered as `repository/TestAuthorityRepositoryOperationLogRetentionSurvivesRepositoryRestart`.

Recovery is a verified backup restoration or a separately reviewed forward
recovery migration after all instances are stopped. Recreating empty tables
cannot restore discarded migration evidence and is not a rollback.
