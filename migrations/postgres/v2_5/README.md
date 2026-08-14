# v2.5 migration baseline

`migrations/postgres/` is the executable Goose history. The SQL file in this
directory is a non-executable hand-off record for the v2.5 rollout; it is kept
separate so Goose cannot apply two files with the same historical version.

All v2.5 schema changes use new, monotonically increasing files in the parent
directory. The compatibility bridge is additive. Cleanup of `team_profiles` is
not part of this release.
