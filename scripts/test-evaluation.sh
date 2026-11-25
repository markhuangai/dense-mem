#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
node --test tests/eval/scripts/workflow.test.mjs tests/eval/scripts/seed.test.mjs
if [[ "${1:-}" != --policy-only ]]; then
  go test ./internal/evalharness ./internal/testfixture/evaluation ./cmd/eval-runner -count=1
fi
