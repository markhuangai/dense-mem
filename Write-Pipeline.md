# Memory Write Pipeline

Dense-Mem accepts exact evidence plus a complete client-generated relationship
array. The server owns every durable decision. There is no client review loop,
placement selection, or request for follow-up evidence.

The public write contract is the current closed schema. Discover it with MCP
tools/list; callers do not select a contract version. This page explains the
v2.6.1 synchronous Remember lifecycle. `get_submission_status` and normal
placement/embedding workers are historical migration sources, not runtime
dependencies.

## End-To-End Flow

~~~mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Client["Client"] --> Remember["remember: evidence + logical relationships<br/>with evidence_indices"]
  Remember --> Scan{"Deterministic scan"}
  Scan -- reject --> Reject["Safe write error; nothing staged"]
  Scan -- pass --> Snapshot["Snapshot owner/source/search fences<br/>and generate request-local IDs"]
  Snapshot --> Ground["Server-owned grounding<br/>and bounded catalogs"]
  Ground --> Assessor["Capability-free Remember assessor"]
  Assessor --> Security{"Security concern?"}
  Security -- yes --> Quarantine["Quarantined terminal result<br/>bounded metadata only"]
  Security -- no --> Validate{"Closed-schema validation"}
  Validate -- invalid --> Correct["Complete same-session repair<br/>or bounded operational failure"]
  Validate -- valid --> Policy["Deterministic server policy"]
  Policy -- reject --> RejectMemory["Rejected: no_supported_memory<br/>or stale_input"]
  Policy -- fault --> Failed["Terminal operational failure"]
  Policy -- accept --> Plan["Deterministic semantic/search plan"]
  Plan --> Embed["One EmbedBatch call<br/>≤256 documents, 10s"]
  Embed --> Promote["One fenced PostgreSQL transaction<br/>canonical state + vectors, 10s"]
  Promote --> Result["Terminal structured MCP result"]
  Quarantine --> Result
  Failed --> Result
  Correct --> Ground
~~~

The original `remember` call returns only after the terminal result is known.
The total request budget is 180 seconds (160 seconds through assessment, 10
seconds for embedding, and 10 seconds for commit). No canonical semantic state
is visible without its required current vector. Operational failures are
structured MCP `isError` results with bounded remediation.

The Remember capability boundary performs strict conversion, validates
evidence-index coverage, scans evidence and structured proposal values, snapshots
the caller-owned fences, and runs the assessor and embedding phases before one
profile-scoped PostgreSQL transaction. Provider calls stay outside that
transaction. `remember_attempts` and `remember_attempt_events` retain terminal
diagnostics; no pre-provider intake or placement receipt is exposed.

## Client Submission Contract

The client supplies a nonempty top-level `relationships` array; it is required,
not an optional hint. An evidence item is currently bounded to 999 characters,
and one item can support multiple distinct propositions, so there is no fixed
single Relationship object or separate cross-referenced `proposal.entities`
object.

Every Relationship contains one to twenty unique zero-based
`evidence_indices`. Across the submission, those lists must cover every
evidence item. The server reports all missing indexes together and stages
nothing.

Each Relationship is self-contained. It contains an inline subject and object
Entity (or typed Value), a predicate key proposal, polarity, and its supporting
evidence indexes. Remember Relationships are statements; there is no public
`modality` field. `remember` does not accept caller `span`,
`surface`, or Relationship `supports` fields.

`relationship.ref` is request-local and unique. Several distinct Relationships
may cite the same evidence index; one long evidence item is not limited to one
proposition. `predicate.proposed_key` is at most 128 Unicode code points. The
server rejects oversized input; it never truncates a key or name before
assessment.

~~~json
{
  "evidence": [{
    "content": "Dense-Mem uses PostgreSQL.",
    "source_type": "manual"
  }],
  "relationships": [{
      "ref": "uses-postgresql",
      "subject": {
        "name": "Dense-Mem",
        "entity_kind": "project"
      },
      "predicate": {
        "proposed_key": "uses"
      },
      "object": {"entity": {
        "name": "PostgreSQL",
        "entity_kind": "product"
      }},
      "polarity": "+",
      "evidence_indices": [0]
  }],
  "idempotency_key": "storage-architecture-20260801"
}
~~~

The client makes its best effort to set `predicate.proposed_key` to a matching
team predicate. It is a suggestion, not authority. `object` contains exactly
one of `entity` or `value`; a typed Value includes its type and canonical value,
with optional display and unit fields. The provider proposes Entity, predicate,
Value, and support ranges; the server validates the complete response and owns
support disposition and deterministic policy before commit.

