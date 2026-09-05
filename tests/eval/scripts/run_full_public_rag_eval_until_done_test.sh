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
# shellcheck source=run_full_public_rag_eval_until_done.sh
source "${ROOT_DIR}/tests/eval/scripts/run_full_public_rag_eval_until_done.sh"

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
