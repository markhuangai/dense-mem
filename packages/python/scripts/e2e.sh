#!/usr/bin/env bash
set -euo pipefail
[[ -f /.dockerenv && "${DENSE_MEM_E2E_SOURCE_ROOT:-/workspace}" == /workspace && "${DENSE_MEM_E2E_RUNTIME:-}" == production ]] || {
  printf 'Python E2E tooling must run in the disposable scenario container\n' >&2
  exit 1
}
node --input-type=module <<'NODE'
import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { constants } from "node:os";

const context = {
  event: "python_dependency_setup",
  run_id: process.env.DENSE_MEM_E2E_RUN_ID ?? null,
  run_attempt: process.env.DENSE_MEM_E2E_RUN_ATTEMPT ?? null,
  scenario: process.env.DENSE_MEM_E2E_SCENARIO ?? null,
  source_revision: process.env.DENSE_MEM_E2E_COMMIT_SHA ?? null,
};
try {
  context.nameservers = [...readFileSync("/etc/resolv.conf", "utf8").matchAll(/^\s*nameserver\s+(\S+)/gm)].map((match) => match[1]);
  context.resolver_capture_state = "captured";
} catch (error) {
  context.resolver_capture_state = "unavailable";
  context.resolver_error_code = error.code;
}

function setupPhase(phase, command, args) {
  const started = performance.now();
  const log = (details) => console.log(JSON.stringify({ ...context, time: new Date().toISOString(), phase, elapsed_ms: Math.round(performance.now() - started), ...details }));
  log({ level: "INFO", outcome: "started" });
  const result = spawnSync(command, args, { stdio: "inherit" });
  const status = result.status ?? (result.signal ? 128 + constants.signals[result.signal] : 1);
  log({ level: status === 0 ? "INFO" : "ERROR", outcome: status === 0 ? "passed" : "failed", exit_code: status, signal: result.signal, error_code: result.error?.code });
  if (status !== 0) process.exit(status);
}

setupPhase("apt_index_refresh", "apt-get", ["update", "-qq", "-o", "APT::Update::Error-Mode=any"]);
setupPhase("apt_package_install", "apt-get", ["install", "-y", "--no-install-recommends", "git", "python3", "python3-venv"]);
NODE
OUTPUT="$(mktemp -d /tmp/densemem-python-e2e.XXXXXX)"
trap 'rm -r -- "$OUTPUT"' EXIT
bash /workspace/packages/python/scripts/check.sh --output "$OUTPUT/artifacts"
python3 -m venv "$OUTPUT/client"
"$OUTPUT/client/bin/python" -m pip install 'pip==26.2.1'
WHEEL="$(find "$OUTPUT/artifacts/dist" -name '*.whl' -print -quit)"
case "${1:-python_client}" in
  google_adk_memory) "$OUTPUT/client/bin/python" -m pip install "${WHEEL}[adk]" ;;
  langchain_retriever) "$OUTPUT/client/bin/python" -m pip install "${WHEEL}[langchain]" ;;
  python_client) "$OUTPUT/client/bin/python" -m pip install "$WHEEL" ;;
  *) printf 'Unknown Python E2E scenario\n' >&2; exit 2 ;;
esac
cp -a /workspace/packages/python/tests "$OUTPUT/tests"
cp -a /workspace/packages/python/examples "$OUTPUT/examples"
export DENSEMEM_E2E_EXAMPLES="$OUTPUT/examples"
cd "$OUTPUT"
if [[ "${1:-python_client}" == google_adk_memory ]]; then
  python3 /workspace/tests/eval/scripts/generate_session_ingest_v1.py --output "$OUTPUT/cohort.json"
  "$OUTPUT/client/bin/python" /workspace/tests/eval/scripts/check_adk_mapping.py "$OUTPUT/cohort.json" "$OUTPUT/mapping.json"
  export DENSEMEM_ADK_MAPPING_REPORT="$OUTPUT/mapping.json"
  "$OUTPUT/client/bin/python" "$OUTPUT/tests/e2e_adk.py"
elif [[ "${1:-python_client}" == langchain_retriever ]]; then
  "$OUTPUT/client/bin/python" "$OUTPUT/tests/e2e_langchain.py"
else
  "$OUTPUT/client/bin/python" "$OUTPUT/tests/e2e.py"
fi
