#!/usr/bin/env bash
set -euo pipefail
bash "$(dirname "${BASH_SOURCE[0]}")/../../../scripts/test-evaluation.sh" --policy-only
