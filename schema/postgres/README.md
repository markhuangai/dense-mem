# Generated PostgreSQL schema catalog

`migrations/postgres/` is the only executable schema authority. This directory
is a deterministic, read-only catalog generated from a disposable instance of
the repository's pinned `pgvector/pgvector` image after every migration has
been applied.

`current.sql` is a restorable schema-only dump, `global.sql` contains extension
and function definitions, and `relations/` contains one normalized DDL file
per public relation. `manifest.tsv` records the migration set and checksums.
The catalog is documentation and drift evidence; production startup never
applies it.

Regenerate or verify with:

```bash
scripts/postgres-schema-catalog.sh write
scripts/postgres-schema-catalog.sh check
```
