# Dense-Mem

Dense-Mem is a standalone HTTP MCP memory server for LLM hosts. It owns durable
memory state, provenance, typed claims and facts, server-side embeddings,
team isolation, and recall. The host LLM owns conversation, judgment, and user
interaction.

HTTP MCP is the v1 supported MCP transport. Dense-Mem serves MCP at `/mcp` from
the main HTTP process and also exposes REST, OpenAPI, and a token-protected
control portal for team and profile-key administration.

Redis is optional for single-node deployments and required for multi-instance
deployments.

## Responsibility Boundary

| Area | Dense-Mem owns | Host LLM owns |
|------|----------------|---------------|
| Memory writes | Evidence fragments, typed claims, verification, gates, promotion | Extracting candidate memories from chat text |
| Embeddings | Fragment embeddings and recall-query embeddings through the configured provider | No vectors for normal writes or recall |
| Retrieval | Facts, validated claims, fragments, contradictions, clarification tasks | Choosing what to ask or cite in the conversation |
| Truth changes | Comparable-conflict detection, confirmation-driven supersession | Asking the user which uncertain memory is correct |
| Operations | Teams, named profiles, API keys, audit metadata, control portal | Client-side MCP configuration |

Dense-Mem is not an agent brain, planner, or external truth arbiter. It stores
memory, applies explicit gates, and returns structured outcomes.

## Architecture

```mermaid
flowchart TB
    subgraph Hosts["LLM hosts"]
        Claude["Claude Code"]
        Codex["Codex"]
        Apps["Other MCP clients"]
    end

    subgraph DenseMem["Dense-Mem server"]
        MCP["/mcp Streamable HTTP"]
        REST["/api/v1 REST"]
        Portal["control portal"]
        Registry["shared tool registry"]
        Memory["memory orchestration"]
        Recall["recall service"]
        Embeds["embedding provider client"]
    end

    subgraph Storage["Storage"]
        Neo4j["Neo4j graph + vector indexes"]
        Postgres["Postgres teams + profiles + audit"]
        Redis["Redis rate limits + SSE concurrency"]
    end

    Claude --> MCP
    Codex --> MCP
    Apps --> MCP
    Portal --> REST
    MCP --> Registry
    REST --> Registry
    Registry --> Memory
    Registry --> Recall
    Memory --> Embeds
    Recall --> Embeds
    Memory --> Neo4j
    Recall --> Neo4j
    REST --> Postgres
    REST --> Redis
```

## Memory Model

Dense-Mem keeps the existing low-level knowledge pipeline:

```mermaid
flowchart LR
    Fragment["SourceFragment evidence"] --> Claim["Claim candidate"]
    Claim --> Verify["verify_claim"]
    Verify --> Validated["validated claim"]
    Validated --> Promote["promote_claim"]
    Promote --> Fact["active fact"]
    Fact --> Supersede["confirmation supersession"]
```

The high-level MCP tools use that pipeline instead of bypassing it.

| Tool | Purpose |
|------|---------|
| `remember` | Normal chat-session memory insertion. Saves evidence, creates typed claims, verifies, promotes when gates pass, and returns structured outcomes. |
| `import_memories` | Ingests summarized historical conversations. By default it records evidence and validated claims without auto-promotion. |
| `recall_memory` | Retrieves facts, validated claims, fragments, and `clarifications[]` for the authenticated team. |
| `reflect_memories` | Reviews active facts, candidate or disputed claims, contradictions, stale memories, and clarification needs. |
| `confirm_memory` | Applies the user's answer to a clarification task, either accepting a claim and superseding comparable active facts or keeping/rejecting it. |

Low-level tools remain available for advanced callers: `save_memory`,
`post_claim`, `verify_claim`, `promote_claim`, search tools, graph query tools,
community tools, and retraction tools.

## Promotion Rules

User messages can become active facts only when all of these are true:

1. The host supplies a typed candidate memory.
2. The predicate is in the curated personal-memory allow-list.
3. Verification and promotion gates approve the claim.
4. No comparable active fact conflicts with the new claim.

Curated predicates cover preferences, identity/profile facts, active projects,
goals, corrections, skills, relationships, tool usage, likes, and work facts.

Comparable conflicts are not resolved silently. Dense-Mem returns
`clarifications[]`, and the host LLM should ask the user which memory is correct.
After the user answers, the host calls `confirm_memory`.

Example clarification payload shape:

```json
{
  "clarifications": [
    {
      "type": "memory_conflict",
      "subject": "user",
      "predicate": "prefers",
      "candidate_claim_id": "claim-123",
      "existing_fact_ids": ["fact-456"],
      "question": "Which preference should Dense-Mem keep?"
    }
  ]
}
```

## Embeddings

Dense-Mem owns embeddings for normal writes and recall:

- Clients send text for fragments, `remember`, `import_memories`, and recall.
- Dense-Mem embeds fragments and recall queries through the configured provider.
- Client-supplied vectors are reserved for advanced `semantic-search`.
- The stored embedding model and dimension are checked on startup so vectors from
  incompatible models are not mixed silently.

To rotate embedding models safely:

1. Re-embed stored fragments or plan to rebuild vector indexes.
2. Clear the stored embedding configuration.
3. Redeploy with the new model and dimensions.
4. Let the next successful write seed the new configuration.

## Quick Start

```bash
cp docker-compose.example.yml docker-compose.yml
cp .env.example .env
docker compose up -d
```

The default compose stack provisions `neo4j:5.26-community` with the Neo4j Graph
Data Science plugin enabled. Redis can be omitted for single-node deployments,
but multi-instance deployments need Redis for shared rate limits and SSE
concurrency.

By default, the main API port is only exposed to Docker networks. Use the
Traefik profile for public HTTPS access. The control portal host bind is
configured with `CONTROL_PORTAL_BIND` and `CONTROL_PORTAL_PORT`.

### Public HTTPS With Traefik

The example compose file includes an optional `traefik` profile for public MCP
and REST exposure. It terminates TLS with Let's Encrypt, redirects HTTP to
HTTPS, applies secure headers, request compression, edge rate limiting, request
body limits, and in-flight request limits.

```bash
cp docker-compose.example.yml docker-compose.yml
cp .env.example .env
# set DENSE_MEM_DOMAIN and ACME_EMAIL in .env
docker compose --profile traefik up -d
```

The server container exposes `8080` only to Docker networks. Traefik routes
public traffic to that internal service port; it does not route to the control
portal. The control portal is the only host-published server port, and its bind
address is controlled by `CONTROL_PORTAL_BIND`. Keep Redis disabled for a single
local instance unless you need shared state across multiple server containers.

The public Docker network is named with `DENSE_MEM_PUBLIC_NETWORK`. Set
`DENSE_MEM_PUBLIC_NETWORK_EXTERNAL=true` when you want compose to attach Traefik
and the server to an existing network instead of creating one.

The app intentionally ignores forwarded client-IP headers in this example.
Traefik remains responsible for public edge controls such as rate limits and
request limits. App-level IP ban protection is available in the control portal,
but it is disabled by default and should only be enabled when dense-mem receives
the real client IP as the direct socket address. When enabled, repeated failed
API-key, MCP, or control-token attempts are counted in Postgres and can create
permanent or time-limited bans.

## Provision A Team And API Key

Dense-Mem API keys are opaque keys bound to a named profile inside a team. The
team binding lives on the server side; callers do not send `X-Team-ID` for
header-scoped memory routes. Multiple MCP client configurations can point at
different teams by using different team-profile API keys.

The container includes `/app/provision-team`:

```bash
docker compose exec server /app/provision-team --name "primary-memory"
```

Local Go development:

```bash
go run ./cmd/provision-profile --name "primary-memory"
```

Example output:

