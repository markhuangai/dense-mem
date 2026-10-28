#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

seed_path="${tmp_dir}/seed_manifest.json"
suite_path="${tmp_dir}/suite.jsonl"
printf '%s\n' '{"counts":{"corpus":991}}' > "${seed_path}"
: > "${suite_path}"

export SEED="${seed_path}"
export SUITE="${suite_path}"
export ALLOW_UNGATED_EVALUATION=1
export V1_DATA_DIR="${tmp_dir}/runtime"
unset IMPORT_CONCURRENCY DENSE_MEM_EVAL_IMPORT_CONCURRENCY
# shellcheck source=run_full_public_rag_eval_until_done.sh
source "${ROOT_DIR}/tests/eval/scripts/run_full_public_rag_eval_until_done.sh"

if [[ -n "${IMPORT_CONCURRENCY}" ]]; then
  echo "monitor must inherit the runner's import concurrency default" >&2
  exit 1
fi

if [[ "$(terminal_attempt_outcomes_sql)" != "'completed'" ]]; then
  echo "terminal attempt outcome list is not completed-only" >&2
  exit 1
fi
if [[ "$(terminal_attempt_count 991)" != "991" ]]; then
  echo "terminal attempt count does not use completed attempts" >&2
  exit 1
fi

attempt_query="$(attempt_docs_sql)"
if [[ "${attempt_query}" != *"LEFT JOIN evidence_fragments AS fragment"* ]]; then
  echo "attempt document query drops fragmentless attempts" >&2
  exit 1
fi
if [[ "${attempt_query}" != *"NULLIF(substring(attempt.idempotency_key FROM 6), '')"* ]]; then
  echo "attempt document query does not derive eval source IDs" >&2
  exit 1
fi
if [[ "${attempt_query}" != *"attempt.idempotency_key LIKE 'eval:%'"* ]]; then
  echo "attempt document fallback is not eval-key scoped" >&2
  exit 1
fi

write_import_gate_result 991 991 991 0 0
jq -e '
  .schema_version == 1 and
  .status == "passed" and
  .passed == true and
  .terminal == 991 and
  .completed == 991 and
  .failed == 0 and
  .reasons == []
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result 991 991 990 1 2
jq -e '
  .status == "failed" and
  .passed == false and
  .terminal == 990 and
  .completed == 990 and
  .failed == 1 and
  .historical_attempts == 2 and
  .reasons == ["incomplete_or_failed_attempts"]
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result "" "" "" "" "" "attempt_or_fragment_count_failed"
jq -e '
  .status == "failed" and
  .passed == false and
  .counts_observed == false and
  .fragments == null and
  .latest_attempts == null and
  .terminal == null and
  .reasons == ["attempt_or_fragment_count_failed"]
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result 992 992 991 1 2 "dataset_count_exceeds_target"
jq -e '
  .status == "failed" and
  .passed == false and
  .counts_observed == true and
  .fragments == 992 and
  .latest_attempts == 992 and
  .terminal == 991 and
  .historical_attempts == 2 and
  .reasons == ["dataset_count_exceeds_target"]
' "${IMPORT_GATE_RESULT}" >/dev/null

printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "${RUNNER}"
chmod +x "${RUNNER}"
runner_hash_before="$(sha256sum "${RUNNER}" | awk '{print $1}')"
REUSE_EXISTING_RUNNER=1
ensure_runner
if [[ "$(sha256sum "${RUNNER}" | awk '{print $1}')" != "${runner_hash_before}" ]]; then
  echo "reuse mode rebuilt the existing runner" >&2
  exit 1
fi

