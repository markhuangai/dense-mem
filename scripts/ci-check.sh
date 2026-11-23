#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

bash packages/python/scripts/check.sh

npm ci --prefix .lint
npm ci --prefix web
npm ci --prefix packages/mcp-proxy
npm run --prefix .lint lint:lines
bash scripts/test-shared-regressions.sh
scripts/static-analysis.sh
node --test tests/uat/staging_deployment.test.mjs
node --test tests/uat/pr_image_cleanup_registry.test.mjs
bash scripts/test-evaluation.sh
npm run test:coverage --prefix web
npm run test:coverage --prefix packages/mcp-proxy

node scripts/coverage-gate.mjs browser web/coverage/coverage-summary.json
node scripts/coverage-gate.mjs "MCP proxy" packages/mcp-proxy/coverage/coverage-summary.json
packages="$(scripts/go-packages.sh)"

printf '%s\n' "${packages}"
go test -timeout=30m ${packages}
go -C cmd/e2e test ./... -count=1

scripts/coverage-report.sh --complete
