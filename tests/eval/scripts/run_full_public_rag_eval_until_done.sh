#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT_DIR}"

: "${SEED:?Set SEED to the v2 seed_manifest.json path}"
: "${SUITE:?Set SUITE to the v2 suite JSONL path}"

if [[ ! -f "${SEED}" ]]; then
  echo "seed manifest not found: ${SEED}" >&2
  exit 2
fi
if [[ ! -f "${SUITE}" ]]; then
  echo "suite not found: ${SUITE}" >&2
  exit 2
fi
if [[ -n "${RELEASE_GATE_POLICY:-}" && ! -f "${RELEASE_GATE_POLICY}" ]]; then
  echo "release gate policy not found: ${RELEASE_GATE_POLICY}" >&2
  exit 2
fi

SEED="$(realpath "${SEED}")"
SUITE="$(realpath "${SUITE}")"
if [[ -n "${RELEASE_GATE_POLICY:-}" ]]; then
  RELEASE_GATE_POLICY="$(realpath "${RELEASE_GATE_POLICY}")"
fi

EVAL_DATA_DIR="$(realpath -m "${EVAL_DATA_DIR:-tests/eval/runtime/v2}")"
export EVAL_COMPOSE_DATA_DIR="$(realpath -m "${EVAL_COMPOSE_DATA_DIR:-${EVAL_DATA_DIR}}")"
EVAL_COMPOSE_PROJECT="${EVAL_COMPOSE_PROJECT:-densemem_eval_submission}"

IMPORT_DIR="${IMPORT_DIR:-${EVAL_DATA_DIR}/runs/import}"
BASELINE_DIR="${BASELINE_DIR:-${EVAL_DATA_DIR}/runs/baseline}"
MONITOR_DIR="${MONITOR_DIR:-${EVAL_DATA_DIR}/monitor}"
PROFILE_PATH="${PROFILE_PATH:-${EVAL_DATA_DIR}/eval_profile.json}"
RUNNER="${RUNNER:-${EVAL_DATA_DIR}/tools/eval-runner}"
IMPORT_CONCURRENCY="${IMPORT_CONCURRENCY:-3}"
SUBMISSION_TIMEOUT="${SUBMISSION_TIMEOUT:-10m}"
SLEEP_SECONDS="${SLEEP_SECONDS:-60}"
MAX_IMPORT_RESTARTS="${MAX_IMPORT_RESTARTS:-100}"
MAX_COUNT_FAILURES="${MAX_COUNT_FAILURES:-12}"

IDENTITY_JSON="${EVAL_DATA_DIR}/dataset_identity.json"
VALIDATION_DIR="${MONITOR_DIR}/validation"
LOG="${MONITOR_DIR}/monitor.log"
STATUS_JSON="${MONITOR_DIR}/status.json"
SUBMISSION_SUMMARY="${MONITOR_DIR}/submission_summary.json"
RESUME_SOURCE_DOC_IDS="${MONITOR_DIR}/completed_source_doc_ids.txt"
BLOCKED_SOURCE_DOC_IDS="${MONITOR_DIR}/blocked_source_doc_ids.txt"

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
export PROMETHEUS_CONTAINER_NAME="${PROMETHEUS_CONTAINER_NAME:-densemem-eval-submission-prometheus}"

compose() {
  docker compose -p "${EVAL_COMPOSE_PROJECT}" -f docker-compose.yml -f tests/eval/docker-compose.eval.yml "$@"
}

