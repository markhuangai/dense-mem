#!/usr/bin/env bash
set -euo pipefail

SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
ROOT_DIR="$(cd "${EVAL_COMPOSE_ROOT:-${SCRIPT_ROOT}}" && pwd)"
RUNNER_ROOT_DIR="$(cd "${EVAL_RUNNER_ROOT_DIR:-${ROOT_DIR}}" && pwd)"
cd "${ROOT_DIR}"

: "${SEED:?Set SEED to the seed_manifest.json path}"
: "${SUITE:?Set SUITE to the suite JSONL path}"

if [[ ! -f "${SEED}" ]]; then
  echo "seed manifest not found: ${SEED}" >&2
  exit 2
fi
if [[ ! -f "${SUITE}" ]]; then
  echo "suite not found: ${SUITE}" >&2
  exit 2
fi

SEED="$(realpath "${SEED}")"
SUITE="$(realpath "${SUITE}")"
EVAL_DATA_DIR="$(realpath -m "${EVAL_DATA_DIR:-${V1_DATA_DIR:-tests/eval/.runtime/v1}}")"
export EVAL_COMPOSE_DATA_DIR="$(realpath -m "${EVAL_COMPOSE_DATA_DIR:-${V1_COMPOSE_DATA_DIR:-${EVAL_DATA_DIR}}}")"
V1_DATA_DIR="${EVAL_DATA_DIR}"
export V1_COMPOSE_DATA_DIR="${EVAL_COMPOSE_DATA_DIR}"
export V2_COMPOSE_DATA_DIR="${V2_COMPOSE_DATA_DIR:-${EVAL_COMPOSE_DATA_DIR}}"

IMPORT_DIR="${IMPORT_DIR:-${V1_DATA_DIR}/runs/import}"
BASELINE_DIR="${BASELINE_DIR:-${V1_DATA_DIR}/runs/baseline}"
MONITOR_DIR="${MONITOR_DIR:-${V1_DATA_DIR}/monitor}"
PROFILE_PATH="${PROFILE_PATH:-${V1_DATA_DIR}/eval_profile.json}"
RUNNER="${RUNNER:-${V1_DATA_DIR}/tools/eval-runner}"
IMPORT_CONCURRENCY="${IMPORT_CONCURRENCY:-30}"
PLACEMENT_TIMEOUT="${PLACEMENT_TIMEOUT:-10m}"
SLEEP_SECONDS="${SLEEP_SECONDS:-60}"
MAX_IMPORT_RESTARTS="${MAX_IMPORT_RESTARTS:-100}"
MAX_COUNT_FAILURES="${MAX_COUNT_FAILURES:-12}"
AI_JUDGE_ENABLED="${AI_JUDGE_ENABLED:-true}"
AI_JUDGE_MODEL="${AI_JUDGE_MODEL:-gpt-5.5}"
AI_JUDGE_CONCURRENCY="${AI_JUDGE_CONCURRENCY:-10}"
AI_JUDGE_TIMEOUT="${AI_JUDGE_TIMEOUT:-2m}"
EVAL_RUN_MODE="${EVAL_RUN_MODE:-baseline}"
EVAL_SERVER_REVISION="${EVAL_SERVER_REVISION:-unknown}"
EVAL_INSTRUMENTATION_SHA256="${EVAL_INSTRUMENTATION_SHA256:-none}"
EVAL_RUNNER_REVISION="${EVAL_RUNNER_REVISION:-unknown}"
EVAL_RUNNER_WORKTREE_SHA256="${EVAL_RUNNER_WORKTREE_SHA256:-unknown}"

if [[ "${EVAL_RUN_MODE}" != "baseline" && "${EVAL_RUN_MODE}" != "candidate" ]]; then
  echo "EVAL_RUN_MODE must be baseline or candidate" >&2
  exit 2
fi

IDENTITY_JSON="${V1_DATA_DIR}/dataset_identity.json"
VALIDATION_DIR="${MONITOR_DIR}/validation"
LOG="${MONITOR_DIR}/monitor.log"
STATUS_JSON="${MONITOR_DIR}/status.json"
PLACEMENT_SUMMARY="${MONITOR_DIR}/placement_summary.json"
RESUME_SOURCE_DOC_IDS="${MONITOR_DIR}/completed_source_doc_ids.txt"
FAILED_SOURCE_DOC_IDS="${MONITOR_DIR}/failed_source_doc_ids.txt"
RECOVERED_MAPPING="${MONITOR_DIR}/completed_knowledge_mapping.json"

