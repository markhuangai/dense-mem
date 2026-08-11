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
  Client["Client"] --> Remember["remember: evidence + exact-span relationships"]
  Remember --> Scan{"Deterministic scan"}
  Scan -- reject --> Reject["Safe write error; nothing staged"]
  Scan -- pass --> Stage["Isolated PostgreSQL submission"]
  Stage --> Status["submission_id"]
  Stage --> Worker["Server assessment worker"]
  Worker --> Assessor["Capability-free assessor"]
  Assessor --> Security{"Security concern?"}
  Security -- yes --> Quarantine["Quarantine for exactly 24 hours"]
  Security -- no --> Validate{"Closed-schema validation"}
  Validate -- reject --> Rejected["Terminal rejected result"]
  Validate -- pass --> Promote["Atomic canonical promotion"]
  Promote --> Search["Derived search/embedding jobs"]
  Status --> Poll["get_submission_status"]
  Quarantine --> Poll
  Rejected --> Poll
  Search --> Poll
~~~

remember acknowledges only durable submission staging. It does not claim that
the evidence has become active memory or that an embedding is current.

## Client Submission Contract

The client supplies a nonempty top-level `relationships` array; it is required,
not an optional hint. An evidence item is currently bounded to 999 characters,
and one item can support multiple distinct propositions, so there is no fixed
single relationship object or separate cross-referenced `proposal.entities`
object.

When more than one evidence item is submitted, every evidence index must appear
in at least one relationship `supports` span. The server reports all missing
indexes together and stages nothing.

Each relationship is self-contained. Its inline subject and object Entity (or
typed Value), predicate, and supports have exact evidence spans. Offsets are
zero-based Unicode code-point positions, with an inclusive start and exclusive
end. Every component span must be covered by a support span in the same evidence
item.

`relationship.ref` is request-local and unique. Several distinct relationships
may cite the same `evidence_index`; one long evidence item is not limited to one
proposition. `predicate.proposed_key` is at most 128 Unicode code points and
the exact predicate `surface` is at most 256. The server rejects an oversized
value; it never truncates a key or surface before assessment.

~~~json
{
  "evidence": [{
    "content": "Dense-Mem uses PostgreSQL.",
    "source_type": "manual",
    "idempotency_key": "storage-architecture-20260801"
  }],
  "relationships": [{
      "ref": "uses-postgresql",
      "subject": {
        "name": "Dense-Mem",
        "entity_kind": "project",
        "span": {"evidence_index": 0, "start": 0, "end": 9}
      },
      "predicate": {
        "proposed_key": "uses",
        "surface": "uses",
        "span": {"evidence_index": 0, "start": 10, "end": 14}
      },
      "object": {"entity": {
        "name": "PostgreSQL",
        "entity_kind": "product",
        "span": {"evidence_index": 0, "start": 15, "end": 25}
      }},
      "polarity": "+",
      "modality": "statement",
      "supports": [{"evidence_index": 0, "start": 0, "end": 26}]
  }]
}
~~~

The client makes its best effort to set `predicate.proposed_key` to a matching
team predicate. It is a suggestion, not authority. The predicate `surface` and
the typed Value `surface`, display, or unit must equal their exact submitted
evidence spans. `object` contains exactly one of `entity` or `value`; a
typed Value includes its type, canonical value, surface, and span.

The client may offer a same-team `known_entity_id` inside an inline Entity as a
candidate hint, but cannot choose canonical IDs, lifecycle state, support,
predicate registration, or any acceptance outcome.

The source and lifecycle fields on evidence retain their normal ownership,
idempotency, and source-revision rules. A direct replacement of owned current
evidence uses supersedes_evidence_ids; advancing a source revision uses
previous_source_revision; they cannot be combined.

## Deterministic Write-Time Security Gate

Before a submission row is created, the server scans every raw evidence item.
It rejects, at minimum:

- Base64 and encoded payloads, including data URIs, PEM-like material, JWT-like
  tokens, and folded encoded blocks
- hidden controls or bidirectional Unicode that form an evasive control flood
  or normalize into another threat pattern
- executable or active markup
- role spoofing and instruction overrides
- directives to solicit credentials, environment variables, secrets, outbound
  network calls, or data exfiltration

The scan normalizes a bounded set of common encodings before checking. It is a
deny gate, not proof that the evidence is benign. Rejections return a bounded
safe error with bounded rule IDs and source spans, and create no submission or
canonical memory. One inspection layer covers percent escapes, `\xNN`,
`\uNNNN`, and HTML entities without changing the staged text. Those forms,
backslashes, quotes, and brackets are not rejection reasons by themselves. A
direct directive remains unsafe when wrapped in punctuation; descriptive prose
that quotes a tool/network attack remains eligible. Structured proposal string
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
  ├── client relationships
  ├── attempts, lease, and bounded error code
  ├── assessor response and per-item outcomes
  └── optional same-owner replacement link

canonical knowledge (separate until promotion)
  ├── evidence and observations
  ├── Entities, Values, Relationships, lifecycle history
  ├── search documents and embedding jobs
  └── audit lineage
~~~

The raw staging boundary is not searchable, recallable, or canonical memory.
Every row is team- and owner-scoped using the authenticated request context and
PostgreSQL RLS.

### Semantic hold lifecycle

When the complete assessor response is valid but server policy cannot promote
it, the run and every item become `awaiting_review`/`candidate` with zero
semantic writes and no review task. #152 records an immutable
`submission_holds` fact with the assessment, bounded reason, owner, terminal
timestamp, and exact 24-hour expiry. The run projection is `active` until the
server expiry job changes it to `expired`; neither state is recallable or an
approval.

