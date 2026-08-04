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

if [[ "$(terminal_placement_statuses_sql)" != "'completed', 'awaiting_review', 'quarantined'" ]]; then
  echo "terminal placement status list excludes quarantined" >&2
  exit 1
fi
if [[ "$(terminal_placement_count 413 577 1)" != "991" ]]; then
  echo "terminal placement count excludes quarantined" >&2
  exit 1
fi

write_import_gate_result 991 991 413 577 1 0 0 0 1
jq -e '
  .schema_version == 1 and
  .status == "comparison_only" and
  .passed == false and
  .terminal == 991 and
  .quarantined == 1 and
  .reasons == ["quarantined_placements"]
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result 991 991 413 578 0 0 0 0 1
jq -e '
  .status == "passed" and
  .passed == true and
  .terminal == 991 and
  .quarantined == 0 and
  .reasons == []
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result "" "" "" "" "" "" "" "" "" "placement_or_fragment_count_failed"
jq -e '
  .status == "failed" and
  .passed == false and
  .counts_observed == false and
  .fragments == null and
  .latest_placements == null and
  .terminal == null and
  .reasons == ["placement_or_fragment_count_failed"]
' "${IMPORT_GATE_RESULT}" >/dev/null

write_import_gate_result 992 992 413 578 0 0 0 0 2 "dataset_count_exceeds_target"
jq -e '
  .status == "failed" and
  .passed == false and
  .counts_observed == true and
  .fragments == 992 and
  .latest_placements == 992 and
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

write_status "test" 991 991 413 577 1 0 0 0 1 "" "" ""
jq -e '
  .quarantined == 1 and
  .terminal == 991 and
  .percent_complete == 100 and
  (.import_gate_result | endswith("import_gate_result.json")) and
  (.quarantined_source_doc_ids | endswith("quarantined_source_doc_ids.txt"))
' "${STATUS_JSON}" >/dev/null
