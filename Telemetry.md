# Telemetry

Telemetry is optional. Use it when you want usage and performance views in the
user portal and control portal, or when you want Prometheus metrics for
operations.

For exact environment variable defaults, see [Configuration](Configuration#telemetry).

## What It Adds

| Surface | What you get |
| --- | --- |
| User portal | Self-scoped telemetry for the authenticated API key. |
| Control portal | Team, profile, route, dependency, and system telemetry views. |
| Prometheus | Metrics scraped from a token-protected `/metrics` endpoint. |
| Operation logs | Database-backed application log inspection in the control portal. |

Telemetry does not expose memory content. It is operational data about requests,
latency, usage, recall, promotion, embedding, verifier, and related runtime
activity.

Operation logs are separate from Prometheus telemetry. They store structured
server log records in Postgres for control-portal troubleshooting and are
retained according to `OPERATION_LOG_RETENTION_DAYS`.

## Enable Telemetry Locally

Start with the base compose setup from [Quick Start](Quick-Start), then add the
telemetry overlay:

```bash
curl -fsSLo prometheus.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.yml
curl -fsSLo docker-compose.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d
```

Prometheus is published only on loopback:

```text
http://127.0.0.1:9090
```

The overlay also sets:

```text
TELEMETRY_ENABLED=true
TELEMETRY_PROMETHEUS_URL=http://prometheus:9090
TELEMETRY_PROMETHEUS_JOB=dense-mem
```

## Use Telemetry In The Portals

Open the user portal:

```text
http://127.0.0.1:8080/ui
```

Use your Dense-Mem API key there. The user portal only shows telemetry for the
authenticated profile.

Open the control portal:

```text
http://127.0.0.1:8090/
```

Use `CONTROL_PORTAL_TOKEN`. The control portal can show broader team, profile,
route, dependency, system, operation-log, and dream-cycle views.

## Operation Logs

The control portal Logs tab reads database-backed operation logs. Logs can be
filtered by severity and sorted by timestamp or severity.

Retention is controlled by:

```text
OPERATION_LOG_RETENTION_DAYS=30
```

This setting is managed from the control portal Config tab. The allowed range is
1 to 365 days.

## Online Recall Feedback

Dense-Mem treats recall quality as online feedback from the host LLM. Production
recall quality is measured from bounded host judgments submitted after the
answer, not from a separate labeled benchmark workflow.

The intended online flow is:

```text
host LLM calls recall_memory
  -> Dense-Mem returns ranked results and, when enabled, a recall_event
  -> host LLM answers the user
  -> host LLM submits one session-level recall evaluation
  -> Dense-Mem records bounded online recall-feedback metrics
  -> Dense-Mem stores recall refs and failure details for investigation
  -> Prometheus scrapes the metrics
  -> portals display online recall quality
```

Recall feedback is observational. Submitting it records metrics and
investigation data; it does not retrain or change ranking automatically. See
[Recall Ranking](Recall-Ranking#recall-feedback-is-record-only).

The feedback request is off by default. Enable recall feedback from the control
portal Config tab (`RECALL_FEEDBACK_ENABLED`) to expose
`submit_recall_session_feedback` and add `recall_event` to `recall_memory`
responses. Leave it disabled when you want to avoid the extra tool-call and
token overhead.

Useful online feedback cards include:

| Card | What it means |
| --- | --- |
| LLM recall used rate | Share of feedback events where the host LLM used at least one recalled result. |
| LLM answer supported rate | Share of feedback events where recalled context supported the final answer. |
| LLM recall quality | Average LLM quality rating for returned recall context. |
| LLM missing context rate | Share of feedback events where the LLM reported that important context was missing. |
| LLM irrelevant result rate | Share of feedback events where returned context was irrelevant to the user request. |

The cards read:

```text
densemem_recall_feedback_total
densemem_recall_feedback_quality_score
```

The feedback tool accepts one or more recall judgments:

```json
{
  "recalls": [
    {
      "recall_id": "rec_...",
      "used": true,
      "answer_supported": false,
      "quality": "low",
      "missing_context": true,
      "irrelevant": true,
      "feedback_comment": "Returned stale UI notes instead of the needed button handling pattern.",
      "irrelevant_result_refs": [
        { "type": "fragment", "id": "019...", "rank": 1 }
      ],
      "dream_feedback": [
        {
          "dream_id": "dream-123",
          "used": true,
          "quality": "medium",
          "contradicted": false,
          "feedback_comment": "Plausible but weak support."
        }
      ]
    }
  ]
}
```

`feedback_comment` is required unless `quality` is `high`, `answer_supported`
is `true`, `missing_context` is `false`, and `irrelevant` is `false`. Keep it
bounded and free of user content. `failure_reason` and `expected_context` are
deprecated aliases for `feedback_comment`.

`dream_feedback` records host-LLM judgments about `related_dreams` returned by
`recall_memory`. It is quality feedback only; it does not reinforce, reject,
stale, promote, or remove the dream. Use `resolve_dream_feedback` when prior
conversation context or user feedback confirms a dream as true or false and the
confirmation should enter normal memory placement.

Do not use `recall_id`, `feedback_comment`, or dream IDs as Prometheus labels.
Prometheus labels stay bounded; the free-text details, judged irrelevant refs,
and dream-feedback judgments are stored in `recall_feedback_events` and surfaced
in the control portal Feedback tab.

For offline measurement, use stored `result_refs` plus submitted failure details
to build labeled recall cases. The codebase includes a pure scoring helper for
Recall@K, MRR, nDCG@K, and judged-bad-at-K so ranking changes can be compared
against captured feedback before they change production recall behavior.

## Metrics Endpoint

When telemetry is enabled, Dense-Mem exposes Prometheus metrics on the control
server at:

```text
http://127.0.0.1:8090/metrics
```

The endpoint requires `TELEMETRY_SCRAPE_TOKEN`. Keep it private. The compose
overlay gives the token to Prometheus as a Compose secret and scrapes the
endpoint from the private network.

Manual check:

```bash
curl -H "Authorization: Bearer $TELEMETRY_SCRAPE_TOKEN" \
  http://127.0.0.1:8090/metrics
```

The endpoint also accepts `X-Telemetry-Scrape-Token`.

## Hosted Demo Telemetry

The disposable demo image uses a separate metrics listener and keeps the control
portal disabled. Use the demo telemetry overlay only with
`docker-compose.demo.yml`:

```bash
curl -fsSLo prometheus.demo.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.demo.yml
curl -fsSLo docker-compose.demo.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.demo.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.demo.yml -f docker-compose.demo.telemetry.yml up -d
```

The demo overlay scrapes `demo:8091` on the private Compose network and uses
`TELEMETRY_PROMETHEUS_JOB=dense-mem-demo`.

## Troubleshooting

| Problem | Check |
| --- | --- |
| Portal says telemetry is unavailable | `TELEMETRY_ENABLED=true` and `TELEMETRY_PROMETHEUS_URL=http://prometheus:9090`. |
| Prometheus has no targets | `prometheus.yml` is in the same directory as `docker-compose.yml`. |
| `/metrics` returns unauthorized | `TELEMETRY_SCRAPE_TOKEN` matches the value Prometheus received. |
| Dashboard is empty | Generate some authenticated traffic, then refresh after one scrape interval. |
| Shared Prometheus shows wrong data | Set `TELEMETRY_PROMETHEUS_JOB` to the intended scrape job. |
| Logs tab is empty | Generate server activity, wait a few seconds for the batch flush, then refresh. |
