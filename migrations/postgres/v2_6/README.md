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
