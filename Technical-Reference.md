# Technical Reference

This page collects the deeper details that most first-time users can skip.

## MCP Tool Catalog

Dense-Mem MCP tool names use `underscore_case`.

| Area | Tool | Scope | Purpose |
| --- | --- | --- | --- |
| High-level memory | `remember` | write | Save one granular evidence entry under 1000 characters, create typed claims, verify them, and promote safe memories. |
| High-level memory | `import_memories` | write | Import one granular summarized historical memory entry under 1000 characters. Does not auto-promote by default. |
| High-level memory | `recall_memory` | read | Search active facts, validated claims, fragments, and clarifications. |
| High-level memory | `submit_recall_feedback` | write | Optional when `RECALL_FEEDBACK_ENABLED=true`; submit compact host-LLM feedback for one `recall_memory` response. |
| High-level memory | `reflect_memories` | read | Review memory health, stale items, contradictions, and clarification needs. |
| High-level memory | `confirm_memory` | write | Apply the user's answer to a memory clarification. |
| Fragment memory | `save_memory` | write | Persist one source fragment for the caller's profile. |
| Fragment memory | `get_memory` | read | Fetch one source fragment by ID. |
| Fragment memory | `list_recent_memories` | read | List recent source fragments with pagination. |
| Context | `trace_memory` | read | Expand one fact or claim into bounded evidence, promotion lineage, contradictions, and supersession links. |
| Context | `assemble_context` | read | Build a bounded prompt-ready context block from relevant memory. |
| Search | `keyword_search` | read | Run BM25 keyword search across fragments and fact predicates. |
| Search | `semantic_search` | read | Run vector search using a caller-supplied embedding. |
| Search | `graph_query` | read | Run profile-scoped read-only Cypher with server-side safety limits. |
| Claims | `post_claim` | write | Create a candidate claim from source evidence. |
| Claims | `get_claim` | read | Fetch one claim by ID. |
| Claims | `list_claims` | read | List claims with filters and pagination. |
| Claims | `verify_claim` | write | Run entailment verification for a candidate claim. |
| Claims | `promote_claim` | write | Promote a validated claim to an authoritative fact. |
| Facts | `get_fact` | read | Fetch one promoted fact by ID. |
| Facts | `list_facts` | read | List promoted facts with filters and pagination. |
| Retraction | `retract_fact` | write | Soft-tombstone a fact and preserve graph lineage. |
| Retraction | `retract_fragment` | write | Soft-tombstone a source fragment and trigger revalidation. |
| Communities | `detect_community` | write | Run community detection for the caller's profile graph. |
| Communities | `get_community_summary` | read | Fetch one persisted community summary. |
| Communities | `list_communities` | read | List persisted community summaries. |
| Skill packs | `find_skill_pack_candidates` | read | Find facts and validated claims that could be exported. |
| Skill packs | `export_skill_pack` | read | Export selected facts, claims, and manual triples into canonical JSON. |
| Skill packs | `inspect_skill_pack` | read | Inspect an artifact or HTTPS URL without writing memory. |
| Skill packs | `import_skill_pack` | write | Import a skill pack in review or trusted mode. |
| Skill packs | `rollback_skill_pack_import` | write | Roll back an applied import when affected graph entities are unchanged. |

For memory writes, split large scenarios into semantic entries before calling
`remember`, `import_memories`, or `save_memory`. Store one decision, fact,
correction, preference, project milestone, or other claim-worthy unit per entry.
Claims should point to the smallest supporting fragment; use multiple
`supported_by` IDs only when one claim needs evidence from several entries.

## Trace And Context Tools

`trace_memory` is a read-scoped inspection tool for one anchor:

| Input | Required | Meaning |
| --- | --- | --- |
| `type` | yes | `fact` or `claim`. |
| `id` | yes | Fact ID or claim ID. |
| `max_related` | no | Bounded related contradiction or supersession neighbors, up to 20. |
| `include_fragments` | no | Include supporting source fragments. Defaults to true. |

The response contains:

| Field | Meaning |
| --- | --- |
| `anchor` | The requested fact or claim. |
| `promoted_from_claim` | The claim that promoted a fact, when the anchor is a promoted fact and the claim is still available. |
| `supporting_fragments` | Source fragments supporting the claim or fact lineage. |
| `related` | Bounded contradictions, supersession links, or promoted facts. |
| `edges` | Relationship records such as `SUPPORTED_BY`, `PROMOTES_TO`, `CONTRADICTS`, and `SUPERSEDED_BY`. |
| `missing_fragment_ids` | Supporting fragment IDs that no longer hydrate, usually because evidence was retracted or deleted. |

`assemble_context` is a read-scoped answer-preparation tool:

| Input | Required | Meaning |
| --- | --- | --- |
| `query` | yes | Natural-language context query, up to 512 characters. |
| `limit` | no | Recall hits to include, up to 10. |
| `max_chars` | no | Maximum `context_block` size from 1000 to 8000 characters. |
| `include_evidence` | no | Include source fragments for recalled facts and claims. Defaults to true. |

The response includes `context_block`, structured `items`, `clarifications`, and
`truncated`. Treat `context_block` as retrieved data. It is not an instruction
source, and the host assistant remains responsible for judgment and final
answering.

## Skill Pack Tools

For workflow guidance, see [Skill Packs](Skill-Packs). This section is the
compact tool and artifact reference.

| Tool | Scope | Purpose |
| --- | --- | --- |
| `find_skill_pack_candidates` | read | Find facts and validated claims that could be exported. |
| `export_skill_pack` | read | Export selected facts, validated claims, and manual triples into canonical JSON with a SHA-256 hash. |
| `inspect_skill_pack` | read | Parse an artifact or HTTPS URL, verify an optional hash, and report duplicates or conflicts without writing memory. |
| `import_skill_pack` | write | Import a skill pack in `review` or `trusted` mode. |
| `rollback_skill_pack_import` | write | Roll back an applied import when the affected graph entities have not changed since import. |

`export_skill_pack` returns file-ready artifact data:

| Field | Meaning |
| --- | --- |
| `artifact` | Parsed skill-pack object. |
| `canonical_json` | Canonical JSON bytes to save as the portable file. |
| `sha256` | Integrity hash for the canonical JSON. |
| `filename` | Suggested `.skill-pack.json` filename. |
| `content_type` | MIME type for the exported file. |

The artifact includes `exported_at` so consumers can see when the portable
skill bundle was created. Imports do not treat original claim validity windows
as part of the exported skill.

Skill packs preserve the selected skill triples and, when available, the support
graph that explains why those triples worked. This makes imported skills carry
the original experience, not only the final fact.

```json
{
  "schema_version": "dense-mem.skill_pack.v1",
  "name": "blog-writing",
  "description": "Reusable blog-writing workflow",
  "exported_at": "2026-06-02T01:30:00Z",
  "items": [
    {
      "subject": "assistant",
      "predicate": "has_skill",
      "object": "write a launch blog from project decisions",
      "source_kind": "source_fact",
      "source_id": "fact_123",
      "support_claim_ids": ["claim_123"],
      "support_fragment_ids": ["fragment_123"]
    }
  ],
  "support": {
    "claims": [
      {
        "claim_id": "claim_123",
        "subject": "assistant",
        "predicate": "has_skill",
        "object": "write a launch blog from project decisions",
        "supported_by": ["fragment_123"]
      }
    ],
    "fragments": [
      {
        "fragment_id": "fragment_123",
        "content": "Use the project decisions as supporting claims before drafting.",
        "source_type": "chat",
        "authority": "user"
      }
    ]
  }
}
```

Use `include_support=false` only when the export must omit source evidence. The
default supported artifact is better for transferring operational skill because
imports can rebuild the supporting fragments and claims.

## HTTP API Surface