```json
{
  "team_id": "11111111-2222-3333-4444-555555555555",
  "team_name": "primary-memory",
  "profile_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  "profile_name": "default profile",
  "scopes": ["read", "write"],
  "api_key": "dm_default-prof_..."
}
```

The plaintext `api_key` is returned only at creation time.
Team-profile keys default to `["read","write"]`. Create a read-only profile key
for automation by passing `scopes: ["read"]` to the team-profile creation API or
by selecting **Read only** in the control portal. Supported scope sets are
`["read"]` and `["read","write"]`; rotate preserves the existing permissions.

Useful operator commands:

```bash
docker compose exec server /app/list-teams
docker compose exec server /app/list-team-profiles --team-id "<team-id>"
docker compose exec server /app/rotate-team-profile-key --team-id "<team-id>" --profile-id "<profile-id>"
docker compose exec server /app/delete-team-profile --team-id "<team-id>" --profile-id "<profile-id>"
docker compose exec server /app/delete-team --team-id "<team-id>"
```

`delete-team` hard-deletes the team, team profiles, and team-owned memory rows.
The audit log remains append-only.

## Configure MCP

Use a team-profile API key:

```bash
export DENSE_MEM_API_KEY="dm_default-prof_..."
```

Dense-Mem serves MCP Streamable HTTP at:

```text
http://localhost:8080/mcp
```

MCP clients authenticate with:

```text
Authorization: Bearer <team-profile-api-key>
```

### Claude Code

```bash
claude mcp add --transport http dense-mem http://localhost:8080/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

You can also pass the key directly:

```bash
claude mcp add --transport http dense-mem http://localhost:8080/mcp \
  --header "Authorization: Bearer dm_live_..."
```

Claude Code documents HTTP MCP servers as the recommended remote-server option
and marks SSE transport as deprecated.

### Codex

Add a Streamable HTTP server to `~/.codex/config.toml` or a trusted project's
`.codex/config.toml`. This is the native Dense-Mem MCP configuration for Codex
CLI and the Codex IDE extension. To store the header directly in the config:

```toml
[mcp_servers.dense_mem]
url = "http://localhost:8080/mcp"
http_headers = { Authorization = "Bearer dm_live_..." }
tool_timeout_sec = 60
enabled = true
```

This stores the API key in plaintext in the Codex config. To keep the key out of
the file, use `bearer_token_env_var` instead:

```toml
[mcp_servers.dense_mem]
url = "http://localhost:8080/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

Codex shares this configuration between the CLI and IDE extension.

### Stdio Compatibility Proxy

Some desktop MCP clients can run stdio MCP commands but do not reliably surface
Streamable HTTP MCP servers in their callable tool registry. For those clients,
use the Dense-Mem stdio proxy.

Once the package is published to npm, use:

```bash
npx -y dense-mem-mcp-proxy
```

Until then, run the package from a local checkout or a local `npm pack` tarball.
For a checkout at `/path/to/dense-mem`:

```toml
[mcp_servers.dense_mem]
command = "node"
args = [
  "/path/to/dense-mem/packages/mcp-proxy/bin/dense-mem-mcp-proxy.js",
  "--url",
  "http://127.0.0.1:8080/mcp",
  "--header",
  "Authorization: Bearer dm_live_..."
]
tool_timeout_sec = 60
enabled = true
```

The same configuration can use `npx` after publication:

```toml
[mcp_servers.dense_mem]
command = "npx"
args = [
  "-y",
  "dense-mem-mcp-proxy",
  "--url",
  "http://127.0.0.1:8080/mcp",
  "--header",
  "Authorization: Bearer dm_live_..."
]
tool_timeout_sec = 60
enabled = true
```

Environment variables are also supported:

```toml
[mcp_servers.dense_mem]
command = "npx"
args = ["-y", "dense-mem-mcp-proxy"]
env = { DENSE_MEM_MCP_URL = "http://127.0.0.1:8080/mcp", DENSE_MEM_API_KEY = "dm_live_..." }
tool_timeout_sec = 60
enabled = true
```

