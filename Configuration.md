# Configuration

This page lists the settings most operators need after the quick start.

For a first local setup, use [Quick Start](Quick-Start) and change only the
required values. The quick start uses local-only access and does not enable
Traefik. Come back here when you need Redis, different AI providers, stricter
limits, or public access.

## Required Settings

| Variable | What it does | First setup value |
| --- | --- | --- |
| `POSTGRES_PASSWORD` | Password for the Postgres container. | Choose a strong secret. |
| `NEO4J_PASSWORD` | Password for Neo4j. | Choose a strong secret. |
| `CONTROL_PORTAL_TOKEN` | Token required to use the local control portal. | Choose a long secret. |
| `AI_API_URL` | OpenAI-compatible embedding provider base URL. | `https://api.openai.com/v1` for OpenAI. |
| `AI_API_KEY` | API key used by the embedding provider. | Your provider key. |
| `AI_API_EMBEDDING_MODEL` | Embedding model name. | `text-embedding-3-small` by default, or the provider model you choose. |
| `AI_API_EMBEDDING_DIMENSIONS` | Embedding vector size. | `1536` for `text-embedding-3-small`; `3072` for `text-embedding-3-large`. |

The server requires a complete embedding configuration at startup:
`AI_API_URL`, `AI_API_KEY`, `AI_API_EMBEDDING_MODEL`, and
`AI_API_EMBEDDING_DIMENSIONS`. Dense-Mem uses an OpenAI-compatible embedding API.
The compose examples supply OpenAI defaults, but production setups should set all
four values explicitly so the model and stored vector dimensions are obvious.

Dense-Mem validates embedding configuration on startup. Do not change embedding
model or dimensions casually after storing memory. Mixed vector dimensions can
break search.

## Local Ports

| Variable | Default | Meaning |
| --- | --- | --- |
| `CONTROL_HTTP_ADDR` | `:8090` | Control portal address inside the container. |
| `CONTROL_PORTAL_BIND` | `127.0.0.1` | Host interface for the control portal. |
| `CONTROL_PORTAL_PORT` | `8090` | Host port for the control portal. |
| `NEO4J_HTTP_PORT` | `7474` | Local Neo4j browser port. |
| `NEO4J_BOLT_PORT` | `7687` | Local Neo4j driver port. |

The main Dense-Mem server always listens on container port `8080`. Change host
exposure with the compose port variables, such as `DENSE_MEM_PORT` for the base
example or `DENSE_MEM_DEMO_PORT` for the demo example.

The base example publishes these local URLs:

| Service | URL |
| --- | --- |
| MCP/API | `http://127.0.0.1:8080/mcp` |
| User portal | `http://127.0.0.1:8080/ui` |
| Control portal | `http://127.0.0.1:8090/` |

Keep the control portal on `127.0.0.1` for local use. It manages API keys.

Manager/member profile access is not controlled by a feature flag. It is active
after the RBAC migration and is stored in the `team_profiles.role` column.

## SSO Settings

SSO is optional. Leave `SSO_PUBLIC_BASE_URL` empty when the deployment should
not show SSO login in `/ui`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `SSO_PUBLIC_BASE_URL` | empty | Public base URL used to build `/ui/api/sso/callback`. Must be absolute `http` or `https` when SSO is enabled. |
| `SSO_ENTITLEMENT_CACHE_TTL_SECONDS` | `300` | Cache TTL for provider-refreshed SSO entitlements. |
| `SSO_SESSION_TTL_SECONDS` | `28800` | Browser SSO session lifetime. Claim-derived entitlements are cached for this duration. |
| `SSO_STATE_TTL_SECONDS` | `600` | OAuth state and PKCE verifier lifetime during login. |
| `SSO_HTTP_TIMEOUT_SECONDS` | `10` | Timeout for OIDC discovery, token exchange, UserInfo, and group endpoint calls. |
| `SSO_COOKIE_SECURE` | derived | Secure-cookie setting. Defaults to `true` for `https` public base URLs and `false` otherwise. |

