# Development And Testing

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
