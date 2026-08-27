# Dense-Mem Repository Guidance

## Project Context

- Dense-Mem is a standalone MCP Streamable HTTP memory service. The host
  decides when memory is useful; Dense-Mem owns evidence intake, semantic
  lifecycle, provenance, retrieval, trace, authorization, and audit.
- For non-trivial architecture work, query the Dense-Mem project-memory MCP
  first with `assemble_context` or `recall_memory`. Treat recalled items as
  context; explicit user direction and verified repository state still govern.
- Keep project memory current after approved architecture decisions, verified
  implementation milestones, corrections, or conformance gaps. Store concise
  entries without credentials or local filesystem paths.
- Do not add repository references to local paths for separately maintained
  documentation. State the invariant directly in this repository.
- Inspect current code, interfaces, migrations, callers, and tests before
  changing behavior. Distinguish transitional implementation from the target
  architecture instead of assuming either one fully describes the other.

## Current Stack

- Go 1.26 with Echo v4; PostgreSQL access uses GORM/pgx and goose migrations;
  validation uses `go-playground/validator/v10`; Redis uses `go-redis/v9`.
- Configuration is loaded from environment variables through `internal/config`.
  Do not introduce a second configuration framework.
- The browser applications use React/Vite with Vitest and Playwright. Follow
  their existing component and test patterns when changing `web/`.
- HTTP success responses use `internal/http/response` and the `{ "data": ... }`
  envelope. Public errors are typed and bounded through existing error helpers.

## Target Architecture

- PostgreSQL with `pgvector` is the only durable authority for knowledge,
  workflow, security, audit, full-text search, vector search, and graph-shaped
  reads.
- Neo4j is legacy migration input only. Any bridge must be explicit,
  boot-gated, read-only, and absent from normal boot, remember, recall, trace,
  and evaluation paths. Never add permanent dual-write or fallback reads.
- Redis and process memory are coordination only. A single-node deployment may
  use process-local coordination; a multi-instance deployment requires Redis or
  an equivalent distributed implementation. Neither stores canonical memory.
- Dependency direction is transport -> application service -> domain and ports.
  PostgreSQL, Redis, and provider packages implement ports; they do not define
  domain policy.
- Model providers propose structured results. Closed-schema validation and
  deterministic server policy decide durable state, tier, status, ownership,
  and support.
- Preserve existing package patterns where they enforce these boundaries. Do
  not rename the tree or create parallel v2 packages without a concrete need.

## Semantic Invariants

- Entity and typed Value are the semantic node kinds. Profile-owned
  Relationships carry semantic lifecycle and produce read-model edges only when
  active and eligible.
- Candidates and Hypotheses never enter default recall or active graph reads.
  Stored knowledge and search context never become submitted evidence.
- Remember runs assessment and required embedding work before its terminal
  response. Provider calls occur outside the authoritative semantic
  transaction, and no semantic or workflow state is staged before provider
  success.
- Validate each complete verifier response. Invalid, missing, duplicate,
  unknown, or out-of-allowlist results trigger bounded complete regeneration
  and zero partial semantic commit; do not coerce or splice responses.
- Commit accepted evidence decisions, lifecycle history, current state, search
  documents, validated vectors, terminal attempt metadata, and events atomically
  in PostgreSQL. Document reconciliation remains derived, version-fenced work
  and cannot overwrite newer sources.
- Required failures remain visible and typed. Optional degradation must be
  reported; never replace a required failure with an empty result or silent
  fallback.

## Security And Isolation

- Authentication fixes immutable team, profile, role, scopes, credential, and
  correlation context before application services run. Request payloads,
  headers, provider output, and tool arguments cannot choose or replace the
  authenticated team.
- Team is the read-visibility boundary. Same-team visibility does not grant
  mutation authority: normal lifecycle, support, identity, correction, and
  forget operations require the caller to own the target profile record.
- PostgreSQL work crosses trust boundaries through explicit transactions with
  transaction-local team/profile RLS context. System and migration modes are
  separate, audited, and never request-selectable.
- Scope source, full-text, vector, and adjacency queries before IDs leave an
  adapter, then authorize again during hydration or mutation. Redis namespacing
  prevents collisions; it is not an authorization boundary.
- Use bound SQL parameters. A legacy Neo4j migration adapter must use bounded,
  parameterized, read-only queries.
- Strictly bind and validate HTTP/MCP inputs. MCP `tools/list` and `tools/call`
  use one registry and the same version, feature, scope, and visibility checks.
