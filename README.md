<h1 align="center">Dense-Mem</h1>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Dense--Mem-trustworthy_AI_memory-0f766e?style=for-the-badge&logo=github&logoColor=white" alt="Dense-Mem" />
</p>

<p align="center">
  <strong>Self-hosted memory for AI agents that preserves evidence, detects conflicts, and never silently rewrites facts.</strong>
</p>

<p align="center">
  <a href="https://demo-dense-mem.markhuang.ai"><img src="https://img.shields.io/badge/Try%20Dense--Mem%20live-Open%20hosted%20demo-0f766e?style=for-the-badge" alt="Try Dense-Mem live" /></a>
</p>

<p align="center">
  <strong>Create a temporary isolated team and test Dense-Mem before self-hosting.</strong>
</p>

<p align="center">
  <a href="https://github.com/markhuangai/dense-mem"><img src="https://img.shields.io/github/stars/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub stars" /></a>
  <a href="https://github.com/markhuangai/dense-mem/issues"><img src="https://img.shields.io/github/issues/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub issues" /></a>
  <a href="https://github.com/markhuangai/dense-mem/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-Apache--2.0-blue?style=flat-square" alt="License: Apache-2.0" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.26" />
  <a href="https://github.com/markhuangai/dense-mem/pkgs/container/dense-mem"><img src="https://img.shields.io/badge/Docker-GHCR-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker image on GHCR" /></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/MCP-Streamable_HTTP-111827?style=flat-square" alt="MCP Streamable HTTP" />
  <img src="https://img.shields.io/badge/Neo4j-5.26-008CC1?style=flat-square&logo=neo4j&logoColor=white" alt="Neo4j 5.26" />
  <img src="https://img.shields.io/badge/PostgreSQL-18-4169E1?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL 18" />
  <img src="https://img.shields.io/badge/OpenAPI-3.0-6BA539?style=flat-square&logo=openapiinitiative&logoColor=white" alt="OpenAPI 3.0" />
  <img src="https://visitor-badge.laobi.icu/badge?page_id=markhuangai.dense-mem&style=flat-square" alt="Visitors" />
</p>

<p align="center">
  <a href="https://zenodo.org/records/20519039"><img src="https://zenodo.org/badge/DOI/10.5281/zenodo.20519039.svg" alt="DOI: 10.5281/zenodo.20519039" /></a>
</p>

Dense-Mem gives MCP clients a durable memory layer with provenance, typed claims
and facts, verification gates, server-side embeddings, recall, team isolation,
REST/OpenAPI, and a token-protected control portal. The host LLM owns
conversation and judgment; Dense-Mem owns durable memory state and returns
structured outcomes the host can explain to users.

Under the hood, Dense-Mem is a standalone HTTP MCP memory server. HTTP MCP is
the v1 supported MCP transport and is served at `/mcp` from the main HTTP
process.

Dense-Mem is part of the research preprint
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/20519039).

## Project Intro

<p align="center">
  <a href="https://cdn.markhuang.ai/videos/dense-mem/intro.mp4" target="_blank" rel="noopener noreferrer">
    <img src="assets/thumbnail.png" alt="Watch the Dense-Mem intro video" width="100%" />
  </a>
</p>

<p align="center">
  <a href="https://cdn.markhuang.ai/videos/dense-mem/intro.mp4" target="_blank" rel="noopener noreferrer"><strong>Watch the Dense-Mem intro video</strong></a>
</p>

## Try the Hosted Demo

