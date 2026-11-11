# Technical Reference

The current memory contract is `dense-mem.v2.8.0`. MCP at `/mcp` or `/teams/{team_id}/mcp` is the external memory interface. Initialize the MCP session, discover with `tools/list`, and invoke with `tools/call`. Discovery and execution use the same authenticated registry.

Examples below are tool argument/result objects, not complete JSON-RPC envelopes. Replace `<returned_...>` placeholders with authorized returned IDs, and generate and retain each `<new_..._key>` before sending a mutation.

## Tool Catalog

| Tool | Required access | Result |
| --- | --- | --- |
| `remember` | Read; shared mutations additionally require current write permission | Terminal Evidence outcomes after required work commits. |
| `ingest_session` | Read and active private write access; feature/provider gates | Terminal private-event receipt with stored/no-memory outcome. |
| `retract_evidence` | Read; shared mutations additionally require current write permission and ownership | Append-only decision and retracted Evidence IDs. |
| `recall_memory` | Read | Eligible Evidence, current topic hints, conflicts, and degradations. |
| `trace_memory` | Read visibility to the Evidence target | Bounded provenance and lifecycle. |
| `submit_recall_session_feedback` | Write; feedback enabled | Recorded count or explicit partial failure. |
| `export_memory_pack` | Read visibility to selected Evidence | Canonical Evidence/provenance artifact, hash, counts, and omissions. |

