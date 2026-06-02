# Standalone MCP Memory Architecture

## Scope

Dense-Mem is a standalone HTTP MCP memory server. It is designed for LLM hosts
that need durable personal or project memory without embedding memory storage
inside the host process.

The v1 supported MCP transport is Streamable HTTP at `/mcp`. REST and OpenAPI
surfaces are maintained for non-MCP integrations and operational tooling.

## Design Goals

- Keep memory state outside the host LLM.
- Preserve evidence provenance instead of storing opaque summaries only.
- Let Dense-Mem own embeddings for normal writes and recall.
- Promote only typed, gate-approved personal-memory claims.
- Return clarification tasks instead of choosing silently during comparable
  conflicts.
- Keep profile and API-key administration token-protected and operator-scoped.
- Preserve optional Redis operation for single-node deployments while requiring
  Redis for multi-instance rate limits and SSE concurrency.

## System Boundary

```mermaid
flowchart LR
    Host["Host LLM"] --> Extract["Extract candidate typed memories"]
    Extract --> MCP["Dense-Mem MCP tools"]
    MCP --> Memory["memory orchestration"]
    Memory --> Verify["verification + gates"]
    Verify --> Graph["Neo4j graph"]
    Memory --> Embed["embedding provider"]
    MCP --> Clarify["clarifications[]"]
    Clarify --> Host
```

The host LLM should extract candidate memory objects from conversation text. It
should not send embedding vectors for normal memory insertion or recall. Dense-Mem
validates, embeds, persists, gates, and returns structured outcomes.

## Data Model

Dense-Mem uses a three-layer memory model:

| Layer | Role | Notes |
|-------|------|-------|
| `SourceFragment` | Immutable evidence | Stores original text, source metadata, embedding model, embedding dimensions, and provenance. |
| `Claim` | Typed candidate assertion | Stores subject, predicate, object, confidence, verification status, and evidence links. |
| `Fact` | Promoted active memory | Stores current or historical facts with status, truth score, and supersession metadata. |

Facts are not overwritten in place. Corrections create new facts and supersede
older comparable facts so audit and history remain intact.

## High-Level MCP Tools

### `remember`

Normal chat-session insertion.

MCP callers should send one granular evidence entry per call. Each entry is
validated to stay under 1000 characters; larger scenarios should be split by
decision, fact, correction, preference, project milestone, or another
claim-worthy unit. Typed claims should point at the smallest supporting entry,
using multiple supporting fragment IDs only when one claim needs evidence from
more than one entry.

1. Save source evidence as a fragment.
2. Embed the fragment through the configured provider.
3. Create typed claims from host-supplied candidate memories.
4. Verify each claim against evidence.
5. Run promotion gates.
6. Promote only if no comparable active fact conflicts.
7. Return created evidence, claim outcomes, promoted facts, rejections, and
   clarification tasks.

### `import_memories`

Historical conversation import.

The import path favors preservation over aggressive promotion. By default it
saves evidence and validated claims without turning summarized history into
active facts. Callers can opt into auto-promotion when the summary is trusted and
the same gates still pass.

### `recall_memory`

Profile-scoped retrieval.

Recall combines active facts, validated claims, fragments, and clarification
needs. Results include `clarifications[]` so host LLMs can ask the user about
conflicts during normal chat.

### `reflect_memories`

Memory review.

Reflection summarizes active facts, candidate or disputed claims,
contradictions, stale memories, and clarification needs. It is the periodic
maintenance surface for hosts that want to review memory health.

### `confirm_memory`

Clarification resolution.

After the host asks the user which memory is correct, it calls
`confirm_memory`. Accepting the candidate claim promotes it and supersedes
comparable active facts. Keeping the existing fact rejects or leaves the
candidate unpromoted.

## Predicate Policy

Dense-Mem only auto-promotes curated personal-memory predicates. The allow-list
covers:

- preferences
- identity and profile facts
- active projects
- goals
- corrections
- skills
- relationships
- tools or technologies the user uses
- likes
- work facts

Unsupported predicates can still exist as claims, but they do not become active
facts through high-level memory insertion.

## Conflict And Clarification Flow

```mermaid
sequenceDiagram
    participant Host
    participant DM as Dense-Mem
    participant User

    Host->>DM: remember(candidate typed memory)
    DM->>DM: verify + gate + compare active facts
    DM-->>Host: clarifications[]
    Host->>User: ask which memory is correct
    User-->>Host: answer
    Host->>DM: confirm_memory(decision)
    DM-->>Host: promoted or rejected result
```

Comparable conflicts include facts with the same profile, subject, predicate,
and comparable object space. Dense-Mem must not infer which one the user meant.

## Embedding Ownership

Normal write and recall paths accept text. Dense-Mem embeds:

- fragment content
- imported memory summaries
- recall queries

Client-supplied vectors remain limited to advanced semantic search. The server
preserves model and dimension consistency checks so vector indexes are not mixed
across incompatible providers.

## Profile Isolation

Every memory operation is scoped to the authenticated API key's profile. MCP and
header-scoped HTTP routes ignore caller-supplied `profile_id` values. Path-scoped
profile-management routes still include the profile ID in the path where the
existing API contract requires it.

Graph nodes and relationships carry profile scope, SQL records are profile-bound,
and Redis keys use profile-aware prefixes.

## HTTP MCP Transport

Dense-Mem implements MCP Streamable HTTP at one endpoint:

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/mcp` | JSON-RPC requests and notifications; JSON or SSE response. |
| `GET` | `/mcp` | Server-to-client SSE stream where supported. |

Security requirements:

- Authenticate with bearer API keys.
- Require control portal tokens for browser-accessible administrative surfaces.
- Let operators choose administrative bind addresses and network exposure.
- Keep the server-owned MCP transport HTTP-first. The optional stdio proxy under
  `packages/mcp-proxy` is a local adapter for clients that cannot load
  Streamable HTTP MCP servers directly; it is not a separate Dense-Mem server
  transport. Publish it to npm before relying on `npx dense-mem-mcp-proxy` as
  a release install path.

## Local Control Portal

The portal is intentionally narrow:

```text
web/
  src/
    App.tsx
    api.ts
    styles.css
  tests/
    control-portal.spec.ts
```

It manages profiles and API keys only. It does not browse or mutate memory,
facts, claims, graph nodes, or database internals.

Runtime controls:

| Variable | Default | Meaning |
|----------|---------|---------|
| `CONTROL_HTTP_ADDR` | `:8090` | Portal bind address. |
| `CONTROL_PORTAL_TOKEN` | empty | Required bearer or `X-Control-Portal-Token` token. |

The server rejects missing and invalid control portal tokens. Network exposure is
an operator deployment decision.

## User Knowledge Portal

The API-key user portal is served by the main API process at `/ui`. It accepts
normal Dense-Mem API keys and is intentionally narrower than the control portal:
it can view the authenticated team's knowledge, show only the current key
profile, and regenerate only that current key when it has `write` scope.

It cannot update team metadata, create keys, list other team profiles, rotate
other keys, or rename the current profile.

## Operational Notes

- Redis is optional for single-node use.
- Redis is required for multi-instance deployments because rate-limit counters
  and SSE stream concurrency must be shared.
- API keys are shown in plaintext only once when created.
- Embedding provider traffic is data egress when the provider is hosted outside
  the operator boundary.
- The tool registry is the source of truth for MCP, HTTP tool catalog, and
  OpenAPI discoverability.
