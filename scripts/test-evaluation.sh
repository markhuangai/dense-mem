#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
node --test tests/eval/scripts/workflow.test.mjs
if [[ "${1:-}" != --policy-only ]]; then
  go test ./internal/evalharness ./internal/testfixture/evaluation ./internal/evalretire ./cmd/eval-runner -count=1
fi