The proxy supports `--authorization "Bearer ..."` and repeated
`--header "Name: value"` flags. It writes MCP JSON-RPC only to stdout; diagnostic
logs go to stderr with Authorization headers and Dense-Mem API keys redacted.

Claude Desktop can use the same package:

```json
{
  "mcpServers": {
    "dense_mem": {
      "command": "node",
      "args": [
        "/path/to/dense-mem/packages/mcp-proxy/bin/dense-mem-mcp-proxy.js",
        "--url",
        "http://127.0.0.1:8080/mcp",
        "--header",
        "Authorization: Bearer dm_live_..."
      ]
    }
  }
}
```

## HTTP API Surface

| Area | Routes |
|------|--------|
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
Read-only profile keys can call read routes, recall, graph/keyword/semantic
search, and read-scoped MCP tools. Write routes and write-scoped MCP tools
return insufficient-permission errors.

Example:

```bash
curl http://localhost:8080/api/v1/tools \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY"
```

```bash
curl "http://localhost:8080/api/v1/recall?q=preferences" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY"
```

Create a read-only automation key:

```bash
curl -X POST "http://localhost:8080/api/v1/teams/$TEAM_ID/profiles" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"automation-readonly","scopes":["read"],"rate_limit":120}'
```

## Local Control Portal

The control portal is for team, team-profile key, and security-ban management. It does
not expose a memory, fact, claim, graph, or database browser.

Environment variables:

```bash
CONTROL_PORTAL_TOKEN=
```

With the local Docker compose setup, the portal is available at
`http://127.0.0.1:8090/`. The compose file publishes the portal port on host
loopback only by default. Operators may publish the port differently for their
own network.

Dense-Mem validates all of the following before starting the portal:

- `CONTROL_PORTAL_TOKEN` must be set.
- Requests must include the portal token.
- Browser `Origin` headers are allowed for token-authenticated requests.

The portal supports:

- list, create, update, and delete teams where the existing deletion rules
  allow deletion
- list, create, and delete named profiles and their API keys
- one-time plaintext key display immediately after key creation
- view and update IP ban settings
- list active bans, add manual bans, and remove bans

## Data Egress

Dense-Mem validates required embedding configuration at server startup. Fragment
content and recall queries are forwarded to the configured embedding provider
for vectorization. Claim verification uses `AI_VERIFIER_API_URL`,
`AI_VERIFIER_API_KEY`, and `AI_VERIFIER_MODEL`; when verifier URL/key are unset,
verification falls back to `AI_API_URL` and `AI_API_KEY`.

The embedding provider sees raw fragment/query text. The verifier provider sees
candidate claims and supporting evidence. Operators must review provider terms
before enabling external AI services. Self-hosted providers keep traffic inside
your boundary; hosted providers do not.

## Tool Discoverability

Dense-Mem exposes three discoverability surfaces backed by one registry:

| Surface | Path | Purpose |
|---------|------|---------|
| Tool catalog | `GET /api/v1/tools` | Runtime tool discovery |
| Runtime OpenAPI | `GET /api/v1/openapi.json` | Agents, codegen, integrations |
| MCP Streamable HTTP | `POST /mcp`, `GET /mcp` | MCP clients over the main HTTP service |

`POST /mcp` handles JSON-RPC requests and can return JSON or SSE when requested.
`GET /mcp` opens the server-to-client SSE stream where supported.

## Reference Docs

- [standalone MCP memory architecture](docs/standalone-mcp-memory-architecture.md)
- [knowledge-pipeline contracts](docs/knowledge-pipeline-contracts.md)
- [knowledge-pipeline client contracts](docs/knowledge-pipeline-client-contracts.md)
- [knowledge-pipeline operability](docs/knowledge-pipeline-operability.md)

## License

MIT
