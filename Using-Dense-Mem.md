# Using Dense-Mem

Dense-Mem exposes its external memory tools through MCP. Contract V2 accepts
evidence, optionally accepts structured hints, and processes semantic placement
asynchronously. The user and control portals are first-party product
interfaces, not alternative memory automation contracts.

This is the user workflow. Exact fields and limits belong in
[Technical Reference](Technical-Reference).

## Normal Write And Read Flow

```mermaid
%%{init: {"sequence": {"rightAngles": true}}}%%
sequenceDiagram
  actor User
  participant Host as AI client
  participant DM as Dense-Mem

  User->>Host: Remember this project information
  Host->>DM: remember(evidence, optional proposal)
  DM-->>Host: ingest_id and queued
  Host->>DM: get_memory_placement(ingest_id)
  DM-->>Host: processing state, search state, outcomes
  alt review required
    Host->>User: Present exact ambiguity/conflict and choices
    User-->>Host: Identity choice or new evidence
    Host->>DM: resolve_memory_placement(...)
  end
  Host->>DM: recall_memory(query)
  DM-->>Host: Ranked evidence contexts and discovery paths
  opt support/history needed
    Host->>DM: trace_memory(relationship_id)
    DM-->>Host: Evidence and decision history
  end
```

`remember` acknowledges durable evidence intake. It does not promise immediate
semantic acceptance or a current vector. `processing_state=completed` can
coexist with `search_state=pending`; committed full-text/adjacency/current-state
reads remain distinct from vector freshness.

## What To Ask

| Goal | Example prompt |
| --- | --- |
| Save knowledge | “Remember this evidence: Mark works on Dense-Mem. Dense-Mem uses PostgreSQL.” |
| Recall context | “Before editing, recall Dense-Mem’s database architecture.” |
| Follow Relationships | “Show Relationships connected to Dense-Mem.” |
| Inspect support | “Trace the database Relationship and show accepted evidence.” |
| Resolve identity | “Show placements waiting for me to identify which Mark.” |
| Correct knowledge | “That deployment target is wrong; record this corrected evidence.” |
| Forget an edge | “Forget the selected Relationship, but preserve audit history.” |
| Review speculation | “List related dreams and keep them labeled Hypotheses.” |

## Evidence First

Smallest write:

```json
{
  "evidence": [
    {
      "content": "Mark works on Dense-Mem. Dense-Mem uses PostgreSQL.",
      "source_type": "conversation",
      "source": "conversation:project-overview",
      "source_group": "conversation:project-overview",
      "idempotency_key": "project-overview-turn-1"
    }
  ]
}
```

Dense-Mem extracts Entity mentions and Relationship observations. A `proposal`
can reduce extraction ambiguity, but remains a hint.

Do not:

- use a name as `entity_id` or invent server IDs
- claim source independence when items share an origin
- send a sentence as one Entity
- set predicate definitions, tier, status, promotion, or lifecycle behavior
- model an alias as an edge
- omit known discriminators for a name such as `Mark`

## Structured Hint Example

```json
{
  "evidence": [
    {
      "content": "Mark works on Dense-Mem. Dense-Mem uses PostgreSQL.",
      "source_type": "conversation",
      "source_group": "conversation:project-overview"
    }
  ],
  "proposal": {
    "entities": [
      {
        "ref": "mark",
        "name": "Mark",
        "entity_kind": "person",
        "identity_context": {
          "team": "Dense-Mem"
        }
      },
      {
        "ref": "dense_mem",
        "name": "Dense-Mem",
        "entity_kind": "product",
        "aliases": ["DM"]
      },
      {
        "ref": "postgresql",
        "name": "PostgreSQL",
        "entity_kind": "product"
      }
    ],
    "relationships": [
      {
        "proposal_id": "mark_works_on_dense_mem",
        "subject_ref": "mark",
        "predicate": "works_on",
        "object_ref": "dense_mem",
        "polarity": "+",
        "modality": "statement",
        "evidence": [
          {
            "evidence_index": 0,
            "start": 0,
            "end": 23
          }
        ]
      },
      {
        "proposal_id": "dense_mem_uses_postgresql",
        "subject_ref": "dense_mem",
        "predicate": "uses",
        "object_ref": "postgresql",
        "polarity": "+",
        "modality": "statement",
        "evidence": [
          {
            "evidence_index": 0,
            "start": 25,
            "end": 50
          }
        ]
      }
    ]
  }
}
```

Spans use Unicode code-point offsets, start inclusive and end exclusive. The
reviewer may split/reject/normalize hints, and the one-call verifier still
resolves Entities and evaluates Relationships.

## Read The Terminology

```text
Entity record       stable real-world identity
entity_id           opaque identifier for that Entity
Entity node         graph-shaped view of the Entity record
Predicate           registered meaning such as works_on
Relationship        one profile's canonical subject-predicate-object lifecycle
SemanticEdge        active validated/fact Relationship as a graph read
Cross-reference     same-team confirmation/challenge/correction link; not an edge
Observation         what one evidence submission expressed
VerificationEvent  final schema-valid verifier judgment
Hypothesis          isolated dream speculation
```

An edge and a Relationship are closely related but not interchangeable:
Relationship includes lifecycle/history references; SemanticEdge is only its
eligible compact graph shape. Full definitions belong in
[Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle).

## Entity Reuse And Homonyms

Existing memory:

```text
entity_id: ent_dense_mem
canonical name: Dense-Mem
alias: DM
```

New evidence:

```text
DM uses Redis.
```

Expected: reuse `ent_dense_mem`, preserve/add `DM` as supported name metadata,
and create/update `uses`. No `DM` node or alias edge.

For `Mark`:

- one materially plausible supplied candidate and no contradiction → reuse
- multiple plausible candidates → `identity_needs_review`
- later contradictory context → show ambiguity and split impact to the client
- confirmed different person → create a new team-visible Entity and move only
  selected caller-owned observations

The client must not silently choose or create from display text.

## Placement Outcomes

Poll `get_memory_placement` and inspect:

- processing: `queued`, `processing`, `awaiting_review`, `completed`, `failed`
- vector search: `not_required`, `pending`, `current`, `failed`
- per-item category, reason, IDs, tier/status, and review task

Important categories:

| Category | What the client does |
| --- | --- |
| `relationship_fact` / `relationship_validated_claim` | may use in normal recall according to relevance |
| `relationship_pending_evidence` | do not present as known; excluded from default recall |
| `identity_needs_review` | present supplied candidates and discriminator question |
| `predicate_needs_review` | show original wording and server-supplied registry options |
| `relationship_needs_review` | show current/proposed evidence and impact |
| `relationship_rejected` / `evidence_quarantined` / `processing_failed` | do not use as knowledge; follow safe guidance |

The canonical table is
[Technical Reference](Technical-Reference#placement-contract).

## Resolve A Review

Use the exact returned item and candidate:

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

The decision is durable input and re-enters strict processing. It never edits
an edge directly. See
[Conflicts And Corrections](Conflicts-And-Corrections).

## Recall Evidence Context

```json
{
  "query": "Dense-Mem database architecture",
  "limit": 10,
  "known_evidence_ids": [],
  "known_relationship_ids": [],
  "expand_from_entity_ids": []
}
```

Default recall returns same-team active-supported evidence contexts in
`results[]`. Relationships remain the durable semantic graph but appear only as
compact `discovery_paths[]` for trace and follow-up. Recall excludes verifier
history and pending candidates from primary results. Server policy may include
matching Hypotheses in `related_hypotheses`. Required `conflicts[]` is empty
when no material disagreement applies. An open/overdue case returns bounded
alternatives and a question; a resolved case returns preferred current evidence
plus a compact status/trace reference. Recall has no `raw_evidence` field:
evidence text enters `results[]` only after final support-path eligibility.

Use bounded `discovery_paths` for discovery; do not recursively fetch every
neighbor. If one part remains uncertain, make a narrower recall and pass the
evidence UUIDs already seen in `known_evidence_ids` plus Relationship UUIDs
already seen in `known_relationship_ids` to expose different context,
optionally with `expand_from_entity_ids` for specific anchors. The returned
`discovery_guidance` states this behavior. See [Recall Ranking](Recall-Ranking).

## Trace Evidence

```json
{
  "relationship_id": "r1",
  "include_evidence_content": true,
  "include_verification": true,
  "include_transitions": true,
  "max_depth": 2,
  "max_edges": 24,
  "predicate_keys": ["uses", "works_on"],
  "topic": "Dense-Mem architecture"
}
```

Trace loads the Relationship, accepted spans, VerificationEvents, transitions,
conflicts/corrections, and bounded current SemanticEdges. Use trace for sources,
conflict, correction, forget, or high-stakes answers.

## Correct And Forget

A correction supplies new evidence and appends history. It does not edit the
old fragment. A correction to another profile creates caller-owned knowledge
and a cross-reference during placement; it never directly changes the other
profile's record. When recall returns an open Conflict case, place its
`conflict_id` and expected version on the relevant Relationship proposal in a
normal `remember` call. Dense-Mem derives the position from the new verified
evidence; there is no bare vote or cross-profile resolve action. Nightly
Conflict review owns any automatic stale-state closure.

`forget` takes a caller-owned `relationship_id` and authorized reason. It appends a
retraction, removes that Relationship from active/default reads in the same
PostgreSQL transaction, and keeps Entity/Value nodes plus evidence and decision
history.

Every write-scoped profile may merge or split its own Entity resolutions after
a dry-run impact preview. It cannot move another profile's observations. See
[Conflicts And Corrections](Conflicts-And-Corrections#cross-profile-corrections).

## Communities And Dreams

Community fill is an opt-in discovery aid that fills unused slots only.

Dreams are Hypotheses between existing endpoints using registered predicates.
They appear separately. Confirming one requires new independent evidence
through `remember`. Dreaming cannot create an Entity, Value, predicate,
Relationship, support, or active edge.

See [Community Memory](Community-And-Dreaming) and [Dreaming](Dreaming).

## Teams, Roles, And Scopes

| Term | Meaning |
| --- | --- |
| Team | top-level memory isolation boundary |
| Profile | named client identity inside one team |
| Manager | same-team administration; still needs operation scope |
| Member | normal memory use |
| `read` | recall and inspect |
| `write` | remember and resolve; merge/split/supersede own knowledge; create evidence-backed corrections to other profiles |
| `feedback:read` | authorized stored-feedback inspection |

Use read-only keys for automation that must never mutate memory.

## Risk

| Failure mode | Mitigation |
| --- | --- |
| client treats pending candidate as a fact | use typed categories and default recall only |
| client guesses which Mark | present server candidates and collect discriminator evidence |
| client assumes vector pending means write failed | distinguish processing from search state and trace by ID |
| client promotes a dream from plausibility | require independent evidence through normal `remember` |
