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

## Portable Skill-Pack Tools

Skill packs are portable artifacts for moving selected knowledge between teams or
deployments. They use schema version `dense-mem.skill_pack.v1`.

| Tool | Scope | Purpose |
| --- | --- | --- |
| `find_skill_pack_candidates` | read | Find active facts and validated claims that match a topic query and can be exported. |
| `export_skill_pack` | read | Export selected facts, validated claims, and manual triples into canonical JSON plus a SHA-256 hash. |
| `inspect_skill_pack` | read | Parse an artifact or HTTPS URL, verify the optional hash, and report duplicates, conflicts, and required decisions without writing memory. |
| `import_skill_pack` | write | Import an artifact in review or trusted mode. |
| `rollback_skill_pack_import` | write | Roll back an applied import through the import ledger. |

Advanced lower-level tools exist for fragments, claims, verification, promotion,
search, graph queries, communities, retraction, and skill-pack workflows.

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

## Skill-Pack Artifact Shape

Minimal artifact:

```json
{
  "schema_version": "dense-mem.skill_pack.v1",
  "name": "Repo review standards",
  "description": "Reusable memories for repository review work.",
  "items": [
    {
      "subject": "assistant",
      "predicate": "has_skill",
      "object": "starts code reviews with bugs, risks, and missing tests",
      "source_kind": "manual"
    }
  ]
}
```

Supported predicates:

| Predicate | Use |
| --- | --- |
| `has_skill` | Capabilities, learned procedures, or review standards. |
| `knows` | Domain facts or project knowledge. |
| `uses` | Tools, frameworks, libraries, or workflows. |

Supported source kinds:

| Source kind | Import behavior |
| --- | --- |
| `manual` | Imported as a claim. In trusted mode, the claim can be marked validated. |
| `source_validated_claim` | Represents a claim that was validated in the source team. |
| `source_fact` | Represents an active fact from the source team. In trusted mode, it can be promoted to an active fact unless demoted. |

Limits:

| Field | Limit |
| --- | --- |
| `name` | 256 characters |
| `description` | 1024 characters |
| `items` | 1 to 100 items |
| `subject` | 256 characters |
| `object` | 1024 characters |

## Skill-Pack Examples

Find export candidates:

```json
{
  "query": "React testing",
  "limit": 10
}
```

Export a manual pack:

```json
{
  "name": "React testing conventions",
  "description": "Reusable testing preferences for frontend work.",
  "manual_items": [
    {
      "subject": "frontend team",
      "predicate": "uses",
      "object": "React Testing Library for component behavior tests",
      "source_kind": "manual"
    }
  ]
}
```

The export response includes:

| Field | Meaning |
| --- | --- |
| `artifact` | Structured skill-pack object. |
| `canonical_json` | Deterministic JSON to share or store. |
| `sha256` | SHA-256 of the canonical JSON. |
| `item_count` | Number of exported items. |

Inspect before import:

```json
{
  "artifact": {
    "schema_version": "dense-mem.skill_pack.v1",
    "name": "React testing conventions",
    "description": "Reusable testing preferences for frontend work.",
    "items": [
      {
        "subject": "frontend team",
        "predicate": "uses",
        "object": "React Testing Library for component behavior tests",
        "source_kind": "manual"
      }
    ]
  },
  "expected_sha256": "<sha256 from export>",
  "recommend_decisions": true
}
```

`inspect_skill_pack` reports item statuses such as:

| Status | Meaning |
| --- | --- |
| `new` | No matching local fact or claim was found. |
| `duplicate_fact` | The item matches an active local fact. |
| `already_claimed` | The item matches a local claim. |
| `conflicts_with_fact` | The item conflicts with an active local fact. |
| `matches_superseded_fact` | The item matches a fact that was superseded locally. |
| `unsupported_predicate` | The item's predicate is not allowed for skill packs. |

Import in review mode:

```json
{
  "artifact": {
    "schema_version": "dense-mem.skill_pack.v1",
    "name": "React testing conventions",
    "description": "Reusable testing preferences for frontend work.",
    "items": [
      {
        "subject": "frontend team",
        "predicate": "uses",
        "object": "React Testing Library for component behavior tests",
        "source_kind": "manual"
      }
    ]
  },
  "expected_sha256": "<sha256 from export>",
  "mode": "review"
}
```

Review mode writes import evidence and candidate claims. It does not promote
imported facts.

Import in trusted mode with an explicit conflict decision:

```json
{
  "artifact": {
    "schema_version": "dense-mem.skill_pack.v1",
    "name": "React testing conventions",
    "description": "Reusable testing preferences for frontend work.",
    "items": [
      {
        "subject": "frontend team",
        "predicate": "uses",
        "object": "React Testing Library for component behavior tests",
        "source_kind": "source_fact"
      }
    ]
  },
  "expected_sha256": "<sha256 from export>",
  "mode": "trusted",
  "conflict_decisions": [
    {
      "index": 0,
      "action": "demote_to_claim"
    }
  ]
}
```

Trusted mode can validate claims and promote `source_fact` items. If selected
items conflict with active local facts or match superseded local facts,
`import_skill_pack` returns `status: "needs_review"` and `decisions_required`
unless the request supplies allowed `conflict_decisions`.

Allowed conflict actions:

| Action | Meaning |
| --- | --- |
| `skip` | Skip the item. |
| `import_anyway` | Import without superseding local facts. |
| `supersede_local` | Supersede conflicting active local facts. Only allowed when there is an active fact conflict. |
| `demote_to_claim` | Import a `source_fact` as a validated claim instead of promoting it. Only allowed for `source_fact` items. |

Import from a URL:

```json
{
  "url": "https://example.com/skill-packs/react-testing.json",
  "expected_sha256": "<sha256 from export>",
  "mode": "trusted"
}
```

URL imports accept HTTPS only. The fetcher rejects local, private, link-local,
unspecified, and multicast addresses. Trusted URL imports require
`expected_sha256`.

Rollback:

```json
{
  "import_id": "11111111-2222-3333-4444-555555555555"
}
```

Rollback returns `status: "rolled_back"` when it reverts the recorded changes.
It returns `status: "blocked"` with `conflicts[]` if affected graph entities were
changed after import.

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
| Skill-pack conflict decider | Conflict prompts and inspected item context when `recommend_decisions` or `auto_decide_conflicts` is enabled. |

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
| Skill-pack import | Roll back with `rollback_skill_pack_import`. Rollback blocks if imported graph entities changed after import. |

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
| `permission denied to create extension "pgcrypto"` during Postgres migrations | Install `pgcrypto` once with a database owner or admin role, then restart the server. See [Configuration](Configuration#database-settings). |
| Recall fails | AI provider key, embedding model, embedding dimensions, provider quota. |
| MCP client cannot connect | URL, bearer token, client MCP config, server logs. |
| Control portal rejects requests | `CONTROL_PORTAL_TOKEN` and portal URL. |
| Public HTTPS does not work | `DENSE_MEM_DOMAIN`, DNS, `ACME_EMAIL`, Traefik logs. |