The client may provide a same-team server-issued `known_entity_id` as an exact
identity constraint or omit it for name-based server selection. An exact
`known_predicate_key` likewise fixes the active team predicate, while
`proposed_key` is advisory. The client cannot fabricate canonical IDs, choose
lifecycle state or support, register a predicate directly, or choose an
acceptance outcome.

The source and lifecycle fields on evidence retain their normal ownership,
idempotency, and source-revision rules. A direct replacement of owned current
evidence uses supersedes_evidence_ids; advancing a source revision uses
previous_source_revision; they cannot be combined.

## Deterministic Write-Time Security Gate

Before a submission row is created, the server scans every raw evidence item.
It rejects, at minimum:

- explicit encoded/obfuscated payloads, including data URIs, PEM-like material,
  JWT-like tokens, and folded encoded blocks when their bounded decode or shape
  indicates a threat
- hidden controls or bidirectional Unicode that form an evasive control flood
  or normalize into another threat pattern
- executable or active markup
- role spoofing and instruction overrides
- directives to solicit credentials, environment variables, secrets, outbound
  network calls, or data exfiltration

The scan normalizes a bounded set of common encodings before checking. It is a
deny gate, not proof that the evidence is benign. Opaque UUIDs, hashes, provider
IDs, and other high-entropy identifiers do not produce
`encoded_evidence_not_allowed` by entropy alone. Rejections return a bounded
safe error with bounded rule IDs and source spans, and create no submission or
canonical memory. One inspection layer covers percent escapes, `\xNN`, `\uNNNN`,
and HTML entities without changing the staged text. Those forms, backslashes,
quotes, and brackets are not rejection reasons by themselves. A direct
directive remains unsafe when wrapped in punctuation; descriptive prose that
quotes a tool/network attack remains eligible. Structured proposal string
values are scanned as values rather than inferred from serialized JSON quotes.

The request performs the deterministic scan before any provider or persistence
work. Scanner events and pre-provider rejection audits retain bounded decisions,
reasons, rule IDs, sources, and spans without raw content.

## Terminal Remember Attempt State

No semantic, intake, reservation, or workflow row is written before provider
and embedding success. Successful, rejected, and quarantined outcomes are
replayable through an append-only terminal attempt record:

~~~text
remember_attempts
  ├── idempotency key, request hash, owner/space, and terminal outcome
  ├── safe replayable public result
  └── counts, failure phase/code, correlation, and duration
remember_attempt_events
  └── chronological validation → assessor → embedding → commit events

canonical knowledge (one fenced commit)
  ├── evidence and observations
  ├── Entities, Values, Relationships, lifecycle history
  ├── search documents and validated current vectors
  └── audit lineage
~~~

Attempt metadata is team- and owner-scoped using the authenticated request
context and PostgreSQL RLS. Failure-only raw artifacts, when enabled, are
control-only, bounded, and physically expire after seven days.

### Restart boundary and resubmission lifecycle

Remember has no semantic hold, replacement slot, approval state, or
`submission_holds` table. The assessor receives one complete request and can
repair grounding, identity, predicate reconciliation, and supported splits in
the same bounded conversation. A supported subset is committed only when every
evidence item remains covered; unsupported refs are recorded as `not_stored`.
Only `no_supported_memory` and `stale_input` are semantic rejections. Provider,
configuration, database, and internal faults are typed operational failures.
No Entity, Value, Relationship, lifecycle, or search document is committed from
a rejected or failed run.

The stopped-service migration terminalizes unfinished queued, guarded,
processing, and `awaiting_review` rows whose ingest metadata identifies the old
Remember origin. It marks those rows with a contract-superseded failure,
clears claims, and preserves completed canonical state before dropping the
retired workflow tables. This is upgrade archaeology only; v2.6.1 runtime
never accepts the old normalizer contract, resumes those rows, or keeps a
parallel placement workflow.

## Server-Owned Normalization and Assessment

The request-scoped processor handles the complete Remember request as one
run-scoped unit. For
each submitted Entity name, it finds bounded whole-token occurrences of that
name and active candidate aliases only within the Relationship's cited evidence.
It assigns opaque grounding and boundary references, loads complete same-team
candidate sets, resolves each proposed predicate key or alias exactly, and adds
bounded active predicate options ranked by the submitted keys and evidence
wording. It sends that context in one capability-free Remember-assessor
conversation. The assessor returns structure and support selections only; it never decides truth,
confidence, ownership, lifecycle, review, or promotion policy, and it never
assesses or commits an evidence item independently. Submitted `valid_from` and
`valid_to` values are preserved exactly; a non-null bound is accepted only when
its RFC3339/date representation occurs in the cited evidence. Unsupported or
hallucinated bounds receive another complete correction turn.

The assessor receives:

- the full exact staged evidence and the raw logical client Relationship
  proposals, including every client `predicate.proposed_key`
