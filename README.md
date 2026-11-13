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
and returns eligible Evidence with provenance and direct topic follow-up.
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
  <img src="assets/readme-hero.jpg" alt="AI clients submit evidence to a governed memory service, which records lifecycle and returns Evidence with provenance." />
</p>

## Why Dense-Mem

- Evidence is exact, durable, and append-only. A lifecycle action changes its
  effective state without deleting provenance or trace lineage.
- Exact Evidence, occurrences, and source revisions retain their provenance.
  Owner-authorized supersession and retraction append lifecycle history.
- Recall selects eligible Evidence directly. Topics organize shared Evidence
  without creating factual claims or expanding descendant memberships.

Authentication resolves one immutable actor as `team + identity + membership +
permanent owner alias + optional credential`. An SSO browser session uses the
selected membership's permanent owner alias and has no direct credential. An
API-key request carries a credential whose stable ID is also its permanent
owner alias. Team, identity, membership, and credential fields never let a
client choose or replace the semantic owner.

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
```

The base stack uses PostgreSQL with pgvector as the only durable authority.
The v2.6.2 release requires the compatible cutover marker created by the
stopped-service migration; it has no legacy database runtime or fallback. The
local ports are:

```text
MCP:            http://127.0.0.1:8080/mcp
User portal:    http://127.0.0.1:8080/ui
Control portal: http://127.0.0.1:8090/
```

Open the control portal with `CONTROL_PORTAL_TOKEN`, create a team from **All teams**,
then open **Credentials** for its first API key. The sidebar groups team work,
operations, and administration; its team picker changes the active workspace.
The user portal offers **Search memory**, **Ontology**, and the management pages
allowed by the session. **Connect** shows the team ID, MCP endpoint, and memory
access. Both portals support System, Light, and Dark appearance, collapsible
navigation, and section links that survive refresh and browser history.
For control-plane automation, use the same private
API:

```bash
control_token="<CONTROL_PORTAL_TOKEN from .env>"

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"primary-memory"}'

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams/<team-id>/credentials \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"default credential"}'
```

Operational diagnostics use one process logger for console and the private
control portal. `LOG_LEVEL` accepts `trace`, `debug`, `info`, `warn`, `error`,
or `fatal` and defaults to `trace`; `fatal` records severity and does not stop
the process. PostgreSQL slow-query logging uses
`POSTGRES_SLOW_QUERY_THRESHOLD_MS`, which defaults to 200 and must be a
positive value. The operation-log sink is required for readiness and reports
gaps and recovery when it is unavailable. Startup and the OAuth compatibility
harness use console-only logging until a PostgreSQL operation-log sink is
available; they do not claim that pre-attachment events were persisted.

The control portal's Logs view filters by severity, correlation ID, request
hash, Remember attempt, execution/replay/conflict classification, and retry
eligibility. A team's Remember diagnostics open on admitted calls and link to
canonical attempts. Capture bodies are loaded only for an authorized detail
view and are labelled as observed preparation, write, disconnect, or unknown
receipt; a captured response does not prove delivery to the caller. Existing
seven-day capture expiry, legal holds, erasure, and bounded unavailable or
truncated states remain in force.

The release image contains one project executable, `/app/server`. It applies
pending PostgreSQL migrations under a database session lock before serving, so
the Compose stack does not need a separate migration container. Multiple server
replicas that share one writable primary serialize this startup step. Keep
ordinary rolling-deployment migrations backward compatible with the previous app
version. The v2.6.1 synchronous Remember migration remains the stopped-service
boundary; v2.6.2 evidence-first activation runs after it. For the v2.6.1
migration, stop every server instance, take the required PostgreSQL snapshot,
and apply the stopped-service boundary before starting the new binary. Independent databases
must each be migrated by a server connected to that database. Administration
stays on the private control portal/API, while ontology maintenance and automatic conflict
review run as server background workers.

Migrations have no overall deadline by default
(`POSTGRES_MIGRATION_TIMEOUT_SECONDS=0`). Set positive seconds, up to 86400, to
choose a limit. Process shutdown still cancels migration, and progress is logged
every 10 seconds. The server remains unready until migration completes. The image
healthcheck has a 30-minute start period and becomes active after its first
success; for larger migrations, extend the deployment's startup/healthcheck grace
and rollout deadlines so the orchestrator does not restart or abandon the server
while it is still migrating.

Release candidates use `vX.Y.Z-rc.N` and `demo-vX.Y.Z-rc.N`. Stable releases use
`vX.Y.Z`, `latest`, and `demo-vX.Y.Z`; there is no rolling demo tag.

The server requires complete embedding and verifier configuration at
startup: `AI_API_URL`, `AI_API_KEY`, `AI_API_EMBEDDING_MODEL`,
`AI_API_EMBEDDING_DIMENSIONS`, and `AI_VERIFIER_MODEL`.
The compose examples provide OpenAI defaults for embeddings; choose the chat
models explicitly in `.env`.
`AI_API_EMBEDDING_MAX_BATCH_ITEMS` limits texts per provider HTTP request. It
defaults to 256, matching the application batch limit; set it to 100 for
Cloudflare Workers AI BGE-M3. One application batch still returns one ordered
result, and a failed request fails the whole batch.
Successful chunks stay private within the call; transient failures retry only
the failed chunk, sharing three retries across the complete batch. Search
reconciliation selects at most `min(256, AI_API_EMBEDDING_MAX_BATCH_ITEMS)`
documents per hourly pass and retains its 10-second provider deadline.

For bounded live BGE-M3 measurements, set `CLOUDFLARE_ACCOUNT_ID` and
`CLOUDFLARE_API_TOKEN` in the environment and run:

```bash
mkdir -p tmp/embedding-measurements
DENSE_MEM_CLOUDFLARE_BATCH_MEASUREMENT=1 \
  DENSE_MEM_CLOUDFLARE_MEASUREMENT_OUTPUT=tmp/embedding-measurements/batches.jsonl \
  go test ./internal/embedding -run '^TestCloudflareBatchMeasurement$' -count=1 -v
