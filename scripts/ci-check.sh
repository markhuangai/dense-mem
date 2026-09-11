#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

npm ci --prefix .lint
npm ci --prefix web
npm ci --prefix packages/mcp-proxy
npm run --prefix .lint lint:lines
node scripts/check-architecture.mjs
node --test tests/uat/architecture_conformance.test.mjs
node --test tests/uat/synchronous_write/*.test.mjs
scripts/static-analysis.sh
node --test tests/uat/team_dreaming_schedule.test.mjs
node --test tests/uat/image_release_policy.test.mjs
node --test tests/uat/e2e_scenario_registry.test.mjs
node --test tests/uat/e2e_host_controller.test.mjs
node --test tests/uat/prerelease_version.test.mjs
node --test tests/uat/go_vulnerability_scan_policy.test.mjs
node --test tests/uat/ai_pr_review_policy.test.mjs
bash tests/eval/scripts/run_full_public_rag_eval_until_done_test.sh
node --test tests/uat/coverage_policy.test.mjs
npm run test:coverage --prefix web
npm run test:coverage --prefix packages/mcp-proxy
packages="$(scripts/go-packages.sh)"

printf '%s\n' "${packages}"
go test ${packages}
go -C cmd/e2e test ./... -count=1

scripts/coverage-report.sh --transitional
scripts/coverage-report.sh --complete
