#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MODE="validate"
SEED="tests/eval/seeds/local_pr_v1/seed_manifest.json"
SUITE="tests/eval/suites/pr.jsonl"
OUT=""
IMPORT_SEED=0
COMPOSE_UP=0
TRACES=""
BASELINE_RUN=""
CANDIDATE_RUN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      MODE="$2"
      shift 2
      ;;
    --seed)
      SEED="$2"
      shift 2
      ;;
    --suite)
      SUITE="$2"
      shift 2
      ;;
    --out)
      OUT="$2"
      shift 2
      ;;
    --import-seed)
      IMPORT_SEED=1
      shift
      ;;
    --compose-up)
      COMPOSE_UP=1
      shift
      ;;
    --traces)
      TRACES="$2"
      shift 2
      ;;
    --baseline-run)
      BASELINE_RUN="$2"
      shift 2
      ;;
    --candidate-run)
      CANDIDATE_RUN="$2"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "${COMPOSE_UP}" == "1" ]]; then
  docker compose -p "${DENSE_MEM_EVAL_COMPOSE_PROJECT:-densemem_eval}" -f docker-compose.yml up -d --build
fi

args=(go run ./cmd/eval-runner --mode "${MODE}" --seed "${SEED}" --suite "${SUITE}")
if [[ -n "${OUT}" ]]; then
  args+=(--out "${OUT}")
fi
if [[ "${IMPORT_SEED}" == "1" ]]; then
  args+=(--import-seed)
fi
if [[ -n "${TRACES}" ]]; then
  args+=(--traces "${TRACES}")
fi
if [[ -n "${BASELINE_RUN}" ]]; then
  args+=(--baseline-run "${BASELINE_RUN}")
fi
if [[ -n "${CANDIDATE_RUN}" ]]; then
  args+=(--candidate-run "${CANDIDATE_RUN}")
fi

"${args[@]}"
