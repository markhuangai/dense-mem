#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT_DIR}"

: "${SEED:?Set SEED to the seed_manifest.json path}"
: "${SUITE:?Set SUITE to the suite JSONL path}"
RELEASE_GATE_POLICY="${RELEASE_GATE_POLICY:-}"
ALLOW_UNGATED_EVALUATION="${ALLOW_UNGATED_EVALUATION:-0}"
REUSE_EXISTING_RUNNER="${REUSE_EXISTING_RUNNER:-0}"

if [[ ! -f "${SEED}" ]]; then
  echo "seed manifest not found: ${SEED}" >&2
  exit 2
fi
if [[ ! -f "${SUITE}" ]]; then
  echo "suite not found: ${SUITE}" >&2
  exit 2
fi
if [[ "${ALLOW_UNGATED_EVALUATION}" != "0" && "${ALLOW_UNGATED_EVALUATION}" != "1" ]]; then
  echo "ALLOW_UNGATED_EVALUATION must be 0 or 1" >&2
  exit 2
fi
if [[ "${REUSE_EXISTING_RUNNER}" != "0" && "${REUSE_EXISTING_RUNNER}" != "1" ]]; then
  echo "REUSE_EXISTING_RUNNER must be 0 or 1" >&2
  exit 2
fi
if [[ -z "${RELEASE_GATE_POLICY}" && "${ALLOW_UNGATED_EVALUATION}" != "1" ]]; then
  echo "set RELEASE_GATE_POLICY or explicitly set ALLOW_UNGATED_EVALUATION=1" >&2
  exit 2
fi
if [[ -n "${RELEASE_GATE_POLICY}" && "${ALLOW_UNGATED_EVALUATION}" == "1" ]]; then
  echo "RELEASE_GATE_POLICY and ALLOW_UNGATED_EVALUATION=1 cannot be combined" >&2
  exit 2
fi
if [[ -n "${RELEASE_GATE_POLICY}" && ! -f "${RELEASE_GATE_POLICY}" ]]; then
  echo "release gate policy not found: ${RELEASE_GATE_POLICY}" >&2
  exit 2
fi

SEED="$(realpath "${SEED}")"
SUITE="$(realpath "${SUITE}")"
if [[ -n "${RELEASE_GATE_POLICY}" ]]; then
  RELEASE_GATE_POLICY="$(realpath "${RELEASE_GATE_POLICY}")"
fi
V1_DATA_DIR="$(realpath -m "${V1_DATA_DIR:-tests/eval/runtime/v1}")"
export V1_COMPOSE_DATA_DIR="$(realpath -m "${V1_COMPOSE_DATA_DIR:-${V1_DATA_DIR}}")"

IMPORT_DIR="${IMPORT_DIR:-${V1_DATA_DIR}/runs/import}"
BASELINE_DIR="${BASELINE_DIR:-${V1_DATA_DIR}/runs/baseline}"
MONITOR_DIR="${MONITOR_DIR:-${V1_DATA_DIR}/monitor}"
PROFILE_PATH="${PROFILE_PATH:-${V1_DATA_DIR}/eval_profile.json}"
RUNNER="${RUNNER:-${V1_DATA_DIR}/tools/eval-runner}"
IMPORT_CONCURRENCY="${IMPORT_CONCURRENCY:-10}"
PLACEMENT_TIMEOUT="${PLACEMENT_TIMEOUT:-10m}"
SLEEP_SECONDS="${SLEEP_SECONDS:-60}"
MAX_IMPORT_RESTARTS="${MAX_IMPORT_RESTARTS:-100}"
MAX_COUNT_FAILURES="${MAX_COUNT_FAILURES:-12}"

IDENTITY_JSON="${V1_DATA_DIR}/dataset_identity.json"
VALIDATION_DIR="${MONITOR_DIR}/validation"
LOG="${MONITOR_DIR}/monitor.log"
STATUS_JSON="${MONITOR_DIR}/status.json"
PLACEMENT_SUMMARY="${MONITOR_DIR}/placement_summary.json"
IMPORT_GATE_RESULT="${MONITOR_DIR}/import_gate_result.json"
RESUME_SOURCE_DOC_IDS="${MONITOR_DIR}/completed_source_doc_ids.txt"
QUARANTINED_SOURCE_DOC_IDS="${MONITOR_DIR}/quarantined_source_doc_ids.txt"
FAILED_SOURCE_DOC_IDS="${MONITOR_DIR}/failed_source_doc_ids.txt"

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

