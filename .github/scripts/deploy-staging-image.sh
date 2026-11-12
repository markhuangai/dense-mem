#!/usr/bin/env bash
set -euo pipefail

exec "$(dirname -- "${BASH_SOURCE[0]}")/deploy-staging-copy.sh" --deploy-only "$@"
