# Conflict Queue

Dense-Mem exposes a read-only conflict queue for operators through the private
control portal. The queue is a bounded projection of PostgreSQL conflict cases;
it does not create, resolve, retract, or otherwise mutate Relationships.

## Control API

```text
GET /control/api/teams/{teamId}/conflicts/queue
```

The route uses the control-plane authentication boundary and the requested team
is taken from the authenticated control context. A successful response uses the
standard `{ "data": ... }` envelope:

```json
{
  "data": {
    "summary": {
      "open_count": 2,
      "overdue_count": 1,
      "active_lease_count": 1,
      "expired_lease_count": 0,
      "failed_assessment_count_24h": 0,
      "lww_resolution_count_24h": 0,
      "pending_derived_task_count": 0,
      "failed_derived_task_count": 0,
      "oldest_open_age_seconds": 3600,
      "oldest_overdue_age_seconds": 7200,
      "collected_at": "2026-08-09T00:00:00Z"
    },
    "items": [],
    "next_cursor": null
  }
}
```

Supported query parameters are:

| Parameter | Values | Behavior |
| --- | --- | --- |
| `status` | empty, `open`, `overdue` | Filters active cases by status. |
| `limit` | `1`–`100` | Defaults to `25`. |
| `cursor` | opaque string | Continues the previous page. |

The cursor is versioned, bounded, and validated from its contents rather than by
a client-selected offset. It includes the team, status filter, status, next review
time, and conflict ID. Replaying it for another team or status filter fails
closed. Results use deterministic keyset ordering: overdue/open status, next
review time, then conflict ID.

## Queue item projection

Each item includes the conflict ID and version, status, question and predicate,
review and lease timestamps, assessment attempt count, the last bounded failure
class, and the active positions. Long text is bounded and reports
`question_truncated` or `predicate_key_truncated` when clipped. A conflict with
more than the display limit reports `positions_truncated`; each position reports
`supporter_count`, bounded supporter summaries, and `supporters_truncated`.

Supporter summaries contain only the profile ID/name, strongest authority, and
accepted timestamp. Profile names are hydrated within the authenticated team
scope. Source-group keys, evidence content, provider responses, and cross-team
identities are not exposed by the queue.

The queue projection uses the same supporter semantics as conflict resolution:
each profile contributes at most one vote, based on its uniquely newest accepted
position; equal newest timestamps abstain. Source groups remain provenance and
conflict-member identity, not additional votes.

## Metrics and portal workspace

The collector publishes team-scoped gauges for active case counts, oldest ages,
leases, incomplete derived tasks, and collection success. A failed collection
sets the success gauge to `0` and suppresses stale queue gauges until a later
successful scrape. The control telemetry card reports collector degradation as
an operator warning.

The first-party Conflicts workspace provides read-only status filtering,
keyset pagination, refresh, summary cards, lease/assessment information, and
explicit loading, empty, error, and truncation states. Queue data renders as
soon as the queue request completes; optional telemetry failure does not hide a
successful queue response.

## Verification

The repository covers the projection and cursor boundary with real PostgreSQL
tests, including status filtering, cross-team cursor rejection, truncation,
RLS/read-only transaction behavior, and supporter loading. Compose-backed E2E
coverage verifies foreign-team isolation, overdue assessment/LWW behavior,
metrics and operator cards, and Chromium/mobile portal rendering.
