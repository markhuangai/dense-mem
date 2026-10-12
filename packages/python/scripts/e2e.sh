#!/usr/bin/env bash
set -euo pipefail
[[ -f /.dockerenv && "${DENSE_MEM_E2E_SOURCE_ROOT:-/workspace}" == /workspace && "${DENSE_MEM_E2E_RUNTIME:-}" == production ]] || {
  printf 'Python E2E tooling must run in the disposable scenario container\n' >&2
  exit 1
}
apt-get update -qq
apt-get install -y --no-install-recommends python3 python3-venv
OUTPUT="$(mktemp -d /tmp/densemem-python-e2e.XXXXXX)"
trap 'rm -r -- "$OUTPUT"' EXIT
bash /workspace/packages/python/scripts/check.sh --output "$OUTPUT/artifacts"
python3 -m venv "$OUTPUT/client"
"$OUTPUT/client/bin/python" -m pip install 'pip==26.2.1'
"$OUTPUT/client/bin/python" -m pip install "$OUTPUT"/artifacts/dist/*.whl
cp -a /workspace/packages/python/tests "$OUTPUT/tests"
cp -a /workspace/packages/python/examples "$OUTPUT/examples"
export DENSEMEM_E2E_EXAMPLES="$OUTPUT/examples"
cd "$OUTPUT"
"$OUTPUT/client/bin/python" "$OUTPUT/tests/e2e.py"