mkdir -p "${IMPORT_DIR}" "${BASELINE_DIR}" "${MONITOR_DIR}" "${VALIDATION_DIR}" "$(dirname "${RUNNER}")"

TARGET="$(jq -r '.counts.corpus // empty' "${SEED}")"
if ! [[ "${TARGET}" =~ ^[1-9][0-9]*$ ]]; then
  echo "seed manifest must contain a positive counts.corpus: ${SEED}" >&2
  exit 2
fi

export DENSE_MEM_PORT="${DENSE_MEM_PORT:-18080}"
export CONTROL_PORTAL_PORT="${CONTROL_PORTAL_PORT:-18090}"
export POSTGRES_HOST_PORT="${POSTGRES_HOST_PORT:-15432}"
export REDIS_PORT="${REDIS_PORT:-16380}"
export PROMETHEUS_PORT="${PROMETHEUS_PORT:-19090}"
export PROMETHEUS_CONTAINER_NAME="${PROMETHEUS_CONTAINER_NAME:-densemem-eval-v1-prometheus}"
export DENSE_MEM_EVAL_TOOL_TRANSPORT="${DENSE_MEM_EVAL_TOOL_TRANSPORT:-mcp}"
export EVAL_COMPOSE_PROJECT="${EVAL_COMPOSE_PROJECT:-densemem_eval_full}"
export EVAL_COMPOSE_OVERRIDE="${EVAL_COMPOSE_OVERRIDE:-tests/eval/docker-compose.eval.yml}"

# shellcheck source=tests/eval/scripts/run_full_public_rag_eval_lib.sh
. "${SCRIPT_ROOT}/tests/eval/scripts/run_full_public_rag_eval_lib.sh"

prepare_identity() {
  "${RUNNER}" \
    --mode validate \
    --seed "${SEED}" \
    --suite "${SUITE}" \
    --out "${VALIDATION_DIR}"

  SEED_HASH="$(jq -r '.seed_hash' "${VALIDATION_DIR}/summary.json")"
  SUITE_HASH="$(sha256sum "${SUITE}" | awk '{print $1}')"
  EMBEDDING_MODEL="${AI_API_EMBEDDING_MODEL:-}"
  EMBEDDING_DIMENSIONS="${AI_API_EMBEDDING_DIMENSIONS:-1536}"
  if [[ "${EMBEDDING_DIMENSIONS}" == "0" ]]; then
    EMBEDDING_DIMENSIONS="1536"
  fi
  EMBEDDING_WORKER_COUNT="${EMBEDDING_WORKER_COUNT:-2}"
  EMBEDDING_BATCH_SIZE="${EMBEDDING_BATCH_SIZE:-64}"
  EMBEDDING_MAX_CONCURRENCY="${AI_API_EMBEDDING_MAX_CONCURRENCY:-8}"
  EMBEDDING_ENDPOINT_HASH="$(printf '%s' "${AI_API_URL:-}" | sha256sum | awk '{print $1}')"
  export SEED_HASH SUITE_HASH

  local candidate="${MONITOR_DIR}/requested_dataset_identity.json"
  jq -n \
    --arg seed_id "$(jq -r '.seed_id' "${SEED}")" \
    --arg seed_hash "${SEED_HASH}" \
    --arg suite_sha256 "${SUITE_HASH}" \
    --arg embedding_model "${EMBEDDING_MODEL}" \
    --arg embedding_dimensions "${EMBEDDING_DIMENSIONS}" \
    --arg embedding_worker_count "${EMBEDDING_WORKER_COUNT}" \
    --arg embedding_batch_size "${EMBEDDING_BATCH_SIZE}" \
    --arg embedding_max_concurrency "${EMBEDDING_MAX_CONCURRENCY}" \
    --arg embedding_endpoint_sha256 "${EMBEDDING_ENDPOINT_HASH}" \
    --arg verifier_model "${AI_VERIFIER_MODEL:-}" \
    --arg judge_model "${AI_JUDGE_MODEL}" \
    --arg run_mode "${EVAL_RUN_MODE}" \
    --arg server_revision "${EVAL_SERVER_REVISION}" \
    --arg instrumentation_sha256 "${EVAL_INSTRUMENTATION_SHA256}" \
    --arg runner_revision "${EVAL_RUNNER_REVISION}" \
    --arg runner_worktree_sha256 "${EVAL_RUNNER_WORKTREE_SHA256}" \
    --arg team_id "${EVAL_TEAM_ID}" \
    --arg tool_transport "${DENSE_MEM_EVAL_TOOL_TRANSPORT}" \
    '{
      seed_id: $seed_id,
      seed_hash: $seed_hash,
      suite_sha256: $suite_sha256,
      embedding_model: $embedding_model,
      embedding_dimensions: ($embedding_dimensions | tonumber),
      embedding_worker_count: ($embedding_worker_count | tonumber),
      embedding_batch_size: ($embedding_batch_size | tonumber),
      embedding_max_concurrency: ($embedding_max_concurrency | tonumber),
      embedding_endpoint_sha256: $embedding_endpoint_sha256,
      verifier_model: $verifier_model,
      judge_model: $judge_model,
      run_mode: $run_mode,
      server_revision: $server_revision,
      instrumentation_sha256: $instrumentation_sha256,
      runner_revision: $runner_revision,
      runner_worktree_sha256: $runner_worktree_sha256,
      team_id: $team_id,
      import_route: "remember",
      tool_transport: $tool_transport
    }' > "${candidate}"

  if [[ -f "${IDENTITY_JSON}" ]]; then
    if ! cmp -s "${IDENTITY_JSON}" "${candidate}"; then
      echo "eval runtime identity does not match the requested seed, suite, embedding config, or team" >&2
      diff -u <(jq -S . "${IDENTITY_JSON}") <(jq -S . "${candidate}") >&2 || true
      return 1
    fi
    return 0
  fi

  local snapshot latest completed failed queued processing attempts mapped_refs
  snapshot="$(placement_counts)"
  IFS='|' read -r latest completed failed queued processing attempts <<< "${snapshot}"
  mapped_refs="$(count_mapped_refs)"
  if [[ "${latest}" != "0" || "${mapped_refs}" != "0" ]]; then
    echo "eval runtime already contains data but has no dataset identity; refusing to adopt it" >&2
    return 1
  fi
  mv "${candidate}" "${IDENTITY_JSON}"
}

