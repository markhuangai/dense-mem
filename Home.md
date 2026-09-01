# Dense-Mem

Dense-Mem gives AI clients a shared, evidence-backed semantic memory service.

This manual describes the current PostgreSQL + `pgvector` architecture and
supported product behavior. A code/design mismatch is implementation work, not
a second valid architecture.

## Current v2.6.1 Remember boundary

`remember` is synchronous: the originating MCP call validates and snapshots
the request, runs at most three complete assessor calls, makes one required
batch embedding request, and commits canonical semantics plus current vectors
atomically. The fixed budgets are 180 seconds total, 160 seconds for the
assessor phase, 10 seconds for embedding, and 10 seconds for commit. The result
is terminal (`completed`, `rejected`, or `quarantined`) and includes every
evidence and Relationship disposition. `get_submission_status`, placement
workers, and the normal embedding queue are removed from the supported catalog.
Operational failures are structured MCP `isError` results with bounded
remediation; clients do not poll.

## What Dense-Mem Remembers

Dense-Mem is suited to:

- project goals, systems, ownership, and current priorities
- preferences and team agreements
- decisions and corrections across sessions
- time-scoped operational knowledge
- provenance needed to explain a remembered Relationship

Dense-Mem is not a truth oracle. A host LLM chooses when to call it. Dense-Mem
stores exact evidence, lets one bounded assessor session repair
grounding/identity/predicate/support decisions, applies deterministic lifecycle
rules, and preserves trace. Unsupported hints can be `not_stored` when every
evidence item remains covered by stored Relationships.

## How It Works

```mermaid
%%{init: {"sequence": {"rightAngles": true}}}%%
sequenceDiagram
  actor User
  participant Host as AI client
  participant DM as Dense-Mem
  participant PG as PostgreSQL + pgvector

  User->>Host: Share evidence
  Host->>DM: remember(evidence, logical Relationships + evidence_indices)
  DM->>DM: Deterministic write-time security scan
  alt Intake rejection
    DM->>PG: Append safe rejection audit metadata
    DM-->>Host: Bounded error, nothing staged
  else Intake pass
    DM->>DM: Snapshot fences and build bounded groundings/candidates
    DM->>DM: Assessor conversation (≤3 complete calls)
    DM->>DM: One ordered embedding batch (≤256 documents)
    alt Assessor security concern
      DM->>PG: Quarantine terminal attempt
    else Clean validated assessment
      DM->>PG: Atomically commit state, history, predicates, documents, and vectors
    end
    DM-->>Host: Terminal structured outcome and per-ref dispositions
    Host->>DM: recall_memory(query)
    DM->>PG: Evidence and Relationship search with optional derived frontiers
    DM-->>Host: Ranked Evidence plus separate related frontiers and degradations
    opt Cross-profile Conflict needs more evidence
      Host->>User: Present authored positions and exact question
      User-->>Host: Provide new evidence
      Host->>DM: remember(evidence, conflict context)
    end
    opt Support or correction detail needed
      Host->>DM: trace_memory(relationship_id)
      DM->>PG: Load normalized provenance/history
      DM-->>Host: Evidence, verification, lifecycle, and Conflict history
    end
  end
```

The convergence rule:

```text
same real identity      -> reuse the Entity
same owner + relationship -> attach evidence to that Relationship
another profile agrees  -> keep its authored Relationship and group on recall
profiles disagree       -> preserve each author; use a team Conflict case
new real identity       -> create one Entity
new relationship        -> create one Relationship
actual ambiguity        -> server assessment; no client resolution loop
unsupported or ungroundable hint -> `not_stored` when other stored splits cover
the evidence; otherwise `rejected/no_supported_memory`; no partial commit
dream speculation       -> Hypothesis only; no new node
```

Aliases are Entity-name metadata, never edges.

## Start Here

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Start["What do you need?"] --> Use["Use memory"]
  Start --> Design["Understand the design"]
  Start --> Run["Deploy and operate"]
  Start --> Build["Implement the refactor"]
  Use --> Workflow["Quick Start / Using Dense-Mem"]
  Design --> Model["Architecture / identity /<br/>Relationship lifecycle"]
  Run --> Ops["Configuration / security / runbook"]
  Build --> Dev["Development / evaluation / release"]
