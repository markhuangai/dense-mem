# Capability contract ownership

The schema-version-2 architecture manifest is assembled from capability
fragments under `architecture/modules/`. Each fragment owns the exact source
units, worker anchors, and database-case registrations for one capability. The
central manifest owns discovery, profiles, and fragment inventory. The loader
rejects missing, duplicate, unlisted, or undiscovered units.

The dependency role matrix is defined once by `architecture/manifest.mjs` and
is injected into the merged manifest. It is not copied into the JSON inventory.

The dependency direction is transport to application to domain and ports.
PostgreSQL adapters implement capability ports and may use only domain, ports,
and the shared PostgreSQL infrastructure. Composition constructs private
adapters. Cross-capability imports require a public target; private units stay
inside their owning capability.

## Native owners

- `internal/ontology/contract` owns versioned organization definitions,
  assignments, equivalence compatibility, persistent override policy, seeding,
  and dependency fingerprints. `internal/ontology/postgres` owns active shared
  source reads and atomic append-only publications, revisions, and rollback.
  It never writes canonical Knowledge. Its maintenance worker supplies bounded
  topic-assignment readers through public contracts to the Community projection
  service. Community owns resumable derived sources, membership, publication,
  dependency freshness, deterministic summaries, and coverage. Composition
  injects the readers; Community never imports the private ontology adapter.
  Projection admission consumes the existing global ontology team-lease capacity
  independently of organization model tokens; scoped Recall reads are repeatable.
  Recall consumes independently eligible topics through existing Community ports.
- `internal/knowledge/contract` and `internal/knowledge/postgres` own
  authoritative evidence, semantic, relationship, search-document, canonical
  projection, and reconciliation writes. The contract owns predicate identity,
  compatibility, support-driven lifecycle and correction/conflict eligibility;
  PostgreSQL loads locked facts and commits state, history and search effects.
  Search projection repair uses Knowledge operation ports and one transaction
  per repair operation.
- `internal/search` owns query and reconciliation orchestration, provider
  bounds, readiness, ranking, and error projection. `internal/search/postgres`
  owns query reads, bootstrap, and index mechanics; it does not persist the
  canonical projection or reconciliation run state.
- `internal/storage/postgres` owns shared relationship projection-generation SQL
  components for Search and Recall. Search retains its all-team readiness
  selection, and Recall retains its stricter current-generation vector selection.
  `internal/knowledge/contract` owns the foreground-generation metadata key.
- `internal/storage/postgres/conflictread` is the private shared owner of
  Conflict projection, supporter and history reads used by Knowledge and
  Conflict adapters. It receives each caller's transaction. Conflict's contract
  owns temporal disposition policy; Knowledge retains canonical conflict writes.
- `internal/recall` owns evidence and relationship candidate fusion, known-result
  suppression, ranking, and final selection. Its PostgreSQL adapter supplies
  bounded candidate batches, hydration, and conflict reads under team RLS.
- `internal/graph`, `internal/trace`, `internal/recall`, `internal/dream`,
  `internal/community`, `internal/conflict`, and `internal/memorypack` own
  their capability APIs and contracts. Their PostgreSQL packages implement
  private adapters for those APIs.
- Graph's contract owns query defaults and type normalization; Trace's contract
  owns input budgets and completeness. Dream retains operator history, derivation
  readers, and diagnostic retention. Community owns current topic projection
  publication/read policy and community recall priorities. The adapters retain
  scoped SQL, fixed expression mapping, top-k selection and transactional reads.
- `internal/service/access`, `internal/privacy/service`,
  `internal/settings`, `internal/operations`, `internal/audit`, and
  `internal/remember/service` own Access, privacy, settings, operational,
  audit, and Remember policy respectively. The root `internal/service` package
  is not an application facade.
- Privacy's contract owns operation hashing and retry/exhaustion decisions.
  Access's contract owns native directory-page validation, with the shared
  maximum in `internal/domain`. Operations' contract owns operation-log query
  and severity normalization and the telemetry query port. Its private
  `internal/operations/prometheus` adapter owns HTTP transport and decoding;
  Operations retains snapshot, scope, pricing and partial-availability policy.
- `internal/http` binds and translates transport contracts. Logger, credential
  lookup, and verifier implementations are supplied by composition; HTTP does
  not import observability or crypto implementations directly.

Every live implementation has one owner. Test fixtures use owner-local
helpers or public capability ports without exposing a second runtime authority.

## Cross-capability contracts

- Conflict's current/history modes, bounded presentation and full voting counts
  remain distinct. Shared readers do not begin transactions; adapters keep
  caller-owned RLS, locks and canonical writes. Support/correction mutation
  requires the owning profile even where same-team reads are visible.
- Graph preserves service versus adapter type ordering and validation order.
  Trace keeps authorized-record-derived space. Dream and Community retain
  distinct eligibility/ranking; SQL sorts before LIMIT, and Recall's explicit
  zero disables Community context while direct adapter zero selects defaults.
- Privacy/Access credential retirement remains one transaction. Operation logs
  preserve diagnostic exposure and sink-failure policy; Audit retains its
  separate bounded/redacted lineage contract. Owner and credential usage totals
  remain distinct.
- Settings, Audit, the Search/Recall pilot, Remember provider orchestration,
  shared RLS/space/projection/traversal mechanics, browser transports and worker
  ownership are retained. Evaluation composition consumes the same native
  contracts, with independent production/evaluation discovery. Private readers
  have adapter consumers; the Prometheus implementation is supplied by
  composition rather than imported by application policy.

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
visibility, dependency direction, worker anchors, database-case ownership, and
the absence of retired migration metadata. Workers remain permanent ownership
records; their identities are discovered from source and compared with the
manifest on every check.