live_import_pid() {
  if [[ ! -f "${IMPORT_DIR}/import.pid" ]]; then
    return 1
  fi
  local pid command
  pid="$(cat "${IMPORT_DIR}/import.pid")"
  if [[ -z "${pid}" ]] || ! kill -0 "${pid}" 2>/dev/null; then
    return 1
  fi
  command="$(ps -ww -p "${pid}" -o args= 2>/dev/null || true)"
  if [[ "${command}" != *"${RUNNER}"* || "${command}" != *"--mode import"* ]]; then
    return 1
  fi
  printf '%s\n' "${pid}"
}

start_import() {
  local completed="$1"
  local recovered_mapping=""
  local -a args
  if [[ "${completed}" -gt 0 && "${embedding_tables:-0}" == "1" ]]; then
    write_completed_mapping "${completed}"
    recovered_mapping="${RECOVERED_MAPPING}"
  fi
  args=(
    "${RUNNER}"
    --mode import
    --seed "${SEED}"
    --suite "${SUITE}"
    --out "${IMPORT_DIR}"
    --import-seed
    --import-concurrency "${IMPORT_CONCURRENCY}"
    --placement-timeout "${PLACEMENT_TIMEOUT}"
    --resume-source-doc-ids "${RESUME_SOURCE_DOC_IDS}"
    --tool-transport "${DENSE_MEM_EVAL_TOOL_TRANSPORT}"
    --max-page-size 500
  )
  if [[ -n "${recovered_mapping}" ]]; then
    args+=(--mapping "${recovered_mapping}")
  fi
  printf '\nresume remember import with concurrency=%s placement_timeout=%s at %s\n' \
    "${IMPORT_CONCURRENCY}" "${PLACEMENT_TIMEOUT}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${IMPORT_DIR}/import.log"
  setsid "${args[@]}" 9>&- >> "${IMPORT_DIR}/import.log" 2>&1 < /dev/null &
  printf '%s\n' "$!" > "${IMPORT_DIR}/import.pid"
  log "started_import pid=$(cat "${IMPORT_DIR}/import.pid") route=remember transport=${DENSE_MEM_EVAL_TOOL_TRANSPORT}"
}

