# Memory Write Pipeline

Dense-Mem accepts exact evidence plus a complete client-generated relationship
array. The server owns every durable decision. There is no client review loop,
placement selection, or request for follow-up evidence.

The public write contract is the current closed schema. Discover it with MCP
tools/list; callers do not select a contract version. This page explains the lifecycle behind remember and
get_submission_status.

## End-To-End Flow

~~~mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Client["Client"] --> Remember["remember: evidence + logical relationships<br/>with evidence_indices"]
  Remember --> Scan{"Deterministic scan"}
  Scan -- reject --> Reject["Safe write error; nothing staged"]
  Scan -- pass --> Stage["Isolated PostgreSQL submission"]
  Stage --> Status["submission_id"]
  Stage --> Worker["Server placement worker"]
  Worker --> Ground["Server-owned grounding<br/>and bounded catalogs"]
  Ground --> Assessor["Capability-free Remember assessor"]
  Assessor --> Security{"Security concern?"}
  Security -- yes --> Quarantine["Quarantine for exactly 24 hours"]
  Security -- no --> Validate{"Closed-schema validation"}
  Validate -- invalid --> Correct["Complete same-session repair<br/>or bounded operational failure"]
  Validate -- valid --> Policy["Deterministic server policy"]
  Policy -- reject --> RejectMemory["Rejected: no_supported_memory<br/>or stale_input"]
  Policy -- fault --> Failed["Terminal operational failure"]
  Policy -- accept --> Promote["Atomic canonical promotion"]
  Promote --> Search["Derived search/embedding jobs"]
  Status --> Poll["get_submission_status"]
  Quarantine --> Poll
  Failed --> Poll
  Correct --> Poll
  Search --> Poll
~~~

remember acknowledges only durable submission staging. It does not claim that
the evidence has become active memory or that an embedding is current.

The intake operation is implemented by the Remember capability boundary. Its
application API performs strict conversion, validates evidence-index coverage,
scans evidence and structured proposal values before persistence, and sends a
single staging request through a consumer-owned port. The PostgreSQL adapter is
composition-only and returns the submission identifier only after the existing
ledger transaction has committed exact evidence plus processing intent. Provider
calls and placement policy remain downstream worker responsibilities.

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

The worker repeats the deterministic scan before it uses staged content. This
catches staged data that could otherwise be processed after a compromised or
stale intake path. Scanner events and pre-stage rejection audits retain bounded
decisions, reasons, rule IDs, sources, and spans without a scanner version or
policy hash.

## Isolated Submission State

Passing evidence is stored only in the submission staging boundary:

~~~text
submission_runs
  ├── exact staged evidence
  ├── client relationships and server processing intent
  ├── attempts, lease, and bounded error code
  ├── assessor response and per-ref dispositions
  └── source/supersession intent until accepted commit

canonical knowledge (separate until promotion)
  ├── evidence and observations
  ├── Entities, Values, Relationships, lifecycle history
  ├── search documents and embedding jobs
  └── audit lineage
~~~

The raw staging boundary is not searchable, recallable, or canonical memory.
Every row is team- and owner-scoped using the authenticated request context and
PostgreSQL RLS.

### Restart boundary and resubmission lifecycle

Remember has no semantic hold, replacement slot, approval state, or
`submission_holds` table. The assessor receives one complete request and can
repair grounding, identity, predicate reconciliation, and supported splits in
the same bounded conversation. A supported subset is committed only when every
evidence item remains covered; unsupported refs are recorded as `not_stored`.
Only `no_supported_memory` and `stale_input` are semantic rejections. Provider,
configuration, database, and internal faults are typed operational failures.
No Entity, Value, Relationship, lifecycle, search document, or embedding job is
committed from a rejected or failed run.

The historical v2.5 restart migration terminalizes unfinished queued, guarded,
processing, and `awaiting_review` placement work whose ingest metadata identifies
the old Remember origin. It marks those rows with a contract-superseded failure,
clears claims, and preserves completed canonical state. This is upgrade
archaeology only; v2.6 runtime never accepts the old normalizer contract or
resumes those rows. Unrelated non-Remember placement work remains untouched.

