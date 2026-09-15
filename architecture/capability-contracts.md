# Capability contract ownership

The architecture manifest is assembled from capability fragments under
`architecture/modules/`. Each fragment owns the exact source units, worker
anchors, database-case registrations, and completion records for one
capability. The central manifest owns discovery, profiles, the role matrix, and
fragment inventory. The loader rejects missing, duplicate, unlisted, or
undiscovered units.

The dependency direction is transport to application to domain and ports.
PostgreSQL adapters implement capability ports and may use only domain, ports,
and the shared PostgreSQL infrastructure. Composition constructs private
adapters. Cross-capability imports require a public target; private units stay
inside their owning capability.

## Native owners

- `internal/knowledge/contract` and `internal/knowledge/postgres` own
  authoritative evidence, semantic, relationship, lifecycle, search-document,
  canonical projection, and reconciliation writes. Search projection repair
  uses Knowledge operation ports and one transaction per repair operation.
- `internal/search` owns query and reconciliation orchestration, provider
  bounds, readiness, ranking, and error projection. `internal/search/postgres`
  owns query reads, bootstrap, and index mechanics; it does not persist the
  canonical projection or reconciliation run state.
- `internal/graph`, `internal/trace`, `internal/recall`, `internal/dream`,
  `internal/community`, `internal/conflict`, and `internal/memorypack` own
  their capability APIs and contracts. Their PostgreSQL packages implement
  private adapters for those APIs.
- `internal/service/access`, `internal/privacy/service`,
  `internal/settings`, `internal/operations`, `internal/audit`, and
  `internal/remember/service` own Access, privacy, settings, operational,
  audit, and Remember policy respectively. The root `internal/service` package
  is not an application facade.
- `internal/http` binds and translates transport contracts. Logger, credential
  lookup, and verifier implementations are supplied by composition; HTTP does
  not import observability or crypto implementations directly.

Every live implementation has one owner. Compatibility aliases and forwarding
facades from the former broad service and repository packages were removed once
their callers moved to these native owners. Test fixtures use owner-local
helpers or the public capability ports; they do not expose a second runtime
authority.

## Database-case registry

Every tagged `_integration_test.go` source has one declaration in
`scripts/e2e-db-cases/`. The declaration's package is the source directory, and
its run expression names one concrete test. The registry loader checks duplicate
IDs, missing declarations, package mismatches, build tags, and stale source
paths before invoking a batch. Database-free cases remain ordinary `_test.go`
files and are covered by the normal Go test suite. The frozen baseline preserves
IDs, run expressions, phases, and scenario ownership while allowing a
capability to relocate a fixture to its native package.

The central architecture manifest, registry loader, composition root, and E2E
host controller are shared read-only infrastructure for capability cutovers.
Native fragments and fixtures may update their own ownership records and
registrations without restoring a broad shared facade.

## Coverage and verification

Database-free Go production and evaluation packages, browser modules, and the
MCP proxy are inventoried independently. Each report fails closed for missing
sources, empty reports, omitted browser modules, exact-boundary percentages,
and profile duplication. PostgreSQL adapter and integration coverage remains a
separate proof of SQL, RLS, transaction, lock, and concurrency behavior.

The architecture conformance UAT and checker validate fragment completeness,
visibility, dependency direction, worker lifecycle obligations, database-case
ownership, and the absence of expired compatibility obligations.