run_baseline() {
  local -a judge_args=()
  local -a recall_source_args=(--mapping "${IMPORT_DIR}/knowledge_mapping.json")
  if [[ "${AI_JUDGE_ENABLED}" == "true" ]]; then
    judge_args=(
      --ai-judge
      --judge-model "${AI_JUDGE_MODEL}"
      --judge-concurrency "${AI_JUDGE_CONCURRENCY}"
      --judge-timeout "${AI_JUDGE_TIMEOUT}"
    )
  fi
  if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" || ! -s "${IMPORT_DIR}/summary.json" ]]; then
    log "import_artifacts_missing"
    return 1
  fi
  if [[ "$(jq -r '.seed_hash // empty' "${IMPORT_DIR}/summary.json")" != "${SEED_HASH}" ]]; then
    log "import_artifact_seed_hash_mismatch"
    return 1
  fi
  if [[ "$(jq -r '.import_route // empty' "${IMPORT_DIR}/run_config.json")" != "remember" ]]; then
    log "import_artifact_route_is_not_remember"
    return 1
  fi
  if [[ "$(jq -r '.tool_transport // empty' "${IMPORT_DIR}/run_config.json")" != "${DENSE_MEM_EVAL_TOOL_TRANSPORT}" ]]; then
    log "import_artifact_transport_mismatch expected=${DENSE_MEM_EVAL_TOOL_TRANSPORT}"
    return 1
  fi
  if [[ -s "${BASELINE_DIR}/summary.json" ]]; then
    if [[ "$(jq -r '.seed_hash // empty' "${BASELINE_DIR}/summary.json")" != "${SEED_HASH}" ]]; then
      log "baseline_seed_hash_mismatch path=${BASELINE_DIR}/summary.json"
      return 1
    fi
    if [[ "$(jq -r '.tool_transport // empty' "${BASELINE_DIR}/run_config.json")" != "${DENSE_MEM_EVAL_TOOL_TRANSPORT}" ]]; then
      log "baseline_transport_mismatch expected=${DENSE_MEM_EVAL_TOOL_TRANSPORT}"
      return 1
    fi
    if cmp -s \
      <(jq -cS . "${IMPORT_DIR}/knowledge_mapping.json") \
      <(jq -cS . "${BASELINE_DIR}/knowledge_mapping.json"); then
      if [[ "${AI_JUDGE_ENABLED}" != "true" ]]; then
        log "baseline_summary_exists path=${BASELINE_DIR}/summary.json"
        return 0
      fi
      if [[ -s "${BASELINE_DIR}/judge_summary.json" ]] && \
        [[ "$(jq -r '.model // empty' "${BASELINE_DIR}/judge_summary.json")" == "${AI_JUDGE_MODEL}" ]]; then
        log "baseline_summary_exists path=${BASELINE_DIR}/summary.json judge_model=${AI_JUDGE_MODEL}"
        return 0
      fi
      log "baseline_judge_missing_or_mismatched rerunning path=${BASELINE_DIR}/judge_summary.json model=${AI_JUDGE_MODEL}"
      if [[ -s "${BASELINE_DIR}/recall_traces.jsonl" && -s "${BASELINE_DIR}/knowledge_mapping.json" ]]; then
        recall_source_args=(
          --traces "${BASELINE_DIR}/recall_traces.jsonl"
          --mapping "${BASELINE_DIR}/knowledge_mapping.json"
        )
        log "baseline_judge_resume_uses_existing_production_traces"
      fi
    else
      log "baseline_mapping_changed_rerunning path=${BASELINE_DIR}/summary.json"
    fi
  fi

  log "starting_baseline_eval"
  if ! "${RUNNER}" \
    --mode "${EVAL_RUN_MODE}" \
    --seed "${SEED}" \
    --suite "${SUITE}" \
    --out "${BASELINE_DIR}" \
    "${recall_source_args[@]}" \
    --tool-transport "${DENSE_MEM_EVAL_TOOL_TRANSPORT}" \
    --max-page-size 500 \
    "${judge_args[@]}"; then
    log "baseline_eval_failed"
    return 1
  fi
  if [[ ! -s "${BASELINE_DIR}/summary.json" ]]; then
    log "baseline_summary_missing_after_runner_success"
    return 1
  fi
  if [[ "${AI_JUDGE_ENABLED}" == "true" ]]; then
    if [[ ! -s "${BASELINE_DIR}/judge_summary.json" ]]; then
      log "baseline_judge_summary_missing_after_runner_success"
      return 1
    fi
    if [[ "$(jq -r '.model // empty' "${BASELINE_DIR}/judge_summary.json")" != "${AI_JUDGE_MODEL}" ]]; then
      log "baseline_judge_model_mismatch expected=${AI_JUDGE_MODEL}"
      return 1
    fi
  fi
  log "baseline_eval_finished path=${BASELINE_DIR}/summary.json"
}

