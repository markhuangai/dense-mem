#!/usr/bin/env bash

compose() {
  docker compose -p "${EVAL_COMPOSE_PROJECT}" -f docker-compose.yml -f "${EVAL_COMPOSE_OVERRIDE}" "$@"
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
  local eval_compose_project="${EVAL_COMPOSE_PROJECT}"
  local eval_compose_override="${EVAL_COMPOSE_OVERRIDE}"
  local eval_data_dir="${V1_DATA_DIR}"
  local eval_compose_data_dir="${V1_COMPOSE_DATA_DIR}"
  local eval_tool_transport="${DENSE_MEM_EVAL_TOOL_TRANSPORT}"
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
  export EVAL_COMPOSE_PROJECT="${eval_compose_project}"
  export EVAL_COMPOSE_OVERRIDE="${eval_compose_override}"
  export EVAL_DATA_DIR="${eval_data_dir}"
  export V1_COMPOSE_DATA_DIR="${eval_compose_data_dir}"
  export V2_COMPOSE_DATA_DIR="${V2_COMPOSE_DATA_DIR:-${eval_compose_data_dir}}"
  export DENSE_MEM_EVAL_TOOL_TRANSPORT="${eval_tool_transport}"
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
  local tmp="${RUNNER}.tmp"
  (cd "${RUNNER_ROOT_DIR}" && go build -o "${tmp}" ./cmd/eval-runner)
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

count_mapped_refs() {
  if [[ -s "${IMPORT_DIR}/knowledge_mapping.json" ]]; then
    jq '(.by_source_doc_id // {}) | length' "${IMPORT_DIR}/knowledge_mapping.json"
  else
    printf '0\n'
  fi
}

placement_counts() {
  psql_eval "
    WITH ranked AS (
      SELECT substring(run.evidence -> item.evidence_index ->> 'idempotency_key' FROM 6) AS source_doc_id,
             run.status,
             row_number() OVER (
               PARTITION BY run.evidence -> item.evidence_index ->> 'idempotency_key'
               ORDER BY run.created_at DESC, run.ingest_id DESC
             ) AS row_num
      FROM memory_placement_runs AS run
      JOIN memory_placement_items AS item
        ON item.ingest_id = run.ingest_id
       AND item.profile_id = run.profile_id
      WHERE run.profile_id = :'team_id'::uuid
        AND run.evidence -> item.evidence_index ->> 'idempotency_key' LIKE 'eval:%'
    ), latest AS (
      SELECT status FROM ranked WHERE row_num = 1 AND btrim(source_doc_id) <> ''
    ), historical AS (
      SELECT count(*) AS attempts
      FROM ranked
    )
    SELECT count(*),
           count(*) FILTER (WHERE status = 'completed'),
           count(*) FILTER (WHERE status = 'failed'),
           count(*) FILTER (WHERE status = 'queued'),
           count(*) FILTER (WHERE status = 'processing'),
           (SELECT attempts FROM historical)
    FROM latest;
  "
}

embedding_counts() {
  local present
  present="$(psql_eval "
    SELECT CASE
      WHEN to_regclass('public.semantic_embedding_jobs') IS NULL
        OR to_regclass('public.semantic_search_documents') IS NULL
      THEN 0 ELSE 1 END;
  ")"
  if [[ "${present}" != "1" ]]; then
    printf '0|0|0|0|0|0|0|0|0|0\n'
    return 0
  fi
  psql_eval "
    WITH job_stats AS (
      SELECT count(*) AS total,
             count(*) FILTER (WHERE status = 'completed') AS completed,
             count(*) FILTER (WHERE status = 'failed') AS failed,
             count(*) FILTER (WHERE status = 'queued') AS queued,
             count(*) FILTER (WHERE status = 'processing') AS processing
      FROM semantic_embedding_jobs
      WHERE team_id = :'team_id'::uuid
    ), doc_stats AS (
      SELECT count(*) AS total,
             count(*) FILTER (WHERE search_state = 'current') AS current,
             count(*) FILTER (WHERE search_state = 'pending') AS pending,
             count(*) FILTER (WHERE search_state = 'failed') AS failed
      FROM semantic_search_documents
      WHERE team_id = :'team_id'::uuid
    )
    SELECT 1,
           job_stats.total,
           job_stats.completed,
           job_stats.failed,
           job_stats.queued,
           job_stats.processing,
           doc_stats.total,
           doc_stats.current,
           doc_stats.pending,
           doc_stats.failed
    FROM job_stats, doc_stats;
  "
}

write_resume_files() {
  local completed_tmp failed_tmp
  completed_tmp="$(mktemp "${RESUME_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  failed_tmp="$(mktemp "${FAILED_SOURCE_DOC_IDS}.tmp.XXXXXX")"
  psql_eval "
    WITH ranked AS (
      SELECT substring(run.evidence -> item.evidence_index ->> 'idempotency_key' FROM 6) AS source_doc_id,
             run.status,
             row_number() OVER (
               PARTITION BY run.evidence -> item.evidence_index ->> 'idempotency_key'
               ORDER BY run.created_at DESC, run.ingest_id DESC
             ) AS row_num
      FROM memory_placement_runs AS run
      JOIN memory_placement_items AS item
        ON item.ingest_id = run.ingest_id
       AND item.profile_id = run.profile_id
      WHERE run.profile_id = :'team_id'::uuid
        AND run.evidence -> item.evidence_index ->> 'idempotency_key' LIKE 'eval:%'
    )
    SELECT source_doc_id
    FROM ranked
    WHERE row_num = 1 AND status = 'completed' AND btrim(source_doc_id) <> ''
    ORDER BY source_doc_id;
  " > "${completed_tmp}"
  psql_eval "
    WITH ranked AS (
      SELECT substring(run.evidence -> item.evidence_index ->> 'idempotency_key' FROM 6) AS source_doc_id,
             run.status,
             row_number() OVER (
               PARTITION BY run.evidence -> item.evidence_index ->> 'idempotency_key'
               ORDER BY run.created_at DESC, run.ingest_id DESC
             ) AS row_num
      FROM memory_placement_runs AS run
      JOIN memory_placement_items AS item
        ON item.ingest_id = run.ingest_id
       AND item.profile_id = run.profile_id
      WHERE run.profile_id = :'team_id'::uuid
        AND run.evidence -> item.evidence_index ->> 'idempotency_key' LIKE 'eval:%'
    )
    SELECT source_doc_id
    FROM ranked
    WHERE row_num = 1 AND status = 'failed' AND btrim(source_doc_id) <> ''
    ORDER BY source_doc_id;
  " > "${failed_tmp}"
  mv "${completed_tmp}" "${RESUME_SOURCE_DOC_IDS}"
  mv "${failed_tmp}" "${FAILED_SOURCE_DOC_IDS}"
}

write_completed_mapping() {
  local expected_completed="$1" tmp mapped_refs evidence_refs relationship_refs
  if ! [[ "${expected_completed}" =~ ^[0-9]+$ ]]; then
    log "invalid_completed_mapping_count value=${expected_completed}"
    return 1
  fi

  tmp="$(mktemp "${RECOVERED_MAPPING}.tmp.XXXXXX")"
  if ! psql_eval "
    WITH ranked AS (
      SELECT run.ingest_id,
             item.evidence_index,
             substring(run.evidence -> item.evidence_index ->> 'idempotency_key' FROM 6) AS source_doc_id,
             run.status,
             row_number() OVER (
               PARTITION BY run.evidence -> item.evidence_index ->> 'idempotency_key'
               ORDER BY run.created_at DESC, run.ingest_id DESC
             ) AS row_num
      FROM memory_placement_runs AS run
      JOIN memory_placement_items AS item
        ON item.ingest_id = run.ingest_id
       AND item.profile_id = run.profile_id
      WHERE run.profile_id = :'team_id'::uuid
        AND run.evidence -> item.evidence_index ->> 'idempotency_key' LIKE 'eval:%'
    ), latest_completed AS (
      SELECT ingest_id, source_doc_id, evidence_index
      FROM ranked
      WHERE row_num = 1
        AND status = 'completed'
        AND btrim(source_doc_id) <> ''
    ), raw_refs AS (
      SELECT latest_completed.source_doc_id,
             item.evidence_index,
             item.created_at,
             evidence.fragment_id::text AS fragment_id
      FROM latest_completed
      JOIN memory_placement_items AS item
        ON item.ingest_id = latest_completed.ingest_id
       AND item.profile_id = :'team_id'::uuid
       AND item.evidence_index = latest_completed.evidence_index
      JOIN semantic_evidence_fragments AS evidence
        ON evidence.team_id = :'team_id'::uuid
       AND evidence.fragment_id::text = nullif(btrim(item.fragment_id), '')
       AND evidence.source_doc_id = latest_completed.source_doc_id
    ), ref_rows AS (
      SELECT source_doc_id,
             'evidence'::text AS ref_type,
             fragment_id AS ref_id,
             evidence_index,
             created_at
      FROM raw_refs
      UNION ALL
      SELECT source_doc_id,
             'relationship'::text AS ref_type,
             relationship.relationship_id::text AS ref_id,
             raw_refs.evidence_index,
             support.created_at
      FROM raw_refs
      JOIN semantic_relationship_supports AS support
        ON support.team_id = :'team_id'::uuid
       AND support.fragment_id::text = raw_refs.fragment_id
      JOIN semantic_relationship_records AS relationship
        ON relationship.team_id = support.team_id
       AND relationship.relationship_id = support.relationship_id
    ), deduplicated AS (
      SELECT source_doc_id,
             ref_type,
             ref_id,
             min(evidence_index) AS evidence_index,
             min(created_at) AS created_at
      FROM ref_rows
      GROUP BY source_doc_id, ref_type, ref_id
    ), refs_by_type AS (
      SELECT source_doc_id,
             ref_type,
             jsonb_agg(
               jsonb_build_object(
                 'type', ref_type,
                 'id', ref_id,
                 'source_doc_id', source_doc_id
               )
               ORDER BY evidence_index, created_at, ref_id
             ) AS refs
      FROM deduplicated
      GROUP BY source_doc_id, ref_type
    ), refs_by_source AS (
      SELECT source_doc_id,
             jsonb_object_agg(ref_type, refs ORDER BY ref_type) AS refs
      FROM refs_by_type
      GROUP BY source_doc_id
    ), default_evidence AS (
      SELECT DISTINCT ON (source_doc_id)
             source_doc_id,
             ref_id
      FROM deduplicated
      WHERE ref_type = 'evidence'
      ORDER BY source_doc_id, evidence_index, created_at, ref_id
    ), default_mapping AS (
      SELECT COALESCE(
               jsonb_object_agg(
                 source_doc_id,
                 jsonb_build_object(
                   'type', 'evidence',
                   'id', ref_id,
                   'source_doc_id', source_doc_id
                 )
                 ORDER BY source_doc_id
               ),
               '{}'::jsonb
             ) AS value
      FROM default_evidence
    ), typed_mapping AS (
      SELECT COALESCE(
               jsonb_object_agg(source_doc_id, refs ORDER BY source_doc_id),
               '{}'::jsonb
             ) AS value
      FROM refs_by_source
    )
    SELECT jsonb_pretty(jsonb_build_object(
      'by_source_doc_id', default_mapping.value,
      'by_source_doc_id_and_type', typed_mapping.value
    ))
    FROM default_mapping, typed_mapping;
  " > "${tmp}"; then
    rm -f "${tmp}"
    log "completed_mapping_query_failed"
    return 1
  fi

  if ! jq -e --argjson expected "${expected_completed}" '
    ((.by_source_doc_id // {}) | type) == "object"
    and ((.by_source_doc_id_and_type // {}) | type) == "object"
    and ((.by_source_doc_id | length) == $expected)
    and ((.by_source_doc_id_and_type | length) == $expected)
    and ([.by_source_doc_id[] | select(
      .type != "evidence"
      or ((.id // "") | length) == 0
      or ((.source_doc_id // "") | length) == 0
    )] | length) == 0
    and ([.by_source_doc_id_and_type[] | select(
      ((.evidence // []) | length) == 0
    )] | length) == 0
  ' "${tmp}" > /dev/null; then
    mapped_refs="$(jq -r '(.by_source_doc_id // {}) | length' "${tmp}" 2>/dev/null || printf 'invalid')"
    rm -f "${tmp}"
    log "completed_mapping_validation_failed expected=${expected_completed} mapped_refs=${mapped_refs}"
    return 1
  fi

  mapped_refs="$(jq '(.by_source_doc_id // {}) | length' "${tmp}")"
  evidence_refs="$(jq '[.by_source_doc_id_and_type[].evidence[]?] | length' "${tmp}")"
  relationship_refs="$(jq '[.by_source_doc_id_and_type[].relationship[]?] | length' "${tmp}")"
  mv "${tmp}" "${RECOVERED_MAPPING}"
  log "completed_mapping_recovered mapped_refs=${mapped_refs} evidence_refs=${evidence_refs} relationship_refs=${relationship_refs}"
}

completed_mapping_matches_import() {
  [[ -s "${RECOVERED_MAPPING}" && -s "${IMPORT_DIR}/knowledge_mapping.json" ]] || return 1
  cmp -s \
    <(jq -cS . "${RECOVERED_MAPPING}") \
    <(jq -cS . "${IMPORT_DIR}/knowledge_mapping.json")
}

write_placement_summary() {
  local tmp
  tmp="$(mktemp "${PLACEMENT_SUMMARY}.tmp.XXXXXX")"
  psql_eval "
    WITH ranked AS (
      SELECT run.ingest_id,
             item.evidence_index,
             substring(run.evidence -> item.evidence_index ->> 'idempotency_key' FROM 6) AS source_doc_id,
             run.status,
             item.status AS item_status,
             item.category,
             row_number() OVER (
               PARTITION BY run.evidence -> item.evidence_index ->> 'idempotency_key'
               ORDER BY run.created_at DESC, run.ingest_id DESC
             ) AS row_num
      FROM memory_placement_runs AS run
      JOIN memory_placement_items AS item
        ON item.ingest_id = run.ingest_id
       AND item.profile_id = run.profile_id
      WHERE run.profile_id = :'team_id'::uuid
        AND run.evidence -> item.evidence_index ->> 'idempotency_key' LIKE 'eval:%'
    ), latest AS (
      SELECT ingest_id, evidence_index, source_doc_id, status, item_status, category
      FROM ranked
      WHERE row_num = 1 AND btrim(source_doc_id) <> ''
    ), latest_stats AS (
      SELECT count(*) AS total,
             count(*) FILTER (WHERE status = 'completed') AS completed,
             count(*) FILTER (WHERE status = 'failed') AS failed,
             count(*) FILTER (WHERE status = 'queued') AS queued,
             count(*) FILTER (WHERE status = 'processing') AS processing
      FROM latest
    ), latest_items AS (
      SELECT latest.category, latest.item_status, latest.status AS run_status
      FROM latest
    ), item_stats AS (
      SELECT count(*) AS total,
             count(*) FILTER (WHERE run_status = 'completed') AS completed_total,
             count(*) FILTER (
               WHERE run_status = 'completed'
                 AND category IN ('promoted_fact', 'accepted_promoted')
             ) AS promoted,
             count(*) FILTER (
               WHERE run_status = 'completed'
                 AND category IN ('rejected_false', 'rejected_explained')
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
        SELECT item_status AS status, count(*) AS status_count
        FROM latest_items
        GROUP BY item_status
        ORDER BY item_status
      ) AS counts
    ), historical_status_counts AS (
      SELECT COALESCE(jsonb_object_agg(status, status_count), '{}'::jsonb) AS value,
             COALESCE(sum(status_count), 0) AS total
      FROM (
        SELECT run.status, count(*) AS status_count
        FROM memory_placement_runs AS run
        JOIN memory_placement_items AS item
          ON item.ingest_id = run.ingest_id
         AND item.profile_id = run.profile_id
        WHERE run.profile_id = :'team_id'::uuid
          AND run.evidence -> item.evidence_index ->> 'idempotency_key' LIKE 'eval:%'
        GROUP BY run.status
        ORDER BY run.status
      ) AS counts
    )
    SELECT jsonb_pretty(jsonb_build_object(
      'generated_at', to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
      'latest_runs', jsonb_build_object(
        'total', latest_stats.total,
        'completed', latest_stats.completed,
        'failed', latest_stats.failed,
        'queued', latest_stats.queued,
        'processing', latest_stats.processing
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

write_status() {
  local phase="$1" mapped_refs="$2" latest="$3" completed="$4" failed="$5" queued="$6" processing="$7" attempts="$8"
  local import_pid="${9:-}" rate_per_minute="${10:-}" eta_seconds="${11:-}"
  local tmp
  tmp="$(mktemp "${STATUS_JSON}.tmp.XXXXXX")"
  jq -n \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg phase "${phase}" \
    --arg mapped_refs "${mapped_refs}" \
    --arg latest "${latest}" \
    --arg completed "${completed}" \
    --arg failed "${failed}" \
    --arg queued "${queued}" \
    --arg processing "${processing}" \
    --arg attempts "${attempts}" \
    --arg target "${TARGET}" \
    --arg embedding_tables "${embedding_tables:-0}" \
    --arg embedding_jobs_total "${embedding_jobs_total:-0}" \
    --arg embedding_jobs_completed "${embedding_jobs_completed:-0}" \
    --arg embedding_jobs_failed "${embedding_jobs_failed:-0}" \
    --arg embedding_jobs_queued "${embedding_jobs_queued:-0}" \
    --arg embedding_jobs_processing "${embedding_jobs_processing:-0}" \
    --arg search_docs_total "${search_docs_total:-0}" \
    --arg search_docs_current "${search_docs_current:-0}" \
    --arg search_docs_pending "${search_docs_pending:-0}" \
    --arg search_docs_failed "${search_docs_failed:-0}" \
    --arg import_pid "${import_pid}" \
    --arg rate_per_minute "${rate_per_minute}" \
    --arg eta_seconds "${eta_seconds}" \
    --arg import_dir "${IMPORT_DIR}" \
    --arg baseline_dir "${BASELINE_DIR}" \
    --arg placement_summary "${PLACEMENT_SUMMARY}" \
    --arg failed_source_doc_ids "${FAILED_SOURCE_DOC_IDS}" \
    --arg dataset_identity "${IDENTITY_JSON}" \
    'def nullable_number: if . == "" then null else tonumber end;
    {
      updated_at: $updated_at,
      phase: $phase,
      target: ($target | tonumber),
      mapped_refs: ($mapped_refs | tonumber),
      latest_placements: ($latest | tonumber),
      completed: ($completed | tonumber),
      failed: ($failed | tonumber),
      queued: ($queued | tonumber),
      processing: ($processing | tonumber),
      historical_attempts: ($attempts | tonumber),
      semantic_embedding: {
        tables_present: ($embedding_tables == "1"),
        jobs: {
          total: ($embedding_jobs_total | tonumber),
          completed: ($embedding_jobs_completed | tonumber),
          failed: ($embedding_jobs_failed | tonumber),
          queued: ($embedding_jobs_queued | tonumber),
          processing: ($embedding_jobs_processing | tonumber)
        },
        search_documents: {
          total: ($search_docs_total | tonumber),
          current: ($search_docs_current | tonumber),
          pending: ($search_docs_pending | tonumber),
          failed: ($search_docs_failed | tonumber)
        }
      },
      percent_complete: (($completed | tonumber) / ($target | tonumber) * 100),
      rate_per_minute: ($rate_per_minute | nullable_number),
      eta_seconds: ($eta_seconds | nullable_number),
      import_pid: $import_pid,
      import_dir: $import_dir,
      baseline_dir: $baseline_dir,
      placement_summary: $placement_summary,
      failed_source_doc_ids: $failed_source_doc_ids,
      dataset_identity: $dataset_identity
    }' > "${tmp}"
  mv "${tmp}" "${STATUS_JSON}"
}
