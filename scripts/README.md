# Development and test commands

Run commands from the repository root. Scripts own their executable behavior;
this guide identifies entry points. The wiki's
[Development And Testing](https://github.com/markhuangai/dense-mem/wiki/Development-And-Testing)
page explains the test layers and review workflow.

## Focused local checks

Select packages and cases for the behavior being changed:

```bash
go test ./internal/remember/... ./internal/recall/... -count=1
npm test --prefix web
node scripts/check-architecture.mjs
node --test tests/uat/architecture_conformance.test.mjs
```

RLS, migrations, transactions, constraints, idempotency, vector execution, and
concurrency require real PostgreSQL-backed proof. Provider transport fixtures
do not replace domain-policy or database assertions. Registered production-entry
scenarios live in [e2e-scenarios.json](e2e-scenarios.json).

For the Dense-Mem delivery workflow, local execution is selective. Complete
suites, coverage, browser builds, vulnerability scans, and repository checks run
in full remote PR CI. The existing [ci-check.sh](ci-check.sh) is the expanded
repository gate; invoking the delivery workflow does not assign it to local
execution. Follow [repository policy](../AGENTS.md) and the approved test map.

## Development E2E on GitHub-hosted runners

Repository admins can test a pushed same-repository commit before opening a PR:

```bash
gh workflow run development-e2e.yml --repo markhuangai/dense-mem --ref main \
  -f 'source_ref=<full-40-character-commit-sha>' \
  -f scenarios=conflict,mcp_oauth \
  -f postgres_shards=all
```

Select registered scenario names or `all`. PostgreSQL selections are `0` through
`6`, comma-separated subsets, or `all`. Either group can be blank when the other
is populated; both blank is invalid. The workflow file on `main` owns current
inputs and authorization. A scenario named `full` is one scenario; `all` selects
the whole registry.

Retain the tested source SHA, image digest, run URL/attempt, selected cases, their
results, and run-owned image-cleanup receipt. A passing build alone does not
prove execution. Follow the delivery workflow's timeout investigation and retry
rules; do not substitute local full CI when the remote route fails.

## Pull-request validation

Main-targeting PRs retain the repository's configured review-first policy.
After current-head reviews and CodeQL are clean, an authorized
`deploy-test-image` request starts the remaining validation for that commit.
Required final results include `Repository CI`, `CodeQL`, `CodeQL analysis`, and
`Production image E2E`; pending, absent, or skipped required results do not pass.

Fork contributors use their own Cloudflare credentials and the trusted preview
receipt through the Contributor fork E2E request workflow. Follow its current
inputs and [development guide](https://github.com/markhuangai/dense-mem/wiki/Development-And-Testing#fork-pull-request-validation).
Never place credentials in inputs, PR comments, or committed files.

## Evaluation and migrations

- [Evaluation guide](../tests/eval/README.md): deterministic comparisons,
  provenance, source locks, scoring, and ignored artifacts. Campaigns and
  benchmarks run only on GitHub-hosted runners.
- [new-postgres-migration.sh](new-postgres-migration.sh): ordered migration
  creation. Read the applicable migration-family guide before changing storage.
- [check-postgres-migrations.sh](check-postgres-migrations.sh): deployed-migration
  immutability checks; pass the correct comparison base.
- [static-analysis.sh](static-analysis.sh): Go static analysis and duplication
  checks, assigned to remote full CI during delivery.

## Documentation branches

Feature PRs target `main`; wiki PRs target `wiki`. Link the companion wiki PR,
or explain why the feature has no documentation impact. Update file-specific
instructions here or in the owning directory guide. Wiki pages and publication
recovery are maintained in
[Documentation Maintenance](https://github.com/markhuangai/dense-mem/wiki/Documentation-Maintenance).
