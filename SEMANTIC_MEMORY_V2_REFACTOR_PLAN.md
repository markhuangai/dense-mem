# Semantic Memory v2 Refactor and Evaluation Plan

Status: branch-local plan and implementation tracker. This file does not update
the wiki and does not authorize commits, pushes, deployments, or data deletion.

## What

Refactor this branch away from the unreleased assertion/Neo4j direction and
toward the v2 semantic-memory design:

- v2 durable semantic truth is PostgreSQL/pgvector.
- Neo4j is not required for v2 boot, remember, recall, trace, or evaluation.
- Redis remains optional coordination infrastructure, not semantic storage.
- The public production tool path is MCP Streamable HTTP at `/mcp`.
- `remember` writes evidence and placement state, then the placement worker
  stores semantic evidence, entities, relationships, supports, and lifecycle
  events in PostgreSQL.
- `recall_memory` returns relationships by default and evidence only when
  explicitly requested.
- `trace_memory` traces a relationship to supports and evidence.
- No quality gate blocks the first comparison report; the report should expose
  raw data so the next design direction can be chosen from evidence.

## Why

The branch had drifted toward a branch-only assertion model with Neo4j in the
normal runtime. That is the wrong direction for v2. The useful proof is not
whether the refactor looks cleaner; it is whether the same public RAG seed,
remember path, recall path, model/embedding config, and scoring harness produce
better or worse retrieval behavior versus `main`.

## Current state

| Area | Current branch state |
| --- | --- |
| Runtime storage | Postgres/pgvector is the v2 semantic store; Neo4j is disabled by default. |
| Postgres topology | Boot validates the connected database is writable primary-like; configured read DSNs and unsupported topology settings stop startup. |
| Schema | Branch-only assertion migrations are replaced by semantic relationship/evidence migrations. |
| Remember | Production remember stores evidence, placement runs/items, semantic relationships, supports, and relationship IDs on placement items. |
| Recall | Production recall uses semantic repository relationship/evidence search. |
| Trace/context | Production context service traces semantic relationships and assembles relationship/evidence context. |
| Public route | Server registers MCP handlers for production tool access; direct REST tool execution is not the production public surface. |
| Evaluation | Eval runner supports `--tool-transport mcp`; monitor defaults to MCP and records transport in runtime identity. |
| Docs in repo | README/examples/eval docs describe Postgres-only v2 defaults and MCP eval. |
| Wiki | Not touched in this branch. Any needed wiki updates are reported to the user. |

## Before / after

| Concern | Wrong branch direction | Target v2 branch |
| --- | --- | --- |
| Durable truth | Neo4j `Assertion` records | PostgreSQL `semantic_relationship_records` |
| Tiers | candidate / validated / fact / dream | candidate / validated_claim / fact; hypotheses are separate |
| Normal boot | Requires Neo4j | Requires writable Postgres primary; no Neo4j |
| Graph | Runtime Neo4j traversal | Postgres-derived relationships and bounded trace/context |
| Verifier | Partial assertion-centric flow | Closed semantic verifier boundary with whole-response validation |
| Eval path | REST/v2 assertion scripts | Production MCP `remember` + production MCP recall |
| Gate | Strict quality pass/fail | Report raw comparison data; decide later |

## Risk

| Failure mode | Mitigation |
| --- | --- |
| A clustered/read-only Postgres deployment is mistaken for supported single-primary v2. | Boot checks `pg_is_in_recovery()`, transaction read-only state, and known distributed-extension markers; unsupported config/read DSNs fail validation. |
| Eval compares different tool surfaces instead of production behavior. | Eval runtime identity records `tool_transport`; import and baseline reject mismatched artifacts; monitor defaults to MCP. |
| Relationship recall loses evidence provenance. | Semantic repository stores supports and trace returns evidence fragments; placement items keep relationship IDs for eval mapping. |
| Existing generated eval runtime makes `go test ./...` traverse database directories. | Future default runtime moved to `tests/eval/.runtime`; legacy visible runtime has a tracked `go.mod` sentinel. |
| The first v2 scoring is bad and a hard quality gate hides useful diagnostics. | No quality gate for the first report; collect raw overall/axis metrics and inspect deltas. |

