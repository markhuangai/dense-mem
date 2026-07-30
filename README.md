<h1 align="center">Dense-Mem</h1>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Dense--Mem-governed_AI_memory-0f766e?style=for-the-badge&logo=github&logoColor=white" alt="Dense-Mem" />
</p>

<p align="center">
  <strong>Self-hosted MCP memory with durable evidence, explicit lifecycle, and support-gated recall.</strong>
</p>

<p align="center">
  <a href="https://demo-dense-mem.markhuang.ai"><img src="https://img.shields.io/badge/Try%20Dense--Mem%20live-Open%20hosted%20demo-0f766e?style=for-the-badge" alt="Try Dense-Mem live" /></a>
</p>

<p align="center">
  <a href="https://github.com/markhuangai/dense-mem"><img src="https://img.shields.io/github/stars/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub stars" /></a>
  <a href="https://github.com/markhuangai/dense-mem/issues"><img src="https://img.shields.io/github/issues/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub issues" /></a>
  <a href="https://github.com/markhuangai/dense-mem/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-Apache--2.0-blue?style=flat-square" alt="License: Apache-2.0" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.26" />
  <a href="https://github.com/markhuangai/dense-mem/pkgs/container/dense-mem"><img src="https://img.shields.io/badge/Docker-GHCR-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker image on GHCR" /></a>
</p>

Dense-Mem is a standalone HTTP MCP memory server using Streamable HTTP. It
stages exact evidence, derives semantic state through validated server policy,
and returns active evidence contexts with graph-shaped Relationship handles.
PostgreSQL is the durable authority for knowledge, lifecycle, provenance,
search, authorization, and audit; Redis is coordination only. A single-node
deployment may use process-local coordination; a multi-instance deployment
requires Redis or an equivalent distributed coordination implementation.

The host LLM owns conversation and judgment. Dense-Mem owns durable evidence,
owner authorization, lifecycle events, support eligibility, and bounded recall.
The external memory automation contract is MCP at `/mcp`; browser routes are
first-party interfaces, not an alternative public automation API.

Dense-Mem is part of the research preprint
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/21403316).

## Try the Hosted Demo