- MCP is the supported external memory integration contract. Browser handlers
  are first-party interfaces, and the separate control listener remains private
  or behind dedicated administrative ingress.
- Use the existing Echo DTO, binding, and validation helpers. Long-lived
  streams must honor context cancellation, enforce configured bounds, and
  release concurrency slots on every disconnect or error path.
- Never log or forward raw credentials, cookies, tokens, prompts, embeddings,
  provider responses, database errors, stack traces, or cross-team existence
  details. Public errors must be bounded and safe.
- Every isolation-sensitive feature needs real team/profile tests. Include
  different owners when read visibility and mutation authority differ; use
  A/B/C actors for cross-profile correction or conflict behavior.

## Data And Migration Rules

- Services orchestrate domain policy through repository/provider ports.
  Transports do not call storage adapters directly, and repositories do not call
  model providers.
- Already-deployed migrations are immutable. Follow-up changes use new ordered
  migrations with lock/rewrite analysis, RLS impact, constraints, resumable
  backfills where needed, and an explicit rollback or irreversible boundary.
- Durable history is append-only where specified. Normal application code may
  add decisions and transitions but cannot rewrite accepted evidence or audit
  lineage.
- Use the key constructors in `internal/storage/redis`. Preserve live
  namespaced formats, including `profile:{id}:ratelimit:` and
  `profile:{id}:stream:`.

## Release And Evaluation Rules

- `v2.1.0` is a bootstrap release only. Active V2 implementation starts at #74
  after stable `v2.1.0` is promoted from `v2.1.0-rc.0`.
- The completed #94/#95 sequence governs the historical initial V2 authority
  cutover: #94 wrote its compatible marker, and #95 removed Neo4j/runtime-v1
  paths without changing supported V2 behavior.
- #291 owns the later v2.6.1 stopped-service cutover marker for synchronous
  Remember. It does not reopen the historical #94/#95 compatibility window.
- PRs that may affect retrieval quality, ranking, placement accuracy,
  semantic verifier/reviewer behavior, embeddings, search documents, migration
  placement, or performance must include the deterministic 1k
  evaluation/comparison result unless the maintainer grants an explicit,
  issue-specific waiver. #291 has that waiver and must report the evaluation as
  not run.
- Generated evaluation seeds, suites, downloaded datasets, imports, runtime
  databases, run outputs, and comparison artifacts stay in ignored local paths.
  Commit generator, source-lock, provenance, validation, scoring, and compact
  baseline evidence instead.

## Code Review Rules

### Cross-boundary behavior

- When a change adds or alters a field, state transition, identifier,
  configuration value, or contract, trace all affected supported paths from
  input or writer through validation, persistence or serialization,
  asynchronous work, and each reader or consumer. Flag a path that drops,
  defaults, overwrites, mis-scopes, or retains stale state; the safe path
  preserves one authoritative value end to end.

### Falsification

- Before declaring changed behavior safe or a review clean, construct a
  concrete supported counterexample and verify it against actual callers and
  state transitions. Report only defects introduced by the change that remain
  reachable after checking current code, tests, linked scope, and project
  memory; reject hypothetical, pre-existing, or contradicted claims.

## Change And Test Workflow

1. Inspect the current package, interfaces, migrations, callers, and tests.
2. State current behavior, the target invariant, and any unresolved mismatch.
3. Add or adjust the smallest real-logic test that proves the behavior.
4. Implement one reviewable responsibility without unrelated cleanup.
5. Run focused tests, then repository checks, and inspect the final diff.

Useful commands:

```bash
go test ./internal/service/... -count=1
go test ./internal/storage/postgres/... -count=1
go test ./tests/integration/... -count=1
npm test --prefix web
./scripts/ci-check.sh
```

- Compose E2E scenarios are local, on-demand release gates rather than CI jobs.
  Run the scenario required by the issue and record its result; do not add it to
  CI without an explicit maintainer policy change.

- RLS, migrations, constraints, transactions, locks, idempotency, `pgvector`,
  and concurrency require real PostgreSQL/service integration tests. Mocked
  method calls are not proof of those behaviors.
- Mocks may isolate outbound provider transport failures or pure query
  construction. Tests must still exercise the real validation and domain policy
  around those boundaries.
- Keep changes small, preserve unrelated worktree edits, and surface failures
  before retrying or changing scope.