Create a temporary isolated team at
[https://demo-dense-mem.markhuang.ai](https://demo-dense-mem.markhuang.ai) and
test Dense-Mem before self-hosting.

<p align="center">
  <img src="assets/readme-hero.jpg" alt="Cartoon architecture illustration: AI clients send evidence into a secure Dense-Mem vault where claims become facts, conflicts become clarification questions, and durable storage sits behind the service." />
</p>

## Why Dense-Mem?

AI agents need memory that can be trusted later, not only text that can be
retrieved later.

- Evidence is first-class. Memories start as source fragments before they become
  claims or facts.
- Facts pass through typed claims, verification, and promotion gates.
- Comparable conflicts become `clarifications[]`; Dense-Mem does not silently
  overwrite active facts.
- The host LLM stays responsible for extracting candidates and asking the user
  questions. Dense-Mem stays responsible for durable state, gates, audit
  metadata, and recall.
- Operators keep control of storage, team/profile isolation, API keys, and data
  egress boundaries.

## 60-Second Quickstart

Download the base local-only compose example and env template, set the required
secrets, and start Dense-Mem:

```bash
mkdir dense-mem-local
cd dense-mem-local

curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.base.yml
curl -fsSLo .env.example \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/.env.example

cp .env.example .env
# Fill in POSTGRES_PASSWORD, NEO4J_PASSWORD, CONTROL_PORTAL_TOKEN, and AI_API_KEY.
${EDITOR:-vi} .env

docker compose up -d
docker compose exec server /app/provision-team --name "primary-memory"
```

The base compose example provisions Postgres, `neo4j:5.26-community` with the
Neo4j Graph Data Science plugin, and the Dense-Mem server. It exposes only local
host ports:

```text
MCP/API:        http://127.0.0.1:8080/mcp
User portal:    http://127.0.0.1:8080/ui
Control portal: http://127.0.0.1:8090/
```

Cold image pulls can take longer than 60 seconds. Redis and public HTTPS are
intentionally omitted from the base example; use the expert example when you
need those deployment options.

The server requires a complete embedding configuration at startup:
`AI_API_URL`, `AI_API_KEY`, `AI_API_EMBEDDING_MODEL`, and
`AI_API_EMBEDDING_DIMENSIONS`. The compose examples provide OpenAI defaults for
the URL, model, and dimensions (`https://api.openai.com/v1`,
`text-embedding-3-small`, `1536`), so the minimal local setup only needs you to
fill in `AI_API_KEY`. Override those values together when using a different
embedding provider or model.

Verifier calls send `temperature: 0` by default. Set
`AI_VERIFIER_DISABLE_TEMPERATURE=true` to omit the field for providers or models
that reject temperature.

### Telemetry Overlay

Prometheus telemetry is optional and off by default. To collect usage,
performance, verifier token, embedding token, recall, and promotion metrics for
the `/ui` app and control portal dashboards, run the base stack with the
telemetry overlay:

```bash
curl -fsSLo prometheus.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.yml
curl -fsSLo docker-compose.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d
```

The overlay starts Prometheus on `127.0.0.1:9090`, retains 30 days of samples,
passes `TELEMETRY_SCRAPE_TOKEN` to Prometheus as a scrape secret, and points
Dense-Mem at `http://prometheus:9090` for telemetry queries. It also sets
`TELEMETRY_PROMETHEUS_JOB=dense-mem` so dashboards query only the `dense-mem`
scrape job when Prometheus is shared.

Online recall-quality cards use `densemem_recall_feedback_total` and
`densemem_recall_feedback_quality_score`. They stay at zero until
recall feedback is enabled from the control portal config panel and a host LLM
submits feedback for `recall_memory` results. Feedback only omits
`feedback_comment` when quality is `high` with no negative flags; medium, low,
or flagged feedback includes a bounded comment and can include irrelevant result
refs for offline analysis. Prometheus still receives only bounded labels; the
free-text comment stays in the recall feedback investigation records. Normal
production recall traffic still contributes request volume, result count, and
latency.

For the disposable demo image, keep the control portal disabled and use the
demo telemetry overlay instead:

```bash
curl -fsSLo prometheus.demo.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.demo.yml
curl -fsSLo docker-compose.demo.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.demo.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.demo.telemetry.yml up -d
```

The demo overlay scrapes the demo service at `demo:8091` on the private Compose
network and sets `TELEMETRY_PROMETHEUS_JOB=dense-mem-demo`. Do not publish that
metrics listener publicly.

## Compare

| Capability | Dense-Mem | File memory | Vector DB | Generic MCP memory |
|------------|-----------|-------------|-----------|--------------------|
| Evidence provenance | Source fragments are stored before claims or facts | Usually absent or informal | Stores chunks, not truth history | Varies by implementation |
| Fact changes | Verification gates and promotion rules | Manual edits | Similarity updates can obscure history | Often tool-specific |
| Conflict handling | Comparable conflicts return clarification tasks | Caller must notice | Similar vectors do not mean contradiction | Usually caller-managed |
| Recall | Facts, claims, fragments, contradictions, and clarifications | Text search | Vector similarity | Varies |
| Agent boundary | Host LLM judges; Dense-Mem stores and enforces | Blurred | Retrieval only | Often blurred |
| Operations | Teams, profiles, API keys, audit metadata, REST, OpenAPI, MCP | Minimal | Database operations | Varies |

Redis is optional for single-node deployments and required for multi-instance
deployments.

## Documentation

The README is the product overview. The full user documentation lives in the
[Dense-Mem wiki](https://github.com/markhuangai/dense-mem/wiki):

| Goal | Wiki page |
|------|-----------|
| Run Dense-Mem locally | [Quick Start](https://github.com/markhuangai/dense-mem/wiki/Quick-Start) |
| Use memory day to day | [Using Dense-Mem](https://github.com/markhuangai/dense-mem/wiki/Using-Dense-Mem) |
| Configure providers, Redis, and Traefik | [Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) |
| Understand the system design | [Architecture](https://github.com/markhuangai/dense-mem/wiki/Architecture) |
| Review API and operations details | [Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference) |

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

## Memory Workflow

| Tool | Purpose |
|------|---------|
| `remember` | Normal chat-session memory insertion. Saves evidence, creates typed claims, verifies, promotes when gates pass, and returns structured outcomes. |
| `import_memories` | Ingests summarized historical conversations. By default it records evidence and validated claims without auto-promotion. |
| `recall_memory` | Retrieves facts, validated claims, fragments, and `clarifications[]` for the authenticated team. |
| `trace_memory` | Expands one fact or claim into bounded evidence, promotion lineage, contradictions, and supersession links. |
| `assemble_context` | Builds a bounded prompt-ready context block plus structured facts, claims, fragments, and clarifications. |
| `reflect_memories` | Reviews active facts, candidate or disputed claims, contradictions, stale memories, and clarification needs. |
| `confirm_memory` | Applies the user's answer to a clarification task, either accepting a claim and superseding comparable active facts or keeping/rejecting it. |
| `find_memory_pack_candidates` | Finds facts and validated claims that can be exported into a portable memory pack. |
| `export_memory_pack` | Exports selected memory into canonical JSON with a SHA-256 integrity hash for review or sharing. |
| `inspect_memory_pack` | Parses a memory-pack artifact or URL and reports duplicates, conflicts, and required decisions without writing memory. |
| `import_memory_pack` | Imports a reviewed or trusted memory pack with ledgered changes and rollback support. |
| `rollback_memory_pack_import` | Rolls back changes from a prior memory-pack import when the ledger has enough state. |

Low-level tools remain available for advanced callers: `post_claim`,
`verify_claim`, `promote_claim`, search tools, graph query tools, community
tools, and retraction tools.

The older `*_skill_pack*` tool names remain accepted as hidden compatibility
aliases, but new clients should use `*_memory_pack*`. Dense-Mem also exposes MCP
prompts through `prompts/list` and `prompts/get`; the first bundled prompt,
`export_memory_as_agent_skill`, guides an LLM to recall Dense-Mem experience and
draft a self-contained, shareable Agent Skill `SKILL.md` file for recipients
without access to the source memory instance, and without relying on memory-pack
import/export.

Memory moves through this path:

```text
source fragment -> typed claim -> verification -> promotion gate -> active fact
                                                   |
                                                   v
                                            clarification task
```

Comparable conflicts are not resolved silently. Dense-Mem returns
`clarifications[]`, and the host LLM asks the user which memory is correct. After
the user answers, the host calls `confirm_memory`.

## Data Egress

Dense-Mem forwards fragment text and recall queries to the configured embedding
provider. Claim verification can send candidate claims and supporting evidence to
the configured verifier provider. Self-hosted providers keep that traffic inside
your boundary; hosted providers do not. See the wiki
[Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) and
[Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference)
for provider settings and egress details.

## Embedding Model Consistency

Dense-Mem owns embeddings for normal writes and recall. It checks the stored
embedding model and dimension on startup so vectors from incompatible models are
not mixed silently. Rotation requires re-embedding or rebuilding vector indexes;
the step-by-step process belongs in the wiki
[Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration).

## Tool Discoverability

Dense-Mem exposes three discoverability surfaces backed by one registry:

| Surface | Path | Purpose |
|---------|------|---------|
| Tool catalog | `GET /api/v1/tools` | Runtime tool discovery |
| Runtime OpenAPI | `GET /api/v1/openapi.json` | Agents, codegen, integrations |
| MCP Streamable HTTP | `POST /mcp`, `GET /mcp` | MCP clients over the main HTTP service, including tools and bundled prompts |

The full route list and client examples live in the wiki
[Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference)
and [Quick Start](https://github.com/markhuangai/dense-mem/wiki/Quick-Start).

## Design Notes

- [standalone MCP memory architecture](https://github.com/markhuangai/dense-mem/wiki/Standalone-MCP-Memory-Architecture)
- [knowledge-pipeline contracts](https://github.com/markhuangai/dense-mem/wiki/Knowledge-Pipeline-Contracts)
- [knowledge-pipeline client contracts](https://github.com/markhuangai/dense-mem/wiki/Knowledge-Pipeline-Client-Contracts)
- [knowledge-pipeline operability](https://github.com/markhuangai/dense-mem/wiki/Knowledge-Pipeline-Operability)

## License

Apache-2.0
