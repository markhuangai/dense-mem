#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MODE="validate"
SEED=""
SUITE=""
OUT=""
IMPORT_SEED=0
IMPORT_CONCURRENCY=""
PLACEMENT_TIMEOUT=""
RESUME_SOURCE_DOC_IDS=""
COMPOSE_UP=0
TRACES=""
MAPPING=""
BASELINE_RUN=""
CANDIDATE_RUN=""
RELEASE_GATE_POLICY=""
MIN_RECALL_AT_K=""
MIN_REQUIRED_RANK1_RATE=""
MAX_AVERAGE_BAD_AT_K=""
MAX_BAD_RANK1_RATE=""
MIN_DREAM_RECALL_AT_K=""
MIN_DREAM_REQUIRED_RANK1_RATE=""
MAX_AVERAGE_DREAM_BAD_AT_K=""
MAX_DREAM_BAD_RANK1_RATE=""

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
    --import-concurrency)
      IMPORT_CONCURRENCY="$2"
      shift 2
      ;;
    --placement-timeout)
      PLACEMENT_TIMEOUT="$2"
      shift 2
      ;;
    --resume-source-doc-ids)
      RESUME_SOURCE_DOC_IDS="$2"
      shift 2
      ;;
    --compose-up)
      COMPOSE_UP=1
      shift
      ;;
    --traces)
      TRACES="$2"
      shift 2
      ;;
    --mapping)
      MAPPING="$2"
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
    --release-gate-policy)
      RELEASE_GATE_POLICY="$2"
      shift 2
      ;;
    --min-recall-at-k)
      MIN_RECALL_AT_K="$2"
      shift 2
      ;;
    --min-required-rank1-rate)
      MIN_REQUIRED_RANK1_RATE="$2"
      shift 2
      ;;
    --max-average-bad-at-k)
      MAX_AVERAGE_BAD_AT_K="$2"
      shift 2
      ;;
    --max-bad-rank1-rate)
      MAX_BAD_RANK1_RATE="$2"
      shift 2
      ;;
    --min-dream-recall-at-k)
      MIN_DREAM_RECALL_AT_K="$2"
      shift 2
      ;;
    --min-dream-required-rank1-rate)
      MIN_DREAM_REQUIRED_RANK1_RATE="$2"
      shift 2
      ;;
    --max-average-dream-bad-at-k)
      MAX_AVERAGE_DREAM_BAD_AT_K="$2"
      shift 2
      ;;
    --max-dream-bad-rank1-rate)
      MAX_DREAM_BAD_RANK1_RATE="$2"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "${MODE}" != "compare" && ( -z "${SEED}" || -z "${SUITE}" ) ]]; then
  echo "--seed and --suite are required" >&2
  exit 2
fi

if [[ "${COMPOSE_UP}" == "1" ]]; then
  docker compose -p "${DENSE_MEM_EVAL_COMPOSE_PROJECT:-densemem_eval}" -f docker-compose.yml up -d --build
fi

args=(go run ./cmd/eval-runner --mode "${MODE}")
if [[ "${MODE}" != "compare" ]]; then
  args+=(--seed "${SEED}" --suite "${SUITE}")
fi
if [[ -n "${OUT}" ]]; then
  args+=(--out "${OUT}")
fi
if [[ "${IMPORT_SEED}" == "1" ]]; then
  args+=(--import-seed)
fi
if [[ -n "${IMPORT_CONCURRENCY}" ]]; then
  args+=(--import-concurrency "${IMPORT_CONCURRENCY}")
fi
if [[ -n "${PLACEMENT_TIMEOUT}" ]]; then
  args+=(--placement-timeout "${PLACEMENT_TIMEOUT}")
fi
if [[ -n "${RESUME_SOURCE_DOC_IDS}" ]]; then
  args+=(--resume-source-doc-ids "${RESUME_SOURCE_DOC_IDS}")
fi
if [[ -n "${TRACES}" ]]; then
  args+=(--traces "${TRACES}")
fi
if [[ -n "${MAPPING}" ]]; then
  args+=(--mapping "${MAPPING}")
fi
if [[ -n "${BASELINE_RUN}" ]]; then
  args+=(--baseline-run "${BASELINE_RUN}")
fi
if [[ -n "${CANDIDATE_RUN}" ]]; then
  args+=(--candidate-run "${CANDIDATE_RUN}")
fi
if [[ -n "${RELEASE_GATE_POLICY}" ]]; then
  args+=(--release-gate-policy "${RELEASE_GATE_POLICY}")
fi
if [[ -n "${MIN_RECALL_AT_K}" ]]; then
  args+=(--min-recall-at-k "${MIN_RECALL_AT_K}")
fi
if [[ -n "${MIN_REQUIRED_RANK1_RATE}" ]]; then
  args+=(--min-required-rank1-rate "${MIN_REQUIRED_RANK1_RATE}")
fi
if [[ -n "${MAX_AVERAGE_BAD_AT_K}" ]]; then
  args+=(--max-average-bad-at-k "${MAX_AVERAGE_BAD_AT_K}")
fi
if [[ -n "${MAX_BAD_RANK1_RATE}" ]]; then
  args+=(--max-bad-rank1-rate "${MAX_BAD_RANK1_RATE}")
fi
if [[ -n "${MIN_DREAM_RECALL_AT_K}" ]]; then
  args+=(--min-dream-recall-at-k "${MIN_DREAM_RECALL_AT_K}")
fi
if [[ -n "${MIN_DREAM_REQUIRED_RANK1_RATE}" ]]; then
  args+=(--min-dream-required-rank1-rate "${MIN_DREAM_REQUIRED_RANK1_RATE}")
fi
if [[ -n "${MAX_AVERAGE_DREAM_BAD_AT_K}" ]]; then
  args+=(--max-average-dream-bad-at-k "${MAX_AVERAGE_DREAM_BAD_AT_K}")
fi
if [[ -n "${MAX_DREAM_BAD_RANK1_RATE}" ]]; then
  args+=(--max-dream-bad-rank1-rate "${MAX_DREAM_BAD_RANK1_RATE}")
fi

"${args[@]}"
