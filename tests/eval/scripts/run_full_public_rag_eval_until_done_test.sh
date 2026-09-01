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

if [[ "$(terminal_attempt_outcomes_sql)" != "'completed', 'quarantined'" ]]; then
  echo "terminal attempt outcome list excludes quarantined" >&2
  exit 1
fi
if [[ "$(terminal_attempt_count 413 578)" != "991" ]]; then
  echo "terminal attempt count excludes quarantined" >&2
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

write_import_gate_result 991 991 413 578 0 0
jq -e '
  .schema_version == 1 and
  .status == "comparison_only" and
  .passed == false and
  .terminal == 991 and
  .quarantined == 578 and
  .reasons == ["quarantined_attempts"]
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result 991 991 991 0 0 0
jq -e '
  .status == "passed" and
  .passed == true and
  .terminal == 991 and
  .quarantined == 0 and
  .reasons == []
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result "" "" "" "" "" "" "attempt_or_fragment_count_failed"
jq -e '
  .status == "failed" and
  .passed == false and
  .counts_observed == false and
  .fragments == null and
  .latest_attempts == null and
  .terminal == null and
  .reasons == ["attempt_or_fragment_count_failed"]
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result 992 992 413 578 0 2 "dataset_count_exceeds_target"
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

write_status "test" 991 991 990 1 0 0 "" "" ""
jq -e '
  .quarantined == 1 and
  .terminal == 991 and
  .percent_complete == 100 and
  (.import_gate_result | endswith("import_gate_result.json")) and
  (.quarantined_source_doc_ids | endswith("quarantined_source_doc_ids.txt"))
' "${STATUS_JSON}" >/dev/null
