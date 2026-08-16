# v2.5 migrations

The executable PostgreSQL history is split by release family:

- `v2_4/` contains the immutable v2.4 and earlier migration files.
- `v2_5/` contains the ordered v2.5 expand-and-contract migrations with
  versions greater than the v2.4 history.

The runtime validates and loads both directories as one strictly ordered Goose
history. The identity bridge runs before the irreversible cleanup so a skipped
release upgrade still translates legacy rows before `team_profiles` is removed.

The transitional-schema cleanup first builds the terminal embedding-job
retention index concurrently. Its following transactional migration validates
direct semantic foreign keys, removes the two semantic mirror tables, and drops
the superseded embedding singleton. That second migration is irreversible and
requires a pre-migration backup for recovery.
