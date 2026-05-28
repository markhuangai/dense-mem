# Knowledge Pipeline — Client Contracts

This document is the client-facing reference for integrating with the knowledge
pipeline. It covers endpoints, error codes, state transitions, and UI/frontend
recommendations.

## Recall Endpoint

### `GET /api/v1/recall`

Returns a ranked list of knowledge hits spanning all pipeline tiers.

**Query parameters**

| Parameter | Type | Required | Constraints | Description |
|-----------|------|----------|-------------|-------------|
| `query` | string | yes | max 512 chars | Natural-language query |
| `limit` | int | no | 0–50; default 10 | Maximum hits to return |
| `valid_at` | RFC3339 timestamp | no | optional | Filters fact/claim tiers to the valid-time view |
| `known_at` | RFC3339 timestamp | no | optional | Filters fact/claim tiers to the known-time view |
| `include_evidence` | bool | no | optional | Includes fact/claim evidence payloads when `true` |

**Success response — 200**

```json
{
  "data": [
    {
      "tier": "1",
      "score": 0.92,
      "fact": {
        "fact_id": "...",
        "predicate": "...",
        "object_value": "...",
        "truth_score": 0.92
      },
      "semantic_rank": 0,
      "keyword_rank": 0,
      "final_score": 0
    },
    {
      "tier": "1.5",
      "score": 0.40,
      "claim": {
        "claim_id": "...",
        "content": "...",
        "extract_conf": 0.80,
        "status": "validated"
      },
      "semantic_rank": 0,
      "keyword_rank": 0,
      "final_score": 0
    },
    {
      "tier": "2",
      "score": 0.016,
      "fragment": {
        "fragment_id": "...",
        "content": "..."
      },
      "semantic_rank": 1,
      "keyword_rank": 2,
      "final_score": 0.016
    }
  ]
}
```

**Error responses**

| Code | Body | Trigger |
|------|------|---------|
| 400 | `{"error": "profile ID is required"}` | Missing authenticated profile context |
| 400 | `{"error": "query is required"}` | Missing or invalid `query` |
| 503 | `{"error": "embedding provider unavailable"}` | AI embedding service down |
| 503 | `{"error": "keyword search unavailable"}` | BM25 index unavailable |

## State Machine — Claim Lifecycle

```
[created] → candidate
                │
                ▼ POST /claims/{id}/verify
           validated ──── disputed
                │
                ▼ POST /claims/{id}/promote
              (Fact)
```

- Only `validated` claims can be promoted.
- `disputed` claims cannot be promoted and are excluded from tier-1.5 recall.
- Promoting a `validated` claim creates a new `Fact` node with `status=active`.

## State Machine — Fragment Lifecycle

```
[created] → active
               │
               ▼ POST /fragments/{id}/retract
            retracted (soft tombstone)
               │
               ▼ (triggers) fact revalidation
```

- Retracted fragments remain in the graph to preserve `SUPPORTED_BY` lineage.
- Facts that relied solely on a retracted fragment are flagged `needs_revalidation`.

## Frontend / UI Recommendations

1. **Tier badges**: Display tier "1" hits with a "Fact" badge, "1.5" with "Claim",
   and "2" with "Fragment" to help users understand authority levels.

2. **Embedding unavailable state**: When the recall endpoint returns 503, show an
   inline message ("Knowledge search temporarily unavailable") rather than an error
   page. The keyword and fragment paths may still be available.

3. **Pagination**: The `limit` parameter caps total hits. For paginated UIs, use
   cursor-based listing on `/api/v1/facts` and `/api/v1/claims` instead of recall.

4. **Score display**: Tier `1` and `1.5` hits are query-matched first and then ordered by
   their authority-oriented tier scores. Tier `2` scores are RRF values. Present all scores
   as relative ranking hints, not calibrated probabilities.

5. **Cross-team isolation**: Header-scoped routes derive team scope from the
   authenticated API key. Clients should not send `X-Team-ID`; attempts to access
   another team through path-scoped routes return 403.
