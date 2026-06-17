# Community Memory

This page covers community summaries, community-aware recall, scheduled
community detection, and operation logs.

Dreaming now has its own page: [Dreaming](Dreaming).

## Detection And Summaries

Community detection groups related graph nodes inside one authenticated profile.
It uses Neo4j Graph Data Science Leiden detection, then persists bounded
community summaries in the same profile scope.

Community summaries are retrieval aids. They are not facts, claims, or evidence.
Run detection again when the graph changes enough that the old community
assignments are stale.

Use MCP tools:

```json
{"tool": "detect_community", "arguments": {}}
```

```json
{"tool": "list_communities", "arguments": {"limit": 20}}
```

```json
{"tool": "get_community_summary", "arguments": {"community_id": "community-1"}}
```

Use REST to read persisted summaries:

```bash
curl "http://127.0.0.1:8080/api/v1/communities?limit=20" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY"
```

Community detection requires write scope because it updates graph community
assignments and stored summaries. Listing and reading communities require read
scope.

### Community-Aware Recall

Normal recall does not expand through communities unless the caller asks for it.
Set `use_communities=true` when a query may benefit from related graph clusters:

```json
{
  "query": "deployment and release process",
  "limit": 10,
  "use_communities": true
}
```

For REST:

```bash
curl "http://127.0.0.1:8080/api/v1/recall?query=deployment&use_communities=true" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY"
```

Community expansion only fills unused recall slots. Direct fact, claim, and
fragment matches still rank first.

### Scheduled Community Detection

Scheduled detection is controlled from the control portal Config tab. It is
disabled by default.

| Setting | Default | Meaning |
| --- | --- | --- |
| `COMMUNITY_DETECTION_ENABLED` | `false` | Enables scheduled detection across profiles. |
| `COMMUNITY_DETECTION_START_TIME_LOCAL` | `03:30` | Local HH:MM start time. |
| `COMMUNITY_DETECTION_MAX_CONCURRENCY` | `1` | Concurrent profile runs, from 1 to 8. |
| `COMMUNITY_DETECTION_JITTER_SECONDS` | `600` | Stable per-profile run jitter, from 0 to 3600 seconds. |
| `APP_TIMEZONE` | `Local` | Shared scheduler timezone. Use an IANA name such as `America/New_York` for predictable production behavior. |
| `AI_COMMUNITY_MAX_NODES` | `500000` | Environment cap for a projected community graph. Larger graphs are skipped. |

The scheduler checks once per minute, reserves one run per profile and local
run date, and stores reservations in Postgres so multiple server instances do
not duplicate the same community run.

## Dreaming

Dreaming creates reviewable hypotheses from existing memory. It is separate from
community detection and from active facts. See [Dreaming](Dreaming) for the full
workflow, tools, schedule, and review behavior.

## Operation Logs

Operation logs are database-backed application logs for the control portal.
They are separate from Prometheus telemetry.

The control portal Logs tab can filter by severity and sort by timestamp or
severity. The Config tab controls retention:

| Setting | Default | Meaning |
| --- | --- | --- |
| `OPERATION_LOG_RETENTION_DAYS` | `30` | Number of days to keep operation logs, from 1 to 365. |

The server flushes operation logs in batches and prunes old rows hourly.

## Troubleshooting

| Problem | Check |
| --- | --- |
| No scheduled community runs | `COMMUNITY_DETECTION_ENABLED=true`, `APP_TIMEZONE`, Neo4j GDS plugin availability, and `AI_COMMUNITY_MAX_NODES`. |
| Community-aware recall has no extra results | Run `detect_community` first and pass `use_communities=true`; expansion only fills unused recall slots. |
| Logs tab is empty | Generate server activity, then refresh; logs are flushed every few seconds. |