compose() {
  docker compose -p densemem_eval_full -f docker-compose.yml -f tests/eval/docker-compose.eval.yml "$@"
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
  local eval_compose_data_dir="${V1_COMPOSE_DATA_DIR}"
  local requested_release_gate_policy="${RELEASE_GATE_POLICY}"
  local requested_allow_ungated_evaluation="${ALLOW_UNGATED_EVALUATION}"
  local requested_api_key="${DENSE_MEM_API_KEY:-}"
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
  export V1_COMPOSE_DATA_DIR="${eval_compose_data_dir}"
  export RELEASE_GATE_POLICY="${requested_release_gate_policy}"
  export ALLOW_UNGATED_EVALUATION="${requested_allow_ungated_evaluation}"
  if [[ -n "${requested_api_key}" ]]; then
    export DENSE_MEM_API_KEY="${requested_api_key}"
  fi
  if [[ -n "${requested_control_token}" ]]; then
    export DENSE_MEM_CONTROL_TOKEN="${requested_control_token}"
  fi
  if [[ -n "${requested_team_id}" ]]; then
    export EVAL_TEAM_ID="${requested_team_id}"
  fi

  export DENSE_MEM_BASE_URL="${DENSE_MEM_BASE_URL:-http://127.0.0.1:${DENSE_MEM_PORT}}"
  export DENSE_MEM_CONTROL_URL="${DENSE_MEM_CONTROL_URL:-http://127.0.0.1:${CONTROL_PORTAL_PORT}}"
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
    echo "invalid eval team UUID: ${EVAL_TEAM_ID}" >&2
    return 1
  fi
}

