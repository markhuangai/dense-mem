# Conflicts And Corrections

Dense-Mem preserves conflicting evidence. It asks for a client decision when
the caller's identity, predicate, scope, time, or current-state behavior cannot
be resolved safely. A resolved cross-profile disagreement may keep both
profiles' eligible Relationships active and link them without allowing either
profile to mutate the other.

This page is the single source of truth for conflict and correction
interaction. Relationship lifecycle is in
[Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle), identity
merge/split is in
[Entity Identity And Resolution](Entity-Identity-And-Resolution#merge-and-split-corrections),
and orchestration is in [Memory Write Pipeline](Write-Pipeline).

## Same-Owner Conflict Flow

```mermaid
%%{init: {"sequence": {"rightAngles": true}}}%%
sequenceDiagram
  actor User
  participant Host as AI client
  participant DM as Dense-Mem
  participant PG as PostgreSQL

  User->>Host: Production now deploys from release
  Host->>DM: remember(new evidence)
  DM->>PG: Preserve evidence, observation, verifier event, and conflict
  DM-->>Host: relationship_needs_review
  Host->>DM: trace_memory(related relationship_id)
  DM->>PG: Load current state and append-only history
  PG-->>DM: Existing/new evidence, decisions, and transitions
  DM-->>Host: Trace and explicit decision choices
  Host->>User: Ask the exact conflict question
  User-->>Host: Decision plus evidence
  Host->>DM: resolve_memory_placement(...)
  DM->>PG: Append decision and recompute affected current state
```

An unresolved same-owner conflicting observation is not an active edge. The
owner's prior eligible Relationship stays current unless accepted evidence and
deterministic predicate behavior permit supersession. A schema-valid, entailed
cross-owner correction can become the caller's active edge and append a
cross-reference to the other profile's record.

## Why Review Is Required

| Cause | Example | Safe result |
| --- | --- | --- |
| Entity ambiguity | context now suggests a different person named Mark | `identity_needs_review`; no guessed split or dependent edge |
| Unknown predicate | “Dense-Mem dogfoods PostgreSQL” maps to no registered predicate | `predicate_needs_review`; preserve wording; create no Relationship |
| Scope ambiguity | “uses PostgreSQL” may mean production or tests | preserve observation and request scope |
| Current-state conflict | production has competing `deploys_from` values | preserve both histories and ask |
| Temporal ambiguity | new evidence may describe past rather than current state | request validity context |
| Insufficient change evidence | weak evidence should not replace a supported current Relationship | retain `candidate/pending_evidence` without an active edge |
| Correction ambiguity | “that is wrong” identifies no Relationship | request `relationship_id` or a disambiguating query |
| Cross-profile disagreement | profile B supplies stronger evidence against profile A's Relationship | preserve A, create B's eligible Relationship, and append a challenge/correction cross-reference |

## Cross-Profile Corrections

Profiles A, B, and C may all read same-team knowledge. Each owns only the
Relationships and observations created under its profile.

Assume profile A has two Relationships based on an identity that later appears
to combine two people:

```text
A: ent_mark -> works_on -> Dense-Mem
A: ent_mark -> works_on -> Billing
```

Profile B obtains distinguishing evidence. B creates its own Entity resolutions
and Relationships through normal `remember` processing:

```text
B: ent_mark_dense   -> works_on -> Dense-Mem
B: ent_mark_billing -> works_on -> Billing
```

In the same transaction, Dense-Mem appends `corrects` or `challenges`
cross-references from B's new Relationships to A's related Relationships. A's
status, tier, supports, and Entity resolutions do not change.

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  A1["A: Mark works_on Dense-Mem"] --> Set["Team conflict set"]
  A2["A: Mark works_on Billing"] --> Set
  B1["B: Mark-Dense works_on Dense-Mem"] --> Set
  B2["B: Mark-Billing works_on Billing"] --> Set
  B1 -->|"corrects"| A1
  B2 -->|"corrects"| A2
  Set --> C["Profile C recall"]
  C --> Decision{"Evidence margin sufficient?"}
  Decision -- Yes --> Prefer["Return stronger position first;<br/>retain conflict refs"]
  Decision -- No --> Ask["Ask user for clarification"]
  Ask --> CWrite["C remembers confirmation<br/>under profile C"]
```

Profile C may rely on the better-supported position or collect new user
evidence. Any confirmation C remembers belongs to C and may `confirm` B or
`challenge` A. It does not edit either record.

If A later accepts the correction, A submits or adopts exact evidence through
the normal verifier. A may then supersede A's old Relationships and create A's
corrected Relationships. Adoption preserves B's evidence author, provenance,
and source-group identity; it does not count the same evidence twice.

The canonical ownership and cross-reference fields are defined in
[Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle#profile-ownership-and-team-visibility).
Conflict grouping and preference ranking are defined in
[Vector Search And Ranking](Vector-Search-And-Ranking#cross-profile-conflict-reranking).

## Inspect Before Deciding

Use the `relationship_id` from recall discovery paths or from a placement
`relationship_outcomes[]` entry:

```json
{
  "relationship_id": "r_deploy_branch",
  "include_evidence_content": true,
  "include_verification": true,
  "include_transitions": true,
  "max_depth": 1,
  "max_edges": 12
}
```

The client presents:

- current subject, predicate, object, polarity, scope, and validity
- tier, status, current-cardinality behavior, and source count
- accepted evidence and material verification events
- the new conflicting observation and its evidence verdict
- exact identity, predicate, scope, or temporal ambiguity
- the before/after impact of each allowed decision

An opaque “accept or reject?” prompt is insufficient.

## Identity Review

The client may select only a server-supplied same-team candidate:

```json
{
  "ingest_id": "ing_01J...",
  "placement_item_id": "item_identity_1",
  "action": "select_entity",
  "decision": {
    "mention_ref": "mark",
    "entity_id": "ent_mark_huang"
  },
  "evidence": [
    {
      "content": "I mean Mark Huang, the Dense-Mem maintainer.",
      "source_type": "manual",
      "authority": "authoritative",
      "source_group": "user-resolution:ing_01J..."
    }
  ],
  "idempotency_key": "identity-resolution-1"
}
```

`confirm_new_entity` requires evidence that distinguishes a new identity. The
client never allocates the new ID.

A late same-name conflict may require a split. Dense-Mem first shows only the
caller's observations and Relationships that may move. After confirmation, it
creates the new Entity and reassigns only selected caller-owned immutable
observation IDs. It never moves another profile's rows or every row based on
display text.

## Predicate Review

An unknown phrase remains on the observation with no `predicate_key`. Review
may:

- select an existing server-supplied registered predicate when meaning matches
- reject the observation mapping
- route a proposed registry change through a separately authorized,
  versioned vocabulary workflow

The verifier and ordinary review endpoint cannot add a predicate definition.
A Hypothesis is not an unknown predicate; it is an isolated dream output using
an already registered predicate. See [Dreaming](Dreaming).

## Relationship Review Actions

| Action | Meaning |
| --- | --- |
| `accept` | append authoritative client evidence for the pending observation, then rerun normal policy |
| `reject` | append the rejection and preserve observation/history |
| `correct` | append replacement evidence and reprocess affected knowledge |
| `forget` | retract the caller-owned selected Relationship from active reads while retaining its ledger |
| `acknowledge` | mark review consumption; change no semantic state |

`accept` does not bypass verification or directly assign `fact`. Fact promotion
uses the authority and independent-source rules in
[Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle#source-independence-and-fact-promotion).

These actions mutate only a Relationship owned by the authenticated profile.
To address another profile's Relationship, the caller submits new evidence
through `remember`; deterministic post-verification identity and conflict
policy derive the appropriate cross-reference. There is no cross-profile
`accept`, `forget`, `supersede`, merge, or split shortcut.

## Correct With New Evidence

A correction appends evidence; it is not an in-place text edit.

```json
{
  "ingest_id": "ing_01J...",
  "placement_item_id": "item_relationship_1",
  "action": "correct",
  "evidence": [
    {
      "content": "The user confirmed that production now deploys from release.",
      "source_type": "manual",
      "authority": "authoritative",
      "source_group": "user-resolution:ing_01J..."
    }
  ],
  "proposal": {
    "entities": [
      {
        "ref": "production",
        "name": "production",
        "entity_kind": "environment",
        "identity_context": {
          "system": "Dense-Mem"
        }
      },
      {
        "ref": "release",
        "name": "release",
        "entity_kind": "git_branch",
        "identity_context": {
          "repository": "Dense-Mem"
        }
      }
    ],
    "relationships": [
      {
        "proposal_id": "production_deploys_from_release",
        "subject_ref": "production",
        "predicate": "deploys_from",
        "object_ref": "release",
        "polarity": "+",
        "modality": "statement",
        "evidence": [
          {
            "evidence_index": 0,
            "start": 0,
            "end": 60
          }
        ]
      }
    ]
  },
  "idempotency_key": "deployment-correction-1"
}
```

The replacement enters the same extraction, one-call verifier, deterministic
lifecycle, and PostgreSQL transaction as every `remember`. Prior evidence and
transitions remain traceable.

This action corrects the caller's Relationship; it does not declare a shared
source globally obsolete. A source owner that can prove stable lineage instead
uses `source_key`, `source_revision`, and `previous_source_revision`, optionally
with its own `supersedes_fragment_ids`. Source activation occurs at intake,
makes the prior revision ineffective for every profile that adopted it, and
recomputes affected tiers from remaining effective support. The caller cannot
mix a profile-local correction with unowned source-fragment retirement.

## Same-Owner Before And After

| Before decision | After an accepted correction |
| --- | --- |
| supported prior Relationship stays current | prior record is superseded or retracted only when registry behavior and evidence permit |
| new conflict is held and has no active edge | eligible replacement is exposed as one SemanticEdge |
| prior evidence and decisions are append-only | both old and new provenance remain traceable |
| vector search may still represent the prior version | source/full-text/adjacency reads change at commit; version-matched embedding job updates vector search |

For a cross-profile correction, the target record does not enter the right-hand
column. The caller creates a separately owned Relationship and cross-reference;
the team recall layer compares both.

## Forget And Retraction

`forget` removes a caller-owned Relationship from current graph-shaped reads
without deleting its semantic nodes or historical ledger. Same-team visibility
does not authorize forgetting another profile's record.

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Before["Relationship status=active<br/>SemanticEdge visible"] --> Decision["Authorized forget decision"]
  Decision --> Append["Append retraction event<br/>and audit"]
  Append --> Current["Set current status=retracted"]
  Current --> After["SemanticEdge no longer returned"]
  After --> Nodes["Entity / Value nodes remain"]
  After --> Trace["Evidence, verification,<br/>support and transitions remain"]
```

Because SemanticEdge is a PostgreSQL read rule, the edge disappears from normal
reads in the same transaction that records the retraction. There is no second
graph copy to delete.

This is logical forgetting. A privacy-erasure request is a separate authorized
workflow that may redact payload content while retaining minimal tombstones,
audit proof, and referential integrity required by policy. Clients must not
emulate either operation with direct SQL deletion.

## Deterministic Current-State Decisions

Predicate registry fields `relationship_kind` and `current_cardinality` guide
whether accepted knowledge coexists or may supersede a prior current value
owned by the same profile. They are not model decisions and never authorize
cross-profile supersession.

```mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart TD
  Accepted["Accepted new Relationship"] --> Kind{"state or event?"}
  Kind -- event --> Coexist["Keep both occurrences"]
  Kind -- state --> Cardinality{"current cardinality"}
  Cardinality -- many --> Coexist
  Cardinality -- one --> Scope{"Same subject, predicate,<br/>scope and comparable time?"}
  Scope -- No --> Coexist
  Scope -- Yes --> Evidence{"Evidence safely establishes<br/>replacement?"}
  Evidence -- No --> Review["Hold conflict for review"]
  Evidence -- Yes --> Supersede["Append supersession;<br/>change current state atomically"]
```

Canonical behavior belongs in
[Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle#predicate-registry);
this page owns how the client sees and resolves uncertainty.

## Risks And Mitigations

| Risk | Concrete failure mode | Mitigation |
| --- | --- | --- |
| Wrong review target | a decision for one conflict applies to another | require ingest/item/Relationship IDs, idempotency key, and echo an impact summary |
| Accept bypasses policy | a client click writes an active fact directly | append evidence and route through strict verification and deterministic lifecycle |
| Correction erases history | old evidence is edited to match new truth | immutable fragments plus append-only observations, decisions, support, and transitions |
| Homonym split corrupts history | finance-team Mark causes all Mark edges to move | client-confirmed impact preview and caller-owned observation-ID-level reassignment |
| Unknown predicate expands vocabulary | verifier invents `dogfoods` as a durable key | registry allowlist and separate authorized vocabulary workflow |
| Wrong supersession | a many-valued predicate is treated as single-current | explicit `current_cardinality`, scope/time comparison, and before/after preview |
| Forget becomes data loss | deleting an edge also removes source and audit history | transactional retraction status; nodes and append-only ledger remain |
| Stale vector confuses review | vector search surfaces meaning from before correction | source-version checks, exact trace by ID, and explicit search freshness |
| Cross-profile correction becomes mutation | B's correction marks A's Relationship superseded | owner predicate on mutations; B writes its own Relationship plus immutable cross-reference |
| Majority becomes truth | several profiles copy one weak source and outvote stronger evidence | preserve source groups, deduplicate common origins, and rank evidence rather than author count alone |
| Correction impersonates source revision | a profile-local `correct` request invalidates evidence adopted by other owners | reserve team-wide invalidation for the immutable source owner and exact prior-revision contract |

## Acceptance Invariants

1. conflict preserves old and new evidence
2. unresolved identity or predicate creates no active dependent edge
3. every decision records actor, provenance, time, target IDs, and idempotency
4. correction never mutates verbatim evidence
5. accept, reject, correct, split, and forget write through normal PostgreSQL
   policy—not a graph side channel
6. a late split moves only explicitly selected caller-owned observations
7. prior current state changes only in the same transaction as its transition
8. forget removes the SemanticEdge but retains Entity/Value nodes and all
   ledger history
9. trace reconstructs conflict, decision, and supersession/retraction lineage
10. a profile cannot accept, forget, supersede, merge, split, or change support
    for another profile's knowledge
11. cross-profile corrections remain independently authored and link both
    Relationships without hiding either from authorized trace
12. adopting cross-profile evidence preserves its original provenance and does
    not create a second independent source group
13. normal correction changes only caller-owned Relationship state, while an
    owned source revision may invalidate its evidence team-wide through system
    recomputation
