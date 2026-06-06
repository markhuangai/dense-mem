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

Telemetry does not expose memory content. It is operational data about requests,
latency, usage, recall, promotion, embedding, verifier, and related runtime
activity.

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
route, dependency, and system views.

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
