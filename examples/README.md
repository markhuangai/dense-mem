# Deployment examples

Run commands below from the repository root with a private `.env` file. The
[root quickstart](../README.md#quickstart) covers a fresh local installation;
the [setup guide](https://markhuangai.github.io/dense-mem/Quick-Start/) explains
setup, client connection, and a first memory workflow.

## Choose an example

| File | Use |
| --- | --- |
| [docker-compose.base.yml](docker-compose.base.yml) | Local PostgreSQL + pgvector and one server, with loopback listeners |
| [docker-compose.expert.yml](docker-compose.expert.yml) | Operator-managed deployment and optional Redis coordination |
| [docker-compose.telemetry.yml](docker-compose.telemetry.yml) | Optional Prometheus collection on the base stack |
| [docker-compose.grafana.yml](docker-compose.grafana.yml) | Provisioned Grafana dashboards; follow the [Grafana guide](grafana/README.md) |
| [docker-compose.demo.yml](docker-compose.demo.yml) | Disposable hosted-demo deployment |

Use matching application images and example files from the same release. Keep
the exported `DENSE_MEM_IMAGE` pin for subsequent starts. Required base-stack
settings are `POSTGRES_PASSWORD`, `CONTROL_PORTAL_TOKEN`, `AI_API_KEY`, and
`AI_VERIFIER_MODEL`; [the template](.env.example) contains the remaining options.

## Provider and local-model setup

The base example supplies OpenAI embedding defaults. Change the embedding
endpoint, model, and dimensions together when selecting another provider.
Chat models use `AI_VERIFIER_MODEL`, with optional Remember/conflict overrides.
The [Configuration](https://markhuangai.github.io/dense-mem/Configuration/)
page owns setting meanings and bounds, including optional typed decisions.

For Ollama on the Docker host, use the OpenAI-compatible endpoint
`http://host.docker.internal:11434/v1`, supply the provider-required API-key
value, select installed embedding/chat models, and set the embedding dimensions.
The base Compose file includes the Linux host-gateway mapping. See
[Public HTTPS And Redis](https://markhuangai.github.io/dense-mem/Public-HTTPS-And-Redis/)
before adding public ingress or multiple instances.

## Telemetry and Grafana

Configure application settings, then generate private telemetry credentials:

```bash
export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 32)"
docker compose --env-file .env \
  -f examples/docker-compose.base.yml \
  -f examples/docker-compose.telemetry.yml \
  -f examples/docker-compose.grafana.yml \
  up -d
```

Prometheus and Grafana bind to loopback at ports 9090 and 3000. The
[Grafana guide](grafana/README.md) owns dashboard provisioning, exporters,
metric interpretation, and credential-handling instructions. The
[Telemetry](https://markhuangai.github.io/dense-mem/Telemetry/) page explains
observation and troubleshooting workflows.

## Startup and operations

```bash
docker compose --env-file .env -f examples/docker-compose.base.yml ps
docker compose --env-file .env -f examples/docker-compose.base.yml logs --no-color server
curl -fsS http://127.0.0.1:8080/ready
```

The server applies pending migrations before serving. Existing databases need
the backup and compatibility checks in the
[Operations Runbook](https://markhuangai.github.io/dense-mem/Operations-Runbook/).
The control portal stays private. Use the
[portals guide](https://markhuangai.github.io/dense-mem/Portals-And-API-Keys/)
for team/credential administration and the
[logging reference](https://markhuangai.github.io/dense-mem/Telemetry/#operation-logs-and-protected-captures)
for diagnostics.

When changing an example, update this guide and its canonical documentation
in the same feature delivery. Keep generated data and credentials out of Git.
