# Development And Testing

> Publication pending: issue #218 branch. Publish after the code PR is merged and session ingestion passes against the exact merged-main commit.

The repository uses Go 1.26, Echo, GORM/pgx, goose, pgvector, and optional Redis.
The browser applications use React/Vite, Vitest, and Playwright. The MCP proxy
has its own Node package and tests.

## Source Ownership

Capability owners are declared in
[the architecture inventory](https://github.com/markhuangai/dense-mem/blob/main/architecture/ownership.v2.json)
and its exact fragments. Application policy belongs to its capability;
PostgreSQL/provider adapters implement ports. Composition constructs concrete
dependencies. The domain does not import transports or infrastructure.

Database-backed fixtures live beside their native capability adapters.
Files named `*_integration_test.go` use the `integration` build tag and
have registrations under `scripts/e2e-db-cases/`. Ordinary `*_test.go`
files belong to the database-free suite. Build-tagged production and evaluation
profiles are both checked.

See [Architecture](Architecture#architecture-conformance) for the role matrix
and [capability contracts](https://github.com/markhuangai/dense-mem/blob/main/architecture/capability-contracts.md)
for exact package ownership.

## Change Workflow

1. Read the affected code, callers, accepted ADRs, migrations, and tests.
2. State the current behavior, required invariant, and concrete failure risk.
3. Add or adjust the smallest test that exercises real policy or storage logic.
4. Implement one responsibility and run the focused checks.
5. Run required repository gates, inspect the diff, and update the affected
   canonical wiki pages.

Similar syntax does not justify a shared abstraction unless it represents the
same authoritative rule. Do not duplicate defaults, state transitions, or error
classification across consumers.

## Local Checks

Run commands from the repository root:

```bash
go test ./internal/tools/registry ./internal/remember/service/... -count=1
go test ./internal/recall/... ./internal/dream/... -count=1
go -C cmd/e2e test ./... -count=1
npm test --prefix web
node scripts/check-architecture.mjs
node --test tests/uat/architecture_conformance.test.mjs
```

Choose the packages affected by the change; these examples are not a replacement
for database-backed checks. `scripts/go-packages.sh` discovers the checked Go
inventory. The complete local repository gate is:

```bash
./scripts/ci-check.sh
```

It installs locked JavaScript dependencies, checks source line limits and
architecture, runs static analysis and harness tests, tests browser/proxy
packages, and runs Go tests and complete coverage. Static analysis uses the
module-pinned tools. CI additionally checks vulnerability audits and immutable
migration history.

Coverage must **exceed** 90%; exactly 90% does not pass. Complete Go coverage
accounts for production, evaluation, the nested E2E module, and cross-package
execution without double counting. Browser and MCP-proxy coverage are separate
inventories. Missing or empty coverage reports fail.

## PostgreSQL And Production-Entry Tests

RLS, transactions, locks, idempotency, concurrency, migrations, constraints, and
pgvector behavior require real PostgreSQL tests. Calling a mocked repository
method does not establish any of those properties.

Use the registered local production-entry scenario that covers the change:

```bash
scripts/e2e.sh synchronous_write
scripts/e2e.sh conflict
scripts/e2e.sh private_memory_erasure
scripts/e2e.sh operation_logging
```

These are separate examples, not a mandatory sequence. The entrypoint resolves
`scripts/e2e-scenarios.json`, builds a production image, and runs the registered
database/UAT/browser cases with its real dependencies. Scenario names and
ownership come from that registry, not an ad hoc environment variable.

The harness uses the configured local CI environment, with the repository
`.env` fallback when applicable. Keep credentials private and use its
diagnostic redaction facilities. Do not paste expanded Compose secrets into a
report. The full production-image scenario matrix is a GitHub gate, not a
routine local prerequisite for every edit.

## Behavioral Acceptance

A changed supported public behavior needs a positive and a distinct adverse
production-entry case under ADR 0002. Existing cases count when their setup and
assertions reach the changed behavior.

| Change | Evidence required |
| --- | --- |
| MCP schemas and tool visibility | Registry/transport tests, valid examples, rejected unknown fields, and scope/feature denial. |
| Remember | Evidence-only and proposal writes; unsafe evidence; invalid provider response; embedding/commit failure; exact retry and changed-body conflict. |
| Authorization or private memory | Same-team owners with different mutation authority, another team, and each affected space binding. |
| Conflict/correction | A/B/C actors, fresh/stale versions, current/historical recall, and preserved owner lineage. |
| Derived jobs or diagnostics | Lease/generation fences, cancellation, retention/erasure, unavailable/truncated payloads, and public/private exposure boundaries. |

Document-only changes do not require a new E2E scenario. They do require
validated examples, commands, source claims, links, and diagrams.

## Database Changes

Deployed migrations are immutable. Add a new ordered migration and describe its
lock/rewrite cost, constraint and RLS effects, backfill/restart requirements, and
rollback or irreversible boundary. Use the repository's migration generator and
history checker.

The server applies embedded release-directory migrations under a database
session lock before serving. Test schema changes on real PostgreSQL and keep
supported rolling deployments compatible across overlapping binaries.

## Review And CI Phases

The `REVIEW_GATED_CI` repository variable controls review-first validation.
When enabled, owner same-repository PRs complete reviews and CodeQL before
ordinary CI/E2E; other same-repository authors retain automatic ordinary CI.
External forks need repository-admin authorization for ordinary CI and E2E.

An admin-applied `deploy-test-image` label requests validation for one current
commit and is consumed. A new commit requires a new request. The final head
must have successful `Repository CI`, `CodeQL analysis`, `CodeQL`, and
`Production image E2E` results before merge. Review completion is distinct
from full validation; pending, skipped, or absent full results are incomplete.

The workflows and repository settings determine the actual phase for a PR.
Do not infer that a missing check passed, alter repository settings, or merge
as part of verification.

## Evaluation And Artifacts

Changes affecting retrieval quality, ranking, semantic assessment, embeddings,
search documents, or performance require the deterministic 1k comparison unless
the linked issue contains an explicit approved waiver and replacement proof.
See [Evaluation](Evaluation).

Generated seeds, datasets, runtime databases, and full run outputs remain in
ignored local storage. Commit reproducible generators, source locks, validation,
scoring, and compact baseline evidence.

## Risks

Tests must fail when the behavior they claim to prove is absent. A provider
fixture may isolate outbound HTTP behavior but must leave real server
validation and policy in the path. A green unit suite cannot substitute for
RLS or commit tests. A report for an earlier commit cannot validate a later
push.

## Enterprise export verification (issue #224)

`enterprise_exports` is a production scenario using the existing OAuth and synchronous-write helpers plus Collector `0.162.0`, pinned to `sha256:39923a8e431bd1f57be82411999d389fcfe40857492e4365456d97a4c1f74be6`. It exercises static/admin authorization, ordinary-team denial, Remember/recall/trace, provider failure, cross-team denial, audit pagination/scope/cancellation, disabled endpoints, content scans, Collector slowness/outage, and private erasure/legal holds. The test Collector writes rotating 32 MiB telemetry files in its disposable container and retains at most one backup; it has no host mounts or published ports.

Run the focused Go packages, real PostgreSQL audit cases, migration history check, and `./scripts/ci-check.sh`. The registered PostgreSQL regressions cover reversed commit order, rollback, immutable transaction IDs, scope-bound replay, non-superuser reads, and historical migration preservation without a rewrite. Required cases must execute rather than skip. Run `enterprise_exports`, `operation_logging`, `private_memory_erasure`, `mcp_oauth`, and `synchronous_write` through `scripts/e2e.sh`.

The maintainer approved an export-only historical 1k waiver before implementation and, on 2026-10-08, waived the forty-run comparative performance acceptance after excessive benchmark cost and unresolved individual live embedding stalls. Replacement verification consists of the focused real-logic, PostgreSQL, repository, and five production scenarios above. Final-head review and full production-image E2E remain required. Comparative latency, throughput, and RSS remain unverified; partial runs do not count as performance acceptance. Features remain disabled by default.

The frozen fixture in `tests/eval/fixtures/enterprise_exports.json` and `scripts/compare-enterprise-exports.sh` remain available for optional future measurements. The benchmark retains its original thresholds and approved disposable-image `GOGC=25`, `GOMEMLIMIT=768MiB`, and 100,000/minute request ceiling. Ordinary production E2E retains its default runtime settings. No response-replay fixture was approved or implemented.

The separately required ontology maintenance write-latency check runs in GitHub-hosted PostgreSQL prechecks. On 2026-10-09 the maintainer retained this remote gate and approved a 2 ms absolute allowance: each paired median or p95 fails only when it increases by both more than 10% and more than 2 ms. Its five paired repetitions with one/eight writers and every other assertion remain unchanged. This delivery runs the latency workload only in remote CI. Historical ontology baseline receipts retain their original 1 ms threshold; final-head remote results establish the current gate.

Keep raw runs and configuration in ignored task-owned paths and commit compact evidence with exact source/image/fixture identities. Inspect the independent plan-audit receipt before running implementation tests. A final-head full production-image E2E pass remains a merge gate; focused local results do not replace it.

PR [#547](https://github.com/markhuangai/dense-mem/pull/547) merged as `8aae241000f629c832d4d3dbfaa84895f04ae6d6`. [Remote validation run 37932480780](https://github.com/markhuangai/dense-mem/actions/runs/37932480780) passed all 529 registered PostgreSQL cases and all 26 production scenarios at `bbd3c4645abcd2743ff996c604ceba4cf3568356`; the merged commit has the same Git tree. The required remote latency benchmark passed all five pairs, with a largest logged increase of 0.704 ms. This is validation of the tested source tree; a separate production E2E run against the merge SHA has not been performed.

## Session Intake Verification

Issue [#218](https://github.com/markhuangai/dense-mem/issues/218) records the approved historical deterministic-1k waiver and its replacement gates. The frozen `session_ingest_v1` source lock and generator define 32 quality cases and twelve existing Remember payload controls. Require three fresh real-provider quality repetitions, precision at least 98% and recall at least 95% in each, complete exact-provenance/boundary/isolation checks, completion within 180 seconds, and latency/token reporting. Compare all twelve Remember controls on the recorded base and candidate. Chat input/output token usage must be provider-reported. Record missing embedding token usage explicitly as unavailable, without estimated counts or a combined total; this measurement clarification is maintainer-approved in #218.

The maintainer approved correcting the mixed-object Remember control to submit registered `known_predicate_key: primary_database` and require that exact result. The original proposed key permitted normalization and therefore could not guarantee the frozen exact-key expectation. All six facts, the other eleven controls, all 32 quality cases, scoring thresholds, provenance/isolation checks and deadlines remain unchanged; retain earlier failed runs and rerun both stacks with the corrected cohort.

The maintainer also approved adding the explicitly stated designer and musician roles to the homonym case's expected facts: `Jordan has_role designer` and `Jordan has_role musician`. All 32 input cases and all twelve Remember controls remain identical. Retain the earlier 45/47 precision failure and rerun both controls and three fresh quality repetitions with the corrected source lock; thresholds, provenance/isolation checks and deadlines remain unchanged.

Before implementation tests, the approved workflow requires an independent plan audit. After conformance, run the focused session/Remember/assessor/registry/server suites, real PostgreSQL knowledge/privacy/trace suites, migration immutability and repository CI. The registered production scenarios are `session_ingest`, `synchronous_write`, `synchronous_write_primitives`, `submission_terminal_errors`, `mcp_boundaries`, `credential_memory_binding`, `space_aware_recall` and `private_memory_erasure`. The final PR commit still requires full-matrix production-image E2E.

Generators, source locks, scoring and compact evidence belong in Git. Generated cohorts, runtime databases, credentials and run outputs stay ignored. The quality runner and provider accounting proxy live under `tests/eval/scripts`; provider usage must be present and no fixture provider qualifies. See the repository evaluation README for invocation and isolated-stack requirements.

Use the maintainer-maintained CI environment's verifier/Remember model and provider configuration for local evaluation. Record the configured alias and runtime source; substitute another model only with explicit maintainer direction. Gateway backend routing belongs to the gateway. Failed attempts and isolated diagnostics earn no credit toward the three complete quality repetitions, and historical passing metrics certify only their recorded source and configuration.

Keep #218 open until code is merged, these wiki changes are published, and `session_ingest` passes against the exact merged-main SHA.