```

Use a new output filename for each run. The harness measures five repetitions
each of 100, 200, and 256 documents, then compares failed-chunk and prior
whole-batch retries with injected later-chunk 429/503 responses. Each batch has
a 60-second measurement bound; the output records whether it exceeds the
reconciliation deadline, HTTP attempts, resent documents, and reported input
tokens. Neurons are calculated from reported tokens using the published BGE-M3
rate of 1075 per million input tokens; they are not measured billing usage.
Missing provider usage remains unavailable.

`AI_REMEMBER_MODEL` and `AI_CONFLICT_REVIEW_MODEL` are optional
overrides for their existing AI sessions. An unset or whitespace-only override
uses `AI_VERIFIER_MODEL`; a configured model failure is returned without trying
the fallback model.

Verifier and assessor calls send `temperature: 0` by default. Set
`AI_VERIFIER_DISABLE_TEMPERATURE=true` to omit the field for providers or models
that reject temperature.

### Optional typed decisions

Configure an independent decision provider to enable evidence deduplication,
security review, conflict review, and recall reranking automatically:

```dotenv
AI_DECISION_PROVIDER=openai
AI_DECISION_API_URL=https://api.openai.com/v1
AI_DECISION_API_KEY=<private-credential>
AI_DECISION_MODEL=gpt-6-luna
AI_DECISION_MAX_INPUT_TOKENS=32000
AI_DECISION_TIMEOUT_SECONDS=10
AI_DECISION_MAX_CONCURRENCY=4
```

The 32k input setting is a conservative deployment limit. OpenAI's Decisions
endpoint supports up to 400 questions per request; its endpoint-specific token
ceiling is unconfirmed. The general model window is not an endpoint guarantee.
For Jev, use `AI_DECISION_PROVIDER=typesafe`,
`AI_DECISION_API_URL=https://api.typesafe.ai/v1`, and pinned
`AI_DECISION_MODEL=jev-1.14.0`. Set request and question limits separately with
`AI_DECISION_MAX_INPUT_TOKENS=64000` and
`AI_DECISION_MAX_QUESTION_INPUT_TOKENS=32000`. Jev's explicit
`AI_DECISION_LONG_CONTEXT_ENABLED=true` opt-in raises both allowed ceilings to
262000, with documented latency and accuracy trade-offs. Input counts use the
configured verifier tokenizer as estimates; reported usage is recorded separately.

An unset provider or `AI_DECISION_PROVIDER=disabled` retains existing behavior.
Enabled providers require complete independent credentials and limits; decision
credentials never inherit from generation. Under **Settings → Decisions**, save
individual task switches in PostgreSQL. Missing preferences default to on after
a successful settings read; explicit off survives restarts and provider changes.
There is no workload environment variable. Changes reach new operations through
the five-second settings refresh; running operations retain one revision. Refresh
failures retain the last valid snapshot marked stale, and required work fails if
no valid snapshot exists. The private control listener exposes strict
`GET`/`PATCH /control/api/config/decisions` using the existing configuration item
format and `{ "data": ... }` envelope. It never returns provider credentials.

