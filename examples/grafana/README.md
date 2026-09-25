# Grafana dashboards

These dashboards are for private Dense-Mem operators. They cover system-wide
operational measures. The canonical lifecycle collector intentionally has no
team or credential labels, so dashboards do not offer those scopes.

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
| Dense-Mem Overview and Lifecycle | HTTP requests, failures and latency; conflict-queue scrape status; current Relationships, lifecycle transitions and corrections. |
| Dense-Mem AI and Recall | Embedding, verifier, assessor, recall and host-feedback measures; Dream feedback; provider usage and estimated cost. |
| Dense-Mem Remember and Dream | Remember calls and phase durations; MCP and logical outcomes; Dream runs, targets, proposals, Hypotheses, feedback and canonical results. |

The `Rolling totals` variable uses the same seven windows supported by the
existing telemetry API. The dashboard time picker controls range charts. The
time picker and rolling-total window have different meanings: counts and ledger
gauges use the selected rolling window; event charts use their plotted range.

Panel descriptions name each first-party card or series for parity checks.
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

Existing scoped telemetry metrics retain team and profile labels for the
first-party API. Dashboard queries neither select nor group by those labels,
and new operational metrics contain no identity labels. Metric labels contain
no evidence or request text.

The issue #433 migration compared every mapped panel with the old system
telemetry snapshot against the same Prometheus data in Grafana 13.2.2. The
control Metrics and user Usage tabs were retired only after that parity check
passed. The control telemetry reader remains for conflict-queue health and
operator diagnostics; team-overview request summaries and private diagnostics
remain in the browser portals.