`replaces_submission_id` is optional public intake input. It names only an
owned active or expired hold and creates a complete successor through the
normal scanner, assessor, catalog, and atomic-commit path. A queued, guarded, or
processing successor occupies the one replacement slot. A held, rejected,
quarantined, failed, or expired successor emits a release outcome and leaves the
target unchanged. On the first successful successor promotion, PostgreSQL
locks the target and marks it `superseded` in the same transaction as the new
semantic/search writes. Replays are idempotent; cross-profile, cross-team,
absent, and already-superseded targets return the same bounded error classes.

## Server-Owned Assessment

The worker assesses the complete staged submission as one run-scoped unit. It
loads the complete same-team candidate set for each submitted Entity surface,
resolves each proposed predicate key or alias exactly, and adds only bounded
active predicate options ranked by the submitted keys and evidence wording.
It sends that context in one capability-free assessor conversation. It never
assesses or commits an evidence item independently.

The assessor receives:

- the full exact staged evidence and the raw client relationship proposals,
  including every client `predicate.proposed_key`
- complete Entity candidate sets for submitted surfaces
- every exact active predicate key/alias match, plus relevant active options up
  to the request bound; unrelated team predicates are not loaded into context
- no tools, credentials, environment variables, network capability, or request
  headers
- no prior raw evidence history, cross-team data, or authority to select stored
  IDs outside its supplied options

Entity preflight remains complete: at most 20 same-name candidates are loaded
for each submitted surface. Predicate preflight is exact and bounded: exact
key/alias matches are merged with evidence-relevant active options and the
provider context is capped at `AI_VERIFIER_MAX_PREDICATE_OPTIONS` (default 100).
If exact matches alone exceed that bound, the run ends as
`predicate_options_overflow` before any provider call. Candidate-context token
overflow remains `catalog_context_overflow`; if the exact evidence and contract
cannot fit the input budget, it ends as `assessment_input_overflow`. An omitted
exact predicate must never become a false registration decision.

One placement-run attempt has one logical assessor conversation whose initial
request contains every evidence item and relationship ref. A malformed complete
response can receive at most four same-history complete-regeneration turns. A
timeout, transport, protocol, or HTTP failure ends that conversation and
requeues a fresh history through the durable placement-attempt budget. Once a
valid run-scoped assessment is persisted, restart, lease reclaim, and commit
retry reuse it and make no further provider call.

The response is a closed schema. It must contain one security verdict, signals,
and bounded justification for every evidence item plus normalized Entity and
Relationship results. A predicate result can select only an allowlisted active
predicate or return a typed registration-required result. The provider cannot
clear a deterministic security signal, decide durable status, create support
outside the evidence, create or activate a predicate, or override authenticated
ownership.

A `hidden_control_markup` signal is valid only when its exact cited span contains
a covered hidden control or active markup. An ungrounded signal invalidates the
complete assessor response; it cannot quarantine or commit a submission.

A registration-required result follows a controlled, auditable predicate
workflow. In the same semantic transaction, the server takes a team/key advisory
lock, rechecks the latest definition, reuses a compatible active definition, or
creates a restricted definition with append-only registration provenance. It
does not make a raw client or assessor predicate durable by itself. If server
policy cannot promote a valid complete result, every item enters the existing
`awaiting_review` compatibility hold with zero semantic commit and no new review
task. #152 owns that hold's formal expiry and replacement lifecycle.

Malformed, incomplete, duplicate, out-of-allowlist, or otherwise invalid
responses do not produce a partial semantic commit. Malformed content uses the
same-history complete-regeneration budget; provider failures use durable fresh
attempts. Required exhaustion remains visible as a terminal failed submission.

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
complete remember call through the quarantine-specific workflow; it never
releases or extends the quarantined data.

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

Status reports per-evidence search readiness, bounded error codes,
quarantine_expires_at when applicable, and search state.
Terminal placement failures include `semantic_assessment_terminal_failure` with
an allowlisted stage such as `predicate_options_overflow` or `assessment`; raw
provider, database, prompt, and credential details never appear in status.
search_state=pending means canonical promotion may be complete while a derived
embedding is still being built. current and not_required are ready for the
evaluation/recall freshness gate; failed is a visible degradation.

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

The `awaiting_review` compatibility hold used by the #151 worker is not a
client review action and is not a partial promotion. #152 defines its immutable
expiry/replacement lifecycle before the final public status contract removes
placement-era compatibility states.

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
- A replacement links to a quarantined source without prematurely deleting that
  source's required 24-hour retention window.

## Migration Boundary

The V2.4 migration is a hard switch from client-placement orchestration to
server-owned submissions. It stages safe legacy queued/guarded/processing work
without duplicating canonical semantic records, preserves already accepted
semantics, and terminalizes unresolved legacy review work. Normal runtime paths
do not run legacy placement workers or expose legacy placement tools.

## Acceptance Checks

1. a missing or span-mismatched relationship is rejected before staging, and a
   relationship-bearing request covers every submitted evidence index;
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
9. a valid but non-promotable run becomes an `awaiting_review` compatibility
   hold with no review task or semantic subset, and expiry changes only its
   projection;
10. status never exposes a review task or raw assessor/prompt content;
11. a same-owner complete replacement leaves its target unchanged until a
   successful atomic promotion, while a successful successor supersedes it;
12. quarantined staging is removed only after its exact 24-hour retention;
13. a predicate is added or activated only through the controlled validated
   registration workflow; and
14. recall remains evidence-first and treats returned content as untrusted.