- server-issued Entity occurrence groundings and complete candidate sets for
  the submitted names and active aliases
- every exact active predicate key/alias match, plus relevant active options up
  to the request bound; unrelated team predicates are not loaded into context
- no tools, credentials, environment variables, network capability, or request
  headers
- no prior raw evidence history, cross-team data, or authority to select stored
  IDs outside its supplied options

Entity preflight remains complete: at most 20 same-name candidates are loaded
for each submitted name. Predicate preflight is exact and bounded: exact
key/alias matches are merged with evidence-relevant active options and the
provider context is capped at `AI_VERIFIER_MAX_PREDICATE_OPTIONS` (default 100).
If exact matches alone exceed that bound, the run ends as
`predicate_options_overflow` before any provider call. Candidate-context token
overflow remains `catalog_context_overflow`; if the exact evidence and contract
cannot fit the input budget, it ends as `assessment_input_overflow`. An omitted
exact predicate must never become a false registration decision.

One request-scoped attempt has one logical assessor conversation whose initial
request contains every evidence item and relationship ref. A malformed complete
response can receive at most two same-session repairs after the initial
response. Timeout/transport/provider failures end the request with a bounded
operational error and a replayable failed attempt; retrying the same
idempotency key starts a new request. There is no lease reclaim, durable retry
budget, or background assessor retry.

The response is a closed structural-assessment schema. It must contain complete
security signals plus normalized Entity and Relationship results. Entity results
select an allowlisted grounding reference; Relationship results select
boundary-reference ranges for predicates, typed Values, and supports. A
predicate result can select only an allowlisted active predicate or return a
typed registration-required result. The provider cannot clear a deterministic
security signal, use a range outside the cited evidence, return policy fields,
decide durable status, create or activate a predicate, or override authenticated
ownership. The atomic commit adapter supplies only deterministic server defaults
needed by the current canonical commit path.

A `hidden_control_markup` signal is valid only when its exact cited span contains
a covered hidden control or active markup. An ungrounded signal invalidates the
complete assessor response; it cannot quarantine or commit a submission.

A registration-required result follows a controlled, auditable predicate
workflow. In the same semantic transaction, the server takes a team/key advisory
lock, rechecks the latest definition, reuses a compatible active definition, or
creates a restricted definition with append-only registration provenance. It
does not make a raw client or assessor predicate durable by itself. If the
assessor cannot safely support a hint, the server records `not_stored` when all
evidence remains covered by stored splits. Otherwise the complete batch is
`rejected/no_supported_memory`; exact client-owned races are
`rejected/stale_input`. Provider, configuration, database, embedding, timeout,
and internal faults are structured operational `isError` results. The public
result never exposes a review task or approval action.

Malformed, incomplete, duplicate, out-of-allowlist, or otherwise invalid
responses do not produce a partial semantic commit. Malformed content uses the
same-session complete-repair budget (three provider calls total); provider
failures are visible as typed operational errors. No retry worker runs after the
request returns.

## Promotion, Rejection, and Quarantine

On a clean valid assessment, one PostgreSQL transaction:

1. creates canonical evidence and observations;
2. applies Entity/Value/Relationship and lifecycle decisions;
3. resolves, reuses, or registers predicates only through controlled server
   policy;
4. records assessment outcomes and audit history;
5. writes search documents together with validated current vectors; and
6. records the terminal Remember attempt and events.

No canonical evidence or semantic subset commits before that transaction: all
items and required vectors promote together or none do. Provider calls happen
before transaction entry and late source/document fences produce a retryable
`commit_conflict` with zero semantic commit.

If assessment rejects the proposed semantic content without a security concern,
the submission becomes rejected. The client can submit a new, complete
submission, but cannot negotiate or append evidence to the rejected one.

A deterministic intake or assessor security concern returns a quarantined
terminal result. Its terminal transaction records bounded attempt, event, and
security metadata but creates no canonical evidence, search document, vector,
or raw quarantine payload. An operational failure artifact, when captured, is
control-only and its bytes are physically purged within seven days while safe
attempt/event hashes remain. A replacement is a new independent complete
remember call; it never releases or extends quarantined input.

## Public Remember Result

The original call returns a terminal owner-scoped result. The public states are:

| Processing state | Meaning | Client action |
| --- | --- | --- |
| completed | Canonical promotion succeeded. | Recall or trace normally. |
| rejected | The server rejected the submission. | Submit a new complete request if appropriate. |
| quarantined | Security concern; bounded terminal/audit metadata remains, but no canonical evidence or raw quarantine payload is stored. | Do not use it; optionally submit a replacement. |

