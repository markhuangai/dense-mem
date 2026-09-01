#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTROLLER="${DENSE_MEM_CI_CONTROLLER:-${DENSE_MEM_CI_HOME:-${HOME}/dense-mem-ci}/e2e-stack.sh}"
SOURCE_DIR="${DENSE_MEM_E2E_SOURCE_ROOT:-$ROOT_DIR}"
RUN_ID="${DENSE_MEM_E2E_RUN_ID:-$(date -u +%s)}"
ATTEMPT="${DENSE_MEM_E2E_RUN_ATTEMPT:-1}"
IMAGE="${DENSE_MEM_E2E_IMAGE:-ghcr.io/markhuangai/dense-mem:latest}"

fail() {
  printf 'dense-mem local production E2E: %s\n' "$*" >&2
  exit 1
}

[[ -x "$CONTROLLER" ]] || fail "host controller is unavailable: $CONTROLLER"
[[ -d "$SOURCE_DIR" && "$SOURCE_DIR" == /* ]] || fail "source directory must be absolute"
[[ "$RUN_ID" =~ ^[1-9][0-9]*$ ]] || fail "run ID must be a positive decimal value"
[[ "$ATTEMPT" =~ ^[1-9][0-9]*$ ]] || fail "run attempt must be a positive decimal value"
[[ "$(git -C "$SOURCE_DIR" rev-parse --is-inside-work-tree 2>/dev/null || true)" == true ]] || fail "source directory is not a Git worktree"
SOURCE_REVISION="$(git -C "$SOURCE_DIR" rev-parse HEAD)"
[[ "$SOURCE_REVISION" =~ ^[0-9a-f]{40}$ ]] || fail "source revision is invalid"

digest="${DENSE_MEM_E2E_IMAGE_DIGEST:-}"
if [[ -z "$digest" ]]; then
  if [[ "$IMAGE" == *@sha256:* ]]; then
    digest="${IMAGE##*@}"
  else
    digest="$(docker image inspect "$IMAGE" --format '{{join .RepoDigests "\\n"}}' 2>/dev/null | sed -n 's/^.*@//p' | head -n 1)"
  fi
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "set DENSE_MEM_E2E_IMAGE_DIGEST to the production image manifest digest or use an image reference with @sha256"
fi
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "image digest is invalid"

"$CONTROLLER" local-all "$RUN_ID" "$ATTEMPT" "$IMAGE" "$digest" "$SOURCE_REVISION" "$SOURCE_DIR"
