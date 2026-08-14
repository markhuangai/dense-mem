# v2.5 migrations

The executable PostgreSQL history is split by release family:

- `v2_4/` contains the immutable v2.4 and earlier migration files.
- `v2_5/` contains additive v2.5 migrations with versions greater than the
  v2.4 history.

The runtime validates and loads both directories as one strictly ordered Goose
history. The compatibility bridge is additive; cleanup of `team_profiles` is
not part of this release.
