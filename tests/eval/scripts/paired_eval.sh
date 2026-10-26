#!/usr/bin/env bash

pair_tool() {
  "${PAIR_RUNNER}" --mode paired --pair-config "${PAIR_CONFIG}" --pair-action "$@"
}

pair_value() {
  jq -er --arg side "${pair_side}" ".[\$side] | $1" "${PAIR_CONFIG}"
}

pair_http() {
  local url="$1" token_name="$2" method="${3:-GET}" body="${4:-}"
  local token="${!token_name:?Missing prepared-runtime credential environment variable}"
  if [[ "${token}" == *$'\n'* || "${token}" == *'"'* || "${token}" == *'\'* ]]; then
    echo "invalid operational credential encoding" >&2
    return 1
  fi
  local -a args=(--silent --show-error --max-time 30 --request "${method}" --header 'Content-Type: application/json')
  if [[ "${5:-false}" == true ]]; then args+=(--write-out $'\n%{http_code}'); else args+=(--fail); fi
  [[ -z "${body}" ]] || args+=(--data-binary "${body}")
  printf 'header = "Authorization: Bearer %s"\n' "${token}" | curl --config - "${args[@]}" "${url}"
}

pair_control() {
  pair_http "$(pair_value '.runtime.control_url')/control/api$1" "$(pair_value '.runtime.control_token_env')" "${2:-GET}" "${3:-}" | jq -e '.data'
}

pair_inspect_runtime() {
  local protocol_response
  protocol_response="$(pair_http "$(pair_value '.runtime.control_url')/control/api/ontology/status" "$(pair_value '.runtime.control_token_env')" GET "" true)"
  jq -n --argjson status "${protocol_response##*$'\n'}" --arg body "${protocol_response%$'\n'*}" '{status:$status,body:$body}' |
    pair_tool protocol --pair-side "${pair_side}" --pair-input - >/dev/null
  docker inspect "$(pair_value '.runtime.server_container')" "$(pair_value '.runtime.postgres_container')" |
    pair_tool containers --pair-side "${pair_side}" --pair-input - >/dev/null
  pair_http "$(pair_value '.runtime.base_url')/ui/api/session" "$(pair_value '.runtime.api_key_env')" |
    pair_tool session --pair-side "${pair_side}" --pair-input - >/dev/null
  pair_http "$(pair_value '.runtime.base_url')/mcp" "$(pair_value '.runtime.api_key_env')" POST \
    '{"jsonrpc":"2.0","id":"paired-preflight","method":"tools/list","params":{}}' |
    pair_tool tools --pair-side "${pair_side}" --pair-input - >/dev/null
}

pair_snapshot() {
  local destination="$1" protocol
  protocol="$(pair_value '.protocol')"
  local temporary
  temporary="$(mktemp -d "${pair_out}/.snapshot.XXXXXX")"
  {
    printf '%s\n' 'BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;' "SET LOCAL statement_timeout='30s';"
    printf '%s\n' 'PREPARE paired_import(uuid,uuid,uuid,bigint,uuid) AS'
    pair_tool sql-import
    printf '%s\n' "; EXECUTE paired_import(:'team',:'owner',:'space',:'generation',:'credential');"
    if [[ "${protocol}" != none ]]; then
      printf '%s\n' 'PREPARE paired_ontology(uuid,uuid,uuid,bigint,uuid) AS'
      pair_tool sql-ontology
      printf '%s\n' "; EXECUTE paired_ontology(:'team',:'owner',:'space',:'generation',:'credential');"
    fi
    printf '%s\n' 'COMMIT;'
  } | docker exec -i "$(pair_value '.runtime.postgres_container')" sh -ec \
    'exec psql -X -q -t -A -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v team="$1" -v owner="$2" -v space="$3" -v generation="$4" -v credential="$5"' \
    paired-snapshot "$(pair_value '.scope.team_id')" "$(pair_value '.scope.owner_id')" \
    "$(pair_value '.scope.space_id')" "$(pair_value '.scope.generation')" "$(pair_value '.runtime.credential_id')" > "${temporary}/database.jsonl"
  jq -s 'add' "${temporary}/database.jsonl" > "${temporary}/database.json"
  pair_control /search/convergence > "${temporary}/search.json"
  if [[ "${protocol}" != none ]]; then
    pair_control /ontology/status > "${temporary}/ontology.json"
    pair_control "/teams/$(pair_value '.scope.team_id')/community/status" > "${temporary}/community.json"
  else
    printf 'null\n' > "${temporary}/ontology.json"
    printf 'null\n' > "${temporary}/community.json"
  fi
  jq -n --slurpfile database "${temporary}/database.json" --slurpfile search "${temporary}/search.json" \
    --slurpfile ontology "${temporary}/ontology.json" --slurpfile community "${temporary}/community.json" \
    '{database:$database[0],search:$search[0],ontology:$ontology[0],community:$community[0]}' > "${destination}"
  rm "${temporary}/database.jsonl" "${temporary}/database.json" "${temporary}/search.json" "${temporary}/ontology.json" "${temporary}/community.json"
  rmdir "${temporary}"
}

pair_observe() {
  local frozen="${1:-false}" path="${pair_stage_dir}/snapshot-$(date -u +%s%N).json"
  pair_snapshot "${path}"
  pair_tool observe --pair-side "${pair_side}" --pair-stage "${pair_stage}" --pair-input "${path}" \
    --pair-frozen="${frozen}" > "${pair_stage_dir}/observation-result.tmp"
  mv "${pair_stage_dir}/observation-result.tmp" "${pair_stage_dir}/observation-result.json"
  jq '.observation' "${pair_stage_dir}/observation-result.json" > "${pair_stage_dir}/observation.tmp"
  mv "${pair_stage_dir}/observation.tmp" "${pair_stage_dir}/observation.json"
}

pair_remaining() {
  local deadline now
  deadline="$(date -u -d "$(jq -er '.deadline' "${PAIR_CONFIG}")" +%s)"
  now="$(date +%s)"
  if (( now >= deadline )); then
    echo "paired observation deadline reached; retain stage evidence before resuming" >&2
    return 124
  fi
  printf '%s\n' "$(( deadline-now ))"
}

pair_wait() {
  local remaining
  remaining="$(pair_remaining)"
  (( remaining > 60 )) && remaining=60
  sleep "${remaining}"
}

pair_begin() {
  pair_stage="$1"
  local started
  started="$(pair_tool begin --pair-side "${pair_side}" --pair-stage "${pair_stage}" --pair-pid "$$")"
  pair_stage_dir="$(jq -er '.directory' <<< "${started}")"
  pair_reused="$(jq -er '.reused | tostring' <<< "${started}")"
  printf '%s %s/%s %s\n' "$(date -u +%FT%TZ)" "${pair_side}" "${pair_stage}" "${pair_reused}"
}

pair_finish() {
  pair_tool finish --pair-directory "${pair_stage_dir}" >/dev/null
  pair_stage_dir=""
}

pair_native() {
  local binary="$1" code=0
  shift
  local key_name remaining
  remaining="$(pair_remaining)" || return "$?"
  key_name="$(pair_value '.runtime.api_key_env')"
  DENSE_MEM_API_KEY="${!key_name:-}" bash -ec '
    "$1" --mode paired --pair-config "$2" --pair-action process --pair-pid "$$" > "$3/native-process.tmp"
    mv "$3/native-process.tmp" "$3/native-process.json"
    shift 3
    exec timeout --signal=TERM --kill-after=30s "$@"
  ' paired-native "${PAIR_RUNNER}" "${PAIR_CONFIG}" "${pair_stage_dir}" "${remaining}s" \
    "${binary}" "$@" > "${pair_stage_dir}/native.log" 2>&1 &
  pair_native_pid=$!
  wait "${pair_native_pid}" || code="$?"
  pair_native_pid=""
  printf '{"exit_code":%s,"finished_at":"%s"}\n' "${code}" "$(date -u +%FT%T.%NZ)" > "${pair_stage_dir}/native-exit.json"
  if (( code != 0 )); then
    if (( code == 124 || code >= 128 )); then pair_interrupted=true; fi
    echo "native ${pair_side}/${pair_stage} failed with exit ${code}; retained ${pair_stage_dir}/native.log" >&2
    return "${code}"
  fi
}

pair_command() {
  local action="$1" key
  key="$(jq -er '.run_id' "${PAIR_CONFIG}"):${pair_side}:${pair_stage}:$(basename "${pair_stage_dir}"):${action}"
  if (( ${#key} > 128 )); then
    key="paired:$(printf '%s' "${key}" | sha256sum | awk '{print $1}')"
  fi
  pair_control "/ontology/${action}" POST "$(jq -n --arg key "${key}" '{operation_key:$key}')" > "${pair_stage_dir}/${action}.json"
}

pair_capacity() {
  pair_begin capacity
  if [[ "${pair_reused}" == true ]]; then pair_stage_dir=""; return; fi
  if [[ "$(pair_value '.protocol')" == adr0014 ]]; then
    pair_command pause
    local body
    body="$(jq -c --arg side "${pair_side}" '.[$side] | {items:[
      {key:"ONTOLOGY_MAINTENANCE_ENABLED",value:"true"},
      {key:"ONTOLOGY_MAINTENANCE_MAX_CONCURRENCY",value:(.capacity.global|tostring)},
      {key:"ONTOLOGY_MAINTENANCE_TEAM_MAX_CONCURRENCY",value:(.capacity.team|tostring)},
      {key:"ONTOLOGY_MAINTENANCE_TEAM_OVERRIDES_JSON",value:([{team_id:.scope.team_id,max_concurrency:.capacity.team}]|tojson)},
      {key:"ONTOLOGY_MAINTENANCE_CONTEXT_WINDOW_TOKENS",value:(.capacity.context_window_tokens|tostring)},
      {key:"ONTOLOGY_MAINTENANCE_MODEL",value:.models.ontology}]}' "${PAIR_CONFIG}")"
    pair_control /config/ontology-maintenance PATCH "${body}" > "${pair_stage_dir}/configuration.json"
  fi
  while true; do
    pair_observe
    if [[ "$(pair_value '.protocol')" == none ]] || jq -e --argjson expected "$(pair_value '.capacity')" --arg model "$(pair_value '.models.ontology')" '.window_id != "" and .policy == $expected and .model == $model' "${pair_stage_dir}/observation.json" >/dev/null; then break; fi
    pair_wait
  done
  pair_finish
}

pair_import() {
  pair_begin import
  if [[ "${pair_reused}" == true ]]; then pair_stage_dir=""; return; fi
  pair_observe
  jq -r '.import.completed_source_ids[]' "${pair_stage_dir}/observation-result.json" > "${pair_stage_dir}/completed-source-ids.txt"
  pair_native "$(pair_value '.runner.path')" --mode import --seed "${pair_seed}" --suite "${pair_suite}" \
    --out "${pair_stage_dir}/native" --base-url "$(pair_value '.runtime.base_url')" --import-seed \
    --import-concurrency "$(pair_value '.import_concurrency')" --resume-source-doc-ids "${pair_stage_dir}/completed-source-ids.txt" \
    --run-id "${pair_run_id}-${pair_side}-import" --max-page-size 500
  pair_tool complete-import-mapping --pair-side "${pair_side}" --pair-directory "${pair_stage_dir}" >/dev/null
  pair_observe
  pair_finish
}

pair_drain() {
  pair_begin drain
  if [[ "${pair_reused}" == true ]]; then pair_stage_dir=""; return; fi
  pair_observe
  if [[ "$(pair_value '.protocol')" == adr0014 ]]; then pair_command resume; fi
  while true; do
    pair_observe
    if [[ "$(jq -r '.readiness.ready' "${pair_stage_dir}/observation-result.json")" == true ]]; then break; fi
    printf '%s %s pending=%s waiting=%s\n' "$(date -u +%FT%TZ)" "${pair_side}" \
      "$(jq -r '.observation.pending' "${pair_stage_dir}/observation-result.json")" \
      "$(jq -c '.readiness.waiting_for' "${pair_stage_dir}/observation-result.json")"
    pair_wait
  done
  if [[ "$(pair_value '.protocol')" == adr0014 ]]; then pair_command pause; fi
  pair_observe true
  pair_finish
}

pair_capture() {
  pair_begin capture
  if [[ "${pair_reused}" != true ]]; then
    pair_observe true
    cp "${pair_stage_dir}/observation.json" "${pair_stage_dir}/before.json"
    local imported
    imported="$(pair_tool latest --pair-side "${pair_side}" --pair-stage import | jq -er '.directory')"
    pair_native "$(pair_value '.runner.path')" --mode "${pair_side}" --seed "${pair_seed}" --suite "${pair_suite}" \
      --mapping "${imported}/native/knowledge_mapping.json" --out "${pair_stage_dir}/native" \
      --base-url "$(pair_value '.runtime.base_url')" --max-unmapped-source-refs 0 --run-id "${pair_run_id}-${pair_side}-capture"
    pair_observe true
    pair_tool finish --pair-directory "${pair_stage_dir}" >/dev/null
  fi
  pair_capture_dir="${pair_stage_dir}/native"
  pair_stage_dir=""
}

pair_score() {
  pair_begin score
  if [[ "${pair_reused}" == true ]]; then pair_stage_dir=""; return; fi
  pair_tool capture-proof --pair-side "${pair_side}" --pair-directory "${pair_capture_dir}" > "${pair_stage_dir}/capture-proof.json"
  jq ' .scorer ' "${PAIR_CONFIG}" > "${pair_stage_dir}/scorer.json"
  pair_native "$(jq -er '.scorer.path' "${PAIR_CONFIG}")" --mode "${pair_side}" --seed "${pair_seed}" --suite "${pair_suite}" \
    --traces "${pair_capture_dir}/recall_traces.jsonl" --mapping "${pair_capture_dir}/knowledge_mapping.json" \
    --out "${pair_stage_dir}/native" --max-unmapped-source-refs 0 --run-id "${pair_run_id}-${pair_side}-common-score"
  pair_finish
}

pair_on_error() {
  local pair_code="$1"
  if [[ -n "${pair_stage_dir}" && "${pair_interrupted}" != true && "${pair_code}" != 124 ]]; then
    pair_tool fail --pair-directory "${pair_stage_dir}" >/dev/null || echo "failed to retain stage failure receipt" >&2
  fi
  exit "${pair_code}"
}

pair_main() {
  set -Eeuo pipefail
  umask 077
  PAIR_CONFIG="$(realpath -- "${PAIR_CONFIG}")"
  local pair_tools=""
  if [[ -z "${PAIR_RUNNER:-}" ]]; then
    pair_tools="$(mktemp -d)"
    PAIR_RUNNER="${pair_tools}/eval-runner"
    go build -o "${PAIR_RUNNER}.tmp" ./cmd/eval-runner || return "$?"
    mv "${PAIR_RUNNER}.tmp" "${PAIR_RUNNER}"
  fi
  local pair_out pair_seed pair_suite pair_run_id pair_side pair_stage="" pair_stage_dir="" pair_reused pair_capture_dir pair_interrupted=false pair_native_pid=""
  pair_tool check-config >/dev/null
  pair_out="$(jq -er '.out_dir' "${PAIR_CONFIG}")"
  mkdir -p "${pair_out}"
  exec {pair_lock}> "${pair_out}/paired.lock"
  flock -n "${pair_lock}" || { echo "another paired monitor owns this output" >&2; return 1; }
  pair_tool validate > "${pair_out}/validated-config.tmp"
  mv "${pair_out}/validated-config.tmp" "${pair_out}/validated-config.json"
  pair_seed="$(jq -er '.seed_manifest' "${PAIR_CONFIG}")"
  pair_suite="$(jq -er '.suite' "${PAIR_CONFIG}")"
  pair_run_id="$(jq -er '.run_id' "${PAIR_CONFIG}")"
  trap 'pair_interrupted=true; if [[ -n "${pair_native_pid}" ]]; then kill -TERM "${pair_native_pid}" 2>/dev/null || true; wait "${pair_native_pid}" || true; fi; exit 130' INT TERM
  trap 'pair_on_error "$?"' ERR
  for pair_side in baseline candidate; do
    pair_begin validate
    if [[ "${pair_reused}" != true ]]; then
      pair_native "$(pair_value '.runner.path')" --mode validate --seed "${pair_seed}" --suite "${pair_suite}" --out "${pair_stage_dir}/native"
      pair_finish
    else pair_stage_dir=""; fi
    pair_capture_dir="$(jq -r --arg side "${pair_side}" '.[$side].capture_dir // ""' "${PAIR_CONFIG}")"
    if [[ -z "${pair_capture_dir}" ]]; then
      pair_inspect_runtime
      local runtime_lock container_id lock_root
      lock_root="/tmp/dense-mem-paired-locks-$(id -u)"
      mkdir -p "${lock_root}"
      for container_id in $(docker inspect --format '{{.Id}}' "$(pair_value '.runtime.server_container')" "$(pair_value '.runtime.postgres_container')"); do
        [[ "${container_id}" =~ ^[0-9a-f]{64}$ ]] || return 1
        exec {runtime_lock}> "${lock_root}/${container_id}.lock"
        flock -n "${runtime_lock}" || { echo "another monitor owns the prepared container" >&2; return 1; }
      done
      pair_snapshot "${pair_out}/${pair_side}/preflight.json"
      pair_tool claim-runtime --pair-side "${pair_side}" --pair-input "${pair_out}/${pair_side}/preflight.json" > "${pair_out}/${pair_side}/runtime-claim.json"
    fi
  done
  for pair_side in baseline candidate; do
    pair_capture_dir="$(jq -r --arg side "${pair_side}" '.[$side].capture_dir // ""' "${PAIR_CONFIG}")"
    if [[ -z "${pair_capture_dir}" ]]; then
      pair_capacity
      pair_import
      pair_drain
      pair_capture
      pair_snapshot "${pair_out}/${pair_side}/after-capture-snapshot.json"
      pair_tool observe --pair-side "${pair_side}" --pair-frozen --pair-input "${pair_out}/${pair_side}/after-capture-snapshot.json" |
        jq '.observation' > "${pair_out}/${pair_side}/after-capture.json"
      pair_tool capture-proof --pair-side "${pair_side}" --pair-directory "${pair_capture_dir}" --pair-input "${pair_out}/${pair_side}/after-capture.json" >/dev/null
    fi
    pair_score
  done
  pair_tool report
  if [[ -n "${pair_tools}" ]]; then rm "${PAIR_RUNNER}"; rmdir "${pair_tools}"; fi
}
