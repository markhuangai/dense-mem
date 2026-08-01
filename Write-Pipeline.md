# Memory Write Pipeline

Dense-Mem accepts a complete, client-generated structural proposal with exact
evidence. The server owns every durable decision. There is no client review
loop, placement selection, or request for follow-up evidence.

The public write contract is dense-mem.v2.4. Discover the current schema with
MCP tools/list; this page explains the lifecycle behind remember and
get_submission_status.

## End-To-End Flow

~~~mermaid
%%{init: {"flowchart": {"curve": "linear"}}}%%
flowchart LR
  Client["Client"] --> Remember["remember: evidence + exact-span proposal"]
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

The client must split source material into the smallest meaningful Entity names
and supply the proposed Relationships that it wants assessed. A proposal is
required, not an optional hint.

Each Entity has exactly one evidence span. Each predicate has an exact surface
and span. Each Relationship cites spans covering its subject, predicate, and
Entity or typed Value object. Offsets are zero-based Unicode code-point offsets,
with an inclusive start and exclusive end.

~~~json
{
  "evidence": [{
    "content": "Dense-Mem uses PostgreSQL.",
    "source_type": "manual",
    "idempotency_key": "storage-architecture-20260801"
  }],
  "proposal": {
    "entities": [
      {
        "ref": "subject",
        "name": "Dense-Mem",
        "entity_kind": "project",
        "evidence": [{"evidence_index": 0, "start": 0, "end": 9}]
      },
      {
        "ref": "object",
        "name": "PostgreSQL",
        "entity_kind": "product",
        "evidence": [{"evidence_index": 0, "start": 15, "end": 25}]
      }
    ],
    "relationships": [{
      "proposal_id": "uses",
      "subject_ref": "subject",
      "predicate": {"surface": "uses", "evidence_index": 0, "start": 10, "end": 14},
      "object_ref": "object",
      "polarity": "+",
      "modality": "statement",
      "evidence": [{"evidence_index": 0, "start": 0, "end": 26}]
    }]
  }
}
~~~

The client may offer a same-team known_entity_id as a candidate hint, but
cannot choose canonical IDs, lifecycle state, support, a predicate definition,
or any acceptance outcome. Values must be typed; a string display or unit must
also be exact evidence text.

The source and lifecycle fields on evidence retain their normal ownership,
idempotency, and source-revision rules. A direct replacement of owned current
evidence uses supersedes_evidence_ids; advancing a source revision uses
previous_source_revision; they cannot be combined.

## Deterministic Write-Time Security Gate

Before a submission row is created, the server scans every raw evidence item.
It rejects, at minimum:

- Base64 and encoded payloads, including data URIs, PEM-like material, JWT-like
  tokens, and folded encoded blocks
- hidden controls, bidirectional controls, and deceptive Unicode forms
- executable or active markup
- role spoofing and instruction overrides
- attempts to solicit credentials, environment variables, secrets, outbound
  network calls, or data exfiltration

The scan normalizes a bounded set of common encodings before checking. It is a
deny gate, not proof that the evidence is benign. Rejections return a bounded
safe error and create no submission or canonical memory.

The worker repeats the deterministic scan before it uses staged content. This
catches staged data that could otherwise be processed after a scanner policy
change or a compromised intake path.

## Isolated Submission State

Passing evidence is stored only in the submission staging boundary:

~~~text
submission_runs
  ├── exact staged evidence
  ├── client proposal
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
Entity candidates, and the team predicate catalog. It sends them to one
capability-free assessor request.

The assessor receives:

- the full exact staged evidence and the client proposal
- bounded Entity candidates and predicate options for normalization
- no tools, credentials, environment variables, network capability, or request
  headers
- no prior raw evidence history, cross-team data, or authority to select stored
  IDs outside its supplied options

The response is a closed schema. It must contain one security verdict, signals,
and bounded justification for every evidence item; normalized Entity and
Relationship results; and either a registered predicate result or a
well-formed novel predicate candidate. The provider cannot clear a deterministic
security signal, decide durable status, create support outside the evidence, or
override authenticated ownership.

The assessor may identify a new predicate. After server validation, the server
registers that predicate atomically with the accepted semantic commit. Clients
do not receive a predicate-review task.

Malformed, incomplete, duplicate, out-of-allowlist, or otherwise invalid
responses do not produce a partial semantic commit. The worker uses bounded
durable retries; required failures remain visible as a terminal failed
submission.

## Promotion, Rejection, and Quarantine

On a clean valid assessment, one PostgreSQL transaction:

1. creates canonical evidence and observations;
2. applies Entity/Value/Relationship and lifecycle decisions;
3. registers a validated new predicate when needed;
4. records assessment outcomes and audit history;
5. creates derived full-text/vector search work; and
6. marks the submission completed.

No canonical evidence or semantic subset commits before that transaction.
Embeddings are asynchronous derived state and cannot overwrite a newer source.

If assessment rejects the proposed semantic content without a security concern,
the submission becomes rejected. The client can submit a new, complete
submission, but cannot negotiate or append evidence to the rejected one.

If either security layer finds a concern, the submission becomes quarantined.
PostgreSQL sets quarantine_expires_at to exactly 24 hours after the quarantine
completion timestamp. The staged evidence remains isolated only for that
interval, then the cleanup job hard-deletes it. A replacement is a new complete
remember call with replaces_quarantined_submission_id; it never releases or
extends the quarantined data.

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

Status also reports per-evidence outcomes, per-proposal Relationship outcomes,
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

1. a missing or span-mismatched proposal is rejected before staging;
2. Base64 and injection evidence are rejected before staging;
3. staged content cannot enter recall or search before atomic promotion;
4. every assessor response covers every evidence item with a security verdict;
5. model concern quarantines rather than partially commits;
6. a clean valid submission promotes canonical state and derived search work
   atomically;
7. status never exposes a review task or raw assessor/prompt content;
8. quarantined staging is removed only after its exact 24-hour retention;
9. a new predicate is registered only through a validated server commit; and
10. recall remains evidence-first and treats returned content as untrusted.

