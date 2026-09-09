# Capability contract ownership

Issue #360 establishes capability-owned contract roots and shared PostgreSQL
mechanics for later cutovers. Each moved declaration has one live definition;
the old package name is only a type alias or a single-hop compatibility
interface.

`internal/graph/contract` owns graph queries, node/edge read models, and the
graph store port. `internal/trace/contract` owns trace inputs, trace result
records, and the relationship-conflict record reused by trace and recall.
`internal/recall/contract` owns the recall request, application result models,
storage inputs/results, recall service port, and evidence-conflict read models.
`internal/search/contract` owns the search read port and its
provider-independent read models. `internal/semanticwrite/contract` owns the
provider-independent semantic-write plan, embedding result, and batch-provider
port.
`internal/embedding/contract` owns the provider-independent embedding interface,
closed provider error vocabulary, retry-hint bounds, failure classification and
sanitized error projection. The concrete OpenAI and retry implementations remain
in `internal/embedding` and consume this public port.

`internal/storage/postgres/graphread` owns bounded breadth-first traversal,
graph snapshot assembly, and graph row decoding.
`internal/storage/postgres/lockadmission` owns the one-per-database-pool
advisory-lock admission budget. `internal/storage/postgres` owns the shared
active-space-generation SQL fragment. These packages contain mechanics only;
adapters retain authorization, transaction, and domain policy.

Compatibility aliases and their current consumers are:

- `internal/repository/semantic_types.go`: graph and trace names are aliases
  consumed by `internal/service/graphview`,
  `internal/service/contextservice`, `internal/service/skillpackservice`,
  `internal/tools/registry`, and repository integration tests.
- `internal/repository/search_types.go`: search names are aliases consumed by
  `internal/repository/search_repository.go`,
  `internal/repository/recall_repository.go`,
  `internal/service/search_reconciliation`,
  `internal/service/memoryservice`, and
  `internal/http/control_portal_search.go`.
- `internal/repository/evidence_conflict_repository.go`: evidence-conflict
  records are aliases consumed by the evidence-conflict repository/query
  files and `internal/service/memoryservice/recall_conflicts.go`.
- `internal/service/memoryservice/recall.go` and
  `internal/service/memoryservice/recall_conflicts.go`: recall request/result
  names and the narrow `RecallSearchRepository` port are aliases consumed by
  `internal/http/handler/recall_handler.go`, the HTTP/MCP tool registries,
  community/space fusion, and recall tests.
- `internal/service/semanticwrite/executor.go`: semantic-write names are
  aliases consumed by `internal/service/memoryservice/lifecycle.go`,
  `internal/service/semanticwrite`, and their tests.
- `internal/service/dreamservice/compat.go`: Dream service names are bounded
  aliases consumed by `cmd/internal/serverapp/application_composition.go`,
  `cmd/internal/serverapp/server.go` (`NewScheduler`), and
  `cmd/internal/serverapp/telemetry_features.go`; canonical application policy
  and ports live in `internal/dream` and `internal/dream/contract`.
- `internal/repository/dream_compat.go` and
  `internal/repository/dream_types.go`: Dream repository methods, contracts,
  and errors are single-hop compatibility aliases consumed by the server
  composition's `SemanticRepositoryImpl` dependency and the retained Dream
  database fixtures under `internal/repository/`; SQL ownership is
  `internal/dream/postgres.Store`.

`internal/dream/postgres` owns Dream's PostgreSQL SQL, transaction boundaries,
Hypothesis evaluation reads, daily graph-dreaming persistence, and hourly
evidence-discovery persistence. `internal/repository/dream_*.go` is no longer a
live implementation: `SemanticRepositoryImpl` keeps only single-hop forwarding
methods for transitional callers. Daily graph dreaming and hourly evidence
discovery remain separate lanes, and Hypothesis records never enter default
recall or active graph reads. The compatibility facades are removed with the
other legacy facades by #382 after all named consumers migrate.

The compatibility removal owner is #382. It removes these aliases and the
legacy repository/service facades after the graph, trace, recall, search, and
semantic-write adopters complete their native cutovers. Lifecycle and Remember
remain consumer-owned ports in this issue, as required by the frozen plan;
their existing `internal/service/memoryservice` and
`internal/service/remember` contracts are not duplicated here.

Recall execution keeps prepared search contracts, embeddings, degradation
state, and derived memory-space scope in private service/adapter wrappers.
The public request and storage ports cannot carry derived scope or provider
state.

## Wave 6 shared-readiness ownership

The wave 6 adopters use disjoint capability fragments and database-case
registries. `access`, `operations`, `remember` and `search` own their populated
registrations; `lifecycle` and `memorypack` are reserved empty fragments until
their adopters add capability-specific cases. The complete registry is loaded
by the existing controller and must retain every case exactly once.

The following shared boundaries are read-only for wave 6 adopters:

- `internal/embedding/contract`, `internal/search/contract`,
  `internal/semanticwrite/contract`, and the existing domain contracts;
- canonical Knowledge PostgreSQL writes and their compatibility facades;
- `cmd/internal/serverapp/application_composition.go`, `server.go`,
  `cmd/e2e/main.go`, and the E2E host controller;
- mixed database fixtures and the central architecture manifest/checker.

Remember, Lifecycle and Search consume the public embedding contract. The root
`internal/embedding` package retains only the concrete provider and compatibility
aliases, so no adopter may reintroduce a private provider dependency or duplicate
failure classification. Compatibility aliases remain bounded single-hop paths
until their named capability cleanup owner removes them.

## Wave 5 shared-readiness ownership

Issue #389 freezes the writable partition for the eight Wave 5 adopters. The
capability fragments are the authoritative source-to-owner map; the central
manifest, shared contracts, role rules, assemblers, and mixed fixtures remain
read-only infrastructure for adopters.

The following files intentionally remain shared-read-only because they contain
cross-capability contracts or mixed acceptance setup:

- `internal/repository/semantic_types.go` and `internal/repository/semantic_read_helpers.go`
- `internal/repository/semantic_trace_graph_integration.e2e`
- `internal/repository/remember_primitives_integration.e2e`
- `cmd/internal/serverapp/application_composition.go` and `cmd/internal/serverapp/server.go`
- `cmd/e2e/main.go` and `scripts/e2e-host-controller.sh`, except for the bounded
  precheck project-name helper owned by #389

Database-case registrations use the same partition: `knowledge` (#362),
`trace` (#363), `privacy` (#364), `dream` (#365), `graph` (#366),
`community` (#367), `audit` (#368), and `settings` (#369). The empty settings
fragment is intentional; its owner is reserved before that capability adds
database-backed cases. Existing `http`, `migration`, `postgres`,
`repository`, `server`, and `service` fragments retain cases outside this
Wave 5 partition.

Remember-attempt diagnostics are retained in PostgreSQL as ordered,
operator-only request/provider/caller exchanges. The diagnostic hold writer is
`internal/storage/postgres/remember_diagnostic_hold.go:SetRememberAttemptDiagnosticHoldStateTx`;
legacy failure-artifact storage and its hash-bearing API were removed after the
verified migration to `remember_attempt_diagnostics`. Bodies are bounded to 16
MiB per exchange and 64 MiB per failed attempt, expire after seven days, and
are sanitized before storage; transport headers, credentials, stack traces, and
database errors are never captured. Control detail reads are audited and
`no-store`, and expired rows retain only capture state until purge.