Decisions replace only confident bounded tasks. Deduplication requires exactly
one equivalence probability ≥0.99 with every alternative ≤0.01; all ≤0.01 means
new evidence. Security passes only when every hazard probability is ≤0.01.
Conflict selection requires both position and sufficiently-supported
probabilities ≥0.99. Uncertainty retains complete generative review and exact
security spans. Generative extraction and other semantic fields remain required.
Required decision failures are visible and never silently invoke generation.
Recall scores hydrated authorized shortlists of at most 50 Evidence items
per space, outside transactions; optional failures retain the
original ranking with an explicit degradation. Existing temporal, grouping, and
cross-space fusion rules still apply. Reranking adds provider work; no live speed
or quality improvement is claimed.

Set `LOG_LEVEL=debug` for bounded, credential-protected payloads, question IDs,
criteria, authorized references, distributions, thresholds, and ranking changes.
Info records task actions, uncertainty routing, settings revisions, usage, and
durations; warnings identify stale settings, rate limiting, overload, refusals, context
rejection, missing usage, and optional degradation. Errors identify required
provider, configuration, validation, and persistence failures. Correlation and
operation IDs connect task, exchange, routing, and existing commit logs. Logging
works with telemetry disabled. Diagnose recorded operations and exchanges; this
feature adds no probe endpoints or diagnostic model calls.

See [ADR 0015](adr/0015-optional-typed-decisions.md) and the
[validation record](tests/eval/decision-providers.md). Live OpenAI comparison is
deferred by maintainer direction because credentials are unavailable; Jev live
quality and latency remain pending access. HTTP fixtures do not establish model
accuracy or performance.

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
  exceed the default 60-second timeout while they load. Remember runs
  synchronously within the request deadline; use the idempotency key to retry
  after a client timeout or transient provider failure.

## Private and Team Memory

Every API key has its own private space, including keys issued by an SSO user.
An SSO user's browser sessions and OAuth tokens share that user's private space
within the selected team. Keys and SSO sessions have separate private memory.

The calling LLM must supply `target_space: "private"` or
`target_space: "team_shared"` on every `remember` request. The target applies to
the entire submission and participates in its idempotency hash. There is no
default or redirection after a denied shared write. Authentication determines
the actual space, team, and owner; clients cannot supply a destination ID.

Team-read-only keys can remember, correct, retract, and erase their own private
memory and use feature-gated private `ingest_session`. Shared mutations require
current write permission and ownership of the affected records. Both portals
show effective `memory_access.private.{read,write}` and
`memory_access.team_shared.{read,write}`. Supplied `memory_binding` fields are
rejected, including null and empty values.

The public contract is `dense-mem.v2.8.0`. Refresh `tools/list` after upgrading.
Retired fields fail validation before provider work. Legacy Remember operation
keys retain their original identity and outcome and return non-retryable
`retired_contract` guidance.

### Upgrade Existing Private History

Server startup automatically upgrades existing private history before starting
HTTP listeners or workers. No separate migration command, preflight approval, or
maintenance-confirmation flag is required. Use the matching binary and existing
environment configuration; keep a verified restorable backup before upgrading.
Do not run an older binary across this incompatible schema cutover.

Startup installs the migration boundary, validates immutable writer attribution,
captures the source fingerprint, moves private rows in bounded resumable batches,
and finishes the schema migration. Concurrent starters serialize the data move;
ordinary writes stay gated until completion. An interrupted upgrade resumes on
the next startup using its retained fingerprint and progress.

Ambiguous ownership or invalid cross-space references still block startup with
an error; automation never guesses an owner. Inspect those blockers with the
optional read-only preflight, which starts no server and applies no migrations:

```bash
go run ./cmd/server private-memory-preflight
```

The content-free report includes `source_fingerprint`, ownership mappings, and
blockers. Resolve attribution problems without guessing ownership, then restart
the matching server. An optional `private-memory-migrate` command uses the same
automatic migration path without starting the server; `--source-fingerprint`
can constrain it to an inspected snapshot. Historical `--maintenance-confirmed`
arguments remain accepted for compatibility but are not required.

Catalog and constraint changes use maintenance locks; allow disk and WAL
capacity for necessary supporting copies and search rebuilding. Verify readiness,
isolation, and effective capabilities before restoring traffic.

