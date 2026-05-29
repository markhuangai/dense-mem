# Technical Reference

This page collects the deeper details that most first-time users can skip.

## High-Level MCP Tools

| Tool | Purpose |
| --- | --- |
| `remember` | Save evidence, create typed claims, verify them, and promote safe memories. |
| `import_memories` | Import summarized historical conversations. Does not auto-promote by default. |
| `recall_memory` | Search active facts, validated claims, fragments, and clarifications. |
| `reflect_memories` | Review memory health, stale items, contradictions, and clarification needs. |
| `confirm_memory` | Apply the user's answer to a memory clarification. |

Advanced lower-level tools exist for fragments, claims, verification, promotion,
search, graph queries, communities, and retraction.

## HTTP API Surface

| Area | Routes |
| --- | --- |
| Health | `GET /health` |
| Fragments | `POST /api/v1/fragments`, `GET /api/v1/fragments`, `GET /api/v1/fragments/{id}`, `DELETE /api/v1/fragments/{id}`, `POST /api/v1/fragments/{id}/retract` |
| Claims | `POST /api/v1/claims`, `GET /api/v1/claims`, `GET /api/v1/claims/{id}`, `DELETE /api/v1/claims/{id}`, `POST /api/v1/claims/{id}/verify`, `POST /api/v1/claims/{id}/promote` |
| Facts | `GET /api/v1/facts`, `GET /api/v1/facts/{id}` |
| Recall | `GET /api/v1/recall` |
| Tools | `GET /api/v1/tools`, `GET /api/v1/tools/{id}`, `POST /api/v1/tools/{name}` |
| Teams | `GET /api/v1/teams/{teamId}`, `PATCH /api/v1/teams/{teamId}`, `GET /api/v1/teams/{teamId}/audit-log` |
| Team profiles | `GET /api/v1/teams/{teamId}/profiles`, `POST /api/v1/teams/{teamId}/profiles`, `POST /api/v1/teams/{teamId}/profiles/{profileId}/rotate`, `DELETE /api/v1/teams/{teamId}/profiles/{profileId}` |
| Streaming | `POST /api/v1/teams/{teamId}/query/stream` |
| Communities | `GET /api/v1/communities`, `GET /api/v1/communities/{id}` |
| MCP | `POST /mcp`, `GET /mcp` |

Header-scoped memory routes derive the team and profile from the bearer API key.
Do not send `X-Team-ID` for normal memory routes.

## Recall Endpoint

```text
GET /api/v1/recall
```

Common query parameters:

| Parameter | Required | Meaning |
| --- | --- | --- |
| `query` | yes | Natural-language search query. |
| `limit` | no | Maximum hits, up to 50. |
| `valid_at` | no | Valid-time filter. |
| `known_at` | no | Known-time filter. |
| `include_evidence` | no | Include evidence payloads when true. |

Example:

```bash
curl "http://localhost:8080/api/v1/recall?query=preferences" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY"
```

## Read-Only Keys

Use read-only keys for automation that should search memory but never write it.

Create one through the API:

```bash
curl -X POST "http://localhost:8080/api/v1/teams/$TEAM_ID/profiles" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"automation-readonly","scopes":["read"],"rate_limit":120}'
```

Read-only keys can call read routes, recall, search, and read-scoped MCP tools.
Write routes return insufficient-permission errors.

## Control Portal

The control portal manages teams, profiles, keys, and security-ban settings.

It does not browse or edit memory content.

Required setting:

```bash
CONTROL_PORTAL_TOKEN=choose-a-long-secret
```

Local URL:

```text
http://127.0.0.1:8090/
```

Keep it private.

## Data Egress

Dense-Mem sends memory-related text to configured AI providers:

| Provider path | Text sent |
| --- | --- |
| Embedding provider | Fragment text and recall queries. |
| Verifier provider | Candidate claims and supporting evidence. |

Use a self-hosted provider if memory text must stay inside your environment.

## Redis And Multi-Instance Deployments

Single-node deployments can leave Redis disabled.

Use Redis when you run more than one Dense-Mem server because rate limits and SSE
stream concurrency must be shared across instances.

## Rollback Notes

| Item | Rollback option |
| --- | --- |
| Fragment | Retract with `POST /api/v1/fragments/{id}/retract` or hard-delete with write scope. |
| Claim | Delete with `DELETE /api/v1/claims/{id}`. This is irreversible. |
| Fact | No direct delete endpoint. Retract supporting evidence so facts can be revalidated. |
| Community assignment | Rerun detection to overwrite community IDs. |

## Operational Checks

Useful checks:

```bash
docker compose ps
docker compose logs -f server
curl http://localhost:8080/health
```

Useful team commands:

```bash
docker compose exec server /app/list-teams
docker compose exec server /app/list-team-profiles --team-id "<team-id>"
docker compose exec server /app/rotate-team-profile-key \
  --team-id "<team-id>" \
  --profile-id "<profile-id>"
```

## Troubleshooting

| Problem | Check |
| --- | --- |
| Server does not start | Required `.env` values, Postgres health, Neo4j health. |
| Recall fails | AI provider key, embedding model, embedding dimensions, provider quota. |
| MCP client cannot connect | URL, bearer token, client MCP config, server logs. |
| Control portal rejects requests | `CONTROL_PORTAL_TOKEN` and portal URL. |
| Public HTTPS does not work | `DENSE_MEM_DOMAIN`, DNS, `ACME_EMAIL`, Traefik logs. |
