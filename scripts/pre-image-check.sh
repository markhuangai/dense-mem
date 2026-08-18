#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_REF="${1:-origin/main}"

cd "${ROOT_DIR}"

scripts/check-postgres-migrations.sh "${BASE_REF}"

export DENSE_MEM_REQUIRE_POSTGRES_TESTS=1
go test \
  -tags=integration \
  ./internal/storage/postgres \
  -run '^(TestMemorySpaceBackfillPreservesAppendOnlyGuards|TestSubmissionDiagnosticsIndexesAreValid)$' \
  -count=1 \
  -timeout=15m

echo "pre-image PostgreSQL migration checks passed"
