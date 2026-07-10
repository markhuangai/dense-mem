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

Dense-Mem gives MCP clients a durable semantic memory layer with raw evidence,
granular entities, open-vocabulary relationships, independent review and
verification, server-side embeddings, bounded graph recall, team isolation,
REST/OpenAPI, and inspectable lifecycle telemetry. The host LLM proposes the
smallest useful knowledge units; Dense-Mem validates, stores, promotes, rejects,
quarantines, and returns structured outcomes the host can explain to users.

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

- Evidence is first-class. Every assertion retains exact Unicode evidence spans
  back to source fragments.
- Semantic edges use validated, open relationship types such as `WORKS_ON`,
  `DEMOED`, or `USES`; they are not reduced to fixed `SUBJECT` and `OBJECT`
  relationships. `SUPPORTED_BY` and `MENTIONS` remain provenance links, not the
  semantic meaning of a memory.
- An AI reviewer can split a client proposal into smaller atomic relationships;
  an independent verifier then decides whether each relation is contradicted,
  retained as a candidate, validated, or eligible for fact promotion.
- Comparable current-state assertions follow explicit lifecycle policies and
  are never silently overwritten.
- The host LLM extracts candidates and asks the user about review tasks.
  Dense-Mem owns durable state, gates, audit metadata, bounded traversal, and
  exact lifecycle telemetry.
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

The user portal includes recall, facts, claims, fragments, communities, dreams,
and a bounded graph explorer. Its default overview shows every node type and
lifecycle state in the authenticated team, including granular entity/value
nodes and direct semantic edges. Local exploration remains depth- and
result-bounded with visited-node deduplication, so cycles do not cause an
unbounded read. The graph endpoint is read-scoped, not raw Cypher.

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

### Fully Local Setup (Ollama)

