# Technical Reference

This page defines public memory contract `dense-mem.v2.3.2`. The external memory
contract is discovered and executed through MCP:

```text
MCP transport:  /mcp
discovery:      tools/list
execution:      tools/call
```

## Contract Principles

1. evidence is required; structured proposals are optional hints
2. clients cannot choose canonical IDs, predicate definitions, support,
   lifecycle status, or verification outcome
3. aliases are Entity-name metadata, not Relationships
4. `relationship_id` is the stable semantic and trace handle
5. normal recall is compact; evidence/history require evidence or trace reads
6. processing state and vector-search freshness are separate
7. pending Relationships are never active/default recall results
8. every operation is authorized from the authenticated principal
9. unknown fields on writes and provider/storage failures are rejected visibly
10. knowledge is team-readable and profile-authored; cross-profile correction
    creates caller-owned records and immutable references, while nightly Conflict
    review owns automatic stale-state closure
11. MCP is the supported external memory integration; user and control portals
    are first-party product interfaces

## MCP Tool Catalog

The LLM host obtains this catalog with `tools/list` and invokes one entry with
`tools/call`. Method purpose and portal boundaries are defined in
[Request Lifecycle And Security](Request-Lifecycle-And-Security#mcp-memory-interface).

### Memory

| Tool | Scope | Purpose |
| --- | --- | --- |
| `remember` | `write` | Persist evidence, optional Entity/Relationship hints, and optional owner-authorized direct supersession targets. |
| `get_memory_placement` | `read` | Poll processing/search state and inspect outcomes or review tasks. |
| `retract_evidence` | `write` | Retract caller-owned current evidence without deleting its provenance or trace lineage. |
| `resolve_memory_placement` | `write` | Supply a client decision, new evidence, correction, rejection, or forget request. |
| `correct_entity_resolution` | `write` | Dry-run or apply a merge/split limited to caller-owned observations and Relationships. |
| `recall_memory` | `read` | Retrieve bounded evidence contexts plus separate Relationship, Community, Conflict, and Hypothesis frontiers. |
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

Semantic memory tools are MCP-only. `remember`, `get_memory_placement`,
`retract_evidence`, `resolve_memory_placement`, `correct_entity_resolution`,
`recall_memory`, and `trace_memory` are advertised by MCP `tools/list` and invoked by MCP
`tools/call`. Removed HTTP catalog/executor routes must stay absent so the
service does not expose a second public memory contract.

```text
POST /mcp
GET  /mcp
```

The registry remains the schema/executor source of truth, but transport
visibility is explicit and checked independently by discovery and execution.
First-party user/control portal handlers may use bounded server-owned browser
endpoints; those endpoints are private product implementation, not an external
memory API and not an alternate way to call an MCP tool.

```text
/ui/api/*       first-party user-portal routes
/control/api/*  private control-portal routes
```

There is no supported public REST memory API and no supported `/api/v1` route.

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
        "conflict_context": null,
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
| `supersedes_evidence_ids` | no | One to fifty unique UUIDs for caller-owned current evidence retired by this replacement. Requires this item's `idempotency_key`. |
| `idempotency_key` | no | Retry key for this evidence item. |
| `labels` | no | Up to 20 bounded labels. |
| `metadata` | no | Bounded source-policy-approved JSON. |

The target accepts 1–20 evidence items. Split larger source material into
multiple semantic evidence items rather than truncating it. Only authorized
actors can submit authoritative user evidence. `manual` does not mean
authoritative, and two matching strings do not prove two independent sources.

### Source revision rules

Clients omit source-revision fields when they cannot prove lineage. `source_key`
and `source_revision` appear together; `previous_source_revision` is the exact
compare-and-set token for advancing that source. Unkeyed evidence never silently
replaces earlier evidence.

```json
{
  "evidence": [
    {
      "content": "The handbook now requires hardware-backed MFA.",
      "source_type": "document",
      "source_key": "employee-handbook",
      "source_revision": "rev-43",
      "previous_source_revision": "rev-42",
      "idempotency_key": "employee-handbook:rev-43:mfa"
    }
  ]
}
```

The canonical source identity is scoped to team plus immutable authenticated
source owner. The first revision omits `previous_source_revision`; every later
revision must match the current token. Source activation occurs at intake and
makes the prior revision ineffective team-wide even if the replacement is later
quarantined or fails semantic placement. A mismatch changes no source or
knowledge state.

All evidence items for one `source_key` in a request must carry the same
revision fields and activate as one bounded batch. An idempotent retry of the
same revision returns the existing intake result; reusing that revision token
with different content is a conflict and changes no state.

### Direct evidence lifecycle

`supersedes_evidence_ids` is a direct, owner-authorized evidence lifecycle
operation. It does not require a source revision, but it cannot be combined
with `previous_source_revision`. Every target must be a current evidence UUID
owned by the authenticated profile; duplicate targets across one intake are
rejected. The server writes an append-only supersession event and retires the
target atomically with the replacement intake, even if the replacement later
fails placement or is quarantined.

```json
{
  "evidence": [
    {
      "content": "The handbook now requires phishing-resistant MFA.",
      "source_type": "manual",
      "supersedes_evidence_ids": ["<owned-current-evidence-uuid>"],
      "idempotency_key": "handbook-mfa-correction-20260729"
    }
  ]
}
```

Use `retract_evidence` when there is no replacement:

```json
{
  "evidence_ids": ["<owned-current-evidence-uuid>"],
  "reason": "The source was withdrawn.",
  "idempotency_key": "handbook-source-withdrawn-20260729"
}
```

It returns an append-only `decision_id`, the retracted evidence IDs, and the
affected/pending/retained-Relationship counts. Neither lifecycle action
physically deletes evidence or trace lineage. Current recall excludes retired
evidence; a `known_at` view before the event can still inspect historical state.

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
| `valid_from` / `valid_to` | no | Evidence-supported real-world validity bounds supplied as assessor hints. |
| `correction_target` | no | Optional same-team Relationship target containing `relationship_id` and `expected_version`; it anchors an explicit correction but grants no winner or mutation authority. |
| `conflict_context` | no | Response context containing one same-team `conflict_id` and `expected_version`; it requests re-evaluation but grants no decision authority. |
| `client_comment` | no | Non-authoritative extraction note. |

The target accepts 0–200 Relationship hints. Questions, proposals,
speculation, and quotations do not become active knowledge merely because they
were submitted.

`conflict_context` is scoped to one Relationship proposal so one bounded batch
may respond to different cases without ambiguity:

```json
{
  "conflict_context": {
    "conflict_id": "conflict_01J...",
    "expected_version": 3
  }
}
```

Both fields are required when `conflict_context` is present;
`expected_version` is a positive integer. The ID/version must come from
same-team `recall_memory` or `trace_memory`. Dense-Mem verifies the current
version and proves that the placed Relationship is materially part of that case.
A stale version, wrong team, dismissed case, or unrelated proposal rejects the
complete `remember` request before evidence or case state commits. The server
derives the matching or new position from verified semantics. Clients cannot
submit a position ID, winner, status, score, deadline, or bare confirmation.

`correction_target` is separate from `conflict_context`. It is used when the
client can identify the specific same-team Relationship being corrected, such as
from a prior `trace_memory` result:

```json
{
  "correction_target": {
    "relationship_id": "rel_01J...",
    "expected_version": 7
  }
}
```

Dense-Mem revalidates team, version, visibility, and semantic relatedness after
assessment. A stale, cross-team, or unrelated target rejects the complete
`remember` request before evidence commits. The target ID can help the server
derive `corrects` or `challenges`; it cannot set the target's status,
validity, support, or conflict winner.

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
status
support or source counts
verification outcome
conflict status, preferred position, or effective disposition
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
          "relationship_status": "active",
          "category": "relationship_active",
          "reason": "Entailed authoritative user evidence.",
          "assessment": {
            "confidence_band": "meets_write_threshold",
            "reason": "Entailed and met the current team automatic-write threshold.",
            "assessment_policy_version": "one_call_v1"
          },
          "review_task": null,
          "conflict": {
            "conflict_id": "conflict_01J...",
            "version": 3,
            "status": "open",
            "position_id": "position_release",
            "review_due_at": "2026-02-26T00:00:00Z"
          }
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
| `awaiting_review` | one or more durable open/acknowledged review tasks need a client decision |
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
observation result that passed integrated assessment.

`conflict` is optional and appears when that Relationship opened or joined a
durable cross-profile case. Placement returns `open` or `overdue`; `resolved`
appears after nightly Conflict review. A dismissed case remains trace-only. An
open team conflict does not make an otherwise complete caller-owned placement
`awaiting_review`: the Relationship placement is durable, and later evidence
arrives through normal `remember` with the returned case ID/version.

Top-level evidence item categories:

| Category | Consumer behavior |
| --- | --- |
| `evidence_processed` | evidence is durable and all bounded semantic outcomes are listed |
| `evidence_quarantined` | exclude from model-backed paths and inspect operationally |
| `processing_failed` | show bounded safe error and retry guidance |

Relationship outcome categories:

| Category | Consumer behavior |
| --- | --- |
| `relationship_active` | active Relationship with effective accepted support |
| `relationship_pending_evidence` | retained Relationship without effective support; exclude from normal recall; it may be eligible for dreaming only after semantic review expiry |
| `relationship_needs_review` | present the exact same-owner Relationship conflict/scope question |
| `predicate_needs_review` | present original wording and server options; no Relationship exists yet |
| `relationship_rejected` | do not treat as true |
| `identity_needs_review` | resolve identity before dependent knowledge can activate |

A relationship outcome may lack `relationship_id` when unresolved identity or
predicate, failed assessment, or unresolved scope/time prevents canonical
Relationship creation. An evidence item may have no Relationship outcomes and
still be current searchable evidence when it is safe and not superseded.

`assessment` never exposes raw confidence or the configured numeric threshold.
`confidence_band` is exactly `meets_write_threshold`,
`below_write_threshold`, or `not_applicable`. Its bounded reason and
`assessment_policy_version` explain the policy branch without presenting model
confidence as an evidence-level truth score.

`relationship_rejected` is a terminal processed outcome. A
`relationship_pending_evidence` outcome carrying an open/acknowledged
support-confidence review keeps placement in `awaiting_review`; after that task
is resolved, canceled, or expired, the pending outcome is terminal. The status
alone does not imply an open task.
`predicate_needs_review`, `identity_needs_review`, and
`relationship_needs_review` require a durable review task; without that task the
placement row is considered repairable orchestration state, not a valid held
review.

`predicate_needs_review` payloads carry the current predicate-policy version.
Current collisions and ambiguities remain `awaiting_review`; operators do not
reset placement rows manually.

The strict internal assessor schema is not a public field. Its only canonical
definition is
[Memory Write Pipeline](Write-Pipeline#strict-assessor-contract).

### Review task

```json
{
  "review_task_id": "review_01J...",
  "version": 1,
  "kind": "support_confidence",
  "state": "open",
  "question": "Should this evidence support Mark works_on Dense-Mem?",
  "options": [
    {
      "action": "accept",
      "requires_new_evidence": true
    },
    {
      "action": "reject",
      "requires_new_evidence": false
    },
    {
      "action": "correct",
      "requires_new_evidence": true
    }
  ],
  "guidance": "Accept or correct only with exact current-session evidence; otherwise ask the user.",
  "expires_at": "2026-08-02T12:00:00Z"
}
```

Semantic `kind` is one of `support_confidence`, `identity`, `predicate`,
`scope`, or `time`. State is `open`, `acknowledged`, `resolved`, `canceled`, or
`expired`. All five semantic kinds expire after seven days. Security quarantine
and cross-profile Conflict records use their separate contracts.
Same-owner current-state conflict, correction, and policy tasks retain their
separate existing review contract; they acquire this deadline only when the
task is classified as one of the five semantic kinds.

At expiry, placement becomes terminal. A structurally complete resolved
Relationship remains `pending_evidence`; unresolved identity, predicate, scope,
or time remains Evidence plus Observation only. Expiry never activates
knowledge or creates a Hypothesis.

## `resolve_memory_placement`

Example:

```json
{
  "ingest_id": "ing_01J...",
  "placement_item_id": "item_1",
  "placement_item_version": 3,
  "action": "select_entity",
  "entity_ref": "person_1",
  "candidate_entity_id": "ent_mark_huang",
  "idempotency_key": "resolution-1"
}
```

| Action | Required input | Result |
| --- | --- | --- |
| `acknowledge` | completed ingest | records acknowledgement only |
| `select_entity` | placement item ID/version, Entity ref, and supplied candidate ID | appends the identity decision and reapplies the persisted assessment with zero AI calls |
| `confirm_new_entity` | placement item ID/version, Entity ref, and exact distinguishing evidence | stages the new evidence for one assessment; client allocates no ID |
| `select_predicate` | placement item ID/version, observation ID, and supplied registered predicate/version | appends the vocabulary decision and reapplies the persisted assessment with zero AI calls |
| `accept` | placement item ID/version plus exact authoritative session evidence | stages new evidence for one assessment; a bare acceptance cannot grant support |
| `reject` | placement item ID/version plus bounded message | records rejection without deleting history |
| `correct` | placement item ID/version plus exact replacement evidence | appends corrected evidence and runs one assessment for that new item |
| `release_quarantine` | placement item ID/version plus bounded reason; manager role and `write` scope | appends a security release and requeues the evidence as guarded |
| `forget` | target Relationship ID, bounded message, and authoritative evidence | retracts active current state; preserves nodes and append-only ledger |

Every action that targets a placement item uses the item `version` returned by
`remember` or `get_memory_placement` as `placement_item_version`. A stale value
fails closed. Resolution fields are top-level closed-schema properties; there
is no nested `decision` compatibility shape.

Clients may select only server-supplied Entity/predicate options. If exact
current-session context cannot resolve the question, the host asks the user.
Selection never causes another assessor conversation. Any action that adds
evidence creates a new placement item and therefore one new bounded assessor
conversation.

No action writes a graph side channel or directly forces active status. Merge, split,
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
`split`, it is null; the integrated assessor decides whether the evidence
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
  "query": "Which branch does production deploy from?",
  "limit": 10,
  "relationship_limit": 5,
  "community_limit": 3,
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
| `relationship_limit` | no | 0–20; default 5; `0` disables the Relationship frontier |
| `community_limit` | no | 0–10; default 3; `0` disables the Community frontier |
| `valid_at` | no | real-world RFC 3339 time |
| `known_at` | no | system-knowledge RFC 3339 time |
| `known_evidence_ids` | no | up to 200 unique evidence UUIDs already seen by the client; suppressed from returned `results[]` |
| `expand_from_entity_ids` | no | up to 50 unique Entity UUIDs; adds only directly touching eligible Relationships |
| `known_relationship_ids` | no | up to 200 unique Relationship UUIDs already seen by the client; suppresses the complete semantic group containing any supplied ID |

### Response

```json
{
  "recall_id": "rec_550e8400-e29b-41d4-a716-446655440010",
  "results": [
    {
      "evidence_id": "e_b_release",
      "context": "Production deployments moved to release on 2026-02-19."
    }
  ],
  "conflicts": [
    {
      "conflict_id": "conflict_01J...",
      "version": 3,
      "kind": "cross_profile_relationship",
      "status": "resolved",
      "question": "Which deployment branch is current for production?",
      "review_due_at": null,
      "effective_at": "2026-02-19T00:00:00Z",
      "effective_time_basis": "evidence_time",
      "preferred_position_id": "position_release",
      "positions": [
        {
          "position_id": "position_main",
          "disposition": "suppressed_current",
          "relationship_ids": ["r_a_main"],
          "owner_profile_ids": ["profile-a"],
          "result_evidence_ids": []
        },
        {
          "position_id": "position_release",
          "disposition": "preferred",
          "relationship_ids": ["r_b_release"],
          "owner_profile_ids": ["profile-b"],
          "result_evidence_ids": ["e_b_release"]
        }
      ],
      "positions_truncated": false
    }
  ],
  "related_relationships": [
    {
      "relationship_id": "r_b_release",
      "equivalent_relationship_ids": [],
      "subject": {
        "entity_id": "ent_production",
        "name": "production"
      },
      "predicate": "deploys_from",
      "object": {
        "entity_id": "ent_release",
        "name": "release"
      },
      "polarity": "+",
      "evidence_ids": ["e_b_release"]
    }
  ],
  "related_communities": [
    {
      "community_id": "community_release",
      "summary": "Production deployment branch and release workflow.",
      "top_entities": [
        {"entity_id": "ent_production", "name": "production"},
        {"entity_id": "ent_release", "name": "release"}
      ],
      "top_predicates": ["deploys_from"],
      "member_count": 3,
      "source_count": 2
    }
  ],
  "related_hypotheses": [],
  "search_states": {
    "evidence": "current",
    "relationships": "pending"
  },
  "degradations": [
    {
      "frontier": "relationships",
      "code": "relationship_vector_warming",
      "optional": true,
      "message": "Relationship vector projection is warming; lexical discovery was used."
    }
  ]
}
```

Recall is evidence-primary. `results[]` is the ranked answer context, bounded
by the server context budget. There is no `raw_evidence` response field and no
unfiltered evidence dump. Evidence text is returned only after final hydration
proves at least one query-relevant active support path remains eligible for the
requested `valid_at`/`known_at` view.

`related_relationships[]` is a separate correlation-ranked discovery frontier.
Each item consumes one `relationship_limit` slot after grouping by
`semantic_group_key`. `relationship_id` is the deterministic representative;
`equivalent_relationship_ids` contains the other eligible authored records in
that group. Subject is always an Entity. Object is an Entity or a typed Value
with `value_id`, `type`, canonical `value`, and optional `display`/`unit`.
`evidence_ids` contains authorized support handles; full provenance and history
require `trace_memory`. Relationship rank never changes Evidence `results[]`.

`related_communities[]` is a separate optional derived frontier. Each item has
exactly `community_id`, `summary`, bounded `top_entities[]`,
`top_predicates[]`, `member_count`, and `source_count`. It is empty when the
feature is disabled or `community_limit=0`. Community membership never changes
Evidence or Relationship rank.

`search_states` is required. `evidence` reports aggregate Evidence freshness;
`relationships` reports the team-wide release projection gate. Both use
`not_required`, `pending`, `current`, or `failed`.
`degradations[]` is required and may be empty. It contains bounded
`frontier`, stable `code`, `optional`, and safe `message` fields. During a
Relationship projection backfill, complete lexical discovery remains available
after text convergence. `pending` uses `relationship_vector_warming`; `failed`
uses `relationship_vector_failed`. Both report that the team-wide Relationship
vector branch is disabled. Ordinary post-activation document freshness remains
visible on placement/search state and does not reopen this release gate.

`known_evidence_ids`, `known_relationship_ids`, and expansion inputs change
only request-local retrieval context and never mutate stored state. Array order
is rank. The public response contains no raw rank score, distance, embedding
version, projection generation ID, or ranking-contract version.

`conflicts[]` is required and may be empty. It contains at most 20 cases
material to the query or returned frontiers. Each case contains at most 10
positions; each position contains at most 20 Relationship IDs, 20 owner profile
IDs, and 50 evidence IDs that also occur in this response's `results[]`. Status
is `open`, `overdue`, or `resolved`; disposition is `candidate`, `preferred`, or
`suppressed_current`. `preferred_position_id`, `review_due_at`, `effective_at`,
and `effective_time_basis` are nullable. `effective_time_basis` is
`evidence_time` or `recorded_fallback` when present. `question` is a bounded
deterministic description, not model-generated evidence.
`positions_truncated=true` means the host should trace a returned Relationship
before acting and never infer a winner from the bounded list.

For an open/overdue applicable case, primary results reserve bounded context for
each returned material position and the host presents the question. For a
resolved applicable case, primary results contain preferred support paths only,
while the compact case retains author and trace handles. Historical `valid_at`
and `known_at` reconstruct the applicable position and case-event state. The
exact policy belongs in
[Conflicts And Corrections](Conflicts-And-Corrections#durable-team-conflict-cases).

Recall eligibility, branch flow, and degradation belong in
[Recall Ranking](Recall-Ranking). Cosine distance, RRF, and reranking belong in
[Vector Search And Ranking](Vector-Search-And-Ranking). Exact/ANN execution and
index versions belong in [ANN, HNSW, And Halfvec](ANN-HNSW-And-Halfvec).
Embedding contracts, search-index generation IDs, ranking-contract versions,
branch scores, and projection-generation IDs are internal telemetry.

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
evidence[]
evidence_lifecycle_events[]
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

Each trace `conflicts[]` item returns the full authorized case summary, bounded
position membership with Relationship versions, and append-only case events:
`opened`, `position_added`, `evaluated`, `resolved`, `reopened`, `overdue`, or
`dismissed`.
Events include prior/resulting status, selected position when present, reason,
policy version, actor profile when client-triggered, system actor when
reviewer-triggered, effective time basis, referenced versions, and `created_at`.
Relationship `transitions[]` include prior/resulting status, `valid_from`, and
`valid_to` when a reviewer closes a stale episode. Trace may include
dismissed cases and non-preferred evidence that ordinary current recall omits.

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

The initial durable cross-profile Conflict workflow has no portal inbox or
portal resolution action. LLM hosts receive status through MCP recall/trace and
submit new evidence through MCP `remember`. A later first-party view may render
the same bounded state but cannot introduce a winner-selection shortcut.

Portal queries are not raw SQL/Cypher and never return embeddings. Portal
behavior and key administration belong in
[Portals And API Keys](Portals-And-API-Keys).

## Product Interfaces

| Interface | Audience | Purpose |
| --- | --- | --- |
| MCP `/mcp` | authenticated LLM hosts | discover and execute memory tools |
| user portal `/ui` | authenticated team users | first-party recall, graph/trace, review, keys, and self telemetry |
| control portal | deployment operators on the private listener | configuration, security, and operations |
| health/readiness | deployment infrastructure | liveness and dependency readiness only |

MCP runtime discovery reflects scopes and feature gates. Portal handlers expose
only the bounded view models required by their own UI.

## Error Contract

MCP protocol errors use the negotiated JSON-RPC structure. Tool/domain failures
carry bounded safe metadata such as:

```json
{
  "code": "ASSESSOR_RESPONSE_INVALID",
  "message": "The assessor did not return the required complete response.",
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
| structure/support assessor | submitted evidence, optional client proposal, exact-span Entity candidate groups, and structured predicate options; no stored Relationships or source policy |
| dream generator | compact allowed active/pending source Relationships plus existing endpoint and predicate IDs |

Providers receive no API keys, password hashes, Redis contents, raw database
access, unrestricted team graphs, or other-team evidence. Existing knowledge is
not assessor input and cannot become source evidence. Dreaming receives only its
separately bounded eligible inputs and has no knowledge-write capability.

Provider configuration belongs in
[Configuration](Configuration#assessor).

## Canonical References

| Concern | Canonical page |
| --- | --- |
| authentication, tenant resolution, transport, and visibility | [Request Lifecycle And Security](Request-Lifecycle-And-Security) |
| write stages, strict assessor response, candidate prefetch, retry, confidence gate, and review expiry | [Memory Write Pipeline](Write-Pipeline) |
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
| Client controls truth | request sends status/support/predicate definition and bypasses policy | closed write schemas reject all lifecycle-control fields |
| Pending Relationship appears active | consumer renders `relationship_pending_evidence` with active knowledge | distinct category, no default recall eligibility, and typed client fixtures |
| Search mistaken for commit | client retries `remember` because a vector is pending | separate intake/processing/search fields and idempotency keys |
| Alias becomes edge | client proposal creates an alias Relationship | aliases exist only on Entity hints and name storage |
| Oversized trace | a hub Entity exhausts response or host context | depth, edge, content, time, and character budgets plus `stopped_reason` |
| Forget is treated as deletion | client expects audit/evidence to vanish | explicit retraction semantics and separate privacy-erasure workflow |
| HTTP bypass diverges from MCP | a caller executes a semantic memory tool through a reintroduced generic HTTP route with a stale schema | route registration keeps generic HTTP tool catalog/execution absent and tests list/call parity separately |
| Stale conflict response changes the wrong case | C submits evidence against version 2 after the case moved to version 3 | per-proposal `conflict_id` plus expected version and pre-commit material-membership validation |
| Resolved response conceals authorship | only B's current evidence returns and the host assumes A was deleted | required compact conflict ref plus full trace positions/events |
| Raw evidence leaks noise | a vector seed returns stale A evidence with no eligible support path | omit `raw_evidence`; hydrate evidence text only through active support-path eligibility |

## Contract Acceptance

Contract `dense-mem.v2.3.2` is incomplete until:

1. generated MCP tool schemas match the committed examples and enums
2. unknown write fields and forbidden lifecycle fields fail validation
3. all semantic results use `relationship_id`
4. pending categories never claim active status
5. default recall contains evidence contexts but no assessment/verification history,
   pending Relationship, Community, or Hypothesis in `results[]`
6. trace returns exact spans and lifecycle/correction lineage
7. processing and search states vary independently
8. cross-team ID hints fail without revealing existence
9. every MCP tool enters only canonical services and PostgreSQL
10. every public error has a correlation ID and bounded safe details
11. `tools/list` visibility and `tools/call` execution agree for every scope and
    feature combination
12. semantic memory tools are discoverable/callable through MCP and no generic
    HTTP discovery or execution route exposes them
13. recall exposes independent Evidence, Relationship, Community, Conflict, and
    Hypothesis fields with the documented limits and exact item shapes
14. `known_relationship_ids` suppresses a complete semantic group and direct
    Entity expansion performs no recursive traversal
15. Relationship vector warming is explicit in `search_states` and
    `degradations[]`; no public score or internal generation identifier leaks
16. `remember` accepts conflict context only on one Relationship proposal and
    rejects stale, cross-team, dismissed, or unrelated cases before commit
17. `remember` accepts optional correction target only on one Relationship
    proposal and rejects stale, cross-team, or unrelated targets before commit
18. every recall response contains bounded `conflicts[]`; open/overdue and
    resolved cases follow their specified primary-result behavior
19. trace reconstructs every authorized Conflict position, version, event, and
    non-preferred evidence path
20. normal semantic placement opens one bounded assessor conversation, permits
    at most five same-history malformed-response turns, retries provider
    failures through durable attempts with fresh history, and persists the first
    valid normalized response for retry/restart reuse
21. public placement exposes threshold-relative confidence band, reason, and
    policy version but never raw confidence
22. Entity/predicate option selection makes zero AI calls, while new
    support/correction evidence creates one new assessment item
23. every semantic review returns an exact question, options, guidance, and
    seven-day `expires_at`
24. semantic review expiry never activates a Relationship or directly creates
    a Hypothesis