Migration preserves shared history and splits private records by immutable
writer. Necessary support copies retain provenance without adding independent
corroboration; old handles resolve only within the authorized destination and
generation. Mixed context is sealed, projections are rebuilt, and unfinished
session checkpoints are recomputed from the original intake.

Erasing a writer's private memory also purges its migration copies in other
private spaces and invalidates dependent memories. Applicable legal holds block
physical purge. Unaffected destination records survive with a new generation,
which fences stale jobs; erased operation keys cannot restore content.

Before accepting new writes, rollback requires restoring the verified backup
and matching application/schema together with all instances stopped. After new
writes, use forward recovery. Down does not discard history or restore legacy
bindings. Deploying the new server also runs its required data migrations.

## Evidence Lifecycle

`remember` accepts exact Evidence and provenance under `dense-mem.v2.8.0`.
Choose an explicit authenticated destination for the whole batch. Each item is
bounded to 999 Unicode code points. Retired graph proposal fields fail
validation before provider work.

```json
{
  "target_space": "team_shared",
  "idempotency_key": "deployment-source-20261110",
  "evidence": [{
    "content": "Dense-Mem stores durable memory in PostgreSQL.",
    "source_type": "manual",
    "source": "Deployment decision"
  }]
}
```

Remember durably stages exact Evidence and processing intent before assessment.
Complete security, equivalence, and Evidence-conflict decisions are validated
before required embeddings. Evidence, occurrences, source revisions, lifecycle,
search documents, and the terminal receipt commit atomically. A failed required
provider call or cancellation cannot partially accept the batch. Initial
security rejection remains content-free in diagnostics.

Retain the complete request and operation key. Replay the unchanged request to
reconcile a timeout; a changed request with the same key conflicts. Operational
failures retain typed retry guidance. Stored context never becomes new submitted
Evidence merely because assessment or Recall used it.

For replacement, supply `supersedes_evidence_ids` on the new Evidence item.
Alternatively advance a source revision with `previous_source_revision`; do not
combine these mechanisms. Replacement retires its owned current targets only
inside successful atomic acceptance. `retract_evidence` accepts owned IDs, a
bounded reason, and an idempotency key. Both append history; they preserve exact
content and provenance. Shared reads do not grant mutation authority.

Feature-gated `ingest_session` accepts exact user-authored events into private
memory. `session_evidence_excerpts_v2` selects exact current-event excerpts with
original Unicode offsets. Prior context is read-only. Empty extraction returns
an explicit `memory_outcome: "no_memory"` terminal result. Event identity,
request hashing, replay, cancellation, and generation fences remain enforced.

## Recall, topics, Trace, and export

`recall_memory` retrieves eligible Evidence directly with bounded exact context,
equivalence IDs, explicit search states, conflicts, and degradation reporting.
`known_evidence_ids` suppress already known eligible results.

`related_topics` supplies topic identity, name, description, eligible unseen
Evidence count, and rank. `topics_truncated` reports omitted hints. `topic_limit`
defaults to three and accepts 0–10. Follow up with `topic_ids`: 1–10 unique UUIDs
select the union of current direct memberships without descendant expansion.
Evidence lifecycle, time, freshness, ownership, and known exclusions apply before
selection and counting. Missing, foreign, and stale topics produce the same
bounded `topic_unavailable` error for explicit filters. Ordinary Recall reports
optional topic degradation. Topic metadata remains current during temporal reads.

Ontology organizes only team-shared Evidence through topics, equivalence, and
manager overrides. Private memory is excluded. Existing enable, pause, disable,
concurrency, and generation fences remain authoritative. Pausing preserves
eligible publications; disabling removes organization from Recall. Topic parent
links are organizational metadata and do not establish factual support.

`trace_memory` takes an `evidence_id`. It returns bounded Evidence decisions,
occurrences, source revisions, and lifecycle. `include_evidence_content` and
`include_transitions` control detail. Defaults are 100 events and 2,000 content
runes; caps are 500 and 8,000.

`export_memory_pack` accepts a name and 1–500 Evidence IDs. Its
`dense-mem.memory-pack.v3.0` artifact preserves provenance, canonical hashes,
content bounds, and explicit omissions. Authorization applies to every target.

## Python Core Client

The [Python package](packages/python) uses the official MCP SDK 1.x and supports
Python 3.11–3.14. It provides async/sync clients, Google ADK ingestion, and a
LangChain retriever using the same Evidence-only schemas. See its README for complete
requests, terminal handling, and unchanged-request replay.

## MCP Tool Catalog

