# Technical Reference

This page defines the v2 public memory contract. The external memory contract
is discovered and executed through MCP:

```text
MCP transport:  /mcp
discovery:      tools/list
execution:      tools/call
```

The temporary `2.1.x` adapter submits legacy corpus evidence through the same
V2 application services used by MCP tools. Version `2.2.0` requires the
completed bridge marker before startup.

## Contract Principles

1. evidence is required; structured proposals are optional hints
2. clients cannot choose canonical IDs, predicate definitions, support, tier,
   status, or verification outcome
3. aliases are Entity-name metadata, not Relationships
4. `relationship_id` is the stable semantic and trace handle
5. normal recall is compact; evidence/history require evidence or trace reads
6. processing state and vector-search freshness are separate
7. candidate Relationships are never active/default recall results
8. every operation is authorized from the authenticated principal
9. unknown fields on writes and provider/storage failures are rejected visibly
10. knowledge is team-readable and profile-owned; cross-profile correction
    creates caller-owned records and immutable references
11. MCP is the supported external memory integration; user and control portals
    are first-party product interfaces

## MCP Tool Catalog

The LLM host obtains this catalog with `tools/list` and invokes one entry with
`tools/call`. Method purpose and portal boundaries are defined in
[Request Lifecycle And Security](Request-Lifecycle-And-Security#mcp-memory-interface).

### Memory

| Tool | Scope | Purpose |
| --- | --- | --- |
| `remember` | `write` | Persist evidence and queue extraction, one-call verification, and lifecycle placement. |
| `get_memory_placement` | `read` | Poll processing/search state and inspect outcomes or review tasks. |
| `resolve_memory_placement` | `write` | Supply a client decision, new evidence, correction, rejection, or forget request. |
| `correct_entity_resolution` | `write` | Dry-run or apply a merge/split limited to caller-owned observations and Relationships. |
| `recall_memory` | `read` | Retrieve bounded evidence contexts with compact Relationship discovery paths and server-controlled related Hypotheses. |
| `trace_memory` | `read` | Expand one Relationship into evidence, verification, lifecycle, conflicts, and bounded current graph context. |

### Dreaming

| Tool | Scope | Purpose |
| --- | --- | --- |
| `list_dreams` | `read` | List reviewable Hypotheses. |
| `get_dream` | `read` | Fetch one Hypothesis and source Relationship refs. |
| `resolve_dream_feedback` | `write` | Change Hypothesis review state; a truth change requires new evidence through `remember`. |

Dreaming can propose a registered predicate between existing endpoints only.
Its boundary is defined in [Dreaming](Dreaming).

### Memory packs

| Tool | Scope | Purpose |
| --- | --- | --- |
| `find_memory_pack_candidates` | `read` | Find exportable eligible Relationships and permitted provenance. |
| `export_memory_pack` | `read` | Produce a versioned canonical artifact and SHA-256 hash. |
| `inspect_memory_pack` | `read` | Parse and compare an artifact without writing. |
| `import_memory_pack` | `write` | Stage reviewed evidence as the authenticated destination profile through normal identity and Relationship processing. |
| `rollback_memory_pack_import` | `write` | Retract that destination profile's import when a dry run proves no later dependency makes rollback unsafe. |

The artifact schema and trust boundary belong in
[Memory Packs](Memory-Packs).

### Feedback and evaluation

| Tool | Scope | Purpose |
| --- | --- | --- |
| `submit_recall_session_feedback` | `write` | Feature-gated bounded judgment; never changes ranking or knowledge directly. |
| `eval_get_manifest` | `read + write` | Report evaluation capabilities and versions. |
| `eval_list_knowledge_refs` | `read + write` | Export paged Entity, Relationship, evidence, and Hypothesis refs. |
| `eval_get_knowledge_item` | `read + write` | Fetch one evaluation item. |
| `eval_run_dream_cycle` | `read + write` | Run an evaluation-only dream cycle. |
| `eval_list_recall_feedback_events` | `read + feedback:read` | Export feedback events. |
| `eval_get_recall_feedback_event` | `read + feedback:read` | Fetch one feedback event. |

Evaluation tools remain hidden unless database configuration enables them. They
must not be enabled for production memory.

## Production HTTP Surface

MCP remains the primary external integration, but the same tool registry is
also exposed through the authenticated HTTP tool surface:

```text
GET  /api/v1/tools
GET  /api/v1/tools/:id
POST /api/v1/tools/:name
GET  /api/v1/openapi.json
GET  /api/v1/recall
GET  /api/v1/dreaming/status
GET  /api/v1/dreaming/runs
GET  /api/v1/dreams
GET  /api/v1/dreams/:dreamId
POST /mcp
GET  /mcp
```

Production v2 clients remember through `remember`, poll
`get_memory_placement`, recall through `recall_memory`, and may use
`GET /api/v1/recall` for the canonical HTTP recall response. Direct v1 routes
such as `POST /api/v1/fragments`, `POST /api/v1/claims`,
`POST /api/v1/claims/:id/verify`, `POST /api/v1/claims/:id/promote`, and
`GET /api/v1/facts` are not the v2 client surface.

## Common Request Rules

- JSON is UTF-8.
- Times are RFC 3339; stored canonical timestamps are UTC.
- IDs are opaque; clients cannot derive meaning from their format.
- Write schemas are closed and reject unknown fields.
- Array, string, nesting, and total-byte limits run before model calls.
- Unicode spans use code-point offsets: `start` inclusive and `end` exclusive.
- A request cannot supply or override `team_id`.
- A normal request cannot supply or override `owner_profile_id`; the
  authenticated profile owns new knowledge.
- ID hints are always revalidated against the authenticated team and state.
- Idempotency keys are scoped to team, profile, and operation.

## `remember`

### Request

Only `evidence` is required:

```json
{
  "evidence": [
    {
      "content": "Mark works on Dense-Mem.",
      "source_type": "conversation",
      "source": "conversation:turn-42",
      "authority": "primary",
      "source_group": "conversation:turn-42",
      "idempotency_key": "turn-42",
      "labels": ["project"],
      "metadata": {}
    }
  ],
  "proposal": {
    "entities": [
      {
        "ref": "person_1",
        "name": "Mark",
        "entity_kind": "person",
        "identity_context": {
          "team": "Dense-Mem"
        },
        "aliases": [],
        "known_entity_id": null
      },
      {
        "ref": "project_1",
        "name": "Dense-Mem",
        "entity_kind": "product",
        "identity_context": {},
        "aliases": ["DM"],
        "known_entity_id": null
      }
    ],
    "relationships": [
      {
        "proposal_id": "rel_1",
        "subject_ref": "person_1",
        "predicate": "works_on",
        "object_ref": "project_1",
        "polarity": "+",
        "modality": "statement",
        "evidence": [
          {
            "evidence_index": 0,
            "start": 0,
            "end": 24
          }
        ],
        "valid_from": null,
        "valid_to": null,
        "client_comment": null
      }
    ]
  }
}
```

When `proposal` is absent, Dense-Mem extracts mentions and observations. When
present, it remains a bounded, independently reviewed hint.

### Evidence fields

| Field | Required | Meaning |
| --- | --- | --- |
| `content` | yes | Verbatim source text; maximum 999 Unicode code points per evidence item. |
| `source_type` | no | Origin metadata: `conversation`, `document`, `observation`, or `manual`; it grants no authority. |
| `source` | no | Bounded provenance locator. |
| `authority` | no | `authoritative`, `primary`, `secondary`, `inferred`, or `unknown`. |
| `source_group` | no | Proposed source grouping; server policy derives the identity used for independence. |
| `source_key` | no | Stable source-owner-scoped identity; must appear with `source_revision`. |
| `source_revision` | no | Opaque current revision token for `source_key`. |
| `previous_source_revision` | no | Exact compare-and-set token required when advancing an existing source. |
| `supersedes_fragment_ids` | no | Bounded prior fragment IDs owned by the same source principal to retire explicitly. |
| `idempotency_key` | no | Retry key for this evidence item. |
| `labels` | no | Up to 20 bounded labels. |
| `metadata` | no | Bounded source-policy-approved JSON. |

The target accepts 1–20 evidence items. Split larger source material into
multiple semantic evidence items rather than truncating it. Only authorized
actors can submit authoritative user evidence. `manual` does not mean
authoritative, and two matching strings do not prove two independent sources.

### Source revision rules

Clients omit source-revision fields when they cannot prove lineage. `source_key`
and `source_revision` appear together; `previous_source_revision` and
`supersedes_fragment_ids` are invalid without that pair. Unkeyed evidence never
silently replaces earlier evidence.

```json
{
  "evidence": [
    {
      "content": "The handbook now requires hardware-backed MFA.",
      "source_type": "document",
      "source_key": "employee-handbook",
      "source_revision": "rev-43",
      "previous_source_revision": "rev-42",
      "supersedes_fragment_ids": ["fragment_rev42_mfa"],
      "idempotency_key": "employee-handbook:rev-43:mfa"
    }
  ]
}
```

The canonical source identity is scoped to team plus immutable authenticated
source owner. The first revision omits `previous_source_revision`; every later
revision must match the current token. Source activation occurs at intake and
makes the prior revision ineffective team-wide even if the replacement is later
quarantined or fails semantic placement. A mismatch or unowned fragment ID
changes no source or knowledge state.

All evidence items for one `source_key` in a request must carry the same
revision fields and activate as one bounded batch. An idempotent retry of the
same revision returns the existing intake result; reusing that revision token
with different content is a conflict and changes no state.

### Entity proposal fields

| Field | Required | Meaning |
| --- | --- | --- |
| `ref` | yes | Request-local endpoint token. |
| `name` | yes | Evidence surface, not a durable ID. |
| `entity_kind` | no | Coarse classification hint. |
| `identity_context` | no | Bounded evidence-supported discriminator map. |
| `aliases` | no | Evidence-supported alternate names; metadata only. |
| `known_entity_id` | no | Server-issued candidate hint; revalidated. |

The target accepts 0–100 Entity hints. Duplicate refs are invalid. Duplicate
names are valid because homonyms exist. Canonical behavior is in
[Entity Identity And Resolution](Entity-Identity-And-Resolution).

### Relationship proposal fields

| Field | Required | Meaning |
| --- | --- | --- |
| `proposal_id` | yes | Request-local stable ref. |
| `subject_ref` | yes | Entity proposal ref. |
| `predicate` | yes | Surface or lower-snake-case predicate hint. |
| `object_ref` | one object form | Entity proposal ref. |
| `object_value` | one object form | Typed scalar, mutually exclusive with `object_ref`. |
| `polarity` | no | `+` or `-`; default `+`. |
| `modality` | no | `statement`, `question`, `proposal`, `speculation`, or `quoted`. |
| `evidence` | yes | One or more exact spans from this request. |
| `valid_from` / `valid_to` | no | Evidence-supported real-world validity bounds. |
| `client_comment` | no | Non-authoritative extraction note. |

The target accepts 0–200 Relationship hints. Questions, proposals,
speculation, and quotations do not become active knowledge merely because they
were submitted.

An `object_value` contains one of `string`, `number`, `boolean`, `date`, or
`date_time` plus a canonical value. `display` and `unit` are optional.

The following are forbidden request fields:

```text
entity_id allocation
relationship_id allocation
owner_profile_id selection
predicate definition
relationship_kind
current_cardinality
tier
status
support or source counts
verification outcome
```

### Intake response

```json
{
  "ingest_id": "ing_01J...",
  "processing_state": "queued",
  "check_after_seconds": 3,
  "status_tool": "get_memory_placement",
  "correlation_id": "req_01J..."
}
```

This means evidence intake committed. It does not claim semantic placement or
vector-search readiness.

## Placement Contract

### Poll request

```json
{
  "ingest_id": "ing_01J..."
}
```

### Poll response

```json
{
  "ingest_id": "ing_01J...",
  "processing_state": "completed",
  "search_state": "current",
  "items": [
    {
      "item_id": "item_1",
      "evidence_id": "e1",
      "category": "evidence_processed",
      "search_state": "current",
      "relationship_outcomes": [
        {
          "proposal_id": "rel_1",
          "observation_id": "obs_1",
          "relationship_id": "r1",
          "owner_profile_id": "profile-a",
          "tier": "fact",
          "relationship_status": "active",
          "category": "relationship_fact",
          "reason": "Entailed authoritative user evidence.",
          "review_task": null
        }
      ],
      "errors": []
    }
  ],
  "errors": []
}
```

Processing states:

| State | Meaning |
| --- | --- |
| `queued` | evidence is durable and waiting |
| `processing` | a worker owns a renewable lease |
| `awaiting_review` | durable review tasks need a client decision |
| `completed` | every item has a durable outcome |
| `failed` | orchestration failed visibly |

Search states:

| State | Meaning |
| --- | --- |
| `not_required` | no eligible vector search document exists |
| `pending` | semantic state committed; current embedding work remains |
| `current` | vectors match current source and embedding-contract versions |
| `failed` | embedding retries exhausted or need operator action |

Search state affects vector freshness, not Relationship truth or PostgreSQL
SemanticEdge visibility.

Top-level placement items are stable evidence-processing items. Each item may
contain zero or more `relationship_outcomes[]`; there is no preferred
Relationship for an evidence item. Relationship outcomes preserve every bounded
observation result that passed extraction and verification.

Top-level evidence item categories:

| Category | Consumer behavior |
| --- | --- |
| `evidence_processed` | evidence is durable and all bounded semantic outcomes are listed |
| `evidence_quarantined` | exclude from model-backed paths and inspect operationally |
| `processing_failed` | show bounded safe error and retry guidance |

Relationship outcome categories:

| Category | Consumer behavior |
| --- | --- |
| `relationship_fact` | active Relationship at fact tier |
| `relationship_validated_claim` | active supported Relationship below fact tier |
| `relationship_pending_evidence` | retained candidate; exclude from normal recall; it may be eligible for dreaming |
| `relationship_needs_review` | present the exact Relationship conflict/scope question |
| `predicate_needs_review` | present original wording and server options; no Relationship exists yet |
| `relationship_rejected` | do not treat as true |
| `identity_needs_review` | resolve identity before dependent knowledge can activate |

A relationship outcome may lack `relationship_id` when unresolved identity or
predicate, failed extraction, or invalid verification prevents canonical
Relationship creation. An evidence item may have no Relationship outcomes and
still be current searchable evidence when it is safe and not superseded.

The strict internal verifier schema is not a public field. Its only canonical
definition is
[Memory Write Pipeline](Write-Pipeline#strict-verifier-contract).

## `resolve_memory_placement`

Example:

```json
{
  "ingest_id": "ing_01J...",
  "placement_item_id": "item_1",
  "action": "select_entity",
  "decision": {
    "mention_ref": "person_1",
    "entity_id": "ent_mark_huang"
  },
  "evidence": [
    {
      "content": "I mean Mark Huang on the Dense-Mem team.",
      "source_type": "manual",
      "authority": "authoritative",
      "source_group": "user-resolution:ing_01J..."
    }
  ],
  "idempotency_key": "resolution-1"
}
```

| Action | Required input | Result |
| --- | --- | --- |
| `acknowledge` | completed ingest | records acknowledgement only |
| `select_entity` | mention ref, supplied candidate ID, and evidence where required | appends identity decision and requeues affected observations |
| `confirm_new_entity` | mention ref plus distinguishing evidence | permits a fresh create decision; client allocates no ID |
| `select_predicate` | observation ref and supplied registered predicate | appends vocabulary review decision and reprocesses |
| `accept` | review item plus authoritative evidence | appends evidence and reruns strict placement |
| `reject` | review item plus reason/evidence | records rejection without deleting history |
| `correct` | replacement evidence and optional proposal | appends and reprocesses corrected input |
| `release_quarantine` | quarantined placement item plus bounded reason; manager role and `write` scope | appends a security release and requeues the evidence as guarded |
| `forget` | target Relationship ID, authority, and reason | retracts active current state; preserves nodes and append-only ledger |

No action writes a graph side channel or directly forces `fact`. Merge, split,
supersede, support changes, and forget target caller-owned records only. A
correction of another profile enters through caller-owned `remember` evidence
and produces a cross-reference; it cannot edit the target.

Quarantine release is not a semantic mutation shortcut. It preserves every scan
and model signal, never changes the evidence bytes, and re-enters the same
pipeline at `guarded`. Public placement output exposes a generic quarantine
reason and review handle; detailed detector rules remain restricted audit data.

Conflict, split, and forget behavior is defined in
[Conflicts And Corrections](Conflicts-And-Corrections).

## `correct_entity_resolution`

Every profile with `write` scope may dry-run and apply a merge or split for its
own observations. Entity handles remain team-visible, but the selected
observation IDs must all be owned by the authenticated profile.

Dry-run request:

```json
{
  "operation": "split",
  "source_entity_id": "ent_mark",
  "target_entity_id": null,
  "owned_observation_ids": ["obs_billing_1"],
  "evidence": [
    {
      "content": "The Billing Mark is a different person from Mark on Dense-Mem.",
      "source_type": "manual",
      "authority": "authoritative",
      "source_group": "identity-review:42"
    }
  ],
  "dry_run": true,
  "idempotency_key": "split-mark-billing-1"
}
```

For `merge`, `target_entity_id` is one server-supplied same-team survivor. For
`split`, it is null; the official reviewer/verifier decides whether the evidence
supports a distinct Entity and Dense-Mem allocates the ID.

The dry run returns selected/blocked observation IDs, caller-owned Relationship
changes, new/reused Entity candidates, cross-profile references that remain
unchanged, and an expiring `impact_token`. Apply repeats the request with
`dry_run=false` and that token. If versions or ownership changed, apply fails and
requires another dry run.

The operation cannot select another profile's observation, redirect an Entity
globally, or supersede another profile's Relationship. Mechanics and invariants
belong in
[Entity Identity And Resolution](Entity-Identity-And-Resolution#merge-and-split-corrections).

## `recall_memory`

### Request

```json
{
  "query": "Who works on Dense-Mem?",
  "limit": 10,
  "valid_at": null,
  "known_at": null,
  "known_evidence_ids": ["550e8400-e29b-41d4-a716-446655440002"],
  "expand_from_entity_ids": ["550e8400-e29b-41d4-a716-446655440000"],
  "known_relationship_ids": ["550e8400-e29b-41d4-a716-446655440001"]
}
```

| Field | Required | Limit/default |
| --- | --- | --- |
| `query` | yes | 1–512 Unicode code points |
| `limit` | no | 1–50; default 10 |
| `valid_at` | no | real-world RFC 3339 time |
| `known_at` | no | system-knowledge RFC 3339 time |
| `known_evidence_ids` | no | up to 200 unique evidence UUIDs already seen by the client; suppressed from returned `results[]` |
| `expand_from_entity_ids` | no | up to 50 unique Entity UUIDs for focused follow-up expansion |
| `known_relationship_ids` | no | up to 200 unique Relationship UUIDs already seen by the client; used as traversal context and suppressed from returned results |

### Evidence response

```json
{
  "recall_id": "rec_550e8400-e29b-41d4-a716-446655440010",
  "results": [
    {
      "evidence_id": "e1",
      "context": "Mark works on Dense-Mem."
    }
  ],
  "discovery_paths": [
    {
      "relationships": [
        {
          "relationship_id": "r1",
          "subject": {
            "entity_id": "ent_mark",
            "name": "Mark"
          },
          "predicate": "works_on",
          "object": {
            "entity_id": "ent_dense_mem",
            "name": "Dense-Mem"
          },
          "polarity": "+"
        }
      ],
      "evidence_ids": ["e1"]
    }
  ],
  "discovery_guidance": "If you are uncertain or have concerns about any specific part, call recall_memory again with a focused query. Pass known_evidence_ids and known_relationship_ids from this response to avoid repeated context, and pass expand_from_entity_ids when you want more context around specific entities. Use trace_memory for supporting evidence or history.",
  "related_hypotheses": []
}
```

Recall is evidence-primary. `results[]` is the ranked answer context, bounded
by the server context budget. `discovery_paths[]` carries compact, separately
selected Relationship paths for follow-up discovery near the returned evidence
or requested expansion entities; it is not extra answer context and does not
affect direct top-K scoring. `known_evidence_ids` and `known_relationship_ids`
change only request-local retrieval context and never mutate stored state.

`subject` is always an Entity. `object` is a tagged union: an Entity with
`entity_id`/`name` or a typed Value with `value_id`, `type`, canonical `value`,
and optional `display`/`unit`.

Recall eligibility, branch flow, and degradation belong in
[Recall Ranking](Recall-Ranking). Cosine distance, RRF, and reranking belong in
[Vector Search And Ranking](Vector-Search-And-Ranking). Exact/ANN execution and
index versions belong in [ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec).
Embedding models, index/ranking profile versions, branch scores, and freshness
diagnostics are internal telemetry. They are intentionally absent from the
public recall response because a client needs context and stable trace handles,
not retrieval implementation details.

## `trace_memory`

Request:

```json
{
  "relationship_id": "r1",
  "include_evidence_content": true,
  "include_verification": true,
  "include_transitions": true,
  "max_depth": 2,
  "max_edges": 24,
  "predicate_keys": [],
  "topic": null,
  "min_relevance": null
}
```

| Field | Range/default |
| --- | --- |
| `relationship_id` | required same-team Relationship ID |
| `include_evidence_content` | default true; false returns handles/spans |
| `include_verification` | default true |
| `include_transitions` | default true |
| `max_depth` | 0–4; default 1 |
| `max_edges` | 0–100; default 24 |
| `predicate_keys` | up to 30 |
| `topic` | up to 256 Unicode code points |
| `min_relevance` | 0–1 |

Response sections:

```text
relationship
observations[]
evidence_supports[]
support_decision_events[]
evidence_fragments[]
verification_events[]
transitions[]
conflicts[]
cross_profile_references[]
identity_corrections[]
supersession_lineage[]
semantic_nodes[]
semantic_edges[]
visited_entity_ids[]
stopped_reason
```

Trace reads authoritative normalized history and may add bounded current
SemanticEdges from PostgreSQL. It never reconstructs history from edge
properties.

## Dream Tools

`list_dreams` returns bounded summaries with `hypothesis_id`, edge-shaped
existing endpoint/predicate refs, status, source refs, and generation metadata.
`get_dream` returns one full Hypothesis after same-team authorization.

`resolve_dream_feedback` accepts non-semantic feedback:

```json
{
  "hypothesis_id": "hyp_01J...",
  "decision": "reject",
  "reason": "Current architecture makes this connection inapplicable."
}
```

`confirm_true` or `confirm_false` requires independent user/external evidence
and returns a normal `ingest_id`. The Hypothesis cannot be its own evidence.

## User Portal Memory Views

The authenticated first-party user portal reads the same PostgreSQL semantic
view used by MCP-backed services. It provides bounded recall, Entity/Value
graph exploration, Relationship detail/trace, and placement review.

The graph shows Entity/Value nodes and SemanticEdges. Evidence, verification,
and transitions appear in detail/trace panels. Hypotheses are an opt-in,
visually distinct overlay and never edges in the primary knowledge graph.

Portal queries are not raw SQL/Cypher and never return embeddings. Portal
behavior and key administration belong in
[Portals And API Keys](Portals-And-API-Keys).

## Product Interfaces

| Interface | Audience | Purpose |
| --- | --- | --- |
| MCP `/mcp` | authenticated LLM hosts | discover and execute memory tools |
| user portal `/ui` | authenticated team users | first-party recall, graph/trace, review, keys, and self telemetry |
| control portal | deployment operators on the private listener | configuration, migration, security, and operations |
| health/readiness | deployment infrastructure | liveness and dependency readiness only |

MCP runtime discovery reflects scopes and feature gates. Portal handlers expose
only the bounded view models required by their own UI.

## Error Contract

MCP protocol errors use the negotiated JSON-RPC structure. Tool/domain failures
carry bounded safe metadata such as:

```json
{
  "code": "VERIFIER_RESPONSE_INVALID",
  "message": "The verifier did not return the required complete response.",
  "retryable": true,
  "retry_after_seconds": 5,
  "correlation_id": "req_01J...",
  "details": {
    "ingest_id": "ing_01J...",
    "attempts_exhausted": 3
  }
}
```

Public errors never include credentials, provider prompts/responses, raw SQL,
vectors, or stack traces. `details` is code-specific and bounded.

## Authentication Boundary

Authentication fixes team, profile, role, and scopes before tool lookup or
service execution.

- role controls administrative authority
- scope controls operation access
- manager role does not imply `write`
- request `team_id` values cannot replace authenticated context
- every input/output ID is checked against the team without revealing
  cross-team existence

The full lifecycle is in
[Request Lifecycle And Security](Request-Lifecycle-And-Security).

## Provider Data Egress

| Provider | Target payload |
| --- | --- |
| embedding | evidence/search document text or recall query under configured policy |
| structure reviewer | submitted evidence and optional client proposal |
| Entity/Relationship verifier | evidence, extracted items, immutable Entity candidate sets, and predicate registry options; no stored Relationships or source policy |
| dream generator | compact allowed source Relationships/candidates plus existing endpoint and predicate IDs |

Providers receive no API keys, password hashes, Redis contents, raw database
access, unrestricted team graphs, or other-team evidence. Existing knowledge is
not verifier input and cannot become source evidence. Dreaming receives only its
separately bounded eligible inputs and has no knowledge-write capability.

Provider configuration belongs in
[Configuration](Configuration#reviewer-and-verifier).

## Canonical References

| Concern | Canonical page |
| --- | --- |
| authentication, tenant resolution, transport, and visibility | [Request Lifecycle And Security](Request-Lifecycle-And-Security) |
| write stages, strict verifier response, regeneration, and failure | [Memory Write Pipeline](Write-Pipeline) |
| Entity reuse, creation, ambiguity, merge, and split | [Entity Identity And Resolution](Entity-Identity-And-Resolution) |
| predicate and Relationship vocabulary/lifecycle | [Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle) |
| recall eligibility, PostgreSQL adjacency, and degradation | [Recall Ranking](Recall-Ranking) |
| vector distance, RRF, deterministic reranking, and conflict preference | [Vector Search And Ranking](Vector-Search-And-Ranking) |
| PostgreSQL, `pgvector`, Redis, queues, and recovery | [Data Model And Storage](Data-Model-And-Storage) |
| conflict, correction, predicate review, and forget | [Conflicts And Corrections](Conflicts-And-Corrections) |

## Contract Risks

| Risk | Concrete failure mode | Mitigation |
| --- | --- | --- |
| Stale tool discovery | an MCP host caches an older schema and sends removed recall fields | strict `additionalProperties=false` schemas and client refresh guidance |
| Client controls truth | request sends tier/status/predicate definition and bypasses policy | closed write schemas reject all lifecycle-control fields |
| Candidate appears confirmed | consumer renders `relationship_pending_evidence` with facts | distinct category, no default recall eligibility, and typed client fixtures |
| Search mistaken for commit | client retries `remember` because a vector is pending | separate intake/processing/search fields and idempotency keys |
| Alias becomes edge | client proposal creates an alias Relationship | aliases exist only on Entity hints and name storage |
| Oversized trace | a hub Entity exhausts response or host context | depth, edge, content, time, and character budgets plus `stopped_reason` |
| Forget is treated as deletion | client expects audit/evidence to vanish | explicit retraction semantics and separate privacy-erasure workflow |

## Contract Acceptance

Version 2 is incomplete until:

1. generated MCP tool schemas match the committed examples and enums
2. unknown write fields and forbidden lifecycle fields fail validation
3. all semantic results use `relationship_id`
4. candidate categories never claim active status
5. default recall contains evidence contexts but no verifier history,
   candidate Relationship, or Hypothesis in `results[]`
6. trace returns exact spans and lifecycle/correction lineage
7. processing and search states vary independently
8. cross-team ID hints fail without revealing existence
9. every MCP tool enters only canonical V2 services and PostgreSQL
10. every public error has a correlation ID and bounded safe details
11. `tools/list` visibility and `tools/call` execution agree for every scope and
    feature combination