These settings can be managed from the control portal SSO configuration screen.
See [Portals And API Keys](Portals-And-API-Keys#user-portal-sso) for generic
OIDC provider setup, group mappings, and the example ZITADEL configuration.

## Database Settings

Postgres stores teams, profiles, API key metadata, migrations, and audit logs.

| Variable | Default | Meaning |
| --- | --- | --- |
| `POSTGRES_USER` | `densemem` | Postgres username. |
| `POSTGRES_PASSWORD` | empty | Required password. |
| `POSTGRES_DB` | `densemem` | Database name. |
| `POSTGRES_HOST` | `postgres` | Compose service name. |
| `POSTGRES_PORT` | `5432` | Postgres port. |
| `POSTGRES_SSLMODE` | `disable` | SSL mode for local compose. |
| `POSTGRES_DSN` | empty | Optional full connection string for non-compose deployments. |

Dense-Mem's first Postgres migration uses `gen_random_uuid()` and creates the
`pgcrypto` extension when it is missing. Fresh installs that use the bundled
`postgres:18-alpine` compose service normally work without a manual step because
the configured `POSTGRES_USER` owns the database. External, managed, or reused
Postgres databases may use a restricted runtime role; in that case, install the
extension once with a database owner or admin role before starting Dense-Mem:

```bash
psql "$ADMIN_POSTGRES_DSN" -c 'CREATE EXTENSION IF NOT EXISTS pgcrypto;'
```

For bundled compose, this preflight uses the Postgres service's own configured
user and database:

```bash
docker compose exec postgres sh -lc \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "CREATE EXTENSION IF NOT EXISTS pgcrypto;"'
```

If that command also fails with `permission denied`, the database or volume was
not initialized with the configured runtime user as a database owner. Use an
admin role to install the extension, or recreate the Postgres volume only when
the existing data is disposable.

Neo4j stores memory graph data, claims, facts, relationships, and vector-backed
search data.

| Variable | Default | Meaning |
| --- | --- | --- |
| `NEO4J_URI` | `bolt://neo4j:7687` | Neo4j connection URI. |
| `NEO4J_USER` | `neo4j` | Neo4j username. |
| `NEO4J_PASSWORD` | `password` in the template | Set a real secret. |
| `NEO4J_DATABASE` | empty | Uses the default database when empty. |

## AI Provider Settings

Embeddings are required for normal writes and recall.

| Variable | Default | Meaning |
| --- | --- | --- |
| `AI_API_URL` | `https://api.openai.com/v1` | AI provider base URL. |
| `AI_API_KEY` | empty | Provider API key. |
| `AI_API_EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding model. |
| `AI_API_EMBEDDING_DIMENSIONS` | `1536` | Expected embedding dimensions. |
| `AI_API_EMBEDDING_TIMEOUT_SECONDS` | `30` | Embedding request timeout. |

`AI_API_URL`, `AI_API_KEY`, `AI_API_EMBEDDING_MODEL`, and
`AI_API_EMBEDDING_DIMENSIONS` are all required by the server. The defaults above
come from the compose examples and target OpenAI's
`text-embedding-3-small` model. For another provider, change the URL, model, and
dimension together.

Common OpenAI choices:

| Model | Dimensions |
| --- | --- |
| `text-embedding-3-small` | `1536` |
| `text-embedding-3-large` | `3072` |

Choose the embedding model and dimensions before storing important memory.
Changing either later requires re-embedding or rebuilding vector indexes; do not
mix vectors from different embedding configurations.

Claim verification can use the same provider or a separate verifier provider.

| Variable | Default | Meaning |
| --- | --- | --- |
| `AI_VERIFIER_API_URL` | empty | Uses `AI_API_URL` when empty. |
| `AI_VERIFIER_API_KEY` | empty | Uses `AI_API_KEY` when verifier URL is empty. |
| `AI_VERIFIER_MODEL` | `gpt-4o-mini` | Model used to verify claims. |
| `AI_VERIFIER_TIMEOUT_SECONDS` | `60` | Verifier request timeout. |
| `AI_VERIFIER_MAX_CONCURRENCY` | `5` | Maximum concurrent verifier requests. |

Provider traffic may leave your environment. The embedding provider sees memory
text and recall queries. The verifier provider sees claims and supporting
evidence.

## Redis

Redis is optional for a single local server.

Use Redis when you run multiple Dense-Mem server instances or need shared rate
limits and shared SSE concurrency controls.

| Variable | Default | Meaning |
| --- | --- | --- |
| `REDIS_ADDR` | empty | Leave empty for single-node in-memory mode. |
| `REDIS_PORT` | `6379` | Host port when using the compose Redis profile. |
| `REDIS_PASSWORD` | empty | Optional Redis password. |
| `REDIS_DB` | `0` | Redis database number. |

For the expert compose Redis service, set `REDIS_ADDR=redis:6379`, then start
Redis:

```bash
docker compose --profile redis up -d
```

See [Public HTTPS And Redis](Public-HTTPS-And-Redis#redis) for the deployment
guide.

## Public HTTPS

The quick start is local only. Skip this section unless you intentionally want
Dense-Mem reachable through a public HTTPS domain.

For public HTTPS, download the [expert compose example](https://github.com/markhuangai/dense-mem/blob/main/examples/docker-compose.expert.yml) after the local setup works:

```bash
curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.expert.yml
```

Set:

```bash
DENSE_MEM_DOMAIN=dense-mem.example.com
ACME_EMAIL=ops@example.com
```

Start:

```bash
docker compose --profile traefik up -d
```

Traefik exposes the main MCP and API service. It does not route to the control
portal. Keep the portal private.

See [Public HTTPS And Redis](Public-HTTPS-And-Redis#public-https) for the
operator guide.

## Public Edge Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `TRAEFIK_RATE_AVERAGE` | `60` | Average public request rate. |
| `TRAEFIK_RATE_PERIOD` | `1m` | Rate limit period. |
| `TRAEFIK_RATE_BURST` | `30` | Burst allowance. |
| `TRAEFIK_MAX_BODY_BYTES` | `1048576` | Public body size limit. |
| `TRAEFIK_INFLIGHT_REQUESTS` | `32` | Max in-flight requests. |

## Telemetry

Prometheus telemetry is optional and disabled by default.

| Variable | Default | Meaning |
| --- | --- | --- |
| `TELEMETRY_ENABLED` | `false` | Enables Prometheus metrics and telemetry query APIs. |
| `TELEMETRY_PROMETHEUS_URL` | empty | Prometheus base URL used by `/ui/api/telemetry` and `/control/api/telemetry`. |
| `TELEMETRY_PROMETHEUS_JOB` | empty | Optional Prometheus job filter for shared Prometheus instances. |
| `TELEMETRY_QUERY_TIMEOUT_SECONDS` | `5` | Timeout for Prometheus query requests. |
| `TELEMETRY_SCRAPE_TOKEN` | empty | Bearer token required by `/metrics` when telemetry is enabled. |
| `PROMETHEUS_PORT` | `9090` | Host port for the telemetry overlay's local Prometheus UI/API. |

To enable telemetry with the base or expert compose example:

```bash
curl -fsSLo prometheus.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.yml
curl -fsSLo docker-compose.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d
```

The overlay starts Prometheus on `127.0.0.1:9090`, keeps metrics on the private
Compose network, scrapes the control server's `/metrics` endpoint with the
scrape token, and sets `TELEMETRY_PROMETHEUS_JOB=dense-mem` by default.

See [Telemetry](Telemetry) for portal usage, demo telemetry, and troubleshooting.

## Runtime Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `HTTP_MAX_BODY_BYTES` | `1048576` | Main server request body limit. |
| `AUTH_VERIFY_MAX_CONCURRENCY` | `8` | Concurrent auth verification limit. |
| `GRAPH_QUERY_DEFAULT_TIMEOUT_SECONDS` | `10` | Default graph query timeout. |
| `GRAPH_QUERY_MAX_TIMEOUT_SECONDS` | `30` | Max graph query timeout. |
| `RATE_LIMIT_PER_MINUTE` | `100` | General API rate limit. |
| `SSE_HEARTBEAT_SECONDS` | `30` | SSE heartbeat interval. |
| `SSE_MAX_DURATION_SECONDS` | `300` | Max SSE stream duration. |
| `SSE_MAX_CONCURRENT_STREAMS` | `10` | Max concurrent SSE streams per client. |
| `FRAGMENT_CREATE_RATE_LIMIT` | `60` | Fragment write limit per minute. |
| `FRAGMENT_READ_RATE_LIMIT` | `300` | Fragment read limit per minute. |
| `CLAIM_WRITE_RATE_LIMIT` | `60` | Claim write limit per minute. |
| `CLAIM_READ_RATE_LIMIT` | `300` | Claim read limit per minute. |
| `RECALL_VALIDATED_CLAIM_WEIGHT` | `0.5` | Extra recall score weight for validated claims. |
| `PROMOTE_TX_TIMEOUT_SECONDS` | `10` | Claim promotion transaction timeout. |
| `AI_COMMUNITY_MAX_NODES` | `500000` | Maximum Neo4j nodes considered by AI community analysis. |

## Logging

| Variable | Default | Meaning |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | Set to `debug` only when troubleshooting. |

## Safer First-Run Checklist

Before using Dense-Mem seriously:

- set real secrets instead of template values
- keep the control portal on `127.0.0.1`
- create one read-write key for your main assistant
- create read-only keys for automation that should not write memory
- verify your AI provider and model dimensions before storing important memory
- back up Docker volumes if the memory matters