## Implementation checklist

- [x] Replace branch-only assertion migrations with Postgres semantic relationship/evidence migrations.
- [x] Add topology validation and boot-time primary/read-only checks.
- [x] Make Neo4j optional and disabled by default in server and compose examples.
- [x] Add semantic domain/repository/service types.
- [x] Wire production remember to semantic storage.
- [x] Wire production recall, trace, and context to semantic storage.
- [x] Add strict semantic verifier boundary tests.
- [x] Update MCP/tool DTOs for relationship/evidence refs.
- [x] Update eval runner for MCP tool transport.
- [x] Update eval monitor for Postgres-only stack and mapped relationship/evidence refs.
- [x] Update repo docs/examples; do not update wiki.
- [x] Validate with `go test ./...`.
- [x] Pass the repository CI unit coverage gate at 90.0%.
- [x] Pass compose-backed Playwright e2e before evaluation.
- [x] Recreate local v2 eval compose override as Postgres-only.
- [ ] Run main-vs-branch paired public RAG eval.
- [ ] Write final eval report with raw metrics and deltas.
- [ ] Record final decisions in project memory.

## Validation already run

```bash
bash -n tests/eval/scripts/run_full_public_rag_eval_until_done.sh
docker compose -f docker-compose.yml -f tests/eval/docker-compose.eval.yml config --quiet
POSTGRES_PASSWORD=x CONTROL_PORTAL_TOKEN=x AI_API_KEY=x docker compose -f examples/docker-compose.base.yml config --quiet
POSTGRES_PASSWORD=x CONTROL_PORTAL_TOKEN=x AI_API_KEY=x docker compose -f examples/docker-compose.expert.yml config --quiet
POSTGRES_PASSWORD=x AI_API_KEY=x docker compose -f examples/docker-compose.demo.yml config --quiet
go test ./...
go test -coverprofile=coverage.out ./...
./scripts/ci-check.sh
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run playwright
```

Current measured unit coverage gate result: `90.0%` statements under the
repository CI coverage scope. Compose-backed Playwright e2e passed `22/22`.

## Paired evaluation plan

### Inputs

| Input | Value |
| --- | --- |
| Seed manifest | `tests/eval/seeds/public_rag_3axis_1k_v2/seed_manifest.json` |
| Suite | `tests/eval/suites/public_rag_3axis_1k_v2.jsonl` |
| Tool transport | `mcp` |
| Import route | production `remember` |
| Recall route | production `recall_memory` through MCP |
| Runtime roots | isolated roots under `tests/eval/.runtime/main` and `tests/eval/.runtime/branch` |
| Neo4j | not used by branch v2; main may use its own production runtime requirements if needed |
| Quality gate | none for first report |

### Procedure

1. Record exact SHAs for `main` and this branch.
2. Build the eval runner and server image for `main`.
3. Start an isolated eval stack for `main` with its own ports, data root, team,
   API key, embedding config, and runtime identity.
4. Run the monitor with:

   ```bash
   SEED=tests/eval/seeds/public_rag_3axis_1k_v2/seed_manifest.json \
   SUITE=tests/eval/suites/public_rag_3axis_1k_v2.jsonl \
   V1_DATA_DIR=tests/eval/.runtime/main \
   V1_COMPOSE_DATA_DIR=tests/eval/.runtime/main \
   DENSE_MEM_EVAL_TOOL_TRANSPORT=mcp \
   tests/eval/scripts/run_full_public_rag_eval_until_done.sh
   ```

5. Save `tests/eval/.runtime/main/runs/baseline/summary.json` and raw run
   artifacts.
6. Repeat steps 2-5 for this branch using
   `tests/eval/.runtime/branch`.
7. Compare run directories with eval-runner compare mode.
8. Produce a report containing:
   - exact SHAs and config;
   - import completion counts and placement category counts;
   - overall metrics;
   - per-axis metrics;
   - per-query deltas where available;
   - latency/throughput/resource notes if collected;
   - known caveats and next design hypotheses.

### Report rule

The report must not claim success from architecture alone. It should state
whether branch scores improved, regressed, or moved mixed metrics, and it should
list the evidence needed for the next iteration.
