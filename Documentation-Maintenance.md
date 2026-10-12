# Documentation Maintenance

This wiki covers current installations and fresh setup. Remove obsolete
release narratives, upgrade procedures, compatibility guides, and completed
implementation plans. Git history retains removed prose.

Keep exact identifiers still emitted by current code, such as the memory-pack
format. A historical `known_at` query is current product functionality, not
obsolete documentation.

## Sources And Authority

Accepted repository ADRs govern architecture. Current registries,
implementations, migrations, callers, and tests establish implemented behavior.
An old wiki statement or recalled memory does not override verified source and
explicit maintainer direction.

Trace a claim through its supported caller and authoritative owner. A source
checkout does not establish which release is deployed. Identify the revision
and worktree state used for an audit, and report unresolved implementation/ADR
mismatches without silently changing either contract.

Record source context with:

```bash
git status --short --branch
git rev-parse HEAD
git log -1 --format='%cI %s'
```

## Canonical Page Ownership

| Subject | Canonical page |
| --- | --- |
| reader navigation and manual status | [Home](Home) |
| system boundaries, dependency direction, and startup | [Architecture](Architecture) |
| Entity identity, names, aliases, reuse/create/ambiguity, merge/split | [Entity Identity And Resolution](Entity-Identity-And-Resolution) |
| Relationship vocabulary, identity, cardinality, status/effective support, trace, and SemanticEdge contract | [Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle) |
| remember stage order, provider retries, commit order, and review orchestration | [Memory Write Pipeline](Write-Pipeline) |
| PostgreSQL/pgvector/Redis ownership, graph-shaped/search reads, jobs, and recovery | [Data Model And Storage](Data-Model-And-Storage) |
| cluster-safe tenant keys, table placement, composite constraints, RLS/query locality, and scheduler routing | [PostgreSQL Cluster-Safe Schema](PostgreSQL-Cluster-Safe-Schema) |
| recall branches, eligibility, frontier, trace escalation, and degradation | [Recall Ranking](Recall-Ranking) |
| embedding and distance meaning, candidate fusion, RRF, and post-fusion reranking | [Vector Search And Ranking](Vector-Search-And-Ranking) |
| exact/ANN execution, HNSW, halfvec, vector index lifecycle/load, and vector-search topology | [ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec) |
| Python core authentication, typed results, installation and safe replay | [Framework Integrations](Framework-Integrations) |
| current MCP contract, examples, and schema discovery | [Technical Reference](Technical-Reference) |
| authentication, tenant identity, scopes, MCP discovery/execution, and portal boundaries | [Request Lifecycle And Security](Request-Lifecycle-And-Security) |
| durable team Conflict cases, recall disposition, and evidence-backed correction | [Conflicts And Corrections](Conflicts-And-Corrections) |
| community derivation and analytical summaries | [Community And Dreaming](Community-And-Dreaming) |
| Hypothesis generation, isolation, review, and staleness | [Dreaming](Dreaming) |
| portable knowledge artifact and export workflow | [Memory Packs](Memory-Packs) |
| observational recall feedback | [Recall Feedback](Recall-Feedback) |
| evaluation manifests, quality metrics, and release gates | [Evaluation](Evaluation) |
| browser SSO and entitlement mapping | [User Portal SSO](User-Portal-SSO) |
| portals, memberships, credentials, roles, and key administration | [Portals And API Keys](Portals-And-API-Keys) |
| environment and database runtime settings | [Configuration](Configuration) |
| deployment, backup, restore, and incidents | [Operations Runbook](Operations-Runbook) |
| telemetry names and operational interpretation | [Telemetry](Telemetry) |
| source layout and validation layers | [Development And Testing](Development-And-Testing) |

[Home](Home#start-here) is the navigation map. This table is the ownership map.
Do not reproduce either map on another page.


## Single-Source Rules

Keep detailed schemas/examples in Technical Reference, configuration names and
defaults in Configuration, operator procedures in the runbook, and lifecycle
rules in their owning pages. Other pages should explain the local consequence
and link to that owner.

Preserve useful page URLs. When a heading changes, update its inbound anchors.
Avoid copied package inventories: link to the checked schema-version-2
architecture manifest and describe only the ownership needed by the reader.

## Updating A Page

1. Read its current content and the relevant source owners, tests, and accepted
   ADRs.
2. Verify supported inputs, outputs, defaults, authorization, failure behavior,
   and any current operational constraints.
3. Replace stale prose and examples in place; update dependent summaries and
   diagrams in the same change.
4. Check a concrete adverse example, such as an unsupported proposal, stale
   correction, private-bound read, or required provider failure.
5. Validate the changed documentation and inspect the final diff.

Do not convert implementation proposals into supported capabilities. Describe
current limitations plainly and leave unimplemented designs out of usage
instructions.

## Validation

| Material | Verify against |
| --- | --- |
| Tools, inputs, results, actions | Registry schemas, validated tool examples, and MCP contract tests. |
| HTTP/portal procedures | Registered routes, DTOs, browser callers, and their tests. |
| Settings and defaults | Startup parser, effective runtime settings, and deployment examples. |
| Storage and lifecycle | Current migrations, adapter queries, domain policy, and real PostgreSQL tests. |
| Development and release commands | Existing scripts, scenario registries, and workflows. |

Resolve every wiki target and GitHub-style heading anchor. Parse complete JSON
examples; mark fragments as fragments. Validate request examples against the
current registry, substituting only clearly identified returned-ID placeholders
in the validation harness.

Extract both backtick and tilde Mermaid fences and parse each diagram.
Use a pinned parser in temporary validation tooling; the application repository
does not currently declare a Mermaid dependency. Spot-check substantial diagrams
for readable layout as well as syntax.

Flowcharts use:

```text
%%{init: {"flowchart": {"curve": "linear"}}}%%
```

Sequence diagrams use:

```text
%%{init: {"sequence": {"rightAngles": true}}}%%
```

Check source references, configuration identifiers, retired-tool descriptions,
and claims of queued Remember work. Inspect matches in context; literal current
schema names and temporal history must not be removed by a blanket text sweep.

```bash
git diff --check
git diff --stat
git status --short
```

## Handoff

Report the source baseline, changed responsibilities, verification performed,
and remaining uncertainty. Distinguish executed checks from static inspection.
Preserve concurrent edits. Commit or publish only when authorized.

A documentation refresh is complete when current behavior is consistent across
the relevant canonical pages, examples and commands are usable, obsolete
sections are gone, and navigation and diagrams validate.