Discover closed schemas with `tools/list`. The registry applies identical scope,
feature, version, and visibility checks to `tools/call`.

| Tool | Contract |
| --- | --- |
| `remember` | Exact Evidence and provenance to an explicit private or shared destination. |
| `ingest_session` | Feature-gated exact event excerpts into authenticated private memory. |
| `retract_evidence` | Owner-authorized retirement with append-only lineage. |
| `recall_memory` | Eligible Evidence, topic hints, and direct-topic follow-up. |
| `trace_memory` | Evidence decisions, occurrences, revisions, and lifecycle. |
| `submit_recall_session_feedback` | Conditional bounded voluntary Recall feedback. |
| `export_memory_pack` | Bounded Evidence/provenance export. |

The evaluation build adds `eval_list_knowledge_refs` and `eval_run_recall_case`.
Production builds cannot register them through configuration. Factual graph,
Dream, and community contracts are retired from both profiles.

Before upgrading, stop incompatible instances and verify a matching restorable
backup. Normal startup runs the ordered Evidence-only migration before workers
or listeners. It preserves Evidence fingerprints and scoped operation identity,
retires graph-specific data and mixed payloads, and blocks on legal holds or
unexpected dependencies. No provider calls occur during migration. Before
commit, errors roll back. After destructive commit, restore the matching backup;
after new writes, recover forward unless separately approved. Never restart an
old binary over the migrated database.

The production log default remains TRACE. DEBUG includes protected assessment
exchanges, exact admitted fields, topic selection reasons, and authorized
Trace/export limits and omissions. WARN reports optional topic degradation and
retired contracts; INFO records session resume and migration milestones; ERROR
records blocked or failed migrations. Capture complete stdout operation windows
before teardown and verify 2,048-byte chunks, hashes, and counts. Existing
16 MiB provider-body, 64 MiB session-envelope, and seven-day diagnostic retention
bounds remain. Missing, truncated, or interrupted capture is explicit.

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

Prometheus telemetry is optional and off by default. To collect HTTP, embedding,
verifier, assessor, recall feedback, Remember, cost, and Evidence lifecycle telemetry, start the base stack with the telemetry
overlay:

```bash
export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 32)"
docker compose \
  -f examples/docker-compose.base.yml \
  -f examples/docker-compose.telemetry.yml \
  -f examples/docker-compose.grafana.yml \
  up -d
```

The telemetry overlay starts Prometheus at `127.0.0.1:9090`, and the optional
Grafana overlay starts Grafana at `127.0.0.1:3000`. It provisions the Prometheus
datasource and dashboards from `examples/grafana/`; use the Grafana time picker
for chart ranges and the `Rolling totals` selector for counters and canonical
ledger windows. Select the Prometheus datasource and bounded scrape job in
Grafana; dashboard JSON contains no credentials or installation-specific IDs.

Existing scoped telemetry metrics retain team and profile labels for the
first-party API. Grafana aggregates them into system-wide values without
selecting or grouping by those labels. New operational metrics add no identity
labels, and metric labels contain no evidence or request text. Missing provider
usage or pricing stays no-data and is called out separately from a real zero.
Ledger collector failure suppresses its gauges and exposes collector health.
Counter measures aggregate across instances and preserve the telemetry service's
sparse first-sample behavior; range rates use Grafana's query step. Durable
ledger gauges use `max` across replicas and never use `rate` or `increase`. The operator dashboards are system-wide
because canonical lifecycle measures carry no team or credential dimensions.

The control Metrics and user Usage dashboards were removed after real Grafana
and Prometheus parity validation. Team-overview request summaries, private
diagnostics, settings, and control telemetry remain. See [`examples/grafana/README.md`](examples/grafana/README.md)
for provisioning and the series map.

### Grafana series map

`internal/operations/telemetry_catalog.go` defines the retained control
telemetry series and their availability rules. Grafana panel descriptions map
to these identifiers. The metric names and labels remain available to operators.

