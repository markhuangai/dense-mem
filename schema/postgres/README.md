# PostgreSQL Schema Catalog

Goose migrations in `migrations/postgres` are the executable schema authority.
The files beside this README are generated, read-only views of the schema after
all migrations run against the PostgreSQL/pgvector image configured by the
repository.

```bash
./scripts/postgres-schema-catalog.sh write
./scripts/postgres-schema-catalog.sh check
```

`write` resolves the configured image to an immutable digest, migrates a
disposable database, canonicalizes the schema through dump and restore, proves
that `current.sql` survives another restore/dump cycle unchanged, and replaces
the generated files only after every check passes. `check` performs the same
work in a temporary directory and fails on stale, missing, modified, or extra
catalog files.

CI checks migration history, Goose syntax, manifest metadata, and catalog
checksums without provisioning PostgreSQL. Run `write` or `check` locally when
changing migrations or generated catalog files; both modes use a disposable
PostgreSQL container for full dump/restore proof.

- `current.sql` is the complete schema-only snapshot used by the restore check.
- `global.sql` contains extensions, functions, and other non-relation objects.
- `relations/public.<relation>.sql` documents relation-owned DDL, including
  constraints, indexes, triggers, row-security state, and policies.
- `manifest.tsv` records the configured image, resolved digest, Goose version,
  latest migration, a digest of every migration filename and content hash, and
  a SHA-256 checksum for every generated SQL file.

The split files are inspection aids, not an ordered restore set. Production
must never apply this catalog or use it for desired-state reconciliation. Add a
new forward Goose migration for every schema change, then regenerate the
catalog.
