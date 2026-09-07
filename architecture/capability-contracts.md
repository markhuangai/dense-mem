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
