#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT_DIR}"

SEED="${SEED:-tests/eval/seeds/public_rag_3axis_5k_v1/seed_manifest.json}"
SUITE="${SUITE:-tests/eval/suites/public_rag_3axis_5k_v1.jsonl}"
IMPORT_DIR="${IMPORT_DIR:-tests/eval/runs/import_public_rag_3axis_5k_v1}"
BASELINE_DIR="${BASELINE_DIR:-tests/eval/runs/baseline_public_rag_3axis_5k_v1}"
MONITOR_DIR="${MONITOR_DIR:-tests/eval/runs/full_eval_monitor}"
RUNNER="${RUNNER:-/tmp/dense-mem-eval-runner}"
IMPORT_CONCURRENCY="${IMPORT_CONCURRENCY:-3}"
DIRECT_IMPORT_BATCH_SIZE="${DIRECT_IMPORT_BATCH_SIZE:-512}"
EMBEDDING_MAX_RETRIES="${DENSE_MEM_EVAL_EMBEDDING_MAX_RETRIES:-60}"
SLEEP_SECONDS="${SLEEP_SECONDS:-60}"
MAX_IMPORT_RESTARTS="${MAX_IMPORT_RESTARTS:-100}"
MAX_COUNT_FAILURES="${MAX_COUNT_FAILURES:-12}"

mkdir -p "${IMPORT_DIR}" "${BASELINE_DIR}" "${MONITOR_DIR}"
LOG="${MONITOR_DIR}/monitor.log"
STATUS_JSON="${MONITOR_DIR}/status.json"
TARGET="$(jq -r '.counts.corpus' "${SEED}")"

if [[ -z "${TARGET}" || "${TARGET}" == "null" ]]; then
  echo "seed manifest missing counts.corpus: ${SEED}" >&2
  exit 2
fi

export DENSE_MEM_PORT="${DENSE_MEM_PORT:-18080}"
export CONTROL_PORTAL_PORT="${CONTROL_PORTAL_PORT:-18090}"
export POSTGRES_HOST_PORT="${POSTGRES_HOST_PORT:-15432}"
export NEO4J_HTTP_HOST_PORT="${NEO4J_HTTP_HOST_PORT:-17474}"
export NEO4J_BOLT_HOST_PORT="${NEO4J_BOLT_HOST_PORT:-17687}"
export REDIS_PORT="${REDIS_PORT:-16380}"
export PROMETHEUS_PORT="${PROMETHEUS_PORT:-19090}"
export PROMETHEUS_CONTAINER_NAME="${PROMETHEUS_CONTAINER_NAME:-densemem-eval-full-prometheus}"

compose() {
  docker compose -p densemem_eval_full -f docker-compose.yml -f tests/eval/docker-compose.eval.yml "$@"
}

count_fragments() {
  compose exec -T neo4j sh -c 'u=${NEO4J_AUTH%%/*}; p=${NEO4J_AUTH#*/}; cypher-shell -u "$u" -p "$p" "MATCH (sf:SourceFragment) RETURN count(sf) AS fragments"' |
    tail -1 |
    tr -d '[:space:]'
}

log() {
  local line
  line="$(printf '%s %s' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*")"
  if [[ -t 1 ]]; then
    printf '%s\n' "${line}" | tee -a "${LOG}"
  else
    printf '%s\n' "${line}" >> "${LOG}"
  fi
}

write_status() {
  local phase="$1"
  local fragments="$2"
  local import_pid="${3:-}"
  local rate_per_minute="${4:-}"
  local eta_seconds="${5:-}"
  local tmp
  tmp="$(mktemp "${STATUS_JSON}.tmp.XXXXXX")"
  jq -n \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg phase "${phase}" \
    --arg fragments "${fragments}" \
    --arg target "${TARGET}" \
    --arg import_pid "${import_pid}" \
    --arg rate_per_minute "${rate_per_minute}" \
    --arg eta_seconds "${eta_seconds}" \
    --arg import_dir "${IMPORT_DIR}" \
    --arg baseline_dir "${BASELINE_DIR}" \
    'def nullable_number: if . == "" then null else tonumber end;
    {
      updated_at: $updated_at,
      phase: $phase,
      fragments: ($fragments | tonumber),
      target: ($target | tonumber),
      percent_complete: (($fragments | tonumber) / ($target | tonumber) * 100),
      rate_per_minute: ($rate_per_minute | nullable_number),
      eta_seconds: ($eta_seconds | nullable_number),
      import_pid: $import_pid,
      import_dir: $import_dir,
      baseline_dir: $baseline_dir
    }' > "${tmp}"
  mv "${tmp}" "${STATUS_JSON}"
}

