#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

npm ci --prefix .lint
npm run --prefix .lint lint:lines
node scripts/check-architecture.mjs
node --test tests/uat/architecture_conformance.test.mjs
scripts/static-analysis.sh
node --test tests/uat/team_dreaming_schedule.test.mjs
node --test tests/uat/image_release_policy.test.mjs
node --test tests/uat/prerelease_version.test.mjs
node --test tests/uat/go_vulnerability_scan_policy.test.mjs
node --test tests/uat/ai_pr_review_policy.test.mjs
bash tests/eval/scripts/run_full_public_rag_eval_until_done_test.sh
packages="$(scripts/go-packages.sh)"

printf '%s\n' "${packages}"
go test ${packages}

packages="$(
	go list -f '{{if .TestGoFiles}}{{.ImportPath}}{{end}}' ./internal/... |
		sed '/^$/d' |
		grep -Ev '/(evalharness|repository)$|/storage/(neo4j|postgres|redis)$'
)"

printf '%s\n' "${packages}"
go test ${packages} -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out

total="$(go tool cover -func=coverage.out | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"
awk \
	-v total="${total}" \
	-v threshold="${COVERAGE_THRESHOLD:-90.0}" \
	'BEGIN {
		if ((total + 0) < (threshold + 0)) {
			printf("coverage %.1f%% is below required %.1f%%\n", total, threshold)
			exit 1
		}
		printf("coverage %.1f%% meets required %.1f%%\n", total, threshold)
	}'
