# Public HTTPS And Redis

The base compose setup is local-only. Use the expert compose example when you
need public HTTPS, Traefik edge controls, optional Redis, or local database
ports for development.

For exact variables and defaults, see [Configuration](Configuration).

## Public HTTPS

Download the expert compose file after the local setup works:

```bash
curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.expert.yml
```

Set public HTTPS values in `.env`:

```text
DENSE_MEM_DOMAIN=dense-mem.example.com
ACME_EMAIL=ops@example.com
```

Start Traefik:

```bash
docker compose --profile traefik up -d
```

Traefik exposes the main MCP and API service over HTTPS. It does not route to
the control portal. Keep the control portal bound to loopback or another private
interface.

## Public Edge Controls

The expert compose file includes Traefik middleware for:

| Control | Variable |
| --- | --- |
| Average request rate | `TRAEFIK_RATE_AVERAGE` |
| Rate window | `TRAEFIK_RATE_PERIOD` |
| Burst allowance | `TRAEFIK_RATE_BURST` |
| Max request body size | `TRAEFIK_MAX_BODY_BYTES` |
| Max in-flight requests | `TRAEFIK_INFLIGHT_REQUESTS` |

These controls protect the public edge. Dense-Mem also has server-side runtime
limits documented in [Configuration](Configuration#runtime-limits).

## Redis

Redis is optional for a single local server. Use it when you run more than one
Dense-Mem server instance or need shared rate limits and shared SSE concurrency
controls.

With the expert compose Redis service, set:

```text
REDIS_ADDR=redis:6379
```

Then start the Redis profile:

```bash
docker compose --profile redis up -d
```

For non-Compose deployments, set `REDIS_ADDR` to the Redis host and port that
the Dense-Mem server process can reach.

## Combined Public Stack

To run public HTTPS and Redis together:

```bash
docker compose --profile traefik --profile redis up -d
```

Keep these boundaries clear:

| Surface | Should be public? |
| --- | --- |
| `/mcp` | Only when intentionally serving public MCP/API over HTTPS. |
| `/api/v1/*` | Same public boundary as `/mcp`. |
| `/ui` | Public only if you intentionally allow users to reach the main API domain and authenticate with their own keys. |
| Control portal `:8090` | No. Keep private. |
| Prometheus `:9090` | No. Keep private. |

## Checks

```bash
docker compose ps
docker compose logs -f traefik
docker compose logs -f server
curl https://dense-mem.example.com/health
```

If HTTPS does not work, check DNS, `DENSE_MEM_DOMAIN`, `ACME_EMAIL`, public port
80/443 reachability, and Traefik logs.