ensure_runner() {
  if [[ "${REUSE_EXISTING_RUNNER}" == "1" ]]; then
    if [[ ! -x "${RUNNER}" ]]; then
      echo "existing eval runner is not executable: ${RUNNER}" >&2
      return 1
    fi
    return 0
  fi
  local tmp="${RUNNER}.tmp"
  go build -o "${tmp}" ./cmd/eval-runner
  mv "${tmp}" "${RUNNER}"
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

count_fragments() {
  psql_eval "
    SELECT count(DISTINCT metadata ->> 'source_doc_id')
    FROM evidence_fragments
    WHERE team_id = :'team_id'::uuid
      AND COALESCE(metadata ->> 'source_doc_id', '') <> '';
  "
}

terminal_placement_statuses_sql() {
  printf '%s' "'completed', 'awaiting_review', 'quarantined'"
}

terminal_placement_count() {
  local completed="$1" awaiting_review="$2" quarantined="$3"
  printf '%s\n' "$((completed + awaiting_review + quarantined))"
}

placement_counts() {
  psql_eval "
    WITH run_docs AS (
      SELECT fragment.metadata ->> 'source_doc_id' AS source_doc_id,
             run.ingest_id,
             run.status,
             run.created_at
      FROM placement_runs AS run
      JOIN evidence_fragments AS fragment
        ON fragment.team_id = run.team_id
       AND fragment.ingest_id = run.ingest_id
      LEFT JOIN evidence_sources AS source
        ON source.team_id = fragment.team_id
       AND source.source_id = fragment.source_id
      WHERE run.team_id = :'team_id'::uuid
        AND COALESCE(fragment.metadata ->> 'source_doc_id', '') <> ''
        AND (
          fragment.source_id IS NULL
          OR source.current_revision_id = fragment.source_revision_id
        )
    ), ranked AS (
      SELECT status,
             row_number() OVER (
               PARTITION BY source_doc_id
               ORDER BY created_at DESC, ingest_id DESC
             ) AS row_num
      FROM run_docs
    ), latest AS (
      SELECT status FROM ranked WHERE row_num = 1
    ), historical AS (
      SELECT count(DISTINCT ingest_id) AS attempts
      FROM run_docs
    )
    SELECT count(*),
           count(*) FILTER (WHERE status = 'completed'),
           count(*) FILTER (WHERE status = 'awaiting_review'),
           count(*) FILTER (WHERE status = 'quarantined'),
           count(*) FILTER (WHERE status = 'failed'),
           count(*) FILTER (WHERE status = 'queued'),
           count(*) FILTER (WHERE status = 'processing'),
           (SELECT attempts FROM historical)
    FROM latest;
  "
}

write_resume_files() {
  local completed_tmp quarantined_tmp failed_tmp
  completed_tmp="$(mktemp "${RESUME_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  quarantined_tmp="$(mktemp "${QUARANTINED_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  failed_tmp="$(mktemp "${FAILED_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  psql_eval "
    WITH run_docs AS (
      SELECT fragment.metadata ->> 'source_doc_id' AS source_doc_id,
             run.status,
             run.created_at,
             run.ingest_id
      FROM placement_runs AS run
      JOIN evidence_fragments AS fragment
        ON fragment.team_id = run.team_id
       AND fragment.ingest_id = run.ingest_id
      LEFT JOIN evidence_sources AS source
        ON source.team_id = fragment.team_id
       AND source.source_id = fragment.source_id
      WHERE run.team_id = :'team_id'::uuid
        AND COALESCE(fragment.metadata ->> 'source_doc_id', '') <> ''
        AND (
          fragment.source_id IS NULL
          OR source.current_revision_id = fragment.source_revision_id
        )
    ), ranked AS (
      SELECT source_doc_id,
             status,
             row_number() OVER (
               PARTITION BY source_doc_id
               ORDER BY created_at DESC, ingest_id DESC
             ) AS row_num
      FROM run_docs
    )
    SELECT source_doc_id
    FROM ranked
    WHERE row_num = 1 AND status IN ($(terminal_placement_statuses_sql))
    ORDER BY source_doc_id;
  " > "${completed_tmp}"
  psql_eval "
    WITH run_docs AS (
      SELECT fragment.metadata ->> 'source_doc_id' AS source_doc_id,
             run.status,
             run.created_at,
             run.ingest_id
      FROM placement_runs AS run
      JOIN evidence_fragments AS fragment
        ON fragment.team_id = run.team_id
       AND fragment.ingest_id = run.ingest_id
      LEFT JOIN evidence_sources AS source
        ON source.team_id = fragment.team_id
       AND source.source_id = fragment.source_id
      WHERE run.team_id = :'team_id'::uuid
        AND COALESCE(fragment.metadata ->> 'source_doc_id', '') <> ''
        AND (
          fragment.source_id IS NULL
          OR source.current_revision_id = fragment.source_revision_id
        )
    ), ranked AS (
      SELECT source_doc_id,
             status,
             row_number() OVER (
               PARTITION BY source_doc_id
               ORDER BY created_at DESC, ingest_id DESC
             ) AS row_num
      FROM run_docs
    )
    SELECT source_doc_id
    FROM ranked
    WHERE row_num = 1 AND status = 'quarantined'
    ORDER BY source_doc_id;
  " > "${quarantined_tmp}"
  psql_eval "
    WITH run_docs AS (
      SELECT fragment.metadata ->> 'source_doc_id' AS source_doc_id,
             run.status,
             run.created_at,
             run.ingest_id
      FROM placement_runs AS run
      JOIN evidence_fragments AS fragment
        ON fragment.team_id = run.team_id
       AND fragment.ingest_id = run.ingest_id
      LEFT JOIN evidence_sources AS source
        ON source.team_id = fragment.team_id
       AND source.source_id = fragment.source_id
      WHERE run.team_id = :'team_id'::uuid
        AND COALESCE(fragment.metadata ->> 'source_doc_id', '') <> ''
        AND (
          fragment.source_id IS NULL
          OR source.current_revision_id = fragment.source_revision_id
        )
    ), ranked AS (
      SELECT source_doc_id,
             status,
             row_number() OVER (
               PARTITION BY source_doc_id
               ORDER BY created_at DESC, ingest_id DESC
             ) AS row_num
      FROM run_docs
    )
    SELECT source_doc_id
    FROM ranked
    WHERE row_num = 1 AND status = 'failed'
    ORDER BY source_doc_id;
  " > "${failed_tmp}"
  mv "${completed_tmp}" "${RESUME_SOURCE_DOC_IDS}"
  mv "${quarantined_tmp}" "${QUARANTINED_SOURCE_DOC_IDS}"
  mv "${failed_tmp}" "${FAILED_SOURCE_DOC_IDS}"
}

write_placement_summary() {
  local tmp
  tmp="$(mktemp "${PLACEMENT_SUMMARY}.tmp.XXXXXX")"
  psql_eval "
    WITH run_docs AS (
      SELECT fragment.metadata ->> 'source_doc_id' AS source_doc_id,
             run.placement_run_id,
             run.ingest_id,
             run.status,
             run.created_at
      FROM placement_runs AS run
      JOIN evidence_fragments AS fragment
        ON fragment.team_id = run.team_id
       AND fragment.ingest_id = run.ingest_id
      LEFT JOIN evidence_sources AS source
        ON source.team_id = fragment.team_id
       AND source.source_id = fragment.source_id
      WHERE run.team_id = :'team_id'::uuid
        AND COALESCE(fragment.metadata ->> 'source_doc_id', '') <> ''
        AND (
          fragment.source_id IS NULL
          OR source.current_revision_id = fragment.source_revision_id
        )
    ), ranked AS (
      SELECT placement_run_id, ingest_id, status,
             row_number() OVER (
               PARTITION BY source_doc_id
               ORDER BY created_at DESC, ingest_id DESC
             ) AS row_num
      FROM run_docs
    ), latest AS (
      SELECT placement_run_id, ingest_id, status FROM ranked WHERE row_num = 1
    ), latest_stats AS (
      SELECT count(*) AS total,
             count(*) FILTER (WHERE status = 'completed') AS completed,
             count(*) FILTER (WHERE status = 'awaiting_review') AS awaiting_review,
             count(*) FILTER (WHERE status = 'quarantined') AS quarantined,
             count(*) FILTER (WHERE status IN ($(terminal_placement_statuses_sql))) AS terminal,
             count(*) FILTER (WHERE status = 'failed') AS failed,
             count(*) FILTER (WHERE status = 'queued') AS queued,
             count(*) FILTER (WHERE status = 'processing') AS processing
      FROM latest
    ), latest_items AS (
      SELECT item.category, item.status, latest.status AS run_status
      FROM latest
      JOIN placement_items AS item
        ON item.placement_run_id = latest.placement_run_id
       AND item.ingest_id = latest.ingest_id
       AND item.team_id = :'team_id'::uuid
    ), item_stats AS (
      SELECT count(*) AS total,
             count(*) FILTER (WHERE run_status = 'completed') AS completed_total,
             count(*) FILTER (
               WHERE run_status = 'completed'
                 AND category IN ('validated_claim', 'fact')
             ) AS promoted,
             count(*) FILTER (
               WHERE run_status = 'completed'
                 AND category IN ('candidate', 'quarantined', 'failed')
             ) AS rejected
      FROM latest_items
    ), category_counts AS (
      SELECT COALESCE(jsonb_object_agg(category, category_count), '{}'::jsonb) AS value
      FROM (
        SELECT category, count(*) AS category_count
        FROM latest_items
        GROUP BY category
        ORDER BY category
      ) AS counts
    ), item_status_counts AS (
      SELECT COALESCE(jsonb_object_agg(status, status_count), '{}'::jsonb) AS value
      FROM (
        SELECT status, count(*) AS status_count
        FROM latest_items
        GROUP BY status
        ORDER BY status
      ) AS counts
    ), historical_status_counts AS (
      SELECT COALESCE(jsonb_object_agg(status, status_count), '{}'::jsonb) AS value,
             COALESCE(sum(status_count), 0) AS total
      FROM (
        SELECT status, count(*) AS status_count
        FROM run_docs
        GROUP BY status
        ORDER BY status
      ) AS counts
    )
    SELECT jsonb_pretty(jsonb_build_object(
      'generated_at', to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
      'latest_runs', jsonb_build_object(
        'total', latest_stats.total,
        'completed', latest_stats.completed,
        'awaiting_review', latest_stats.awaiting_review,
        'quarantined', latest_stats.quarantined,
        'terminal', latest_stats.terminal,
        'failed', latest_stats.failed,
        'queued', latest_stats.queued,
        'processing', latest_stats.processing,
        'review_burden_rate', CASE
          WHEN latest_stats.total = 0 THEN 0
          ELSE round(latest_stats.awaiting_review::numeric / latest_stats.total, 6)
        END
      ),
      'placement_items', jsonb_build_object(
        'total', item_stats.total,
        'by_category', category_counts.value,
        'by_status', item_status_counts.value,
        'promotion_rate', CASE
          WHEN item_stats.completed_total = 0 THEN 0
          ELSE round(item_stats.promoted::numeric / item_stats.completed_total, 6)
        END,
        'rejection_rate', CASE
          WHEN item_stats.completed_total = 0 THEN 0
          ELSE round(item_stats.rejected::numeric / item_stats.completed_total, 6)
        END
      ),
      'historical_attempts', jsonb_build_object(
        'total', historical_status_counts.total,
        'by_status', historical_status_counts.value
      )
    ))
    FROM latest_stats, item_stats, category_counts, item_status_counts, historical_status_counts;
  " > "${tmp}"
  mv "${tmp}" "${PLACEMENT_SUMMARY}"
}

write_import_gate_result() {
  local fragments="$1" latest="$2" completed="$3" awaiting_review="$4" quarantined="$5" failed="$6" queued="$7" processing="$8" attempts="$9" forced_reason="${10:-}"
  local terminal passed status reason counts_observed="true" tmp

  if [[ -n "${forced_reason}" ]]; then
    if [[ ! "${forced_reason}" =~ ^[a-z0-9_]+$ ]]; then
      echo "invalid import gate failure reason: ${forced_reason}" >&2
      return 1
    fi
    passed="false"
    status="failed"
    reason="${forced_reason}"
    if [[ -z "${fragments}" ]]; then
      terminal=""
      counts_observed="false"
    else
      terminal="$(terminal_placement_count "${completed}" "${awaiting_review}" "${quarantined}")"
    fi
  else
    terminal="$(terminal_placement_count "${completed}" "${awaiting_review}" "${quarantined}")"

    if [[ "${latest}" == "${TARGET}" && "${fragments}" == "${TARGET}" && "${terminal}" == "${TARGET}" && "${failed}" == "0" && "${queued}" == "0" && "${processing}" == "0" ]]; then
      if [[ "${quarantined}" == "0" ]]; then
        passed="true"
        status="passed"
        reason=""
      else
        passed="false"
        status="comparison_only"
        reason="quarantined_placements"
      fi
    else
      passed="false"
      status="failed"
      reason="incomplete_or_failed_placements"
    fi
  fi

  tmp="$(mktemp "${IMPORT_GATE_RESULT}.tmp.XXXXXX")"
  jq -n \
    --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg status "${status}" \
    --argjson passed "${passed}" \
    --argjson counts_observed "${counts_observed}" \
    --arg target "${TARGET}" \
    --arg fragments "${fragments}" \
    --arg latest "${latest}" \
    --arg completed "${completed}" \
    --arg awaiting_review "${awaiting_review}" \
    --arg quarantined "${quarantined}" \
    --arg terminal "${terminal}" \
    --arg failed "${failed}" \
    --arg queued "${queued}" \
    --arg processing "${processing}" \
    --arg attempts "${attempts}" \
    --arg reason "${reason}" \
    'def optional_count:
       if . == "" then null else tonumber end;
     {
      schema_version: 1,
      generated_at: $generated_at,
      status: $status,
      passed: $passed,
      counts_observed: $counts_observed,
      target: ($target | tonumber),
      fragments: ($fragments | optional_count),
      latest_placements: ($latest | optional_count),
      completed: ($completed | optional_count),
      awaiting_review: ($awaiting_review | optional_count),
      quarantined: ($quarantined | optional_count),
      terminal: ($terminal | optional_count),
      failed: ($failed | optional_count),
      queued: ($queued | optional_count),
      processing: ($processing | optional_count),
      historical_attempts: ($attempts | optional_count),
      reasons: (if $reason == "" then [] else [$reason] end)
    }' > "${tmp}"
  mv "${tmp}" "${IMPORT_GATE_RESULT}"
}

write_status() {
  local phase="$1" fragments="$2" latest="$3" completed="$4" awaiting_review="$5" quarantined="$6" failed="$7" queued="$8" processing="$9" attempts="${10}"
  local import_pid="${11:-}" rate_per_minute="${12:-}" eta_seconds="${13:-}"
  local tmp
  tmp="$(mktemp "${STATUS_JSON}.tmp.XXXXXX")"
  jq -n \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg phase "${phase}" \
    --arg fragments "${fragments}" \
    --arg latest "${latest}" \
    --arg completed "${completed}" \
    --arg awaiting_review "${awaiting_review}" \
    --arg quarantined "${quarantined}" \
    --arg failed "${failed}" \
    --arg queued "${queued}" \
    --arg processing "${processing}" \
    --arg attempts "${attempts}" \
    --arg target "${TARGET}" \
    --arg import_pid "${import_pid}" \
    --arg rate_per_minute "${rate_per_minute}" \
    --arg eta_seconds "${eta_seconds}" \
    --arg import_dir "${IMPORT_DIR}" \
    --arg baseline_dir "${BASELINE_DIR}" \
    --arg placement_summary "${PLACEMENT_SUMMARY}" \
    --arg import_gate_result "${IMPORT_GATE_RESULT}" \
    --arg quarantined_source_doc_ids "${QUARANTINED_SOURCE_DOC_IDS}" \
    --arg failed_source_doc_ids "${FAILED_SOURCE_DOC_IDS}" \
    --arg dataset_identity "${IDENTITY_JSON}" \
    'def nullable_number: if . == "" then null else tonumber end;
    {
      updated_at: $updated_at,
      phase: $phase,
      target: ($target | tonumber),
      fragments: ($fragments | tonumber),
      latest_placements: ($latest | tonumber),
      completed: ($completed | tonumber),
      awaiting_review: ($awaiting_review | tonumber),
      quarantined: ($quarantined | tonumber),
      terminal: (($completed | tonumber) + ($awaiting_review | tonumber) + ($quarantined | tonumber)),
      failed: ($failed | tonumber),
      queued: ($queued | tonumber),
      processing: ($processing | tonumber),
      historical_attempts: ($attempts | tonumber),
      percent_complete: ((($completed | tonumber) + ($awaiting_review | tonumber) + ($quarantined | tonumber)) / ($target | tonumber) * 100),
      rate_per_minute: ($rate_per_minute | nullable_number),
      eta_seconds: ($eta_seconds | nullable_number),
      import_pid: $import_pid,
      import_dir: $import_dir,
      baseline_dir: $baseline_dir,
      placement_summary: $placement_summary,
      import_gate_result: $import_gate_result,
      quarantined_source_doc_ids: $quarantined_source_doc_ids,
      failed_source_doc_ids: $failed_source_doc_ids,
      dataset_identity: $dataset_identity
    }' > "${tmp}"
  mv "${tmp}" "${STATUS_JSON}"
}

validate_release_gate_seed() {
  local -a args=(
    "${RUNNER}"
    --mode validate
    --seed "${SEED}"
    --suite "${SUITE}"
    --out "${VALIDATION_DIR}"
  )
  if [[ -n "${RELEASE_GATE_POLICY}" ]]; then
    args+=(--release-gate-policy "${RELEASE_GATE_POLICY}")
  fi
  "${args[@]}"

  SEED_HASH="$(jq -r '.seed_hash' "${VALIDATION_DIR}/summary.json")"
  export SEED_HASH
}

prepare_identity() {
  SUITE_HASH="$(sha256sum "${SUITE}" | awk '{print $1}')"
  EMBEDDING_MODEL="${AI_API_EMBEDDING_MODEL:-}"
  EMBEDDING_DIMENSIONS="${AI_API_EMBEDDING_DIMENSIONS:-3072}"
  if [[ "${EMBEDDING_DIMENSIONS}" == "0" ]]; then
    EMBEDDING_DIMENSIONS="3072"
  fi
  EMBEDDING_ENDPOINT_HASH="$(printf '%s' "${AI_API_URL:-}" | sha256sum | awk '{print $1}')"
  RELEASE_GATE_POLICY_HASH=""
  if [[ -n "${RELEASE_GATE_POLICY}" ]]; then
    RELEASE_GATE_POLICY_HASH="$(canonical_json_sha256 "${RELEASE_GATE_POLICY}")"
  fi
  RUNNER_HASH="sha256:$(sha256sum "${RUNNER}" | awk '{print $1}')"
  export SUITE_HASH RELEASE_GATE_POLICY_HASH RUNNER_HASH

  local candidate="${MONITOR_DIR}/requested_dataset_identity.json"
  jq -n \
    --arg seed_id "$(jq -r '.seed_id' "${SEED}")" \
    --arg seed_hash "${SEED_HASH}" \
    --arg suite_sha256 "${SUITE_HASH}" \
    --arg embedding_model "${EMBEDDING_MODEL}" \
    --arg embedding_dimensions "${EMBEDDING_DIMENSIONS}" \
    --arg embedding_endpoint_sha256 "${EMBEDDING_ENDPOINT_HASH}" \
    --arg assessor_model "${AI_VERIFIER_MODEL:-}" \
    --arg team_id "${EVAL_TEAM_ID}" \
    --arg release_gate_policy_sha256 "${RELEASE_GATE_POLICY_HASH}" \
    --arg runner_sha256 "${RUNNER_HASH}" \
    --arg server_image_id "${SERVER_IMAGE_ID}" \
    '{
      seed_id: $seed_id,
      seed_hash: $seed_hash,
      suite_sha256: $suite_sha256,
      embedding_model: $embedding_model,
      embedding_dimensions: ($embedding_dimensions | tonumber),
      embedding_endpoint_sha256: $embedding_endpoint_sha256,
      assessor_model: $assessor_model,
      team_id: $team_id,
      release_gate_policy_sha256: $release_gate_policy_sha256,
      runner_sha256: $runner_sha256,
      server_image_id: $server_image_id,
      tool_transport: "mcp",
      tool_contract: "mcp.tools/call.v1",
      import_route: "remember"
    }' > "${candidate}"

  if [[ -f "${IDENTITY_JSON}" ]]; then
    if ! cmp -s "${IDENTITY_JSON}" "${candidate}"; then
      echo "eval runtime identity does not match the requested seed, policy, contract, runner, model config, or team" >&2
      diff -u <(jq -S . "${IDENTITY_JSON}") <(jq -S . "${candidate}") >&2 || true
      return 1
    fi
    return 0
  fi

  local snapshot latest completed awaiting_review quarantined failed queued processing attempts fragments
  snapshot="$(placement_counts)"
  IFS='|' read -r latest completed awaiting_review quarantined failed queued processing attempts <<< "${snapshot}"
  fragments="$(count_fragments)"
  if [[ "${latest}" != "0" || "${fragments}" != "0" ]]; then
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
  local -a args
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
    --max-page-size 500
  )
  printf '\nresume remember import with concurrency=%s placement_timeout=%s at %s\n' \
    "${IMPORT_CONCURRENCY}" "${PLACEMENT_TIMEOUT}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${IMPORT_DIR}/import.log"
  setsid "${args[@]}" 9>&- >> "${IMPORT_DIR}/import.log" 2>&1 < /dev/null &
  printf '%s\n' "$!" > "${IMPORT_DIR}/import.pid"
  log "started_import pid=$(cat "${IMPORT_DIR}/import.pid") route=remember"
}

run_baseline() {
  if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" || ! -s "${IMPORT_DIR}/summary.json" || ! -s "${IMPORT_DIR}/run_config.json" ]]; then
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
  local import_mapping_hash
  if ! import_mapping_hash="$(canonical_json_sha256 "${IMPORT_DIR}/knowledge_mapping.json")"; then
    log "import_mapping_invalid path=${IMPORT_DIR}/knowledge_mapping.json"
    return 1
  fi
  if [[ "$(jq -r '.mapping_sha256 // empty' "${IMPORT_DIR}/run_config.json")" != "${import_mapping_hash}" ]]; then
    log "import_mapping_hash_mismatch path=${IMPORT_DIR}/knowledge_mapping.json"
    return 1
  fi
  if [[ "$(jq -r '.tool_transport // empty' "${IMPORT_DIR}/run_config.json")" != "mcp" || "$(jq -r '.tool_contract // empty' "${IMPORT_DIR}/run_config.json")" != "mcp.tools/call.v1" ]]; then
    log "import_contract_mismatch path=${IMPORT_DIR}/run_config.json"
    return 1
  fi
  if [[ -s "${BASELINE_DIR}/summary.json" ]]; then
    if [[ ! -s "${BASELINE_DIR}/run_config.json" || ! -s "${BASELINE_DIR}/knowledge_mapping.json" ]]; then
      log "baseline_identity_artifacts_missing path=${BASELINE_DIR}"
      return 1
    fi
    if [[ "$(jq -r '.seed_hash // empty' "${BASELINE_DIR}/summary.json")" != "${SEED_HASH}" ]]; then
      log "baseline_seed_hash_mismatch path=${BASELINE_DIR}/summary.json"
      return 1
    fi
    local baseline_policy_path baseline_policy_hash
    baseline_policy_path="$(jq -r '.release_gate_policy_path // empty' "${BASELINE_DIR}/run_config.json")"
    baseline_policy_hash="$(jq -r '.release_gate_policy_sha256 // empty' "${BASELINE_DIR}/run_config.json")"
    if [[ -n "${RELEASE_GATE_POLICY}" ]]; then
      if [[ ! -s "${BASELINE_DIR}/release_gate_result.json" ]]; then
        log "baseline_gate_result_missing policy=${RELEASE_GATE_POLICY}"
        return 1
      fi
      if [[ "${baseline_policy_path}" != "${RELEASE_GATE_POLICY}" ]]; then
        log "baseline_gate_policy_mismatch policy=${RELEASE_GATE_POLICY}"
        return 1
      fi
      if [[ "${baseline_policy_hash}" != "${RELEASE_GATE_POLICY_HASH}" ]]; then
        log "baseline_gate_policy_hash_mismatch policy=${RELEASE_GATE_POLICY}"
        return 1
      fi
    else
      if [[ -s "${BASELINE_DIR}/release_gate_result.json" || -n "${baseline_policy_path}" || -n "${baseline_policy_hash}" ]]; then
        log "baseline_unexpected_release_gate_artifacts"
        return 1
      fi
    fi
    if [[ "$(jq -r '.mapping_sha256 // empty' "${BASELINE_DIR}/run_config.json")" != "${import_mapping_hash}" ]]; then
      log "baseline_mapping_hash_mismatch path=${BASELINE_DIR}/run_config.json"
      return 1
    fi
    if [[ "$(jq -r '.tool_transport // empty' "${BASELINE_DIR}/run_config.json")" != "mcp" || "$(jq -r '.tool_contract // empty' "${BASELINE_DIR}/run_config.json")" != "mcp.tools/call.v1" ]]; then
      log "baseline_contract_mismatch path=${BASELINE_DIR}/run_config.json"
      return 1
    fi
    local baseline_mapping_hash
    if ! baseline_mapping_hash="$(canonical_json_sha256 "${BASELINE_DIR}/knowledge_mapping.json")"; then
      log "baseline_mapping_invalid path=${BASELINE_DIR}/knowledge_mapping.json"
      return 1
    fi
    if [[ "${baseline_mapping_hash}" != "${import_mapping_hash}" ]]; then
      log "baseline_mapping_artifact_mismatch path=${BASELINE_DIR}/knowledge_mapping.json"
      return 1
    fi
    if [[ -n "${RELEASE_GATE_POLICY}" &&
      "$(jq -r '.passed // false' "${BASELINE_DIR}/release_gate_result.json")" != "true" ]]; then
      log "baseline_gate_result_not_passed path=${BASELINE_DIR}/release_gate_result.json"
      return 1
    fi
    log "baseline_summary_exists path=${BASELINE_DIR}/summary.json"
    return 0
  fi

  local -a baseline_args
  baseline_args=(
    "${RUNNER}"
    --mode baseline
    --seed "${SEED}"
    --suite "${SUITE}"
    --out "${BASELINE_DIR}"
    --mapping "${IMPORT_DIR}/knowledge_mapping.json"
    --max-page-size 500
  )
  if [[ -n "${RELEASE_GATE_POLICY}" ]]; then
    baseline_args+=(--release-gate-policy "${RELEASE_GATE_POLICY}")
  fi

  log "starting_baseline_eval"
  if ! "${baseline_args[@]}"; then
    log "baseline_eval_failed path=${BASELINE_DIR}/release_gate_result.json"
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
  validate_release_gate_seed
  load_env
  if ! compose exec -T postgres true || ! compose exec -T server true; then
    echo "eval stack is not running; start it with the eval compose override" >&2
    return 1
  fi
  SERVER_IMAGE_ID="$(server_image_id)"
  if [[ -z "${SERVER_IMAGE_ID}" ]]; then
    echo "could not resolve the local eval server image ID" >&2
    return 1
  fi
  export SERVER_IMAGE_ID
  prepare_identity

  printf '%s\n' "$$" > "${MONITOR_DIR}/monitor.pid"
  log "monitor_started target=${TARGET} seed_hash=${SEED_HASH}"

  local restarts=0 count_failures=0 previous_terminal="" previous_epoch=""
  while true; do
    local now_epoch snapshot latest completed awaiting_review quarantined failed queued processing attempts fragments terminal pending
    now_epoch="$(date +%s)"
    if ! snapshot="$(placement_counts)" || ! fragments="$(count_fragments)"; then
      count_failures=$((count_failures + 1))
      log "placement_or_fragment_count_failed failures=${count_failures}/${MAX_COUNT_FAILURES}"
      if [[ "${count_failures}" -ge "${MAX_COUNT_FAILURES}" ]]; then
        log "max_count_failures_reached failures=${count_failures}"
        write_import_gate_result "" "" "" "" "" "" "" "" "" "placement_or_fragment_count_failed"
        return 1
      fi
      sleep "${SLEEP_SECONDS}"
      continue
    fi
    IFS='|' read -r latest completed awaiting_review quarantined failed queued processing attempts <<< "${snapshot}"
    for value in "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "${fragments}"; do
      if ! [[ "${value}" =~ ^[0-9]+$ ]]; then
        log "non_numeric_monitor_count value=${value}"
        write_import_gate_result "" "" "" "" "" "" "" "" "" "non_numeric_monitor_count"
        return 1
      fi
    done
    terminal="$(terminal_placement_count "${completed}" "${awaiting_review}" "${quarantined}")"
    pending=$((queued + processing))
    count_failures=0

    write_resume_files
    write_placement_summary

    local rate_per_minute="" eta_seconds=""
    if [[ -n "${previous_terminal}" && -n "${previous_epoch}" && "${now_epoch}" -gt "${previous_epoch}" && "${terminal}" -ge "${previous_terminal}" ]]; then
      local delta_count=$((terminal - previous_terminal))
      local delta_seconds=$((now_epoch - previous_epoch))
      if [[ "${delta_count}" -gt 0 ]]; then
        rate_per_minute="$(awk -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { printf "%.2f", c * 60 / s }')"
        eta_seconds="$(awk -v remaining="$((TARGET - terminal))" -v c="${delta_count}" -v s="${delta_seconds}" 'BEGIN { if (c > 0) printf "%.0f", remaining * s / c }')"
      fi
    fi
    previous_terminal="${terminal}"
    previous_epoch="${now_epoch}"

    if [[ "${latest}" -gt "${TARGET}" || "${fragments}" -gt "${TARGET}" ]]; then
      log "dataset_count_exceeds_target latest=${latest}/${TARGET} fragments=${fragments}/${TARGET}; refusing_eval"
      write_import_gate_result "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "dataset_count_exceeds_target"
      write_status "failed" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "$(live_import_pid || true)" "${rate_per_minute}" "${eta_seconds}"
      return 1
    fi

    local pid=""
    pid="$(live_import_pid || true)"
    if [[ "${latest}" == "${TARGET}" && "${terminal}" == "${TARGET}" && "${failed}" == "0" && "${queued}" == "0" && "${processing}" == "0" && "${fragments}" == "${TARGET}" ]]; then
      if [[ -n "${pid}" ]]; then
        log "import_finalizing pid=${pid} terminal=${terminal}/${TARGET} completed=${completed} awaiting_review=${awaiting_review} quarantined=${quarantined}"
        write_status "import_finalizing" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "${pid}" "${rate_per_minute}" "0"
        sleep "${SLEEP_SECONDS}"
        continue
      fi
      if [[ ! -s "${IMPORT_DIR}/knowledge_mapping.json" || ! -s "${IMPORT_DIR}/summary.json" ]]; then
        if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
          log "max_import_restarts_reached while finalizing artifacts"
          write_import_gate_result "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "import_artifacts_missing"
          return 1
        fi
        restarts=$((restarts + 1))
        log "placements_complete_but_import_artifacts_missing restart=${restarts}"
        start_import
        sleep "${SLEEP_SECONDS}"
        continue
      fi
      log "full_import_verified terminal=${terminal}/${TARGET} completed=${completed} awaiting_review=${awaiting_review} quarantined=${quarantined} fragments=${fragments}/${TARGET} attempts=${attempts}"
      write_import_gate_result "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}"
      write_status "full_import_verified" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
      if ! run_baseline; then
        write_status "failed" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
        return 1
      fi
      # Preserve complete scores for V1/V2 comparison without treating quarantined input as accepted.
      if [[ "$(jq -r '.passed' "${IMPORT_GATE_RESULT}")" != "true" ]]; then
        log "import_gate_failed path=${IMPORT_GATE_RESULT}"
        write_status "failed" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
        return 1
      fi
      write_status "done" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "0"
      log "done"
      return 0
    fi

    if [[ -n "${pid}" ]]; then
      log "import_running pid=${pid} terminal=${terminal}/${TARGET} completed=${completed} awaiting_review=${awaiting_review} quarantined=${quarantined} failed=${failed} pending=${pending} fragments=${fragments}"
      write_status "import_running" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "${pid}" "${rate_per_minute}" "${eta_seconds}"
    elif [[ "${pending}" -gt 0 ]]; then
      log "placement_worker_draining terminal=${terminal}/${TARGET} completed=${completed} awaiting_review=${awaiting_review} quarantined=${quarantined} queued=${queued} processing=${processing}; import_not_restarted"
      write_status "placement_worker_draining" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
    else
      if [[ "${restarts}" -ge "${MAX_IMPORT_RESTARTS}" ]]; then
        log "max_import_restarts_reached restarts=${restarts} terminal=${terminal}/${TARGET}"
        write_import_gate_result "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "max_import_restarts_reached"
        write_status "failed" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
        return 1
      fi
      restarts=$((restarts + 1))
      log "import_not_running restart=${restarts} terminal=${terminal}/${TARGET} completed=${completed} awaiting_review=${awaiting_review} quarantined=${quarantined} failed=${failed} unseen=$((TARGET - latest))"
      write_status "import_restarting" "${fragments}" "${latest}" "${completed}" "${awaiting_review}" "${quarantined}" "${failed}" "${queued}" "${processing}" "${attempts}" "" "${rate_per_minute}" "${eta_seconds}"
      start_import
    fi
    sleep "${SLEEP_SECONDS}"
  done
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