ensure_runner() {
  if [[ ! -x "${RUNNER}" ]]; then
    go build -o "${RUNNER}" ./cmd/eval-runner
  fi
}

load_env() {
  set -a
  . ./.env
  set +a
  export AI_API_EMBEDDING_TIMEOUT_SECONDS="${AI_API_EMBEDDING_TIMEOUT_SECONDS:-240}"
  export DENSE_MEM_EVAL_EMBEDDING_MAX_RETRIES="${EMBEDDING_MAX_RETRIES}"
  export DENSE_MEM_BASE_URL="${DENSE_MEM_BASE_URL:-http://127.0.0.1:${DENSE_MEM_PORT}}"
  export DENSE_MEM_CONTROL_URL="${DENSE_MEM_CONTROL_URL:-http://127.0.0.1:${CONTROL_PORTAL_PORT}}"
  export DENSE_MEM_API_KEY="${DENSE_MEM_API_KEY:-$(jq -r .api_key tests/eval/runs/eval_full_profile.json)}"
  export DENSE_MEM_CONTROL_TOKEN="${DENSE_MEM_CONTROL_TOKEN:-${CONTROL_PORTAL_TOKEN}}"
}

start_import() {
  load_env
  ensure_runner
  printf '\nresume import with concurrency=%s max_embedding_retries=%s at %s\n' \
    "${IMPORT_CONCURRENCY}" "${EMBEDDING_MAX_RETRIES}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${IMPORT_DIR}/import.log"
  setsid "${RUNNER}" \
    --mode import \
    --seed "${SEED}" \
    --suite "${SUITE}" \
    --out "${IMPORT_DIR}" \
    --import-seed \
    --import-concurrency "${IMPORT_CONCURRENCY}" \
    --direct-import \
    --direct-import-team-id "$(jq -r .team_id tests/eval/runs/eval_full_profile.json)" \
    --direct-import-batch-size "${DIRECT_IMPORT_BATCH_SIZE}" \
    --neo4j-uri "bolt://127.0.0.1:${NEO4J_BOLT_HOST_PORT}" \
    --neo4j-user "${NEO4J_USER}" \
    --neo4j-database "${NEO4J_DATABASE:-neo4j}" \
    --max-page-size 500 \
    >> "${IMPORT_DIR}/import.log" 2>&1 < /dev/null &
  echo "$!" > "${IMPORT_DIR}/import.pid"
  log "started_import pid=$(cat "${IMPORT_DIR}/import.pid")"
}

live_import_pid() {
  if [[ -f "${IMPORT_DIR}/import.pid" ]]; then
    local pid
    pid="$(cat "${IMPORT_DIR}/import.pid")"
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      printf '%s\n' "${pid}"
      return 0
    fi
  fi
  return 1
}

run_baseline() {
  load_env
  ensure_runner
  if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" ]]; then
    log "knowledge_mapping_missing; regenerating import artifacts from existing rows"
    "${RUNNER}" \
      --mode import \
      --seed "${SEED}" \
      --suite "${SUITE}" \
      --out "${IMPORT_DIR}" \
      --import-seed \
      --import-concurrency 1 \
      --direct-import \
      --direct-import-team-id "$(jq -r .team_id tests/eval/runs/eval_full_profile.json)" \
      --direct-import-batch-size "${DIRECT_IMPORT_BATCH_SIZE}" \
      --neo4j-uri "bolt://127.0.0.1:${NEO4J_BOLT_HOST_PORT}" \
      --neo4j-user "${NEO4J_USER}" \
      --neo4j-database "${NEO4J_DATABASE:-neo4j}" \
      --max-page-size 500
  fi
  if [[ -s "${BASELINE_DIR}/summary.json" ]]; then
    log "baseline_summary_exists path=${BASELINE_DIR}/summary.json"
    return 0
  fi
  log "starting_baseline_eval"
  "${RUNNER}" \
    --mode baseline \
    --seed "${SEED}" \
    --suite "${SUITE}" \
    --out "${BASELINE_DIR}" \
    --mapping "${IMPORT_DIR}/knowledge_mapping.json" \
    --max-page-size 500
  log "baseline_eval_finished path=${BASELINE_DIR}/summary.json"
}

