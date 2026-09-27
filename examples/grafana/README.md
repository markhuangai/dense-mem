# Grafana dashboards

These dashboards are for private Dense-Mem operators. The overview shows
system health. Canonical lifecycle detail is grouped in the Remember and Dream
view; team and credential usage has its own private view.

## Start Grafana

From the repository root, configure the existing application and telemetry
environment values. Generate a high-entropy Grafana admin password, then start
the three compose files together:

```bash
export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 32)"
docker compose \
  -f examples/docker-compose.base.yml \
  -f examples/docker-compose.telemetry.yml \
  -f examples/docker-compose.grafana.yml \
  up -d
```

Open Grafana at `http://127.0.0.1:3000` and sign in with the configured
`GRAFANA_ADMIN_USER` (default `admin`) and `GRAFANA_ADMIN_PASSWORD`. Anonymous
access and sign-up are disabled. Prometheus and Grafana ports bind to loopback.
The Compose secret supplies the admin password to Grafana.

The provisioning files create a Prometheus datasource and load all checked-in
dashboards. Grafana creates the datasource UID locally; dashboards select a
Prometheus datasource variable and contain no deployment credentials or fixed
datasource UIDs. To import manually, select a Prometheus datasource when Grafana
asks, then choose the `job` variable matching the scrape configuration.

## Dashboards and parity

| Dashboard | Measures |
| --- | --- |
| Dense-Mem Overview | Scrape and collector health, HTTP and MCP failures, Remember failures, tail latency, read-stage backlog and pricing completeness. |
| Dense-Mem AI and Recall | Provider, assessor, recall and feedback signals, plus AI usage by feature, model, token source and pricing status. |
| Dense-Mem Remember and Dream | Remember calls, attempts, phase outcomes and durations, MCP outcomes, Dream runs and canonical lifecycle results. |
| Dense-Mem Team and Credential Usage | Requests, errors, MCP outcomes, provider attempts, tokens and estimated cost by team, credential ID and protected credential name. |
| Dense-Mem Performance and Dependencies | Read-stage p50/p95/p99, SQL and item volume, Remember phases, provider latency, HTTP transport detail, process health and optional dependency exporters. |

The `Rolling totals` variable uses the same seven windows supported by the
existing telemetry API. The dashboard time picker controls range charts. The
time picker and rolling-total window have different meanings: counts and ledger
gauges use the selected rolling window; event charts use their plotted range.

Rows group related operator questions on every dashboard. Panel descriptions
name each first-party card or series for parity checks.
Dashboard definitions live in `generate_dashboards.mjs`; regenerate the checked
in Grafana JSON after editing those definitions:

```bash
node examples/grafana/generate_dashboards.mjs
```

Existing counter families retain their current names and labels. Windowed
counts and histogram measures use the telemetry service's sparse first-sample
fallback; range panels use Grafana's scrape-safe rate interval. Lifecycle
and Dream measures use windowed ledger gauges. Gauge queries group away the
replica dimension with `max`; applying counter `rate` or `increase` to these
gauges would count the same canonical rows multiple times. Provider usage that
was not reported, missing pricing, absent host feedback, scrape failure, and
ledger collection failure remain distinct from a real zero. The ledger health
panel must be healthy before ledger values are treated as current.

The new `densemem_usage_*` families retain authenticated credential IDs.
`densemem_usage_credential_last_observed_timestamp_seconds` provides a
protected display name for the same team/profile/credential tuple. If the name
is absent or cannot be protected, the usage table displays `unavailable` and
the ID remains visible. Requests without a credential and team background work
have explicit attribution labels. These series begin at deployment and cannot
retroactively divide old owner-level totals among several credentials.
The retained private `/control/api/metrics` reader now returns credential
rows from new 30-day credential buckets; its `credential_id` query filter also
scopes system, team and route totals to that credential. Historical owner
buckets still contribute to unfiltered system/team/route totals but do not
appear as falsely named keys. Credential bucket writes share the existing
flush transaction and retry ID with owner totals.
Prometheus `increase` values are scrape estimates, not billing ledger totals.
Unpriced operations stay visible separately from estimated cost.

Telemetry pricing retains the three existing default-model rates. The
`Additional model prices (JSON)` control setting accepts up to 32 exact
component/model rates, for example:

```json
[
  {"component":"verifier","model":"alternate-chat-model","input_usd_per_million_tokens":1.25,"output_usd_per_million_tokens":5},
  {"component":"embedding","model":"alternate-embedding-model","input_usd_per_million_tokens":0.13}
]
```

The model named by an observation must match its configured default model or
an additional entry. A different or incomplete model rate remains unpriced;
zero is a valid explicit rate. An exact additional entry takes precedence over
the default rate for the same model. Set prices to your provider's current
contract.

Set the `Control portal URL` textbox variable to the private control portal
base URL, including scheme and port, for example `http://127.0.0.1:8090`.
On the Usage or Performance dashboard, open **Team diagnostic links** and
select a concrete team row. Its two data links open protected Remember history
and operation logs for that row's `team_id`. The table includes only UUID team
IDs observed in HTTP usage metrics; an inactive or unobserved team has no row.
The default `All` filter does not create a portal link, and aggregate panels
have no team destination. The portal accepts scoped `team_id`, `remember_view`,
`attempt_id`, and `invocation_id` links; detail is loaded only when selected.
Prometheus series do not contain individual request IDs, so the row link opens
the team's diagnostic list rather than a specific invocation. The portal still
enforces the signed-in operator's team access.

Overview severity defaults are green/yellow/red at 0/1/5 percent HTTP errors
and at 0/500/1500 ms p95 HTTP or read-stage latency. Scrape and collector
status displays **HEALTHY** or **FAILED**. Remember and MCP failures and
unpriced AI operations warn at 1 and turn red at 10 in the rolling window.
These are operational starting points, not service objectives. Thresholds are
editable in Grafana; update `generate_dashboards.mjs` for a durable provisioned
default. A colored zero on a failure card requires observed calls in the
window. The overview p95 cards need at least 20 observations; adjacent count
cards show volume. Missing data, insufficient samples, an absent exporter,
missing provider usage, and absent feedback display **No data**, never a green
zero. The dependency health cards show **FAILED** when a configured exporter
target or its dependency fails, and **No data** when no matching target exists.
Every dashboard links to the other four.

Go runtime and process collectors are included on the private Dense-Mem scrape.
PostgreSQL, Redis, and host panels require exporters on the same private
Prometheus network. Configure them as separate `postgres`, `redis`, and `node`
scrape jobs, or change the dashboard job variables to their actual names.
Give the PostgreSQL exporter a read-only monitoring role and supply its
password through a secret file; keep exporter ports off public interfaces.
Redis and node exporters are optional for deployments that do not run Redis or
control the host. These panels remain no-data until their exporter is present.

For the example Compose stack, create a dedicated PostgreSQL user with
`pg_monitor` and database connect rights, then set
`POSTGRES_EXPORTER_USER` and `POSTGRES_EXPORTER_PASSWORD` in the operator
environment. Add `examples/docker-compose.telemetry-exporters.yml` after the
telemetry overlay. It mounts `examples/prometheus.exporters.yml` and starts
private PostgreSQL and host exporters without published ports. The node
exporter reads the host root filesystem through a read-only mount and joins the
host PID namespace; use it only on a host you intend to monitor. For the expert
stack with Redis and `REDIS_PASSWORD`, add
`examples/docker-compose.telemetry-redis-exporter.yml` after the exporter
overlay. Its password also enters through a Compose secret, and it switches
Prometheus to `examples/prometheus.exporters.redis.yml` so Redis is scraped
only when the service exists. The pinned exporter tags
and documented password-file options were checked against their published
images and upstream documentation.

The release migration rehearsal refreshes the shared staging database from a
production copy. Its Actions summary now records the UTC start, restore and
rehearsal times and warns that staging-only Remember history and test records
are replaced. Keep staging-only diagnostics outside that database if they must
survive a rehearsal.

The issue #433 migration compared every mapped panel with the old system
telemetry snapshot against the same Prometheus data in Grafana 13.2.2. The
control Metrics and user Usage tabs were retired only after that parity check
passed. The control telemetry reader remains for conflict-queue health and
operator diagnostics; team-overview request summaries and private diagnostics
remain in the browser portals.
