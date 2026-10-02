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
  owns input budgets and completeness. Dream's contract owns proposal,
  derivation, feedback and hypothesis recall policy. Community's contract owns
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

Every live implementation has one owner. Compatibility aliases and forwarding
facades from the former broad service and repository packages were removed once
their callers moved to these native owners. Test fixtures use owner-local
helpers or the public capability ports; they do not expose a second runtime
authority.

## Ownership and benefit verification

This assessment certifies application source at
`1e616dc00cfa68f4e569d0ea562606a10bb743ba`, against the original
`41e632b2c8ecdbed793fcc5cb3388484eeed0d5f` assessment. The research baseline is
the [maintainer-approved #459 assessment](https://github.com/markhuangai/dense-mem/issues/459#issuecomment-5873490026)
and its published T01–T13 contracts. All thirteen implementation PRs below are
merged ancestors of the certified source. Their final PR heads have successful
Repository CI, CodeQL analysis, CodeQL and Production image E2E results; their
PR descriptions retain audit and local-test receipts. The certification PR
requires those four results again on its own final head.

Each original verified gap has the following source owner and supported
consumers. Paths and symbols describe the certified source; comparison receipts
identify their own measured sources and test-only overlays.

| Original gap | Final owner and supported consumers | Merged implementation and comparison | Verified maintenance benefit |
|---|---|---|---|
| T01: duplicated Conflict projections and historical policy | [conflictread](../internal/storage/postgres/conflictread/reader.go) receives Knowledge review/resolution and Conflict queue/read transactions; [Conflict temporal policy](../internal/conflict/contract/temporal.go) owns `ApplyConflictKnownAt` and position dispositions. Recall/Trace use composed Conflict read callbacks. | [#481 / PR #495](https://github.com/markhuangai/dense-mem/pull/495), `74386b7a`; [T01 receipt](../tests/eval/baselines/domain_ownership_t01.json) | Two query implementations now use one reader. The original 901 identical-line finding is resolved by removing the duplicate blocks; it is not a claim of 901 net deleted lines. |
| T02: predicate identity and compatibility | [Knowledge predicate policy](../internal/knowledge/contract/predicate_policy.go) owns normalization, canonical/collision keys, candidate selection and compatibility. Remember catalog preflight, inline preview and locked final commit reuse it. | [#482 / PR #500](https://github.com/markhuangai/dense-mem/pull/500), `7b1cb50f`; [T02 receipt](../tests/eval/baselines/domain_ownership_t02.json) | One policy owner replaces adapter rule bodies; preflight and commit keep their separate authoritative reads and drift fences. |
| T03: Dream proposal and feedback lifecycle | [Dream proposal policy](../internal/dream/contract/proposal_policy.go), [derivation policy](../internal/dream/contract/derivation_policy.go) and [feedback policy](../internal/dream/contract/feedback_policy.go) serve generation, locked confirmation and adapter validation. `LifecycleStatus` is reused by application and persistence. | [#483 / PR #499](https://github.com/markhuangai/dense-mem/pull/499), `e40c48d1`; [T03 receipt](../tests/eval/baselines/domain_ownership_t03.json) | Proposal, identity, source eligibility and feedback decisions have one Dream owner; graph and evidence-discovery lanes retain distinct rules. |
| T04: Graph defaults and normalization | [Graph query policy](../internal/graph/contract/query.go) supplies `NormalizeQuery`, `NormalizeTypes` and `NormalizeNodeType` to the service, adapter and shared traversal reader. | [#484 / PR #498](https://github.com/markhuangai/dense-mem/pull/498), `1759b1d9`; [T04 receipt](../tests/eval/baselines/domain_ownership_t04.json) | Service/adapter defaults and traversal aliases reuse one owner while retaining boundary-specific ordering and error precedence. |
| T05: Privacy operation identity and retry decisions | [Privacy policy](../internal/privacy/contract/private_memory.go) owns `Hash`, `MaximumAttempts` and `DecideRetry`; application requests, credential retirement, claiming and locked release reuse it. | [#485 / PR #497](https://github.com/markhuangai/dense-mem/pull/497), `22e7fc6d`; [T05 receipt](../tests/eval/baselines/domain_ownership_t05.json) | Duplicate hash/retry bodies are removed; durable replay bytes, legal holds and the atomic retirement transaction remain intact. |
| T06: SCIM directory paging | [domain directory maximum](../internal/domain/directory_identity.go) owns `DirectoryPageMaxResults`; [Access page policy](../internal/access/contract/ports.go) serves the application and adapter. SCIM advertises that maximum and translates protocol input. | [#486 / PR #515](https://github.com/markhuangai/dense-mem/pull/515), `20b32895`; [T06 receipt](../tests/eval/baselines/domain_ownership_t06.json) | One 100-result maximum replaces two constants; native bounds and filters have one owner, including metadata-only count zero. |
| T07: operation-log query/severity policy | [Operations policy](../internal/operations/contract/ports.go) owns `NormalizeOperationLogFilter` and `NormalizeOperationLogSeverity`; the service and indexed PostgreSQL list/detail paths reuse them. | [#487 / PR #511](https://github.com/markhuangai/dense-mem/pull/511), `33a687da`; [T07 receipt](../tests/eval/baselines/domain_ownership_t07.json) | One default/normalization owner; flush ordering, sink suppression, explicit false filters and fixed SQL predicates remain in their existing layers. |
| T08: relationship lifecycle decisions | [Knowledge lifecycle policy](../internal/knowledge/contract/lifecycle.go) owns `StatusForEffectiveSupport`, `RelationshipEligibleForCorrection` and `RelationshipEligibleForConflictPlacement`; support, retraction, revision and correction writers call it on authoritative facts. | [#488 / PR #516](https://github.com/markhuangai/dense-mem/pull/516), `057df3e3`; [T08 receipt](../tests/eval/baselines/domain_ownership_t08.json) | Three pure decisions replace adapter policy bodies; terminal/alias guards, ownership, locks and atomic history/search updates are preserved. |
| T09: hypothesis recall priorities | [Dream recall policy](../internal/dream/contract/recall_policy.go) owns `RecallHypothesisMatchOrder`, normalization and 5/20/200 bounds. Recall consumes the native default; PostgreSQL compiles the fixed order before SQL LIMIT and hydrates in the same transaction. | [#489 / PR #513](https://github.com/markhuangai/dense-mem/pull/513), `169d6f86`; [T09 receipt](../tests/eval/baselines/domain_ownership_t09.json) | Modest separation of relevance policy from SQL mechanics; no configurable ranking framework or post-LIMIT reranking. |
| T10: Trace budgets and completeness | [Trace input policy](../internal/trace/contract/input.go) supplies `NormalizeInput` to service and direct adapter callers; [result policy](../internal/trace/contract/result_policy.go) owns `Completeness`. | [#490 / PR #514](https://github.com/markhuangai/dense-mem/pull/514), `4323a596`; [T10 receipt](../tests/eval/baselines/domain_ownership_t10.json) | One budget/completeness owner replaces service/adapter decisions; authorized relationship-derived space and edge-before-event stop precedence remain intact. |
| T11: Prometheus transport versus telemetry policy | [Operations telemetry port](../internal/operations/contract/ports.go) exposes `TelemetryQuerier`; the private [Prometheus client](../internal/operations/prometheus/client.go) is constructed by server composition. Operations owns snapshot deadlines, scope, pricing, cards and partial results. | [#491 / PR #517](https://github.com/markhuangai/dense-mem/pull/517), `15aaf5d7`; [T11 receipt](../tests/eval/baselines/domain_ownership_t11.json) | HTTP/JSON transport is removed from the application implementation; constructors reduce from three to one, with 23 added production lines across the measured affected files. |
| T12: Community publication and recall choices | [Community input policy](../internal/community/contract/input_policy.go) serves transactional publication/read entry points; [recall policy](../internal/community/contract/recall_policy.go) owns `RecallCommunityMatchOrder` and native bounds used by Recall and PostgreSQL. | [#492 / PR #518](https://github.com/markhuangai/dense-mem/pull/518), `82b5c1e9`; [T12 receipt](../tests/eval/baselines/domain_ownership_t12.json) | One Community policy owner; covered-group suppression, LIMIT+1 truncation, fixed SQL lanes and batched snapshot hydration remain in PostgreSQL. |
| T13: unmeasured usage-flush scaling | [Usage flush benchmark](../internal/operations/postgres/usage_flush_scaling_benchmark_test.go) measures the existing `UsageMetricsServiceImpl.Flush` → `UpsertBuckets` path with real PostgreSQL and concurrent recording. | [#493 / PR #519](https://github.com/markhuangai/dense-mem/pull/519), `c46ba960`; [scaling receipt](../tests/eval/baselines/usage_flush_scaling.json) and [protocol](../tests/eval/README.md#issue-493-usage-flush-scaling) | Supplies measured cardinality costs and correctness obligations for a separate batching decision; production batching behavior is unchanged. |

### Measured outcomes and cost

T01–T12 record 54 fixed workloads with equal base/candidate result signatures
and SQL/transaction/provider counts. Each paired comparison used 20 warmups,
200 measured operations and five repetitions per source; each median p50/p95
increase passed its `max(5% of base, 1 ms)` gate. The table aggregates workload
ranges, not pooled percentiles. Positive latency deltas mean an increase.

| Receipt | Workloads | SQL statements; transactions; provider calls per operation | p50 delta range (ms) | p95 delta range (ms) |
|---|---:|---|---:|---:|
| T01 | 3 | 7/14/27; 1; 0 | -0.070 to +0.029 | -0.077 to +0.149 |
| T02 | 4 | 6/8/16; 1; 0 | -0.611 to +0.085 | +0.029 to +2.522 |
| T03 | 3 | 8/14/20; 1; 0 | -0.254 to +0.072 | -0.720 to +0.527 |
| T04 | 3 | 5/6/9; 1; 0 | -0.073 to +0.077 | -0.081 to +0.614 |
| T05 | 4 | 7/8/13; 1; 0 | -0.079 to +0.096 | -0.135 to +0.145 |
| T06 | 6 | 6/7/8; 1; 0 | -0.096 to +0.131 | -0.147 to +0.309 |
| T07 | 4 | 7; 1; 0 | -0.086 to +0.118 | -0.371 to +0.290 |
| T08 | 3 | 13/14/35; 1; 0 | -0.154 to +0.287 | -0.415 to +0.707 |
| T09 | 9 | 7; 1; 0 | -0.291 to +0.017 | -0.833 to +0.067 |
| T10 | 4 | 17; 1; 0 | -0.362 to +0.199 | -1.268 to -0.219 |
| T11 | 2 | 0; 0; 57 | +0.111 to +0.131 | +0.274 to +0.770 |
| T12 | 9 | 7; 1; 0 | -1.128 to +0.724 | -2.716 to +0.666 |

T11's provider counts are Prometheus HTTP requests; its local snapshot fixture
has no lifecycle reader, so SQL and transaction counts are zero. Production E2E
separately proves lifecycle integration. T02's +2.522 ms p95 case has a 58.954 ms
base median and passes its 2.948 ms allowance. These results establish measured
preservation, not an aggregate speedup or a new retrieval-quality score.

The manifest grows from 100 to 102 Go units through the private `conflictread`
and Prometheus packages; browser units remain 39 and worker anchors remain 46.
In the nine affected native contract directories plus those two private owners,
Go AST counts of non-test, package-level exported functions grow from 13 to 87
and exported named types from 336 to 345. This is internal API growth supporting
shared policy and the telemetry port, not a public HTTP/MCP contract change.
The count includes aliases and both Go profiles' source union, excludes methods,
and compares the two recorded assessment/source SHAs.

Allocation costs are also recorded: Dream proposal normalization adds about
one to two allocations, Privacy queued retry adds one escaping retry timestamp,
and Community maximum hydration adds 12,582 B/op (0.50%) and five allocations
with overlapping run ranges. The Community investigation identifies signature
serialization as the dominant sampled allocator without claiming an exact
causal allocation contribution. Each receipt retains the full workload values
and investigation; lower latency in some workloads does not prove a speedup.

T13 verifies 225 measured records and 45,000 operations across B=1/100/1000,
credential distributions, insert/update/replay and concurrent recording. The
write data-statement model is `1+B+C`; replay uses one ledger statement and
record-only workloads issue no SQL. Five RLS statements and transaction setup
are counted separately for database flushes, with no model discrepancy.
Owner/credential SQL consumes
87.9001–94.4431% of flush time in all five repetitions of the 18 large write
workloads. Its conditional GO recommends a separately approved grouped-key,
chunked-upsert implementation for the measured B=100/1000 cardinalities. Replay,
attribution, ledger/both-table rollback and concurrent next-flush recording are
required obligations. There is no measured batching speedup, production workload
frequency or production savings estimate.

### Preserved contracts and evidence boundaries

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
- Comparison receipts certify their recorded measured sources. Later accepted
  changes are traced separately: T11 extends the T07 contract file with telemetry
  port/error types while leaving log normalization intact; the measured T02 and
  T11 production source locks match this source. Each other receipt retains its
  source manifest, overlays and post-measurement notes. The final certification
  requires architecture/profile/browser/worker checks and full PR validation;
  historical measurements alone do not certify a later head.

The #459 assessment and T01–T13 tickets record the maintainer-approved historical
deterministic 1k waiver and its replacement verification. This documentation
adds no new evaluation run. No original verified gap is deferred by this
certification, and ticket lifecycle state is not added to permanent manifests.

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
