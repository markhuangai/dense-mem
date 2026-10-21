#!/usr/bin/env bash
set -euo pipefail
[[ -f /.dockerenv && "${DENSE_MEM_E2E_SOURCE_ROOT:-/workspace}" == /workspace && "${DENSE_MEM_E2E_RUNTIME:-}" == production ]] || {
  printf 'Python E2E tooling must run in the disposable scenario container\n' >&2
  exit 1
}
apt-get update -qq
apt-get install -y --no-install-recommends git python3 python3-venv
OUTPUT="$(mktemp -d /tmp/densemem-python-e2e.XXXXXX)"
trap 'rm -r -- "$OUTPUT"' EXIT
bash /workspace/packages/python/scripts/check.sh --output "$OUTPUT/artifacts"
python3 -m venv "$OUTPUT/client"
"$OUTPUT/client/bin/python" -m pip install 'pip==26.2.1'
WHEEL="$(find "$OUTPUT/artifacts/dist" -name '*.whl' -print -quit)"
if [[ "${1:-python_client}" == google_adk_memory ]]; then
  "$OUTPUT/client/bin/python" -m pip install "${WHEEL}[adk]"
else
  "$OUTPUT/client/bin/python" -m pip install "$WHEEL"
fi
cp -a /workspace/packages/python/tests "$OUTPUT/tests"
cp -a /workspace/packages/python/examples "$OUTPUT/examples"
export DENSEMEM_E2E_EXAMPLES="$OUTPUT/examples"
cd "$OUTPUT"
if [[ "${1:-python_client}" == google_adk_memory ]]; then
  python3 /workspace/tests/eval/scripts/generate_session_ingest_v1.py --output "$OUTPUT/cohort.json"
  "$OUTPUT/client/bin/python" /workspace/tests/eval/scripts/check_adk_mapping.py "$OUTPUT/cohort.json" "$OUTPUT/mapping.json"
  export DENSEMEM_ADK_MAPPING_REPORT="$OUTPUT/mapping.json"
  "$OUTPUT/client/bin/python" "$OUTPUT/tests/e2e_adk.py"
else
  "$OUTPUT/client/bin/python" "$OUTPUT/tests/e2e.py"
fi
