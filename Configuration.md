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
| `AI_API_KEY` | API key used by the embedding provider. | Your provider key. |
| `AI_API_EMBEDDING_MODEL` | Embedding model name. | Default is `text-embedding-3-small`. |
| `AI_API_EMBEDDING_DIMENSIONS` | Embedding size expected from the model. | Default is `1536`. |

Dense-Mem validates embedding configuration on startup. Do not change embedding
model or dimensions casually after storing memory. Mixed vector dimensions can
break search.

## Local Ports

| Variable | Default | Meaning |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Main server address inside the container. |
| `CONTROL_HTTP_ADDR` | `:8090` | Control portal address inside the container. |
| `CONTROL_PORTAL_BIND` | `127.0.0.1` | Host interface for the control portal. |
| `CONTROL_PORTAL_PORT` | `8090` | Host port for the control portal. |
| `NEO4J_HTTP_PORT` | `7474` | Local Neo4j browser port. |
| `NEO4J_BOLT_PORT` | `7687` | Local Neo4j driver port. |

Keep the control portal on `127.0.0.1` for local use. It manages API keys.

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

Claim verification can use the same provider or a separate verifier provider.

| Variable | Default | Meaning |
| --- | --- | --- |
| `AI_VERIFIER_API_URL` | empty | Uses `AI_API_URL` when empty. |
| `AI_VERIFIER_API_KEY` | empty | Uses `AI_API_KEY` when verifier URL is empty. |
| `AI_VERIFIER_MODEL` | `gpt-4o-mini` | Model used to verify claims. |
| `AI_VERIFIER_TIMEOUT_SECONDS` | `60` | Verifier request timeout. |

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

Start Redis when using the expert compose example:

```bash
docker compose --profile redis up -d
```

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

## Public Edge Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `TRAEFIK_RATE_AVERAGE` | `60` | Average public request rate. |
| `TRAEFIK_RATE_PERIOD` | `1m` | Rate limit period. |
| `TRAEFIK_RATE_BURST` | `30` | Burst allowance. |
| `TRAEFIK_MAX_BODY_BYTES` | `1048576` | Public body size limit. |
| `TRAEFIK_INFLIGHT_REQUESTS` | `32` | Max in-flight requests. |

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