main() {
  echo "$$" > "${MONITOR_DIR}/monitor.pid"
  log "monitor_started target=${TARGET}"

  local restarts=0
  local count_failures=0
  local previous_count=""
  local previous_epoch=""
  while true; do
    local now_epoch
    now_epoch="$(date +%s)"
    local count
    if ! count="$(count_fragments)"; then
      count_failures=$((count_failures + 1))
      log "count_fragments_failed failures=${count_failures}/${MAX_COUNT_FAILURES}"
      write_status "count_failed" "0" "$(live_import_pid || true)"
      if [[ "${count_failures}" -ge "${MAX_COUNT_FAILURES}" ]]; then
        log "max_count_failures_reached failures=${count_failures}"
        write_status "failed" "0" "$(live_import_pid || true)"
        return 1
      fi
      sleep "${SLEEP_SECONDS}"
      continue
    fi
    if ! [[ "${count}" =~ ^[0-9]+$ ]]; then
      count_failures=$((count_failures + 1))
      log "count_fragments_non_numeric value=${count} failures=${count_failures}/${MAX_COUNT_FAILURES}"
      write_status "count_failed" "0" "$(live_import_pid || true)"
      if [[ "${count_failures}" -ge "${MAX_COUNT_FAILURES}" ]]; then
        log "max_count_failures_reached failures=${count_failures}"
        write_status "failed" "0" "$(live_import_pid || true)"
        return 1
      fi
      sleep "${SLEEP_SECONDS}"
      continue
    fi
    count_failures=0
    local rate_per_minute=""
    local eta_seconds=""
    if [[ -n "${previous_count}" && -n "${previous_epoch}" && "${now_epoch}" -gt "${previous_epoch}" && "${count}" -ge "${previous_count}" ]]; then
      local delta_count=$((count - previous_count))
      local delta_seconds=$((now_epoch - previous_epoch))
      if [[ "${delta_count}" -gt 0 && "${delta_seconds}" -gt 0 ]]; then
        rate_per_minute="$(awk -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { printf "%.2f", c * 60 / s }')"
        eta_seconds="$(awk -v remaining="$((TARGET - count))" -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { if (c > 0) printf "%.0f", remaining * s / c }')"
      fi
    fi
    previous_count="${count}"
    previous_epoch="${now_epoch}"
    if [[ "${count}" == "${TARGET}" ]]; then
      log "full_import_verified fragments=${count}/${TARGET}"
      write_status "full_import_verified" "${count}" "$(live_import_pid || true)" "${rate_per_minute}" "${eta_seconds}"
      run_baseline
      write_status "done" "${count}" "" "${rate_per_minute}" "0"
      log "done"
      return 0
    fi
    if [[ "${count}" -gt "${TARGET}" ]]; then
      log "fragment_count_exceeds_target fragments=${count}/${TARGET}; refusing_eval"
      write_status "failed" "${count}" "$(live_import_pid || true)" "${rate_per_minute}" "${eta_seconds}"
      return 1
    fi

    if pid="$(live_import_pid)"; then
      log "import_running pid=${pid} fragments=${count}/${TARGET}"
      write_status "import_running" "${count}" "${pid}" "${rate_per_minute}" "${eta_seconds}"
    else
      if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
        log "max_import_restarts_reached restarts=${restarts} fragments=${count}/${TARGET}"
        write_status "failed" "${count}" "" "${rate_per_minute}" "${eta_seconds}"
        return 1
      fi
      restarts=$((restarts + 1))
      log "import_not_running restart=${restarts} fragments=${count}/${TARGET}"
      write_status "import_restarting" "${count}" "" "${rate_per_minute}" "${eta_seconds}"
      start_import
    fi
    sleep "${SLEEP_SECONDS}"
  done
}

main "$@"