Create a temporary isolated team at
[https://demo-dense-mem.markhuang.ai](https://demo-dense-mem.markhuang.ai) to
test disposable data before self-hosting.

<p align="center">
  <img src="assets/readme-hero.jpg" alt="AI clients submit evidence to a governed memory service, which records lifecycle and returns active relationships with provenance." />
</p>

## Why Dense-Mem

- Evidence is exact, durable, and append-only. A lifecycle action changes its
  effective state without deleting provenance or trace lineage.
- Entity and typed Value are semantic nodes. Profile-owned Relationships become
  active graph edges only when their evidence support is eligible.
- Provider output is a proposal. Closed-schema validation and deterministic
  server policy decide durable state.
- Default recall excludes candidates and Hypotheses and returns evidence only
  when its active Relationship support path is eligible for the requested time.
- Team visibility and profile mutation authority are distinct. An author can
  change only their own evidence or owned semantic records.

## 60-Second Quickstart

Download the local compose example and environment template, configure the
required secrets, and start Dense-Mem:

```bash
mkdir dense-mem-local
cd dense-mem-local

curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.base.yml
curl -fsSLo .env.example \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/.env.example

cp .env.example .env
# Fill in POSTGRES_PASSWORD, CONTROL_PORTAL_TOKEN, and AI_API_KEY.
${EDITOR:-vi} .env

docker compose up -d
docker compose exec server /app/provision-team --name "primary-memory"
```

The base stack uses PostgreSQL with pgvector as the durable authority. Leave
`NEO4J_*` unset for normal operation; a legacy Neo4j corpus is migration input,
not a runtime fallback. The local ports are:

```text
MCP:            http://127.0.0.1:8080/mcp
User portal:    http://127.0.0.1:8080/ui
Control portal: http://127.0.0.1:8090/
```

The server requires complete embedding and verifier configuration at
startup: `AI_API_URL`, `AI_API_KEY`, `AI_API_EMBEDDING_MODEL`,
`AI_API_EMBEDDING_DIMENSIONS`, and `AI_VERIFIER_MODEL`.
The compose examples provide OpenAI defaults for embeddings; choose the chat
models explicitly in `.env`.

Verifier and assessor calls send `temperature: 0` by default. Set
`AI_VERIFIER_DISABLE_TEMPERATURE=true` to omit the field for providers or models
that reject temperature.

### Fully Local Setup (Ollama)

Any OpenAI-compatible endpoint can provide embeddings and verification. With
[Ollama](https://ollama.com) running on the Docker host:

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

Use `host.docker.internal`, not `127.0.0.1`, because the server calls the
provider from the compose network. `AI_API_KEY` must remain non-empty because
startup validation requires a complete provider configuration.

- Set `AI_VERIFIER_MODEL` to a model that exists on the selected chat endpoint.
  Startup validates the model configuration before the service accepts memory
  writes. A 7B-8B class model works for local smoke tests; larger models can
  exceed the default 60-second timeout while they load, leaving placement
  attempts retryable until the model responds.

## Evidence Lifecycle

`remember` durably stages exact evidence and returns an `ingest_id`; provider
calls and placement happen after acknowledgement. Poll `get_memory_placement`
for the authoritative processing state.

To replace a specific current evidence item you own, put its UUID in the new
item's `supersedes_evidence_ids`. Direct targeting is separate from advancing a
source revision with `previous_source_revision`; do not combine them.

```json
{
  "evidence": [
    {
      "content": "The deployment target is now PostgreSQL only.",
      "source_type": "manual",
      "supersedes_evidence_ids": ["<owned-current-evidence-uuid>"],
      "idempotency_key": "deployment-target-correction-20260729"
    }
  ]
}
```

The target is retired atomically when the replacement is accepted for intake,
even if later placement is rejected or quarantined. This preserves the exact
correction decision instead of silently leaving stale evidence effective.

To retract evidence without a replacement, call `retract_evidence` with owned
current IDs, a bounded reason, and an idempotency key:

```json
{
  "evidence_ids": ["<owned-current-evidence-uuid>"],
  "reason": "The source was withdrawn.",
  "idempotency_key": "withdrawn-source-20260729"
}
```

Both operations append lifecycle events. They never physically delete evidence
or trace lineage. Current recall excludes retired evidence, while a historical
`known_at` view before the event can still show what the system knew then.

## Recall and Graph State

`recall_memory` is evidence-first but support-path gated. Its `results[]`
contain evidence contexts only after final hydration proves an active,
query-relevant Relationship support path remains eligible for the requested
`valid_at` and `known_at` view. Related Relationships, communities, and
Hypotheses are separate bounded fields; candidates and Hypotheses are not
default memory results.

```text
remember evidence (+ optional Entity/Relationship proposals)
        |
        v
durable staging -> validated placement -> active eligible Relationships
        |                                      |
        +-- lifecycle event -------------------+
                                               |
                                               v
                         support-gated evidence recall and trace lineage
```

## MCP Tool Catalog

The active contract is `dense-mem.v2.4`. Discover the authorized catalog with
MCP `tools/list`; the server applies the same scope, feature, and visibility
checks to `tools/call`.

| Tool | Purpose |
|------|---------|
| `remember` | Submit exact evidence and optional Entity/Relationship proposal hints for server-owned placement. |
| `get_memory_placement` | Poll a placement run. |
| `retract_evidence` | Retract caller-owned evidence while preserving append-only provenance. |
| `resolve_memory_placement` | Resolve placement review items through append-only evidence decisions. |
| `correct_entity_resolution` | Dry-run or apply caller-owned Entity merge and split corrections. |
| `recall_memory` | Recall active evidence contexts and Relationship handles. |
| `trace_memory` | Trace one same-team Relationship through evidence, decisions, and lineage. |
| `submit_recall_session_feedback` | Record bounded session-level recall quality feedback. |
| `list_dreams` | List reviewable Hypotheses without treating them as memory. |
| `get_dream` | Fetch one authorized Hypothesis and its source references. |
| `resolve_dream_feedback` | Resolve Hypothesis feedback without using the Hypothesis as evidence. |
| `find_memory_pack_candidates` | Find active Relationships that may be exported. |
| `export_memory_pack` | Export selected active Relationships with support provenance. |
| `inspect_memory_pack` | Inspect a memory-pack artifact without writing durable state. |
| `import_memory_pack` | Import a reviewed memory pack through normal evidence placement. |
| `rollback_memory_pack_import` | Roll back an import when no selected state changed. |

Memory-pack writers emit `dense-mem.memory-pack.v2.4`; strict readers preserve
support for prior v2.3 and v1 artifacts after validating their original hashes.

## Supported HTTP Surfaces

| Surface | Path | Intended use |
|---------|------|--------------|
| Streamable HTTP MCP | `GET /mcp`, `POST /mcp` | Supported external memory integration contract. |
| User portal | `/ui` and `/ui/api/*` | First-party browser interface. |
| Control portal | `/control/api/*` | Private or dedicated administrative ingress. |
| Health | `/health`, `/ready` | Container liveness and readiness checks. |

There is no supported public REST memory API. Do not automate browser routes or
depend on retired `/api/v1` paths.

## Telemetry Overlay

Prometheus telemetry is optional and off by default. To collect HTTP,
embedding, verifier, recall, feedback, and conflict-review metrics for the
first-party dashboards, start the base stack with the overlay:

```bash
curl -fsSLo prometheus.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.yml
curl -fsSLo docker-compose.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d
```

The overlay starts Prometheus on `127.0.0.1:9090` and scopes dashboard queries
to `TELEMETRY_PROMETHEUS_JOB=dense-mem`. Free-text recall-feedback comments stay
in bounded investigation records; Prometheus receives only bounded labels.

## Responsibility Boundary

| Area | Dense-Mem owns | Host LLM owns |
|------|----------------|---------------|
| Evidence | Exact staging, provenance, lifecycle, and owner checks | Choosing what source material to submit |
| Semantic state | Validation, deterministic policy, support eligibility | Proposing optional Entity/Relationship hints |
| Recall | Active evidence contexts and Relationship handles | Selecting what to cite or ask in the conversation |
| Corrections | Authorized supersession, retraction, and append-only lineage | Deciding whether a correction is warranted |
| Operations | Teams, profiles, API keys, audit, and portals | MCP client configuration |

## Data Egress and Consistency

Dense-Mem can send evidence text, proposal context, and recall queries to the
configured embedding and verifier providers. Self-hosted providers keep that
traffic within your boundary; hosted providers do not. Embeddings are derived,
versioned state and cannot overwrite newer sources. Startup checks prevent
mixing incompatible embedding models or dimensions.

## Documentation

| Goal | Wiki page |
|------|-----------|
| Run Dense-Mem locally | [Quick Start](https://github.com/markhuangai/dense-mem/wiki/Quick-Start) |
| Use evidence lifecycle and recall | [Using Dense-Mem](https://github.com/markhuangai/dense-mem/wiki/Using-Dense-Mem) |
| Configure providers, Redis, and ingress | [Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) |
| Understand the design | [Architecture](https://github.com/markhuangai/dense-mem/wiki/Architecture) |
| Review MCP and portal routes | [Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference) |

## License

Apache-2.0
