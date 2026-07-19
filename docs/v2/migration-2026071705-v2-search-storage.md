# V2 Search Storage Migration 2026071705

## Scope

Adds dormant V2 PostgreSQL search storage:

- `embedding_contracts` and `search_index_generations`
- tenant-owned `search_documents` and `embedding_jobs`
- full-text GIN index on generated `search_tsv`
- metadata tables for the config-resolved embedding contract and internal
  search-index generation
- RLS policies for team-scoped search documents and embedding jobs

The migration installs `pgvector` with `CREATE EXTENSION IF NOT EXISTS vector`.
It does not seed a model-specific active contract or HNSW index. The configured
embedding model and dimensions must initialize the active embedding contract and
derived search-index generation before V2 cutover, then startup must reject
later model/dimension mismatches until a deliberate re-embedding workflow
replaces the stored contract. The migration also does not route active v1
recall, placement, or worker execution through the new tables.

## Lock, WAL, And Disk Notes

The migration creates new empty tables and generic indexes. On a fresh V2
rollout this does not rewrite existing V1 knowledge rows. Dimension-specific
HNSW indexes are created by the config-driven bootstrap or operator workflow
after the active embedding contract is known.

Future rebuilds on populated tables must use a durable operator workflow:

- capacity preflight for disk, memory, WAL, and replica lag
- topology-specific `CREATE INDEX CONCURRENTLY` or shard-local build steps
- progress and invalid-index cleanup checks
- activation only after every required physical index is present

HNSW indexes are rebuildable search structures. They are not semantic truth and
can be dropped/rebuilt without losing knowledge, provided the old active
contract/generation remains in place until readiness passes.

## Readiness

Runtime readiness must check:

- `pgvector` extension exists
- active embedding contract dimensions match stored/query vectors
- active HNSW generation has its named physical index present
- exact search is allowed only by a generation that uses exact search or a
  bounded fallback

Missing contract/index state is non-ready with a typed reason. The repository
must not silently fall back to an unbounded vector scan, and startup/bootstrap
must surface configured-vs-stored embedding mismatches before workers write new
embeddings.

## Rollback

Down migration drops the dormant search tables and contract metadata. It does not
drop the `vector` extension because the extension may be used by other database
objects or later migrations.

Rollback loses only derived V2 search documents, queued embedding work, and
search contract metadata. Authoritative evidence, semantic state, and lifecycle
history remain in earlier V2 ledger tables.

## Verification

Run against PostgreSQL with pgvector installed:

```bash
DATABASE_URL="postgres://testuser:testpass@127.0.0.1:55433/testdb?sslmode=disable" \
  go test -tags integration ./internal/storage/postgres \
  -run 'TestMigratorRunUp|TestMigratorRunDown' -count=1

DATABASE_URL="postgres://testuser:testpass@127.0.0.1:55433/testdb?sslmode=disable" \
  go test ./internal/repository \
  -run 'TestV2Ledger|TestV2Semantic|TestV2Search' -count=1
```