| Dashboard series | Prometheus source | Owner | Zero and failure meaning |
| --- | --- | --- | --- |
| `http_requests`, `http_errors`, `http_rps`, `http_errors_rps`, `avg_http_latency` | `densemem_http_requests_total`, `densemem_http_request_duration_seconds` | `internal/http/middleware` and `internal/observability` | A successful scrape with no requests is zero; HTTP error and latency series require request activity. |
| `embedding_requests`, `embedding_errors`, `embedding_tokens`, `avg_embedding_latency` | `densemem_embedding_requests_total`, `densemem_embedding_errors_total`, `densemem_embedding_tokens_total`, `densemem_embedding_duration_seconds` | Embedding provider instrumentation in `internal/observability` | Missing provider usage stays unavailable when embedding calls occurred. |
| `verifier_requests`, `verifier_tokens`, `avg_verifier_latency` | `densemem_verifier_requests_total`, `densemem_verifier_tokens_total`, `densemem_verifier_duration_seconds` | Assessor and provider instrumentation in `internal/observability` | Missing provider usage stays unavailable when verifier calls occurred. |
| `recalls`, `avg_recall_results`, `p95_recall_latency`, `recall_results`, `recall_p95_latency` | `densemem_recall_requests_total`, `densemem_recall_results`, `densemem_recall_duration_seconds` | `internal/recall` | Results and latency are unavailable until a Recall request provides a sample. |
| `llm_recall_used_rate`, `llm_recall_answer_supported_rate`, `llm_recall_quality_score`, `llm_recall_missing_context_rate`, `llm_recall_irrelevant_rate` | `densemem_recall_feedback_total`, `densemem_recall_feedback_quality_score` | `internal/recall/feedback.go` | No host feedback is unavailable, not a negative judgment. `llm_recall_feedback_events` is an internal parent activity series. |
| `remember_requests`, `avg_remember_duration`, `p95_remember_duration` | `densemem_remember_acknowledgements_total`, `densemem_remember_acknowledgement_duration_seconds` | `internal/remember` | A successful scrape with no acknowledgements is zero. |
| `assessor_requests`, `assessor_request_failures`, `assessor_validation_failures`, `assessor_tokens`, `avg_assessor_duration`, `assessor_duration`, `assessor_terminal_failures` | `densemem_assessor_requests_total`, `densemem_assessor_validation_failures_total`, `densemem_assessor_tokens_total`, `densemem_assessor_duration_seconds`, `densemem_assessor_terminal_failures_total` | Integrated assessor instrumentation | Token usage is unavailable when the provider does not report usage. |
| `ai_cost_usd`, `verifier_cost_usd`, `embedding_cost_usd` | `densemem_ai_operation_cost_usd_total`, `densemem_ai_operation_unpriced_total` | `internal/observability/telemetry_cost.go` | Missing usage, pricing, or rate configuration remains unavailable, not zero cost. |

Additional operational families are `densemem_mcp_transport_requests_total`,
`densemem_mcp_transport_duration_seconds`, `densemem_mcp_tool_results_total`,
`densemem_logical_operation_attempts_total`,
`densemem_logical_operation_duration_seconds`,
`densemem_logical_operation_recoveries_total`,
`densemem_remember_phase_duration_seconds`,
`densemem_operation_provider_tokens_total`, and
`densemem_operation_provider_usage_unpriced_total`. They distinguish MCP HTTP
status from logical tool outcome, Remember execution/replay/conflict/recovery,
bounded phase and provider usage, and canonical ledger state. New families use closed labels without
team, profile, request, model, or content values. Their histogram buckets
include the 180-second Remember budget and larger overruns. The canonical ledger collector reports
`densemem_operational_ledger_collection_success`; a zero collection status
means its other gauge families are omitted for that scrape. It uses a
two-second read-only collection deadline.

To compare baseline and candidate overhead, run
[`compare_telemetry_load.py`](tests/eval/scripts/compare_telemetry_load.py)
against isolated deployments with matching data and configuration. It warms
both servers, sends paired authenticated `tools/list` requests, and scrapes
both metrics endpoints during the measured run. It fails when candidate p95
latency or throughput regresses by more than 10 percent. Set a shared
`DENSE_MEM_LOAD_TOKEN` or the per-deployment
`DENSE_MEM_BASELINE_LOAD_TOKEN` and `DENSE_MEM_CANDIDATE_LOAD_TOKEN`; set
`DENSE_MEM_LOAD_SCRAPE_TOKEN` for both metrics endpoints. Use the optional
`DENSE_MEM_BASELINE_SCRAPE_TOKEN` and `DENSE_MEM_CANDIDATE_SCRAPE_TOKEN` when
the two scrape endpoints use different credentials. The JSON receipt is
written under the ignored evaluation runtime directory.

## Responsibility Boundary

| Area | Dense-Mem owns | Host LLM owns |
|------|----------------|---------------|
| Evidence | Exact staging, provenance, lifecycle, and owner checks | Choosing what source material to submit |
| Assessment | Complete validation, deterministic policy, and atomic acceptance | Selecting exact Evidence and provenance |
| Recall | Eligible Evidence and topic hints | Selecting what to cite or ask in the conversation |
| Corrections | Authorized supersession, retraction, and append-only lineage | Deciding whether a correction is warranted |
| Operations | Teams, memberships, credentials, API keys, audit, and portals | MCP client configuration |