```

| Goal | Canonical page |
| --- | --- |
| System boundary | [Architecture](Architecture) |
| Entity IDs, names, aliases, homonyms, merge/split | [Entity Identity And Resolution](Entity-Identity-And-Resolution) |
| Predicate, Relationship, evidence, verification, status/support, forget | [Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle) |
| `remember` process and strict assessor | [Memory Write Pipeline](Write-Pipeline) |
| PostgreSQL, `pgvector`, Redis, queues, and recovery | [Data Model And Storage](Data-Model-And-Storage) |
| Tenant keys, composite constraints, RLS, queues, and future team sharding | [PostgreSQL Cluster-Safe Schema](PostgreSQL-Cluster-Safe-Schema) |
| Recall eligibility, branches, adjacency, and trace escalation | [Recall Ranking](Recall-Ranking) |
| Embedding/cosine meaning, candidate fusion, RRF, and reranking | [Vector Search And Ranking](Vector-Search-And-Ranking) |
| Exact/ANN execution, HNSW, halfvec, index lifecycle, and vector scale | [ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec) |
| Public MCP tool schemas | [Technical Reference](Technical-Reference) |
| Local setup | [Quick Start](Quick-Start) |
| Everyday client workflow | [Using Dense-Mem](Using-Dense-Mem) |
| MCP clients | [MCP Clients](MCP-Clients) |
| Conflict, correction, identity split, and forget | [Conflicts And Corrections](Conflicts-And-Corrections) |
| Hypotheses | [Dreaming](Dreaming) |
| Community discovery | [Community Memory](Community-And-Dreaming) |
| Configuration and operation | [Configuration](Configuration), [Operations Runbook](Operations-Runbook) |
| Security, portals, SSO, keys | [Request Lifecycle And Security](Request-Lifecycle-And-Security), [Portals And API Keys](Portals-And-API-Keys), [User Portal SSO](User-Portal-SSO) |
| Telemetry, feedback, evaluation | [Telemetry](Telemetry), [Recall Feedback](Recall-Feedback), [Evaluation](Evaluation) |
| Source changes and testing | [Development And Testing](Development-And-Testing) |
| Manual ownership | [Documentation Maintenance](Documentation-Maintenance) |

## Semantic Model At A Glance

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Fragment["EvidenceFragment"] --> Observation["RelationshipObservation"]
  Observation --> Verification["VerificationEvent"]
  Verification --> Record["RelationshipRecord"]
  Record --> Edge["0..1 SemanticEdge read"]
  Record --> Cross["Cross-profile references"]
  Record --> Case["Team Conflict case membership"]
  Record --> Trace["Support and transition trace"]
  Record -. "eligible input" .-> Dream["Hypothesis process"]
```

- Entity is one team-scoped real-world identity.
- `entity_id` is an opaque ID, not display text.
- Value is a canonical typed scalar endpoint.
- Predicate is the registered meaning such as `works_on`.
- RelationshipRecord owns one profile's canonical subject-predicate-object
  lifecycle and is readable across its team.
- A profile corrects another profile by writing its own verified knowledge and
  an immutable cross-reference, never by mutating the target.
- SemanticEdge is the read shape of an active Relationship with effective
  support.
- Hypothesis is isolated speculation between existing endpoints.

Full definitions exist only in
[Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle).

## Storage At A Glance

| Component | Target responsibility |
| --- | --- |
| PostgreSQL | every durable knowledge/control record plus graph-shaped, full-text, and vector-ready reads |
| `pgvector` | vector type/index inside PostgreSQL |
| Redis | optional distributed coordination: limits, short leases/signals, SSE |
| process memory | the same coordination interfaces for one process only |

Redis never stores the only copy of an Entity, Relationship, evidence, submission,
key, or audit event.

## What Dense-Mem Does Not Do

Dense-Mem does not:

- use a name as an Entity ID
- create alias edges or a new Entity for every spelling
- let an AI select arbitrary database IDs or assign lifecycle status/support
- treat stored facts, graph context, or search hits as new evidence
- infer a third fact merely because two facts form a plausible chain
- expose pending Relationships in the active graph/default recall
- let dreaming create Entities, Values, predicates, or Relationships
- erase nodes/history when a Relationship is forgotten
- store durable memory only in Redis
- browse the internet to validate a submitted claim

## Manual Status

Canonical pages define the current architecture. Operational commands must be
checked against the deployed contract/version.
Update and evidence rules are in
[Documentation Maintenance](Documentation-Maintenance).