## Server-Owned Normalization and Assessment

The worker processes the complete staged submission as one run-scoped unit. For
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

One placement-run attempt has one logical assessor conversation whose initial
request contains every evidence item and relationship ref. A malformed complete
response can receive at most four same-session complete-repair turns after the
initial response. Timeout/transport failures receive bounded in-claim transport
retries; an exhausted provider failure requeues through the durable
placement-attempt budget. Once a valid run-scoped response is persisted,
restart, lease reclaim, and commit retry reuse it and make no further provider
call.

The response is a closed structural-assessment schema. It must contain complete
security signals plus normalized Entity and Relationship results. Entity results
select an allowlisted grounding reference; Relationship results select
boundary-reference ranges for predicates, typed Values, and supports. A
predicate result can select only an allowlisted active predicate or return a
typed registration-required result. The provider cannot clear a deterministic
security signal, use a range outside the cited evidence, return policy fields,
decide durable status, create or activate a predicate, or override authenticated
ownership. The existing atomic commit adapter supplies only deterministic
server defaults needed by the legacy commit path.

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
`rejected/stale_input`. Provider, configuration, database, and internal faults
are `failed`. Public status never exposes a review task or approval action.

Malformed, incomplete, duplicate, out-of-allowlist, or otherwise invalid
responses do not produce a partial semantic commit. Malformed content uses the
same-session complete-repair budget; provider failures use durable fresh
attempts. Required exhaustion remains visible as a typed operational failure.

## Promotion, Rejection, and Quarantine

On a clean valid assessment, one PostgreSQL transaction:

1. creates canonical evidence and observations;
2. applies Entity/Value/Relationship and lifecycle decisions;
3. resolves, reuses, or registers predicates only through controlled server
   policy;
4. records assessment outcomes and audit history;
5. creates derived full-text/vector search work; and
6. marks the submission completed.

No canonical evidence or semantic subset commits before that transaction: all
items in the run promote together or none do. Embeddings are asynchronous
derived state and cannot overwrite a newer source.

If assessment rejects the proposed semantic content without a security concern,
the submission becomes rejected. The client can submit a new, complete
submission, but cannot negotiate or append evidence to the rejected one.

An intake scan concern is not a submission: it returns a bounded error and
records only safe audit metadata before any evidence or provider-bound proposal
is staged. A repeated worker scan or an assessor security concern quarantines an
already-staged submission. A deterministic worker hit records its matching
placement fragment's security event, quarantine, and terminal placement result in one PostgreSQL
transaction. PostgreSQL sets quarantine_expires_at to exactly 24 hours after the
quarantine completion timestamp. The isolated raw payload copy remains only for
that interval, then the cleanup job hard-deletes it. Append-only evidence,
identifiers, hashes, security events, and lineage remain restricted audit
history and are never recallable or provider input. A replacement is a new
independent complete remember call; it never releases or extends the
quarantined data.

## Public Status Contract

The client polls only get_submission_status with the returned submission_id.
The status is owner-scoped and contains safe, bounded state only:

| Processing state | Meaning | Client action |
| --- | --- | --- |
| queued | Staged and waiting for a worker. | Poll later. |
| processing | A worker owns a live assessment lease. | Poll later. |
| completed | Canonical promotion succeeded. | Recall or trace normally. |
| rejected | The server rejected the submission. | Submit a new complete request if appropriate. |
| quarantined | Security concern; the isolated raw payload copy expires after 24 hours while immutable source history remains restricted and non-recallable. | Do not use it; optionally submit a replacement. |
| failed | Required worker/provider/persistence work exhausted its bounded retry policy. | Diagnose using the safe terminal stage in `errors` and correlation context, then submit anew if appropriate. |