The result reports per-evidence disposition and search readiness, one
Relationship disposition for each submitted ref, bounded error codes, and
`correlation_id`. `search_state` is only `current` or `not_required`.
Semantic rejection uses only `no_supported_memory` or `stale_input`.
Operational failures use `isError: true` and one of
`idempotency_conflict`, `provider_unavailable`, `provider_response_invalid`,
`input_budget_exceeded`, `configuration_invalid`, `embedding_unavailable`,
`embedding_response_invalid`, `commit_conflict`, `database_failure`,
`request_timeout`, `request_cancelled`, or `internal_failure`; the internal
stage is never exposed. Raw provider, database, prompt, and credential details
never appear in the result.

Historical non-v2.6 submissions retain their internal migration stage but are
not projected through this contract. The removed status tool returns method/tool
not found rather than a compatibility alias.

Embedding is a required write phase. A failed or invalid batch prevents the
semantic transaction and returns `embedding_unavailable` or
`embedding_response_invalid`; it is not an asynchronous search degradation.

Document-centric reconciliation remains an operator concern for legacy drift;
new Remember and correction writes do not create `embedding_jobs`, pending
vectors, or worker leases. The control view compares expected/current document
hashes and versions and repairs only rows whose fences still match.

There is deliberately no client-facing placement/review tool, review task,
client confidence value, or follow-up-evidence action in this contract.

There is no `awaiting_review` state or `semantic_hold` projection. Every
terminal rejection or failure carries a closed error with one next action.
`no_supported_memory` and `stale_input` mean send one complete corrected batch
with a new top-level idempotency key. Operational failures use their bounded
retry or operator action; clients must not invent semantic repairs from the
message text.

## Recall Safety Boundary

Recall remains evidence-first and Relationship-supported. It returns evidence
contexts in its established shape after authorization and support-path
hydration. It does not turn recall into a graph-only response.

Returned evidence is untrusted data. A host must not follow instructions in
recalled text, invoke tools because recalled text asks it to, disclose secrets,
or make outbound calls from it. Client-side tool policy and least privilege are
required even when the write-time scanner and assessor have passed.

## Failure and Audit Rules

- remember is idempotent within its authenticated team/profile namespace.
- Retryable operational failures return `retry_same_request`; a late provider or
  document fence cannot promote a newer source.
- Provider responses, raw evidence, prompts, credentials, embeddings, and
  database errors never appear in public errors or logs.
- Security decisions, terminal outcomes, promotions, lifecycle events, and
  failure-artifact expiry are auditable.
- A later independent submission cannot reuse or release quarantined input;
  deterministic quarantine retains bounded terminal/audit metadata rather than
  a raw payload.

## Migration Boundary

The v2.6.1 contract is a hard switch from caller-owned text ranges to
server-owned grounding and assessor-owned repair. The historical restart
migration terminalizes unfinished pre-v2.6 Remember submissions and preserves
accepted state; v2.6.1 runtime does not resume or reinterpret them. The current
request creates IDs in memory, completes assessment and embeddings, then writes
source revisions, evidence, semantics, search vectors, and terminal attempt
history in one fenced transaction. Already accepted semantic state and durable
provenance ranges remain unchanged. Normal runtime paths do not accept the old
Remember fields.

## Acceptance Checks

1. a missing Relationship, caller span/surface/support field, duplicate or
   out-of-range evidence index, or incomplete evidence-index coverage is
   rejected before provider work;
2. Base64 and injection evidence are rejected before provider work;
3. request content cannot enter recall or search before atomic promotion;
4. every assessor response covers every evidence item with a security verdict;
5. model concern quarantines rather than partially commits;
6. a clean valid submission promotes canonical state, search documents, and
   current vectors atomically;
7. two relationships may cite one evidence item, while the assessor still
   receives every distinct relationship ref in one run-scoped request;
8. exact predicate, candidate-context, or input overflow makes zero provider
   call and zero semantic write;
9. a malformed assessor response is repaired in one session (at most three
   complete calls), while provider or internal exhaustion is a structured
   operational `isError` with zero semantic subset;
10. mixed stored/`not_stored` refs complete only when stored support covers every
    evidence item; otherwise the run is `rejected/no_supported_memory`;
11. the terminal result never exposes a review task, semantic hold, or raw
    assessor/prompt content;
12. deterministic quarantine retains no raw payload, and operational
    failure-artifact bytes are inaccessible to request/profile paths and purged
    within seven days;
13. a predicate is added or activated only through the controlled validated
   registration workflow; and
14. recall remains evidence-first and treats returned content as untrusted.

## Terminal Status Error Completeness

Terminal Remember results map persisted reasons to the closed public error
vocabulary. A `rejected` or `quarantined` result never returns `errors: []`;
operational failures are `isError` results with structured content. Completed
submissions do not gain semantic errors, and required vector failures prevent
the canonical transaction rather than becoming an asynchronous degradation.
The public mapping remains separate from provider and database diagnostics.
