# Memory Write Pipeline

> Session-ingestion documentation for [PR #553](https://github.com/markhuangai/dense-mem/pull/553). Code merge and validation against the exact merged `main` commit remain pending.

Remember accepts exact evidence and required client Relationship proposals. It returns
a terminal result in the originating MCP call. The server owns authentication,
grounding validation, canonical identity, lifecycle, and the atomic commit.

Exact fields and request examples belong in
[Technical Reference](Technical-Reference#remember).

## End-To-End Flow

~~~mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart TD
  Input["Validate and scan"] --> Key{"Replay final attempt?"}
  Key -- yes --> Result["Terminal result"]
  Key -- new or retryable --> Context["Duplicate detection<br/>and authorized context"]
  Context --> Assess["Assess complete batch<br/>up to three turns"]
  Assess --> Valid{"Valid and safe?"}
  Input -- rejected --> Failure["Typed failure<br/>no canonical commit"]
  Valid -- no --> Failure
  Valid -- yes --> Embed["Plan and embed<br/>required documents"]
  Embed --> Commit["Recheck fences<br/>and commit atomically"]
  Commit --> Result
~~~

Destination-aware authorization runs before provider calls and again at the authoritative commit. Team-read-only callers can write their own private memory; shared mutation also requires current write permission and semantic ownership.

Provider calls occur outside the canonical semantic transaction. Exact evidence
and canonical state become durable in the successful terminal commit.
Operational attempt records and diagnostic captures are separate from accepted
knowledge. The server does not acknowledge an unfinished write as success.

## Client Submission Contract

A request contains evidence, a required `target_space: "private" | "team_shared"`, and one top-level `idempotency_key`. The target is part of the request hash. The LLM chooses it; the server resolves the space from authentication and rejects unauthorized targets without redirection.
`relationships` must be nonempty. Each supplied proposal has a unique
request-local `ref`, subject, predicate, exactly one Entity or typed Value
object, polarity, and nonempty `evidence_indices`. At least one proposal must cite every submitted evidence item.

The client supplies logical names and values. The server owns exact spans and
canonical IDs. A supplied `known_entity_id` or `known_predicate_key` is an
exact constraint revalidated against authorized current state.

A Relationship can include explicit `known_evidence_ids`. They are read-only
context, never newly submitted evidence. A stored split must still cite an
exact span from submitted evidence. Access, source, space, and lifecycle fences
are rechecked before commit.

See [Entity Identity And Resolution](Entity-Identity-And-Resolution) for
candidate resolution and [Conflicts And Corrections](Conflicts-And-Corrections)
for evidence replacement and correction.

## Deterministic Write-Time Security Gate

Strict binding and the initial scan precede provider work. The scan examines
evidence and proposal values for instruction overrides, role spoofing, active
markup, hidden control attacks, credential extraction, and exfiltration
directives. It inspects a bounded decoded view while preserving exact source
text.

Opaque identifiers, ordinary paths, punctuation, and harmless escape notation
are not threats by themselves. Rejection retains bounded rule, span, and
correlation metadata. Initial security rejection does not retain request
content or send it to a model provider.

Passing this scan admits processing; it does not prove the evidence is safe.
Every submitted evidence item also receives assessor security review.

## Duplicate Evidence and Occurrences

The server resolves exact duplicates deterministically within the authorized
owner and memory space. For eligible non-exact evidence it can embed the input
before assessment, retrieve a bounded duplicate candidate set, and ask the
assessor to choose a supplied candidate or new evidence.

Reusing canonical evidence preserves the exact submitted occurrence and its
provenance. Different text is not silently rewritten into an existing quote.
`force_insert` bypasses semantic duplicate reuse, while exact reuse still
applies. Lifecycle-bearing inputs, such as a source revision or direct
supersession, follow their own write rules.

Duplicate embedding results can be reused in the final write plan. A request
may therefore make a duplicate-candidate embedding batch and a final-document
batch; it is not guaranteed to make exactly one embedding call.

## Server-Owned Assessment

The processor supplies exact submitted evidence, client proposals, authorized
Entity candidates, active predicate options, duplicate candidates, and bounded
explicit known-evidence context. It issues opaque references for allowable
grounding boundaries and candidates.

The assessor reviews the complete batch. It may normalize or split submitted
proposals, choose allowed identity/predicate candidates, request controlled
predicate registration, and mark unsupported proposals `not_stored`. It
does not search the database or invent additional Relationships.

Entity preflight loads complete bounded name/alias candidate sets. Predicate
preflight preserves exact matches and adds relevant active options. An overflow
is a typed failure, not permission to silently discard exact candidates.

The server validates the entire response: evidence security coverage,
uniqueness, allowed references, submitted support, typed Values, polarity,
validity bounds, and proposal coverage. An invalid response receives a complete
repair turn in the same conversation, up to three total calls. The server
never splices valid items from separate invalid responses.

Unsafe evidence fails the batch with `submission_policy_rejected`.
Unsupported Relationship proposals can instead receive `not_stored` while
safe evidence is stored. The presence of a supported Relationship is not a
prerequisite for evidence storage.

## Deadlines and Atomic Commit

The total request cap is 180 seconds, anchored at service entry. Assessment is
capped at 160 seconds from entry. Each embedding phase has at most 10 seconds
and cannot exceed the entry-anchored 170-second deadline. Commit has at most
10 seconds and cannot exceed the total deadline. Earlier caller cancellation
or deadlines remain authoritative.

Before commit, the server renders the required search documents and validates
provider vectors. The commit rechecks idempotency, source revisions, duplicate
decisions, memory-space generation, known evidence, and active search-contract
fences.

One PostgreSQL transaction persists admitted evidence and occurrences,
observations, accepted Entity/Value/Relationship changes, predicate
registrations, support and lifecycle events, conflict effects, search
documents, current vectors, and the terminal replay result. A failure rolls
back the accepted effects together. A later diagnostic capture is not a
second knowledge commit.

Required embedding failure prevents canonical commit. Search reconciliation
is a separate operator maintenance workflow; it does not complete a successful
Remember call after the response.

## Idempotency and Recovery

The key is scoped to team, permanent owner alias, and operation. A canonical
request hash detects a changed payload under an existing key.

A completed attempt replays its saved result. A non-retryable failed attempt
replays the failure. A later call with the same unchanged request may re-execute
a retryable failed attempt. A concurrent waiter receives the lock owner's
result, including its retryable failure; it does not immediately start another
provider run.

The host must retain the entire request, including metadata and the key.
Follow the returned `next_action`. A deliberately corrected request is a new
operation; changing keys must not be used to evade an idempotency conflict.

## Public Remember Result

| Outcome | Meaning |
| --- | --- |
| `completed` | Safe evidence and accepted semantic effects committed with required current vectors. |
| `failed` | The operation failed; structured errors explain retry or remediation. |
| Per-proposal `not_stored` | A proposal was unsupported; inspect its reason separately from evidence storage. |

Results include per-evidence dispositions, one Relationship result per submitted
ref, `errors`, correlation context, and optional warnings. Failures use MCP
`isError` and bounded structured data. Invalid requests may fail before a
submission result exists.

## Diagnostics and Recall Boundary

The private control portal can inspect admitted invocations, canonical
attempts, assessor exchanges, and delivery observations under bounded capture
and retention rules. Initial security rejection stays content-free. Operational
credentials are protected; admitted user text may remain exact in an authorized
capture. See [Telemetry](Telemetry) and
[Request Lifecycle And Security](Request-Lifecycle-And-Security).

Stored and indexed evidence is not automatically a default recall result.
Recall hydration still requires an active eligible Relationship support path
for its requested time view. Hypotheses remain separate from accepted memory.
See [Recall Ranking](Recall-Ranking).

## Risks and Verification

| Failure mode | Required protection |
| --- | --- |
| A changed retry silently alters an accepted operation | Scoped key, canonical hash, and replay verification. |
| Partial assessor validation commits only convenient items | Complete-response validation and atomic accepted effects. |
| A concurrent replacement changes the evidence used by a provider | Recheck source, ownership, space, and version fences at commit. |
| Evidence storage is mistaken for guaranteed recall visibility | Verify support-path hydration independently. |
| Provider failure details escape to clients | Typed public errors; separately authorized operator diagnostics. |

The processor tests and Knowledge PostgreSQL integration cases exercise
provider failures, duplicate decisions, retryability, atomicity, source races,
and isolation through their owning packages.

## Private Session Intake

Session intake preserves raw events and processing intent in one private RLS transaction before provider work. Dense-Mem builds bounded overlapping windows, validates complete extraction responses, and links draft Entity references without changing proposed facts. Earlier completed events are same-session context, never new evidence. Every accepted Relationship requires a current-event citation.

All validated proposals are assessed together through the existing Remember grounding, identity, lifecycle and embedding owners. One final transaction commits accepted evidence, canonical effects, current search projections, exact event provenance and the successful session receipt. Any required extraction, assessment, embedding or commit failure produces zero new semantic knowledge for that request. Raw events and validated extraction checkpoints remain available for unchanged-request recovery. Unsupported proposals retain explicit `not_stored` dispositions.

Processing is serialized per private session with bounded PostgreSQL advisory locking outside semantic transactions. Commit rechecks generation, ownership, evidence and embedding fences. Private erasure covers raw requests/events, checkpoints, receipts and protected diagnostics. Diagnostics expire after seven days unless held; legal holds and erasure use the existing space-locking boundary.

Disable new intake to roll back behavior while retaining compatible schema and history. Deploy the intake migration and matching private-erasure manifest together with old instances stopped; populated intake history cannot be rolled back by dropping tables.

See [Technical Reference](Technical-Reference#ingest-session) for the exact request and recovery rules.