export MONITOR_TEST_CAPTURE="${tmp_dir}/import-arguments"
cat > "${RUNNER}" <<'RUNNER'
#!/usr/bin/env bash
printf '%s\0' "${DENSE_MEM_EVAL_IMPORT_CONCURRENCY:-}" "$@" > "${MONITOR_TEST_CAPTURE}"
RUNNER
for selection in default environment 1 4 30; do
  IMPORT_CONCURRENCY=""
  unset DENSE_MEM_EVAL_IMPORT_CONCURRENCY
  if [[ "${selection}" != default ]]; then
    export DENSE_MEM_EVAL_IMPORT_CONCURRENCY=30
  fi
  if [[ "${selection}" =~ ^[0-9]+$ ]]; then IMPORT_CONCURRENCY="${selection}"; fi
  start_import
  wait "$(cat "${IMPORT_DIR}/import.pid")"
  mapfile -d '' -t captured < "${MONITOR_TEST_CAPTURE}"
  expected_environment=""
  if [[ "${selection}" != default ]]; then expected_environment=30; fi
  [[ "${captured[0]}" == "${expected_environment}" ]] || { echo "runner environment was lost" >&2; exit 1; }
  concurrency_flag_count=0
  for index in "${!captured[@]}"; do
    if [[ "${captured[index]}" == --import-concurrency ]]; then
      concurrency_flag_count=$((concurrency_flag_count + 1))
      [[ "${captured[index+1]}" == "${selection}" ]] || { echo "explicit concurrency was changed" >&2; exit 1; }
    fi
  done
  expected_flags=0
  if [[ "${selection}" =~ ^[0-9]+$ ]]; then expected_flags=1; fi
  [[ "${concurrency_flag_count}" == "${expected_flags}" ]] || { echo "unexpected monitor concurrency override" >&2; exit 1; }
done
unset DENSE_MEM_EVAL_IMPORT_CONCURRENCY
IMPORT_CONCURRENCY=""

write_status "test" 991 991 990 0 0 "import-123" "17.5" "42"
jq -e '
  .terminal == 990 and
  .completed == 990 and
  .failed == 0 and
  .percent_complete < 100 and
  .import_pid == "import-123" and
  .rate_per_minute == 17.5 and
  .eta_seconds == 42 and
  (.import_gate_result | endswith("import_gate_result.json")) and
  (.failed_source_doc_ids | endswith("failed_source_doc_ids.txt"))
' "${STATUS_JSON}" >/dev/null

attempt_counts() { printf '0|0|0|0\n'; }
count_fragments() { printf '0\n'; }
SEED_HASH="identity-seed-hash"
SERVER_IMAGE_ID="identity-image"
EVAL_TEAM_ID="00000000-0000-0000-0000-000000000000"
AI_VERIFIER_MODEL=$'\u00a0verifier-fallback\u00a0'
AI_REMEMBER_MODEL=""
prepare_identity
jq -e '.assessor_model == "verifier-fallback"' "${IDENTITY_JSON}" >/dev/null

AI_REMEMBER_MODEL=$'\u00a0\t\u00a0'
prepare_identity
unset AI_REMEMBER_MODEL
prepare_identity

AI_REMEMBER_MODEL=$'\u00a0remember-override\u00a0'
if prepare_identity 2>/dev/null; then
  echo "changing the Remember model did not invalidate the evaluation identity" >&2
  exit 1
fi
jq -e '.assessor_model == "remember-override"' "${MONITOR_DIR}/requested_dataset_identity.json" >/dev/null

runner_hash_before="$(sha256sum "${RUNNER}" | awk '{print $1}')"
REUSE_EXISTING_RUNNER=0
go() { return 41; }
if ensure_runner; then
  echo "failed build reused an older runner" >&2
  exit 1
fi
[[ "$(sha256sum "${RUNNER}" | awk '{print $1}')" == "${runner_hash_before}" ]]
unset -f go

for cleanup_case in success build_failure returned_failure error deadline INT TERM supplied retained_success retained_error; do
  case_dir="${tmp_dir}/${cleanup_case} "'quote '\'' $() `literal` [*]'
  mkdir -p "${case_dir}/temporary" "${case_dir}/evidence" "${case_dir}/supplied"
  printf 'retained evaluation evidence\n' > "${case_dir}/evidence/stage.json"
  printf 'caller-owned runner\n' > "${case_dir}/supplied/eval-runner"
  printf 'caller-owned partial build\n' > "${case_dir}/supplied/eval-runner.tmp"
  before_hashes="$(sha256sum "${case_dir}/evidence/stage.json" "${case_dir}/supplied/"*)"
  case_status=0
  TMPDIR="${case_dir}/temporary" bash -s -- "${ROOT_DIR}" "${cleanup_case}" "${case_dir}" > "${case_dir}/output.log" 2>&1 <<'BASH' || case_status="$?"
