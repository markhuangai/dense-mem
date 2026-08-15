# PostgreSQL v2.4 migration history

This directory contains the immutable Goose history shipped through Dense-Mem
v2.4. The files retain their deployed numeric versions and bytes. Do not edit,
rename, delete, or move them.

Create v2.5 migrations with:

```bash
./scripts/new-postgres-migration.sh <snake_case_name>
```

Validate the complete release-organized history against the merge base with:

```bash
./scripts/check-postgres-migrations.sh origin/main
```

Goose remains the only executable schema authority. Runtime code loads every
release directory as one globally ordered migration history.