main() {
  exec 9> "${MONITOR_DIR}/monitor.lock"
  if ! flock -n 9; then
    echo "another eval monitor is already running for ${MONITOR_DIR}" >&2
    return 1
  fi
  load_env
  ensure_runner
  if ! compose exec -T postgres true || ! compose exec -T server true; then
    echo "eval stack is not running; start it with the eval compose override" >&2
    return 1
  fi
  prepare_identity

  printf '%s\n' "$$" > "${MONITOR_DIR}/monitor.pid"
  log "monitor_started target=${TARGET} seed_hash=${SEED_HASH}"

  local restarts=0 count_failures=0 previous_completed="" previous_epoch=""
  while true; do
    local now_epoch snapshot latest completed failed queued processing attempts mapped_refs
    local embedding_snapshot embedding_tables embedding_jobs_total embedding_jobs_completed embedding_jobs_failed embedding_jobs_queued embedding_jobs_processing
    local search_docs_total search_docs_current search_docs_pending search_docs_failed
    now_epoch="$(date +%s)"
    if ! snapshot="$(placement_counts)" || ! mapped_refs="$(count_mapped_refs)" || ! embedding_snapshot="$(embedding_counts)"; then
      count_failures=$((count_failures + 1))
      log "placement_or_mapping_count_failed failures=${count_failures}/${MAX_COUNT_FAILURES}"
      if [[ "${count_failures}" -ge "${MAX_COUNT_FAILURES}" ]]; then
        log "max_count_failures_reached failures=${count_failures}"
        return 1
      fi
      sleep "${SLEEP_SECONDS}"
      continue
    fi
    IFS='|' read -r latest completed failed queued processing attempts <<< "${snapshot}"
    IFS='|' read -r embedding_tables embedding_jobs_total embedding_jobs_completed embedding_jobs_failed embedding_jobs_queued embedding_jobs_processing search_docs_total search_docs_current search_docs_pending search_docs_failed <<< "${embedding_snapshot}"
    for value in "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "${mapped_refs}" \
      "${embedding_tables}" "${embedding_jobs_total}" "${embedding_jobs_completed}" "${embedding_jobs_failed}" \
      "${embedding_jobs_queued}" "${embedding_jobs_processing}" "${search_docs_total}" "${search_docs_current}" \
      "${search_docs_pending}" "${search_docs_failed}"; do
      if ! [[ "${value}" =~ ^[0-9]+$ ]]; then
        log "non_numeric_monitor_count value=${value}"
        return 1
      fi
    done
    count_failures=0

    write_resume_files
    write_placement_summary

    local rate_per_minute="" eta_seconds=""
    if [[ -n "${previous_completed}" && -n "${previous_epoch}" && "${now_epoch}" -gt "${previous_epoch}" && "${completed}" -ge "${previous_completed}" ]]; then
      local delta_count=$((completed - previous_completed))
      local delta_seconds=$((now_epoch - previous_epoch))
      if [[ "${delta_count}" -gt 0 ]]; then
        rate_per_minute="$(awk -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { printf "%.2f", c * 60 / s }')"
        eta_seconds="$(awk -v remaining="$((TARGET - completed))" -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { if (c > 0) printf "%.0f", remaining * s / c }')"
      fi
    fi
    previous_completed="${completed}"
    previous_epoch="${now_epoch}"

    if [[ "${latest}" -gt "${TARGET}" || "${mapped_refs}" -gt "${TARGET}" ]]; then
      log "dataset_count_exceeds_target latest=${latest}/${TARGET} mapped_refs=${mapped_refs}/${TARGET}; refusing_eval"
      write_status "failed" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "$(live_import_pid || true)" "${rate_per_minute}" "${eta_seconds}"
      return 1
    fi

    local pid=""
    pid="$(live_import_pid || true)"
    if [[ "${latest}" == "${TARGET}" && "${completed}" == "${TARGET}" && "${failed}" == "0" && "${queued}" == "0" && "${processing}" == "0" && "${mapped_refs}" == "${TARGET}" ]]; then
      if [[ -n "${pid}" ]]; then
        log "import_finalizing pid=${pid} completed=${completed}/${TARGET}"
        write_status "import_finalizing" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "${pid}" "${rate_per_minute}" "0"
        sleep "${SLEEP_SECONDS}"
        continue
      fi
      if [[ "${embedding_tables}" == "1" ]]; then
        write_completed_mapping "${completed}"
        if ! completed_mapping_matches_import; then
          if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
            log "max_import_restarts_reached while refreshing completed mapping"
            return 1
          fi
          restarts=$((restarts + 1))
          log "completed_mapping_artifact_stale restart=${restarts}"
          start_import "${completed}"
          sleep "${SLEEP_SECONDS}"
          continue
        fi
      fi
      if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" || ! -s "${IMPORT_DIR}/summary.json" ]]; then
        if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
          log "max_import_restarts_reached while finalizing artifacts"
          return 1
        fi
        restarts=$((restarts + 1))
        log "placements_complete_but_import_artifacts_missing restart=${restarts}"
        start_import "${completed}"
        sleep "${SLEEP_SECONDS}"
        continue
      fi
      if [[ "${embedding_tables}" == "1" ]]; then
        if [[ "${search_docs_total}" == "0" ]]; then
          log "semantic_search_documents_missing; refusing_eval"
          write_status "failed" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
          return 1
        fi
        if [[ "${embedding_jobs_failed}" != "0" || "${search_docs_failed}" != "0" ]]; then
          log "semantic_embedding_failed jobs_failed=${embedding_jobs_failed} search_docs_failed=${search_docs_failed}; refusing_eval"
          write_status "failed" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
          return 1
        fi
        if [[ "${search_docs_pending}" != "0" && $((embedding_jobs_queued + embedding_jobs_processing)) -eq 0 ]]; then
          log "semantic_search_documents_stuck pending=${search_docs_pending} active_jobs=0; refusing_eval"
          write_status "failed" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
          return 1
        fi
        if [[ "${embedding_jobs_queued}" != "0" || "${embedding_jobs_processing}" != "0" || "${search_docs_pending}" != "0" ]]; then
          log "semantic_embedding_draining jobs_queued=${embedding_jobs_queued} jobs_processing=${embedding_jobs_processing} search_docs_pending=${search_docs_pending}"
          write_status "semantic_embedding_draining" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
          sleep "${SLEEP_SECONDS}"
          continue
        fi
      fi
      log "full_import_verified placements=${completed}/${TARGET} mapped_refs=${mapped_refs}/${TARGET} attempts=${attempts} embedding_jobs_completed=${embedding_jobs_completed} search_docs_current=${search_docs_current}"
      write_status "full_import_verified" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
      if ! run_baseline; then
        write_status "failed" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
        return 1
      fi
      write_status "done" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
      log "done"
      return 0
    fi

    if [[ -n "${pid}" ]]; then
      log "import_running pid=${pid} completed=${completed}/${TARGET} failed=${failed} pending=$((queued + processing)) mapped_refs=${mapped_refs}"
      write_status "import_running" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "${pid}" "${rate_per_minute}" "${eta_seconds}"
    elif [[ $((queued + processing)) -gt 0 ]]; then
      log "placement_worker_draining completed=${completed}/${TARGET} queued=${queued} processing=${processing}; import_not_restarted"
      write_status "placement_worker_draining" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
    else
      if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
        log "max_import_restarts_reached restarts=${restarts} completed=${completed}/${TARGET}"
        write_status "failed" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
        return 1
      fi
      restarts=$((restarts + 1))
      log "import_not_running restart=${restarts} completed=${completed}/${TARGET} failed=${failed} unseen=$((TARGET - latest))"
      write_status "import_restarting" "${mapped_refs}" "${latest}" "${completed}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
      start_import "${completed}"
    fi
    sleep "${SLEEP_SECONDS}"
  done
}

main "$@"
