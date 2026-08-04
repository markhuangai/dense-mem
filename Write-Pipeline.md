# Memory Write Pipeline

Dense-Mem accepts exact evidence plus a complete client-generated relationship
array. The server owns every durable decision. There is no client review loop,
placement selection, or request for follow-up evidence.

The public write contract is dense-mem.v2.4. Discover the current schema with
MCP tools/list; this page explains the lifecycle behind remember and
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

Each relationship is self-contained. Its inline subject and object Entity (or
typed Value), predicate, and supports have exact evidence spans. Offsets are
zero-based Unicode code-point positions, with an inclusive start and exclusive
end. Every component span must be covered by a support span in the same evidence
item.

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
safe error and create no submission or canonical memory. Its tool/network rule
requires directive wording, so descriptive prose and valid bidirectional
formatting do not become intake rejections.

The worker repeats the deterministic scan before it uses staged content. This
catches staged data that could otherwise be processed after a scanner policy
change or a compromised intake path.

## Isolated Submission State

Passing evidence is stored only in the submission staging boundary:

~~~text
submission_runs
  ├── exact staged evidence
  ├── client relationships
  ├── attempts, lease, and bounded error code
  ├── assessor response and per-item outcomes
  └── optional replacement link

canonical knowledge (separate until promotion)
  ├── evidence and observations
  ├── Entities, Values, Relationships, lifecycle history
  ├── search documents and embedding jobs
  └── audit lineage
~~~

The raw staging boundary is not searchable, recallable, or canonical memory.
Every row is team- and owner-scoped using the authenticated request context and
PostgreSQL RLS.

## Server-Owned Assessment

The worker loads the complete staged submission, a bounded list of same-team
Entity candidates, and the active team predicate catalog. It sends them to one
capability-free assessor request.

The assessor receives:

- the full exact staged evidence and the raw client relationship proposals,
  including every client `predicate.proposed_key`
- bounded Entity candidates and active predicate options for normalization
- no tools, credentials, environment variables, network capability, or request
  headers
- no prior raw evidence history, cross-team data, or authority to select stored
  IDs outside its supplied options

The predicate catalog is selected deterministically: exact key or alias matches
first, then structured relevance/rank. The request records whether that catalog
is complete for the decision; truncation must not turn an existing predicate
into a false new one.

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

A registration-required result follows the controlled, auditable predicate
workflow. It does not make a raw client or assessor predicate durable, and
clients do not receive a predicate-review task.

Malformed, incomplete, duplicate, out-of-allowlist, or otherwise invalid
responses do not produce a partial semantic commit. The worker uses bounded
durable retries; required failures remain visible as a terminal failed
submission.

## Promotion, Rejection, and Quarantine

On a clean valid assessment, one PostgreSQL transaction:

1. creates canonical evidence and observations;
2. applies Entity/Value/Relationship and lifecycle decisions;
3. consumes only a predicate decision already validated by the controlled
   registration workflow;
4. records assessment outcomes and audit history;
5. creates derived full-text/vector search work; and
6. marks the submission completed.

No canonical evidence or semantic subset commits before that transaction.
Embeddings are asynchronous derived state and cannot overwrite a newer source.

If assessment rejects the proposed semantic content without a security concern,
the submission becomes rejected. The client can submit a new, complete
submission, but cannot negotiate or append evidence to the rejected one.

An intake scan concern is not a submission: it returns a bounded error and
records only safe audit metadata before any evidence or provider-bound proposal
is staged. A repeated worker scan or an assessor security concern quarantines an
already-staged submission. A deterministic worker hit records its matching
placement fragment's security event, quarantine, and terminal placement result in one PostgreSQL
transaction. PostgreSQL sets quarantine_expires_at to exactly 24 hours after the
quarantine completion timestamp. The staged evidence remains isolated only for
that interval, then the cleanup job hard-deletes it. A replacement is a new
complete remember call with replaces_quarantined_submission_id; it never
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
| quarantined | Security concern; isolated staged data expires after 24 hours. | Do not use it; optionally submit a replacement. |
| failed | Required worker/provider/persistence work exhausted its bounded retry policy. | Diagnose using safe error/correlation context, then submit anew if appropriate. |

Status also reports per-evidence outcomes and per-relationship outcomes,
bounded error codes, quarantine_expires_at when applicable, and search state.
search_state=pending means canonical promotion may be complete while a derived
embedding is still being built. current and not_required are ready for the
evaluation/recall freshness gate; failed is a visible degradation.

There is deliberately no get_memory_placement, resolve_memory_placement,
review task, client confidence value, or follow-up-evidence action in this
contract.

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

1. a missing or span-mismatched relationship is rejected before staging;
2. Base64 and injection evidence are rejected before staging;
3. staged content cannot enter recall or search before atomic promotion;
4. every assessor response covers every evidence item with a security verdict;
5. model concern quarantines rather than partially commits;
6. a clean valid submission promotes canonical state and derived search work
   atomically;
7. status never exposes a review task or raw assessor/prompt content;
8. quarantined staging is removed only after its exact 24-hour retention;
9. a predicate is added or activated only through the controlled validated
   registration workflow; and
10. recall remains evidence-first and treats returned content as untrusted.