Dense-Mem also runs with no hosted AI provider at all. Any OpenAI-compatible
endpoint can serve embeddings and verification; with [Ollama](https://ollama.com)
on the Docker host, pull the two models once and point `.env` at them:

```bash
ollama pull nomic-embed-text
ollama pull llama3.1:8b
```

```text
AI_API_URL=http://host.docker.internal:11434/v1
AI_API_KEY=ollama
AI_API_EMBEDDING_MODEL=nomic-embed-text
AI_API_EMBEDDING_DIMENSIONS=768
AI_VERIFIER_MODEL=llama3.1:8b
AI_VERIFIER_TIMEOUT_SECONDS=300
```

Three details matter on this path:

- Use `host.docker.internal`, not `127.0.0.1`; the server calls the provider
  from inside the compose network. The base compose example maps that name to
  the Docker host so it also works on Linux, where Docker does not define it
  by default.
- `AI_API_KEY` must be non-empty even though Ollama ignores it; startup
  validation requires a complete embedding configuration.
- Change `AI_VERIFIER_MODEL` together with the embedding settings. The default
  is `gpt-4o-mini`, which does not exist on Ollama. Startup still succeeds;
  the failure only appears later, at claim-verification time. Pick a model
  that answers within the verifier timeout on your hardware. A 7B-8B class
  model verifies comfortably on a laptop; larger models can exceed the
  default 60-second timeout while they load, leaving claims parked as
  `candidate_claim` with the error recorded in the placement item.

### Your First Memory

`remember` accepts raw evidence and an atomic graph proposal, then returns an
`ingest_id` immediately. Evidence spans use zero-based Unicode code-point
offsets (`start` inclusive, `end` exclusive):

```json
{
  "evidence": [{
    "content": "Mark works on Dense-Mem, which uses Neo4j and PostgreSQL.",
    "source_type": "conversation",
    "source_group": "conversation:demo"
  }],
  "proposal": {
    "entities": [
      {"ref": "mark", "name": "Mark", "type": "person"},
      {"ref": "dense-mem", "name": "Dense-Mem", "type": "project"},
      {"ref": "neo4j", "name": "Neo4j", "type": "technology"},
      {"ref": "postgresql", "name": "PostgreSQL", "type": "technology"}
    ],
    "relationships": [
      {
        "proposal_id": "mark-works-on-dense-mem",
        "subject_ref": "mark",
        "predicate": "works_on",
        "object_ref": "dense-mem",
        "policy_family": "versioned",
        "polarity": "+",
        "modality": "assertion",
        "evidence": [{"evidence_index": 0, "start": 0, "end": 57}]
      }
    ]
  }
}
```

The reviewer may return more atomic relationships than the client proposed.
Poll `get_memory_placement` with the `ingest_id` to inspect every item:

| Category | Meaning |
|----------|---------|
| `assertion_fact` | Independent entailment plus trusted authority or two independent source groups satisfied the fact gate. |
| `assertion_validated` | The relation was entailed but has not met the fact gate. |
| `assertion_candidate` | Evidence is insufficient for validation; the relation remains discoverable at lower authority. |
| `assertion_needs_review` | Identity, scope, conflict, or a legacy decomposition requires user confirmation. |
| `assertion_quarantined` | Evidence matched the prompt-injection safety filter and is excluded from model-backed reads. |
| `assertion_rejected` | The independent verifier contradicted the proposed relationship. |

Use `resolve_memory_placement` only after showing the placement question to the
user. `accept` and `reject` resolve a review item; `correct` submits new evidence
and a replacement proposal through the same reviewer and verifier. Repeated
independent evidence can promote a validated assertion to a fact without
rewriting its provenance.

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
free-text comment stays in the recall feedback investigation records. When
related dreams are returned, feedback can also include bounded `dream_feedback`
judgments without promoting or rejecting the dream automatically. Confirmed true
or false dreams should be resolved through `resolve_dream_feedback`, which
records dream-specific telemetry and routes the confirmation evidence through
normal memory placement. Normal production recall traffic still contributes
request volume, result count, and latency.

Promotion, validation, rejection, review, quarantine, correction, and reversal
cards are computed from the append-only PostgreSQL assertion-transition ledger.
Those window totals and rates are exact and remain tenant/profile scoped.
Prometheus assertion counters provide operational time series; they are not
used as a second, potentially divergent source for the exact lifecycle cards.

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

The closest architectural peer is Graphiti, not a chunk-only vector store.
Mem0's current open-source algorithm removed its external graph-store path in
favor of vector, BM25, and entity-linking score fusion; relationships are no
longer returned as a directly traversable graph. See Mem0's official
[migration note](https://docs.mem0.ai/platform/features/graph-memory). Graphiti
builds temporal entity/relationship graphs from source episodes and supports
hybrid semantic, keyword, and graph retrieval. See the official
[Graphiti project](https://github.com/getzep/graphiti).

| Capability | Dense-Mem semantic edge V2 | Mem0 OSS current | Graphiti |
|------------|----------------------------|------------------|----------|
| Durable unit | Evidence fragment plus atomic assertion | Extracted memory/fact in a vector collection | Episode-derived entity and relationship graph |
| Relationships | Open Neo4j relationship types, returned as typed paths and bounded frontier hints | Entity links influence ranking; direct graph relations are not exposed by the current OSS algorithm | Temporal fact edges between entity nodes |
| Truth lifecycle | Candidate → validated claim → fact, with reject, quarantine, review, correction, reversal, and supersession events | Add-only extraction with hash deduplication in the current OSS algorithm | Temporal invalidation preserves prior fact history |
| Verification | Separate graph reviewer and independent verifier; fact gate requires authority or independent source groups | Extraction/ranking pipeline; no equivalent Dense-Mem fact gate documented | LLM extraction and temporal conflict handling |
| Provenance | Exact evidence spans plus `SUPPORTED_BY`/`MENTIONS` and an append-only transition ledger | Original memory metadata | Every fact traces to source episodes |
| Governance | Team/profile API keys, manager-only migration, user-confirmed review tasks, exact scoped telemetry | Identifier-scoped memory and application-managed policy | Flexible OSS graph core; surrounding governance is application-managed |
| Recall | Vector + keyword + graph assertions; typed paths; topic-selectable, cycle-safe frontier | Semantic candidates reranked with BM25/entity signals | Semantic + keyword + graph traversal with temporal queries |

### Design risks

| Failure mode | Mitigation in Dense-Mem |
|--------------|-------------------------|
| Over-extraction creates noisy entities and an edge explosion. | Atomic proposal limits, independent review, canonical entity resolution, duplicate IDs, lifecycle states, and bounded recall keep noise inspectable instead of treating every edge as fact. |
| Two model outputs based on one source falsely look like independent support. | Promotion counts stable `source_group` values, not model calls; two readers of one document remain one source. |
| Dense or cyclic graphs cause loops, latency, or irrelevant context. | Recall returns ranked one-hop frontier hints; local graph and trace operations enforce depth/result limits and visited-node deduplication. |
| A malicious memory instructs the reviewer or leaks a credential. | Evidence is treated as untrusted data, injection-shaped submissions are quarantined before model execution, and graph display redaction is an additional best-effort safeguard. |

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
| Memory writes | Evidence fragments, independent graph review, verification, lifecycle policy, promotion | Submitting evidence plus the smallest entity/relationship proposal it can support |
| Embeddings | Fragment embeddings and recall-query embeddings through the configured provider | No vectors for normal writes or recall |
| Retrieval | Active semantic assertions, bounded typed paths/frontiers, legacy memory, and review tasks | Choosing which returned relationship frontier to follow or cite |
| Truth changes | Policy-aware supersession, transition ledger, and user-authorized resolution | Asking the user whether an uncertain relation is true, false, or needs correction |
| Operations | Teams, named profiles, API keys, audit metadata, control portal | Client-side MCP configuration |

Dense-Mem is not an agent brain, planner, or external truth arbiter. It stores
memory, applies explicit gates, and returns structured outcomes.

## Memory Workflow

| Tool | Purpose |
|------|---------|
| `remember` | Stores evidence plus granular entity and atomic open-relationship proposals; returns an asynchronous placement run. Manager keys may attach `migration_refs` for legacy decomposition. |
| `get_memory_placement` | Polls each independently reviewed relation, including candidate, validated, fact, review, quarantine, and rejection outcomes. |
| `resolve_memory_placement` | Acknowledges a completed run or applies a user-confirmed `accept`, `reject`, or evidence-backed `correct` decision. |
| `recall_memory` | Retrieves active semantic assertions with typed paths and bounded next-hop frontier hints, plus legacy facts, claims, fragments, and hypothesis-only dreams. |
| `resolve_dream_feedback` | Records dream-specific decisions. `ignore` leaves the dream for future recall, while confirmed true or false dreams enter normal memory placement and are removed from future dream recall. |
| `trace_memory` | Expands an assertion, fact, or claim through a bounded, cycle-safe evidence and relationship trace. |
| `assemble_context` | Builds bounded prompt-ready context from semantic assertions and legacy memory. |
| `find_memory_pack_candidates` | Finds facts and validated claims that can be exported into a portable memory pack. |
| `export_memory_pack` | Exports selected memory into canonical JSON with a SHA-256 integrity hash for review or sharing. |
| `inspect_memory_pack` | Parses a memory-pack artifact or URL and reports duplicates, conflicts, and required decisions without writing memory. |
| `import_memory_pack` | Imports a reviewed or trusted memory pack with ledgered changes and rollback support. |
| `rollback_memory_pack_import` | Rolls back changes from a prior memory-pack import when the ledger has enough state. |

Direct client tools for claim/fact promotion, raw fragment mutation, raw
keyword/vector/graph search, community detection, and retractions are not part
of the public client surface. Dense-Mem keeps the underlying logic server-side
for verifier, recall, migration, and maintenance flows.

The older `*_skill_pack*` tool names remain accepted as hidden compatibility
aliases, but new clients should use `*_memory_pack*`. Dense-Mem also exposes MCP
prompts through `prompts/list` and `prompts/get`; the first bundled prompt,
`export_memory_as_agent_skill`, guides an LLM to recall Dense-Mem experience and
draft a self-contained, shareable Agent Skill `SKILL.md` file for recipients
without access to the source memory instance, and without relying on memory-pack
import/export.

### Retired memory tools

The V2 workflow removes legacy tools from MCP discovery and the tool catalog.
Authenticated direct HTTP calls receive `410 Gone` with migration guidance:

| Retired tool | Replacement |
|--------------|-------------|
| `dispute_memory_placement` | Read the run with `get_memory_placement`, confirm with the user, then call `resolve_memory_placement` with `correct` and new evidence when needed. |
| `import_memories` | Call `remember` with V2 evidence/proposal fields. For existing project memory, a manager key also supplies `migration_refs`; the decomposed bundle stays inactive until the user accepts it. |
| `reflect_memories` | Use `recall_memory`, `trace_memory`, and the detailed team graph view. |
| `confirm_memory` | Use `resolve_memory_placement` with the relevant `placement_item_id` and the user's decision. |

Memory moves through this path:

```text
evidence + atomic proposal
          |
          v
 independent graph reviewer --split/normalize--> entity -[OPEN_RELATION]-> entity/value
          |                                                   |
          v                                                   v
 independent verifier ------------------------------> candidate / validated / fact
          |                                                   |
          +------------------------------------------> reject / quarantine / user review
```

Comparable conflicts and ambiguous identities are not resolved silently.
Dense-Mem returns placement review tasks, the host LLM asks the user, and the
host calls `resolve_memory_placement` with the confirmed decision.

## Data Egress

Dense-Mem forwards fragment text and recall queries to the configured embedding
provider. Claim verification can send candidate claims and supporting evidence to
the configured verifier provider. Self-hosted providers keep that traffic inside
your boundary; hosted providers do not. See the wiki
[Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) and
[Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference)
for provider settings and egress details.

The graph UI applies best-effort regex redaction for common secret shapes and
never returns embeddings. That display safeguard is not a DLP system: do not
submit API keys, passwords, tokens, or other credentials as memory content.

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