compose_service_host_port() {
  local service="$1" container_port="$2" mapping=""
  mapping="$(compose port "${service}" "${container_port}")"
  mapping="${mapping%%$'\n'*}"
  if [[ "${mapping}" =~ ^[^:]+:([0-9]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
    return 0
  fi
  if [[ "${mapping}" =~ ^\[[^]]+\]:([0-9]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
    return 0
  fi
  echo "could not resolve ${service}:${container_port} host port from compose mapping ${mapping}" >&2
  return 1
}

canonical_json_sha256() {
  local digest
  digest="$(jq -cS -j . "$1" | sha256sum | awk '{print $1}')"
  printf 'sha256:%s\n' "${digest}"
}

server_image_id() {
  local container_id
  container_id="$(compose ps -q server)"
  if [[ -z "${container_id}" ]]; then
    return 1
  fi
  docker inspect --format '{{.Image}}' "${container_id}"
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

load_env() {
  local eval_dense_mem_port="${DENSE_MEM_PORT}"
  local eval_control_portal_port="${CONTROL_PORTAL_PORT}"
  local eval_postgres_host_port="${POSTGRES_HOST_PORT}"
  local eval_redis_port="${REDIS_PORT}"
  local eval_prometheus_port="${PROMETHEUS_PORT}"
  local eval_prometheus_container_name="${PROMETHEUS_CONTAINER_NAME}"
  local eval_compose_data_dir="${EVAL_COMPOSE_DATA_DIR}"
  local requested_api_key="${DENSE_MEM_API_KEY:-}"
  local requested_base_url="${DENSE_MEM_BASE_URL:-}"
  local requested_control_url="${DENSE_MEM_CONTROL_URL:-}"
  local requested_control_token="${DENSE_MEM_CONTROL_TOKEN:-}"
  local requested_team_id="${EVAL_TEAM_ID:-}"
  if [[ ! -f .env ]]; then
    echo ".env is required for the eval stack" >&2
    return 1
  fi
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a

  export DENSE_MEM_PORT="${eval_dense_mem_port}"
  export CONTROL_PORTAL_PORT="${eval_control_portal_port}"
  export POSTGRES_HOST_PORT="${eval_postgres_host_port}"
  export REDIS_PORT="${eval_redis_port}"
  export PROMETHEUS_PORT="${eval_prometheus_port}"
  export PROMETHEUS_CONTAINER_NAME="${eval_prometheus_container_name}"
  export EVAL_COMPOSE_DATA_DIR="${eval_compose_data_dir}"
  if [[ -n "${requested_api_key}" ]]; then
    export DENSE_MEM_API_KEY="${requested_api_key}"
  fi
  if [[ -n "${requested_base_url}" ]]; then
    export DENSE_MEM_BASE_URL="${requested_base_url}"
  else
    unset DENSE_MEM_BASE_URL
  fi
  if [[ -n "${requested_control_url}" ]]; then
    export DENSE_MEM_CONTROL_URL="${requested_control_url}"
  else
    unset DENSE_MEM_CONTROL_URL
  fi
  if [[ -n "${requested_control_token}" ]]; then
    export DENSE_MEM_CONTROL_TOKEN="${requested_control_token}"
  fi
  if [[ -n "${requested_team_id}" ]]; then
    export EVAL_TEAM_ID="${requested_team_id}"
  fi

  export DENSE_MEM_CONTROL_TOKEN="${DENSE_MEM_CONTROL_TOKEN:-${CONTROL_PORTAL_TOKEN:-}}"
  if [[ -z "${DENSE_MEM_API_KEY:-}" && -f "${PROFILE_PATH}" ]]; then
    DENSE_MEM_API_KEY="$(jq -r '.api_key // empty' "${PROFILE_PATH}")"
    export DENSE_MEM_API_KEY
  fi
  if [[ -z "${EVAL_TEAM_ID:-}" && -f "${PROFILE_PATH}" ]]; then
    EVAL_TEAM_ID="$(jq -r '.team_id // empty' "${PROFILE_PATH}")"
    export EVAL_TEAM_ID
  fi

  : "${DENSE_MEM_API_KEY:?Set DENSE_MEM_API_KEY or provide PROFILE_PATH with api_key}"
  : "${DENSE_MEM_CONTROL_TOKEN:?Set DENSE_MEM_CONTROL_TOKEN or CONTROL_PORTAL_TOKEN}"
  : "${EVAL_TEAM_ID:?Set EVAL_TEAM_ID or provide PROFILE_PATH with team_id}"
  if ! [[ "${EVAL_TEAM_ID}" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]; then
    echo "invalid eval team UUID" >&2
    return 1
  fi
}

resolve_evaluator_urls() {
  local api_port control_port
  if [[ -z "${DENSE_MEM_BASE_URL:-}" ]]; then
    api_port="$(compose_service_host_port server 8080)"
    export DENSE_MEM_BASE_URL="http://127.0.0.1:${api_port}"
  fi
  if [[ -z "${DENSE_MEM_CONTROL_URL:-}" ]]; then
    control_port="$(compose_service_host_port server 8090)"
    export DENSE_MEM_CONTROL_URL="http://127.0.0.1:${control_port}"
  fi
}

ensure_runner() {
  local temporary_runner="${RUNNER}.tmp"
  go build -o "${temporary_runner}" ./cmd/eval-runner
  mv "${temporary_runner}" "${RUNNER}"
}

psql_eval() {
  local sql="$1"
  printf '%s\n' "${sql}" | compose exec -T postgres \
    psql -v ON_ERROR_STOP=1 \
      -U "${POSTGRES_USER:-densemem}" \
      -d "${POSTGRES_DB:-densemem}" \
      -v "team_id=${EVAL_TEAM_ID}" \
      -At -F '|'
}

submission_search_cte() {
  cat <<'SQL'
WITH submissions AS (
  SELECT submission_id, status, canonical_ingest_id, idempotency_key
  FROM submission_runs
  WHERE team_id = :'team_id'::uuid
    AND idempotency_key LIKE 'eval:%'
), search_state AS (
  SELECT submission.submission_id,
         CASE
           WHEN count(linked.search_document_id) = 0 THEN 'not_required'
           WHEN bool_or(document.search_document_id IS NULL OR document.search_state = 'failed') THEN 'failed'
           WHEN bool_or(document.search_state = 'pending') THEN 'pending'
           WHEN bool_or(document.search_state = 'current') THEN 'current'
           ELSE 'not_required'
         END AS search_state
  FROM submissions AS submission
  LEFT JOIN placement_items AS item
    ON item.team_id = :'team_id'::uuid
   AND item.ingest_id = submission.canonical_ingest_id
  LEFT JOIN LATERAL jsonb_array_elements_text(
    CASE
      WHEN jsonb_typeof(item.result -> 'search_document_ids') = 'array'
        THEN item.result -> 'search_document_ids'
      ELSE '[]'::jsonb
    END
  ) AS linked(search_document_id) ON true
  LEFT JOIN search_documents AS document
    ON document.team_id = item.team_id
   AND document.owner_profile_id = item.owner_profile_id
   AND document.search_document_id::text = linked.search_document_id
  GROUP BY submission.submission_id
)
SQL
}

submission_counts() {
  psql_eval "
    $(submission_search_cte)
    SELECT count(*) AS total,
           count(*) FILTER (WHERE submission.status = 'completed' AND search.search_state IN ('current', 'not_required')) AS completed_ready,
           count(*) FILTER (WHERE submission.status = 'completed' AND search.search_state = 'pending') AS completed_search_pending,
           count(*) FILTER (WHERE submission.status = 'completed' AND search.search_state = 'failed') AS completed_search_failed,
           count(*) FILTER (WHERE submission.status = 'rejected') AS rejected,
           count(*) FILTER (WHERE submission.status = 'quarantined') AS quarantined,
           count(*) FILTER (WHERE submission.status = 'failed') AS failed,
           count(*) FILTER (WHERE submission.status = 'queued') AS queued,
           count(*) FILTER (WHERE submission.status = 'processing') AS processing
    FROM submissions AS submission
    JOIN search_state AS search ON search.submission_id = submission.submission_id;
  "
}

count_canonical_evidence() {
  psql_eval "
    SELECT count(DISTINCT submission.submission_id)
    FROM submission_runs AS submission
    JOIN evidence_fragments AS evidence
      ON evidence.team_id = submission.team_id
     AND evidence.ingest_id = submission.canonical_ingest_id
    WHERE submission.team_id = :'team_id'::uuid
      AND submission.idempotency_key LIKE 'eval:%'
      AND submission.status = 'completed';
  "
}

write_resume_files() {
  local complete_tmp blocked_tmp
  complete_tmp="$(mktemp "${RESUME_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  blocked_tmp="$(mktemp "${BLOCKED_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  psql_eval "
    $(submission_search_cte)
    SELECT substring(submission.idempotency_key FROM 6)
    FROM submissions AS submission
    JOIN search_state AS search ON search.submission_id = submission.submission_id
    WHERE submission.status = 'completed'
      AND search.search_state IN ('current', 'not_required')
    ORDER BY submission.idempotency_key;
  " > "${complete_tmp}"
  psql_eval "
    SELECT substring(idempotency_key FROM 6)
    FROM submission_runs
    WHERE team_id = :'team_id'::uuid
      AND idempotency_key LIKE 'eval:%'
      AND status IN ('rejected', 'quarantined', 'failed')
    ORDER BY idempotency_key;
  " > "${blocked_tmp}"
  mv "${complete_tmp}" "${RESUME_SOURCE_DOC_IDS}"
  mv "${blocked_tmp}" "${BLOCKED_SOURCE_DOC_IDS}"
}

write_submission_summary() {
  local total="$1" completed_ready="$2" completed_pending="$3" completed_search_failed="$4" rejected="$5" quarantined="$6" failed="$7" queued="$8" processing="$9"
  local temporary_summary
  temporary_summary="$(mktemp "${SUBMISSION_SUMMARY}.tmp.XXXXXX")"
  jq -n \
    --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg total "${total}" \
    --arg completed_ready "${completed_ready}" \
    --arg completed_pending "${completed_pending}" \
    --arg completed_search_failed "${completed_search_failed}" \
    --arg rejected "${rejected}" \
    --arg quarantined "${quarantined}" \
    --arg failed "${failed}" \
    --arg queued "${queued}" \
    --arg processing "${processing}" \
    '{
      generated_at: $generated_at,
      submissions: {
        total: ($total | tonumber),
        completed_ready: ($completed_ready | tonumber),
        completed_search_pending: ($completed_pending | tonumber),
        completed_search_failed: ($completed_search_failed | tonumber),
        rejected: ($rejected | tonumber),
        quarantined: ($quarantined | tonumber),
        failed: ($failed | tonumber),
        queued: ($queued | tonumber),
        processing: ($processing | tonumber)
      }
    }' > "${temporary_summary}"
  mv "${temporary_summary}" "${SUBMISSION_SUMMARY}"
}

write_status() {
  local phase="$1" fragments="$2" total="$3" completed_ready="$4" completed_pending="$5" completed_search_failed="$6" rejected="$7" quarantined="$8" failed="$9" queued="${10}" processing="${11}"
  local import_pid="${12:-}" rate_per_minute="${13:-}" eta_seconds="${14:-}"
  local temporary_status
  temporary_status="$(mktemp "${STATUS_JSON}.tmp.XXXXXX")"
  jq -n \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg phase "${phase}" \
    --arg target "${TARGET}" \
    --arg fragments "${fragments}" \
    --arg total "${total}" \
    --arg completed_ready "${completed_ready}" \
    --arg completed_pending "${completed_pending}" \
    --arg completed_search_failed "${completed_search_failed}" \
    --arg rejected "${rejected}" \
    --arg quarantined "${quarantined}" \
    --arg failed "${failed}" \
    --arg queued "${queued}" \
    --arg processing "${processing}" \
    --arg import_pid "${import_pid}" \
    --arg rate_per_minute "${rate_per_minute}" \
    --arg eta_seconds "${eta_seconds}" \
    --arg import_dir "${IMPORT_DIR}" \
    --arg baseline_dir "${BASELINE_DIR}" \
    --arg submission_summary "${SUBMISSION_SUMMARY}" \
    --arg blocked_source_doc_ids "${BLOCKED_SOURCE_DOC_IDS}" \
    --arg dataset_identity "${IDENTITY_JSON}" \
    'def nullable_number: if . == "" then null else tonumber end;
    {
      updated_at: $updated_at,
      phase: $phase,
      target: ($target | tonumber),
      canonical_evidence: ($fragments | tonumber),
      submissions: {
        total: ($total | tonumber),
        completed_ready: ($completed_ready | tonumber),
        completed_search_pending: ($completed_pending | tonumber),
        completed_search_failed: ($completed_search_failed | tonumber),
        rejected: ($rejected | tonumber),
        quarantined: ($quarantined | tonumber),
        failed: ($failed | tonumber),
        queued: ($queued | tonumber),
        processing: ($processing | tonumber)
      },
      percent_complete: (($completed_ready | tonumber) / ($target | tonumber) * 100),
      rate_per_minute: ($rate_per_minute | nullable_number),
      eta_seconds: ($eta_seconds | nullable_number),
      import_pid: $import_pid,
      import_dir: $import_dir,
      baseline_dir: $baseline_dir,
      submission_summary: $submission_summary,
      blocked_source_doc_ids: $blocked_source_doc_ids,
      dataset_identity: $dataset_identity
    }' > "${temporary_status}"
  mv "${temporary_status}" "${STATUS_JSON}"
}

validate_seed() {
  local -a args
  args=("${RUNNER}" --mode validate --seed "${SEED}" --suite "${SUITE}" --out "${VALIDATION_DIR}")
  if [[ -n "${RELEASE_GATE_POLICY:-}" ]]; then
    args+=(--release-gate-policy "${RELEASE_GATE_POLICY}")
  fi
  "${args[@]}"
  SEED_HASH="$(jq -r '.seed_hash // empty' "${VALIDATION_DIR}/summary.json")"
  if [[ ! "${SEED_HASH}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "seed validation did not produce a valid seed hash" >&2
    return 1
  fi
  export SEED_HASH
}

prepare_identity() {
  local suite_hash embedding_dimensions endpoint_hash release_policy_hash=""
  suite_hash="$(sha256sum "${SUITE}" | awk '{print $1}')"
  embedding_dimensions="${AI_API_EMBEDDING_DIMENSIONS:-3072}"
  if [[ "${embedding_dimensions}" == "0" ]]; then
    embedding_dimensions="3072"
  fi
  endpoint_hash="$(printf '%s' "${AI_API_URL:-}" | sha256sum | awk '{print $1}')"
  if [[ -n "${RELEASE_GATE_POLICY:-}" ]]; then
    release_policy_hash="$(canonical_json_sha256 "${RELEASE_GATE_POLICY}")"
  fi
  local candidate="${MONITOR_DIR}/requested_dataset_identity.json"
  jq -n \
    --arg seed_id "$(jq -r '.seed_id' "${SEED}")" \
    --arg seed_hash "${SEED_HASH}" \
    --arg suite_sha256 "${suite_hash}" \
    --arg embedding_model "${AI_API_EMBEDDING_MODEL:-}" \
    --arg embedding_dimensions "${embedding_dimensions}" \
    --arg embedding_endpoint_sha256 "${endpoint_hash}" \
    --arg assessor_model "${AI_VERIFIER_MODEL:-}" \
    --arg assessor_max_concurrency "${AI_VERIFIER_MAX_CONCURRENCY:-5}" \
    --arg submission_assessment_worker_count "${SUBMISSION_ASSESSMENT_WORKER_COUNT:-1}" \
    --arg import_concurrency "${IMPORT_CONCURRENCY}" \
    --arg team_id "${EVAL_TEAM_ID}" \
    --arg release_gate_policy_sha256 "${release_policy_hash}" \
    --arg runner_sha256 "sha256:$(sha256sum "${RUNNER}" | awk '{print $1}')" \
    --arg server_image_id "${SERVER_IMAGE_ID}" \
    '{
      seed_id: $seed_id,
      seed_hash: $seed_hash,
      suite_sha256: $suite_sha256,
      embedding_model: $embedding_model,
      embedding_dimensions: ($embedding_dimensions | tonumber),
      embedding_endpoint_sha256: $embedding_endpoint_sha256,
      assessor_model: $assessor_model,
      assessor_max_concurrency: ($assessor_max_concurrency | tonumber),
      submission_assessment_worker_count: ($submission_assessment_worker_count | tonumber),
      import_concurrency: ($import_concurrency | tonumber),
      team_id: $team_id,
      release_gate_policy_sha256: $release_gate_policy_sha256,
      runner_sha256: $runner_sha256,
      server_image_id: $server_image_id,
      tool_transport: "mcp",
      tool_contract: "mcp.tools/call.v1",
      import_route: "remember",
      status_tool: "get_submission_status"
    }' > "${candidate}"

  if [[ -f "${IDENTITY_JSON}" ]]; then
    if ! cmp -s "${IDENTITY_JSON}" "${candidate}"; then
      echo "eval runtime identity does not match the requested seed, contract, runner, model configuration, image, or team" >&2
      return 1
    fi
    return 0
  fi

  local snapshot total completed_ready completed_pending completed_search_failed rejected quarantined failed queued processing fragments
  snapshot="$(submission_counts)"
  IFS='|' read -r total completed_ready completed_pending completed_search_failed rejected quarantined failed queued processing <<< "${snapshot}"
  fragments="$(count_canonical_evidence)"
  if [[ "${total}" != "0" || "${fragments}" != "0" ]]; then
    echo "eval runtime already contains submission data but has no dataset identity; refusing to adopt it" >&2
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
  local -a args
  args=(
    "${RUNNER}"
    --mode import
    --seed "${SEED}"
    --suite "${SUITE}"
    --out "${IMPORT_DIR}"
    --import-seed
    --import-concurrency "${IMPORT_CONCURRENCY}"
    --submission-timeout "${SUBMISSION_TIMEOUT}"
    --resume-source-doc-ids "${RESUME_SOURCE_DOC_IDS}"
    --max-page-size 500
  )
  printf '\nresume submission import with concurrency=%s submission_timeout=%s at %s\n' \
    "${IMPORT_CONCURRENCY}" "${SUBMISSION_TIMEOUT}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${IMPORT_DIR}/import.log"
  setsid "${args[@]}" 9>&- >> "${IMPORT_DIR}/import.log" 2>&1 < /dev/null &
  printf '%s\n' "$!" > "${IMPORT_DIR}/import.pid"
  log "started_import pid=$(cat "${IMPORT_DIR}/import.pid") route=remember status_tool=get_submission_status"
}

run_baseline() {
  if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" || ! -s "${IMPORT_DIR}/summary.json" || ! -s "${IMPORT_DIR}/run_config.json" ]]; then
    log "import_artifacts_missing"
    return 1
  fi
  if [[ "$(jq -r '.seed_hash // empty' "${IMPORT_DIR}/summary.json")" != "${SEED_HASH}" ||
    "$(jq -r '.import_route // empty' "${IMPORT_DIR}/run_config.json")" != "remember" ]]; then
    log "import_identity_mismatch"
    return 1
  fi
  local import_mapping_hash
  import_mapping_hash="$(canonical_json_sha256 "${IMPORT_DIR}/knowledge_mapping.json")"
  if [[ "$(jq -r '.mapping_sha256 // empty' "${IMPORT_DIR}/run_config.json")" != "${import_mapping_hash}" ]]; then
    log "import_mapping_hash_mismatch"
    return 1
  fi
  if [[ -s "${BASELINE_DIR}/summary.json" ]]; then
    if [[ "$(jq -r '.seed_hash // empty' "${BASELINE_DIR}/summary.json")" != "${SEED_HASH}" ||
      "$(jq -r '.mapping_sha256 // empty' "${BASELINE_DIR}/run_config.json")" != "${import_mapping_hash}" ]]; then
      log "baseline_identity_mismatch"
      return 1
    fi
    log "baseline_summary_exists path=${BASELINE_DIR}/summary.json"
    return 0
  fi

  local -a args
  args=(
    "${RUNNER}"
    --mode baseline
    --seed "${SEED}"
    --suite "${SUITE}"
    --out "${BASELINE_DIR}"
    --mapping "${IMPORT_DIR}/knowledge_mapping.json"
    --max-page-size 500
  )
  if [[ -n "${RELEASE_GATE_POLICY:-}" ]]; then
    args+=(--release-gate-policy "${RELEASE_GATE_POLICY}")
  fi
  log "starting_baseline_eval"
  if ! "${args[@]}"; then
    log "baseline_eval_failed"
    return 1
  fi
  log "baseline_eval_finished path=${BASELINE_DIR}/summary.json"
}

main() {
  exec 9> "${MONITOR_DIR}/monitor.lock"
  if ! flock -n 9; then
    echo "another eval monitor is already running for ${MONITOR_DIR}" >&2
    return 1
  fi
  ensure_runner
  validate_seed
  load_env
  if ! compose exec -T postgres true || ! compose exec -T server true; then
    echo "eval stack is not running; start it with the eval compose override" >&2
    return 1
  fi
  resolve_evaluator_urls
  SERVER_IMAGE_ID="$(server_image_id)"
  if [[ -z "${SERVER_IMAGE_ID}" ]]; then
    echo "could not resolve the local eval server image ID" >&2
    return 1
  fi
  export SERVER_IMAGE_ID
  prepare_identity

  printf '%s\n' "$$" > "${MONITOR_DIR}/monitor.pid"
  log "monitor_started target=${TARGET} seed_hash=${SEED_HASH}"

  local restarts=0 count_failures=0 previous_completed="" previous_epoch=""
  while true; do
    local now_epoch snapshot total completed_ready completed_pending completed_search_failed rejected quarantined failed queued processing fragments terminal pending blocked
    now_epoch="$(date +%s)"
    if ! snapshot="$(submission_counts)" || ! fragments="$(count_canonical_evidence)"; then
      count_failures=$((count_failures + 1))
      log "submission_or_evidence_count_failed failures=${count_failures}/${MAX_COUNT_FAILURES}"
      if [[ "${count_failures}" -ge "${MAX_COUNT_FAILURES}" ]]; then
        return 1
      fi
      sleep "${SLEEP_SECONDS}"
      continue
    fi
    IFS='|' read -r total completed_ready completed_pending completed_search_failed rejected quarantined failed queued processing <<< "${snapshot}"
    for value in "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${fragments}"; do
      if ! [[ "${value}" =~ ^[0-9]+$ ]]; then
        log "non_numeric_monitor_count"
        return 1
      fi
    done
    terminal="${completed_ready}"
    pending=$((completed_pending + queued + processing))
    blocked=$((completed_search_failed + rejected + quarantined + failed))
    count_failures=0

    write_resume_files
    write_submission_summary "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}"

    local rate_per_minute="" eta_seconds=""
    if [[ -n "${previous_completed}" && -n "${previous_epoch}" && "${now_epoch}" -gt "${previous_epoch}" && "${terminal}" -ge "${previous_completed}" ]]; then
      local delta_count=$((terminal - previous_completed))
      local delta_seconds=$((now_epoch - previous_epoch))
      if [[ "${delta_count}" -gt 0 ]]; then
        rate_per_minute="$(awk -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { printf "%.2f", c * 60 / s }')"
        eta_seconds="$(awk -v remaining="$((TARGET - terminal))" -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { if (c > 0) printf "%.0f", remaining * s / c }')"
      fi
    fi
    previous_completed="${terminal}"
    previous_epoch="${now_epoch}"

    if [[ "${total}" -gt "${TARGET}" || "${fragments}" -gt "${TARGET}" ]]; then
      log "dataset_count_exceeds_target submissions=${total}/${TARGET} evidence=${fragments}/${TARGET}"
      write_status "failed" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "$(live_import_pid || true)" "${rate_per_minute}" "${eta_seconds}"
      return 1
    fi
    if [[ "${blocked}" -gt 0 ]]; then
      log "submission_terminal_failure rejected=${rejected} quarantined=${quarantined} failed=${failed} search_failed=${completed_search_failed}"
      write_status "failed" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "$(live_import_pid || true)" "${rate_per_minute}" "${eta_seconds}"
      return 1
    fi

    local pid=""
    pid="$(live_import_pid || true)"
    if [[ "${total}" == "${TARGET}" && "${terminal}" == "${TARGET}" && "${pending}" == "0" && "${fragments}" == "${TARGET}" ]]; then
      if [[ -n "${pid}" ]]; then
        log "import_finalizing pid=${pid} completed=${terminal}/${TARGET}"
        write_status "import_finalizing" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${pid}" "${rate_per_minute}" "0"
        sleep "${SLEEP_SECONDS}"
        continue
      fi
      if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" || ! -s "${IMPORT_DIR}/summary.json" ]]; then
        if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
          log "max_import_restarts_reached while rebuilding import artifacts"
          return 1
        fi
        restarts=$((restarts + 1))
        log "submissions_complete_but_import_artifacts_missing restart=${restarts}"
        start_import
        sleep "${SLEEP_SECONDS}"
        continue
      fi
      log "full_import_verified completed=${terminal}/${TARGET} evidence=${fragments}/${TARGET}"
      write_status "full_import_verified" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "" "${rate_per_minute}" "0"
      if ! run_baseline; then
        write_status "failed" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "" "${rate_per_minute}" "0"
        return 1
      fi
      write_status "done" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "" "${rate_per_minute}" "0"
      log "done"
      return 0
    fi

    if [[ -n "${pid}" ]]; then
      log "import_running pid=${pid} completed=${terminal}/${TARGET} pending=${pending} evidence=${fragments}"
      write_status "import_running" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${pid}" "${rate_per_minute}" "${eta_seconds}"
    elif [[ "${pending}" -gt 0 ]]; then
      log "submission_worker_or_embedding_draining completed=${terminal}/${TARGET} queued=${queued} processing=${processing} search_pending=${completed_pending}"
      write_status "submission_worker_or_embedding_draining" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "" "${rate_per_minute}" "${eta_seconds}"
    else
      if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
        log "max_import_restarts_reached completed=${terminal}/${TARGET}"
        write_status "failed" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "" "${rate_per_minute}" "${eta_seconds}"
        return 1
      fi
      restarts=$((restarts + 1))
      log "import_not_running restart=${restarts} completed=${terminal}/${TARGET} submitted=${total}/${TARGET}"
      write_status "import_restarting" "${fragments}" "${total}" "${completed_ready}" "${completed_pending}" "${completed_search_failed}" "${rejected}" "${quarantined}" "${failed}" "${queued}" "${processing}" "" "${rate_per_minute}" "${eta_seconds}"
      start_import
    fi
    sleep "${SLEEP_SECONDS}"
  done
}

main "$@"