## Data Egress and Consistency

Dense-Mem can send evidence text, proposal context, and recall queries to the
configured embedding and verifier providers. Self-hosted providers keep that
traffic within your boundary; hosted providers do not. Embeddings are derived,
versioned state and cannot overwrite newer sources. Startup checks prevent
mixing incompatible embedding models or dimensions.

## Remote development tests

Commit and push a worktree branch, then open **Actions → Development E2E → Run
workflow**. Select `main` for the workflow and enter the branch or full commit
SHA in `source_ref`. This route requires repository-admin permission and works
before a pull request exists.

Enter registered scenario names in `scenarios`, separated by commas, or `all`
for the entire scenario registry. Leave it blank for PostgreSQL-only testing.
For `postgres_shards`, enter `0` through `5`, a comma-separated subset, or `all`;
leave it blank for E2E-only testing. Select at least one group. The scenario
named `full` runs Evidence lifecycle, telemetry, and portal coverage; `all` runs
every registered scenario. Available names live in
[`scripts/e2e-scenarios.json`](scripts/e2e-scenarios.json).

```bash
gh workflow run development-e2e.yml --ref main \
  -f source_ref=issue/my-change \
  -f scenarios=conflict,mcp_oauth \
  -f postgres_shards=all
```

The workflow resolves one commit, builds one native production image in the
dedicated `dense-mem-e2e` GHCR package, and gives every selected job the same
image digest and source SHA. E2E and PostgreSQL groups run independently, with
eight concurrent scenarios and six PostgreSQL shards per run. The backlog
regression has a dedicated shard; shard selections cover every registered case
exactly once. Execution jobs are capped at 30 minutes, with bounded test time
before cleanup. Production validation starts scenarios after the rootless
controller check while PostgreSQL shards continue independently, and its final
gate requires every group to pass. The Actions
summary records the exact SHA, digest, selections, and results. Inspect the
matching **Clean development E2E images** run as well: it removes eligible owned
images after completion, including failures and cancellations. GitHub prevents
deleting a package's final version, so cleanup retains that verified completed
image and reports its version, digest, and run identity. An hourly sweep recovers
missed cleanup and preserves active runs or unexpected image ownership.

Start a fresh dispatch for retries. Each privileged job rechecks the acting
accounts and rejects job reruns, which can reuse cached authorization or an
image that has already been deleted.
Successful remote results can satisfy the corresponding focused E2E and real
PostgreSQL checks for their exact commit. Keep lightweight local checks and
attach the run URL and summary to the PR. Every PR still requires its current-head
full production-image E2E gate through the ordinary validation route.

New manual workflows become dispatchable after their workflow files reach the
default branch. Live rollout must verify E2E-only, PostgreSQL-only, combined,
failed, and cancelled runs together with GHCR cleanup.

Shared CI runs repository/browser checks, Go tests and analysis, and core and
PostgreSQL coverage producers independently. Its required merge gate verifies
that all seven raw Go profiles belong to the same source commit and workflow
attempt, then applies the unchanged deduplicated coverage threshold above 90%.
The local `scripts/coverage-report.sh --complete` command still runs both parts
and the merge. PR validation and post-merge CI use the same shared workflow.

Repository admins can benchmark changes to CI/E2E workflows before merge using
`production-image-e2e.yml` on their own current PR branch with `pr_number`.
The manual trial requires the trusted preview receipt and runs candidate shared
CI alongside every database shard and registered scenario. It does not replace
the normal required PR checks. Record the exact source SHA, image digest,
workflow attempt, preview-build duration, queue and execution times, and cleanup
results; accept the latency change only when all execution jobs finish below
30 minutes and the trial plus preview-build allowance finishes within 60 minutes.

## Fork pull-request validation

Public fork PRs use contributor-owned Cloudflare credentials for production
E2E. The upstream repository never forwards its Cloudflare token to a fork.

1. In your public fork, enable Actions and configure the repository variable
   `CLOUDFLARE_ACCOUNT_ID` and secret `CLOUDFLARE_API_TOKEN`. The token needs
   Workers AI access. Keep the token out of workflow inputs and PR comments.
2. Ask a repository administrator to apply `deploy-test-image` to the upstream
   PR. After the preview finishes, its comment contains the approved source
   SHA, image digest, preview run ID/attempt, and trusted upstream revision.