Status reports per-evidence search readiness, one relationship disposition for
each submitted ref, bounded error codes,
quarantine_expires_at when applicable, and search state.
Terminal v2.6 Remember status uses `no_supported_memory` or `stale_input` for
semantic rejection and `provider_unavailable`, `provider_response_invalid`,
`input_budget_exceeded`, `configuration_invalid`, `database_failure`,
or `internal_failure` for operational failure; the
internal stage is never exposed. Raw
provider, database, prompt, and credential details never appear in status.
search_state=pending means canonical promotion may be complete while a derived
embedding is still being built. current and not_required are ready for the
evaluation/recall freshness gate; failed is a visible degradation.

Historical non-v2.6 submissions retain their internal migration stage but are
not projected through this contract. Polling one returns the same bounded
not-found response as an unavailable submission.

Failed embedding is an asynchronous search degradation, not a rollback of
accepted evidence or canonical Relationships. The aggregate and affected
Evidence status may include the bounded `search_indexing_delayed` code; it
never includes a provider body, credential, prompt, or raw transport error.
Failed documents remain eligible for lexical recall, while their vectors stay
out of vector search.

An always-on daily reconciliation run records durable run history, makes one
raw-provider canary request, and only after a successful canary requeues the
pre-cutoff recoverable backlog in bounded batches. `embedding_jobs` is the only
current failure authority. The control projection groups unresolved failed,
queued-after-failure, and processing-after-failure jobs by team, contract,
dimensions, source kind, failure class, and failure code. Quota,
authentication, and provider-access failures remain eligible for the next daily
canary; input, contract, and unknown failures stay blocked. There is no user
retry action. Operators use the control Search convergence view for failure
codes, affected teams, guidance, run/canary state, bounded operation logs, and
metrics.

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
- Retryable worker failures are lease-bound and durable; a stale worker cannot
  promote a newer claim.
- Provider responses, raw evidence, prompts, credentials, embeddings, and
  database errors never appear in public errors or logs.
- Security decisions, terminal outcomes, promotions, lifecycle events, and
  quarantine cleanup are auditable.
- A later independent submission cannot prematurely delete or release a
  quarantined source during its required 24-hour retention window.

## Migration Boundary

The v2.6 contract is a hard switch from caller-owned text ranges to server-owned
grounding and assessor-owned repair. The historical restart migration
terminalizes unfinished pre-v2.6 Remember submissions and preserves accepted
state; v2.6 runtime does not resume or reinterpret them. New staging records
source/supersession intent, and only an accepted atomic commit activates those
effects. Already accepted semantic state and durable provenance ranges remain
unchanged. Normal runtime paths do not accept the old Remember fields.

## Acceptance Checks

1. a missing Relationship, caller span/surface/support field, duplicate or
   out-of-range evidence index, or incomplete evidence-index coverage is
   rejected before staging;
2. Base64 and injection evidence are rejected before staging;
3. staged content cannot enter recall or search before atomic promotion;
4. every assessor response covers every evidence item with a security verdict;
5. model concern quarantines rather than partially commits;
6. a clean valid submission promotes canonical state and derived search work
   atomically;
7. two relationships may cite one evidence item, while the assessor still
   receives every distinct relationship ref in one run-scoped request;
8. exact predicate, candidate-context, or input overflow makes zero provider
   call and zero semantic write;
9. a malformed assessor response is repaired in one session, while provider or
   internal exhaustion is `failed` with zero semantic subset;
10. mixed stored/`not_stored` refs complete only when stored support covers every
    evidence item; otherwise the run is `rejected/no_supported_memory`;
11. status never exposes a review task, semantic hold, or raw assessor/prompt content;
12. quarantined staging is removed only after its exact 24-hour retention;
13. a predicate is added or activated only through the controlled validated
   registration workflow; and
14. recall remains evidence-first and treats returned content as untrusted.

## Terminal Status Error Completeness

Status projection maps persisted terminal reasons to the closed public error
vocabulary. A `rejected` or `failed` state never returns `errors: []`; repeated
polls return the same deduplicated code/message pairs. Completed submissions do
not gain semantic errors, while search-index failures continue to use the
existing `search_indexing_delayed` behavior. The public mapping is intentionally
separate from provider and database diagnostics.