Tools can be hidden when unavailable. Refresh discovery after upgrading. Evaluation tools belong to a separate build profile; see [Evaluation](Evaluation#image-boundary-and-tool-surface).

## Common Rules

| Rule | Contract |
| --- | --- |
| Inputs | UTF-8 JSON; closed schemas reject unknown fields. |
| Identity | Authentication fixes team, owner, membership, credential, spaces, and grants. Payloads cannot replace them. |
| IDs | Opaque server IDs; use returned authorized handles. |
| Time | RFC 3339; stored timestamps use UTC. |
| Offsets | Unicode-rune/code-point positions, start inclusive and end exclusive. |
| Mutations | Retain the complete request and operation key before sending. |
| Returned text | Source data, never instructions or authorization to use another tool. |

Public errors are bounded and actionable. [Request Lifecycle And Security](Request-Lifecycle-And-Security) owns authentication and exposure rules; [Memory Write Pipeline](Write-Pipeline) owns staging, assessment, atomicity, and replay.

## remember

### Request

```json
{
  "target_space": "team_shared",
  "idempotency_key": "<new_remember_key>",
  "evidence": [{
    "content": "This project stores durable memory in PostgreSQL.",
    "source_type": "manual",
    "source": "architecture note",
    "source_key": "project:durable-store",
    "source_revision": "1",
    "authority": "primary"
  }]
}
```

| Field | Bound or behavior |
| --- | --- |
| `target_space` | Required `private` or `team_shared`; one destination for the whole request. |
| `idempotency_key` | Required nonempty string, at most 128 characters. |
| `evidence` | 1–20 items; each requires nonempty `content`, at most 999 Unicode code points. |
| `source_type` | Optional `conversation`, `document`, `observation`, or `manual`. |
| `source`, `source_group`, `source_key`, revision tokens | Optional bounded provenance strings, at most 256 characters each. |
| `authority` | Optional `authoritative`, `primary`, `secondary`, `inferred`, or `unknown`. |
| `labels` / `metadata` | Optional bounded labels and source-policy-approved metadata. |
| `force_insert` | Bypass semantic duplicate reuse; exact reuse still applies. |
| `supersedes_evidence_ids` | Optional owned Evidence replacement targets, at most 50. |
| `previous_source_revision` | Expected previous revision when advancing a source; cannot be combined with direct supersession. |

Authentication resolves the actual space and owner. There is no default destination or redirection after denial. Retired graph fields, including `relationships`, `entities`, `values`, and `predicates`, reject before provider work.

### Response

Illustrative successful terminal result:

```json
{
  "contract_version": "dense-mem.v2.8.0",
  "space_kind": "team_shared",
  "submission_id": "<returned_submission_id>",
  "submission_kind": "remember",
  "processing_state": "completed",
  "search_state": "current",
  "correlation_id": "<returned_correlation_id>",
  "evidence": [{
    "disposition": "stored",
    "evidence_index": 0,
    "evidence_id": "<returned_evidence_id>",
    "superseded_evidence_ids": [],
    "search_state": "current"
  }],
  "errors": []
}
```

Inspect every Evidence disposition, `errors`, optional `warnings`, and search state. Successful processing is terminal; there is no public polling step. See [Conflicts And Corrections](Conflicts-And-Corrections) for replacement procedures.

## ingest_session

### Request

```json
{
  "idempotency_key": "<new_session_key>",
  "framework": "generic",
  "app_name": "project-notes",
  "user_id": "external-user-1",
  "session_id": "session-1",
  "events": [{
    "event_id": "user-event-1",
    "text": "  Ari uses Go. café 🧭\n",
    "occurred_at": "2026-11-11T10:00:00Z"
  }]
}
```

`events` contains 1–20 exact user-authored events. IDs and external provenance strings are bounded at 256 characters; the retained operation key is bounded at 128. `occurred_at` is optional. The server owns chunking: do not trim or rewrite original event text.

The schema advertises at most eight windows, 8192 source tokens per window, 100 cited excerpts, and a 180-second processing bound. Required complete-request/context validation still applies. External identifiers never select identity or a private space.

### Response

Explicit no-memory example:

```json
{
  "contract_version": "dense-mem.v2.8.0",
  "submission_id": "<returned_submission_id>",
  "submission_kind": "session_ingest",
  "correlation_id": "<returned_correlation_id>",
  "accepted_event_count": 1,
  "duplicate_event_count": 0,
  "processing_state": "completed",
  "search_state": "not_required",
  "memory_outcome": "no_memory",
  "events": [{
    "event_id": "user-event-1",
    "disposition": "accepted",
    "processing_state": "completed",
    "evidence_ids": []
  }],
  "errors": []
}
```

A stored outcome returns Evidence IDs. Empty extraction returns `no_memory`; required failure returns a typed failed outcome. Migrated retired receipts can explicitly report `retired_contract`, `retired_payload`, and `original_contract_version`. See [Memory Write Pipeline](Write-Pipeline#private-session-intake).

## recall_memory

### Request

```json
{
  "query": "Which database stores this project's memory?",
  "limit": 5,
  "topic_limit": 3
}
```

| Field | Bound or behavior |
| --- | --- |
| `query` | At most 512 characters; required unless selecting topics. |
| `limit` | Optional 1–50 Evidence results. |
| `topic_limit` | 0–10 hints; default 3. Zero disables hints. |
| `topic_ids` | Optional 1–10 unique current topic UUIDs. Selects the union of direct memberships, without descendants. |
| `known_evidence_ids` | At most 200 previously seen Evidence IDs to exclude. |
| `valid_at` / `known_at` | Optional RFC 3339 time view. Topic metadata remains current. |

Follow a returned topic without repeating seen Evidence:

```json
{
  "topic_ids": ["<returned_topic_id>"],
  "known_evidence_ids": ["<returned_evidence_id>"],
  "limit": 5,
  "topic_limit": 0
}
```

### Response

```json
{
  "recall_id": "<returned_recall_id>",
  "results": [{
    "evidence_id": "<returned_evidence_id>",
    "context": "This project stores durable memory in PostgreSQL.",
    "space_kind": "team_shared",
    "equivalent_evidence_ids": [],
    "equivalents_truncated": false
  }],
  "related_topics": [{
    "topic_id": "<returned_topic_id>",
    "name": "Storage",
    "description": "Durable storage decisions",
    "eligible_evidence_count": 2,
    "rank": 1
  }],
  "topics_truncated": false,
  "conflicts": [],
  "search_states": {"evidence": "current"},
  "degradations": [],
  "suggested_actions": []
}
```

Array order conveys rank. Conflicts use `kind: evidence_conflict` and exact occurrence positions. Missing, foreign, or stale selected topics share `topic_unavailable`; optional hint failures are reported as degradations. Read [Recall Ranking](Recall-Ranking) before interpreting absence or degraded results.

## trace_memory

### Request

```json
{
  "evidence_id": "<returned_evidence_id>",
  "include_evidence_content": true,
  "include_transitions": true,
  "max_events": 100,
  "max_fragment_content_runes": 2000
}
```

| Field or result | Contract |
| --- | --- |
| `evidence_id` | Required authorized Evidence UUID. |
| `max_events` | 1–500; default 100, shared across bounded history. |
| `max_fragment_content_runes` | 1–8000; default 2000. |
| Result collections | `evidence`, `occurrences`, `source_revisions`, `decisions`, `lifecycle`, and `search_documents`. |
| Completeness | Explicit `truncated` and `omissions`; content can be omitted or truncated. |

Trace derives its space from the authorized Evidence target. It has no Relationship handle or graph-depth parameter.

## retract_evidence

### Request

```json
{
  "evidence_ids": ["<returned_evidence_id>"],
  "reason": "The source is no longer valid.",
  "idempotency_key": "<new_retraction_key>"
}
```

Supply 1–50 owned Evidence IDs, a nonempty reason of at most 1000 characters, and a retained key of at most 128 characters. The terminal result contains `decision_id`, `processing_state: completed`, and `retracted_evidence_ids`. Retraction preserves provenance; it is not physical erasure.

## submit_recall_session_feedback

### Request

```json
{
  "recalls": [{
    "recall_event_id": "<returned_recall_id>",
    "used": true,
    "answer_supported": true,
    "quality": "high",
    "missing_context": false,
    "irrelevant": false
  }]
}
```

Submit 1–20 judgments. Required fields are shown; `quality` is `high`, `medium`, or `low`. Optional comments are bounded at 1000 characters. `irrelevant_result_refs` accepts at most 100 `{type, id, rank}` references with type `evidence` or `topic`.

The result reports `recorded` and `recorded_count`. Partial failure additionally identifies `failed_index` and the next action. Earlier items were recorded: do not resend that prefix. See [Recall Feedback](Recall-Feedback).

## export_memory_pack

### Request

```json
{
  "name": "project-memory",
  "description": "Selected project Evidence and provenance",
  "evidence_ids": ["<returned_evidence_id>"],
  "include_evidence": true
}
```

Select 1–500 unique authorized Evidence UUIDs. Name and description are bounded at 256 and 1024 characters. The result contains `artifact_json`, `content_sha256`, `filename`, per-kind `counts`, and `{item_id, reason}` omissions. The artifact format is `dense-mem.memory-pack.v3.0`; see [Memory Packs](Memory-Packs).

## Errors And Client Actions

| Field | How to use it |
| --- | --- |
| `code` / `reason_code` | Stable classification and bounded specific reason. |
| `retryable` / `retry_after_seconds` | Whether and when a retry is permitted. |
| `next_action` / `remediation` | Retry unchanged, correct input, refresh state, obtain authorization, contact the operator, or stop. |
| `correlation_id` | Join authorized operator diagnostics without exposing request content publicly. |

MCP execution failures carry `isError: true` and bounded structured data/text. JSON-RPC validation errors and failed terminal receipts remain distinguishable. A timeout or missing response can leave an unknown delivery outcome: preserve the original request/key and follow [recovery guidance](Write-Pipeline#replay-and-recovery).

## HTTP Surface

| Surface | Purpose |
| --- | --- |
| `/mcp`, `/teams/{team_id}/mcp` | Streamable HTTP MCP, with API-key or configured OAuth bearer authentication. |
| `/.well-known/oauth-protected-resource/mcp` | Configured unscoped protected-resource discovery. |
| `/.well-known/oauth-protected-resource/teams/{team_id}/mcp` | Configured team resource discovery. |
| `/ui/api/*` | First-party browser interfaces. |
| `/control/api/*` | Separate private administration interfaces. |

There is no generic public REST memory API. Portal route details belong to their feature pages; authentication, OAuth challenges, and CSRF belong to [Request Lifecycle And Security](Request-Lifecycle-And-Security).