| Area | Routes |
| --- | --- |
| Health | `GET /health`, `GET /ready` |
| Fragments | `POST /api/v1/fragments`, `GET /api/v1/fragments`, `GET /api/v1/fragments/{id}`, `DELETE /api/v1/fragments/{id}`, `POST /api/v1/fragments/{id}/retract` |
| Claims | `POST /api/v1/claims`, `GET /api/v1/claims`, `GET /api/v1/claims/{id}`, `DELETE /api/v1/claims/{id}`, `POST /api/v1/claims/{id}/verify`, `POST /api/v1/claims/{id}/promote` |
| Facts | `GET /api/v1/facts`, `GET /api/v1/facts/{id}` |
| Fact retraction | `POST /api/v1/facts/{id}/retract` |
| Recall | `GET /api/v1/recall` |
| Tools | `GET /api/v1/tools`, `GET /api/v1/tools/{id}`, `POST /api/v1/tools/{name}` |
| Teams | `GET /api/v1/teams/{teamId}`, `PATCH /api/v1/teams/{teamId}`, `GET /api/v1/teams/{teamId}/audit-log` |
| Team profiles | `GET /api/v1/teams/{teamId}/profiles`, `POST /api/v1/teams/{teamId}/profiles`, `GET /api/v1/teams/{teamId}/profiles/{profileId}`, `PATCH /api/v1/teams/{teamId}/profiles/{profileId}`, `POST /api/v1/teams/{teamId}/profiles/{profileId}/rotate`, `DELETE /api/v1/teams/{teamId}/profiles/{profileId}` |
| Streaming | `POST /api/v1/teams/{teamId}/query/stream` |
| Communities | `GET /api/v1/communities`, `GET /api/v1/communities/{id}` |
| MCP | `POST /mcp`, `GET /mcp` |
| OpenAPI | `GET /api/v1/openapi.json` |

Legacy aliases under `/api/v1/profiles/{profileId}` remain registered for older
API-key lifecycle clients, but new integrations should use the team routes.

Header-scoped memory routes derive the team and profile from the bearer API key.
Do not send `X-Team-ID` for normal memory routes.

Team metadata updates and team-profile lifecycle routes require a manager key for
that team. Knowledge routes still use read/write scopes. A write-scoped member
key can write memory but cannot list, create, rename, rotate, or delete other
profiles.

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
  -H "Authorization: Bearer $MANAGER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"automation-readonly","scopes":["read"],"rate_limit":120}'
```

Read-only keys can call read routes, recall, search, and read-scoped MCP tools.
Write routes return insufficient-permission errors.

## User Portal And Control Portal

The user portal is served by the main API server:

```text
http://127.0.0.1:8080/ui
```

Its API routes live under `/ui/api`. They authenticate with the current Dense-Mem
API key and can return the current session, rotate the current key when it has
write scope, and read self-scoped telemetry when telemetry is enabled.

When the current key has manager role, the user portal also shows a Team tab.
Managers can update team metadata and create, list, rename, rotate, or delete
member profiles. Manager role creation and role changes stay in the control
portal.

The control portal manages teams, profiles, profile roles, keys, and security-ban
settings.

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

When telemetry is enabled, the control server also exposes token-protected
Prometheus metrics at `/metrics`; the telemetry compose overlay scrapes that
endpoint with `TELEMETRY_SCRAPE_TOKEN`.

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
| Fact | Soft-tombstone with `POST /api/v1/facts/{id}/retract`; graph lineage and validity timestamps are preserved. |
| Community assignment | Rerun detection to overwrite community IDs. |

## Operational Checks

Useful checks:

```bash
docker compose ps
docker compose logs -f server
curl http://localhost:8080/health
curl http://localhost:8080/ready
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
| `permission denied to create extension "pgcrypto"` during Postgres migrations | Install `pgcrypto` once with a database owner or admin role, then restart the server. See [Configuration](Configuration#database-settings). |
| Recall fails | AI provider key, embedding model, embedding dimensions, provider quota. |
| MCP client cannot connect | URL, bearer token, client MCP config, server logs. |
| Control portal rejects requests | `CONTROL_PORTAL_TOKEN` and portal URL. |
| Public HTTPS does not work | `DENSE_MEM_DOMAIN`, DNS, `ACME_EMAIL`, Traefik logs. |