set -Eeuo pipefail
cd "$1"
source tests/eval/scripts/paired_eval.sh
cleanup_case="$2"; case_dir="$3"; PAIR_RUNNER=""
PAIR_CONFIG="${case_dir}/pair.json"
printf '{}' > "${PAIR_CONFIG}"
if [[ "${cleanup_case}" == build_failure || "${cleanup_case}" == returned_failure ]]; then
  go() {
    printf '%s\n' "${PAIR_RUNNER}" > "${case_dir}/owned-runner"
    printf 'partial compiler output\n' > "$3"
    return 42
  }
  if [[ "${cleanup_case}" == build_failure ]]; then pair_main; exit 99; fi
  if pair_main; then exit 99; else original_status="$?"; fi
  PAIR_RUNNER="${case_dir}/supplied/eval-runner"
  exit "${original_status}"
fi
if [[ "${cleanup_case}" == supplied ]]; then PAIR_RUNNER="${case_dir}/supplied/eval-runner"; fi
pair_ensure_runner
if [[ "${cleanup_case}" == supplied ]]; then exit 0; fi
printf '%s\n' "${PAIR_RUNNER}" > "${case_dir}/owned-runner"
[[ -x "${PAIR_RUNNER}" ]]
if [[ "${cleanup_case}" == retained_* ]]; then
  mkdir "${PAIR_RUNNER}.tmp"
  printf 'unexpected retained contents\n' > "$(dirname "${PAIR_RUNNER}")/evidence.json"
else
  printf 'partial compiler output\n' > "${PAIR_RUNNER}.tmp"
fi
pair_stage_dir=""; pair_interrupted=false
trap 'pair_on_error "$?"' ERR
case "${cleanup_case}" in
  error|retained_error) bash -c 'exit 43' ;;
  deadline) bash -c 'exit 124' ;;
  INT|TERM) trap 'exit 130' INT TERM; kill -s "${cleanup_case}" "$$" ;;
esac
PAIR_RUNNER="${case_dir}/supplied/eval-runner"
BASH
  expected_status=0
  case "${cleanup_case}" in
    build_failure|returned_failure) expected_status=42 ;;
    error|retained_error) expected_status=43 ;;
    deadline) expected_status=124 ;;
    INT|TERM) expected_status=130 ;;
  esac
  [[ "${case_status}" == "${expected_status}" ]] || {
    cat "${case_dir}/output.log" >&2
    echo "${cleanup_case}: exit ${case_status}, expected ${expected_status}" >&2
    exit 1
  }
  [[ "$(sha256sum "${case_dir}/evidence/stage.json" "${case_dir}/supplied/"*)" == "${before_hashes}" ]]
  if [[ "${cleanup_case}" == supplied ]]; then
    [[ -z "$(find "${case_dir}/temporary" -mindepth 1 -print -quit)" ]]
    continue
  fi
  owned_runner="$(cat "${case_dir}/owned-runner")"
  [[ ! -e "${owned_runner}" ]]
  if [[ "${cleanup_case}" == retained_* ]]; then
    [[ -d "${owned_runner}.tmp" ]]
    [[ "$(cat "$(dirname "${owned_runner}")/evidence.json")" == 'unexpected retained contents' ]]
    [[ "$(cat "${case_dir}/output.log")" == *'failed to clean paired runner files'* ]]
    [[ "$(cat "${case_dir}/output.log")" == *'failed to remove paired runner directory'* ]]
  else
    [[ ! -e "$(dirname "${owned_runner}")" ]]
  fi
done