3. In your fork's Actions page, manually run **Contributor fork E2E request**
   on the branch at that exact PR head. Enter the upstream PR number,
   `source_sha`, `preview_run_id`, `preview_run_attempt`, and `trusted_revision`
   from the current preview receipt. The workflow must already be present on
   your fork's default branch for GitHub to offer manual dispatch.
4. Keep the PR head unchanged while all 24 scenarios, three PostgreSQL
   prechecks, rootless-controller checks, and cleanup finish. The upstream
   verifier waits up to 120 minutes and automatically verifies the signed
   receipt before reporting `Production image E2E` success.

The trusted upstream workflow signs in a separate job that runs no candidate
code and receives no Cloudflare secret. Verification binds the signer revision,
PR head, image digest, both run attempts, and complete results. Missing
credentials, failed/skipped jobs, forged signatures, stale approvals, and
timeouts fail validation. If the head or approved upstream workflow changes,
request a fresh administrator-approved preview and start its matching run.
Same-repository PRs retain the ordinary production E2E route.

Issue #508's live public-fork acceptance run is deferred by maintainer direction.
Signed-fixture, receipt-policy, and workflow tests cover the bridge locally and
in CI; they do not establish that live fork acceptance has completed.

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

### Evidence-only migration progress

The Evidence-only upgrade emits structured `evidence_only_migration_progress`
events before listeners start. At `LOG_LEVEL=info` or more verbose (the default
is `trace`), collect one execution from the server console:

```bash
docker compose -f examples/docker-compose.base.yml logs --no-color --no-log-prefix server |
  jq -Rc --arg run "$MIGRATION_EXECUTION_ID" \
    'fromjson? | select(.migration_execution_id == $run)'
```

Each observation identifies the phase, table or work unit, phase start and
elapsed time, metric and unit, completed/total/remaining counts, and the last
meaningful progress time. The ten-second heartbeat is liveness; it never
advances that progress time. A long SQL statement can have a known table and an
unknown row-progress count until it finishes. `counted` is an exact count,
`affected` is PostgreSQL's command row count (not a measurement of scanned rows
or changed values), `matched` counts hold matches, and `verified` counts retained
fingerprints only after the equality check succeeds. Fingerprint-copy command
counts use `affected` rows; they do not claim canonical Evidence was rewritten. Table completion counts table work units, including checked absent
tables. No catalog estimate is represented as an exact count. Dependency closure
reports discovered records and iterations; its remaining work is unknown until
convergence. ETA is unavailable because the remaining phases have no stable cost
model.

All phase counts describe attempted transaction-local work. Only
`evidence_only_migration_completed` with `transaction_outcome=committed` and its
verified receipt confirms durable results. A validation or hold failure identifies
the bounded reason and failing phase. `rolled_back` requires PostgreSQL to report the exact migration transaction
aborted. In-progress completion is observed for at most five seconds; another
starter's commit cannot replace this attempt's outcome. `unconfirmed` means outcome
verification failed or the state could not be reconciled. Do not infer success
from the SQL stream finishing. Already-applied or concurrently completed upgrades
emit an explicit skipped event with no newly attempted or committed migration
work; `previous_commit_verified` describes their existing receipt.

`evidence_only_migration_wait` records the current phase, wait type/event and
blocker count without query text. The migration session lock can be waiting
between PostgreSQL lock probes. A wait event explains a stall; it does not
authorize terminating its blockers. Repetitive progress is coalesced to at most
once per second; phase/table boundaries and final counts are retained.
`evidence_only_migration_capture_incomplete` and `capture_reason` identify missing,
invalid, oversized or interrupted notices and unavailable wait observations.
`wait_observation_unavailable=true` separately retains wait-probe degradation
without replacing an existing notice-capture reason. The first failed wait sample
emits a warning with a bounded `wait_observation_failure_reason`; repeated failed
samples do not repeat that warning. The flag remains on subsequent progress and
final records, including when wait sampling recovers. Investigate both notice
decoding and the classified observation failure when both causes are present.
A committed migration can have incomplete diagnostic capture. Preserve the log
and investigate the stated capture reason rather than treating missing counts
as zero. `collection_queries` counts additional client round trips; notice-helper
SQL executes inside the original batches and is represented by `sql_notices`
and measured elapsed overhead. WARN-level filtering excludes INFO progress; keep INFO or more verbose
when collecting migration evidence. These events contain no memory text or row
identifiers and use the existing credential-protected root logger.
