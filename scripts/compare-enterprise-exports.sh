#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE_IMAGE="${1:?base production image is required}"
CANDIDATE_IMAGE="${2:?candidate production image is required}"
ARTIFACT_DIR="${3:?ignored artifact directory is required}"
BASE_SHA="${4:?exact base SHA is required}"
CONTROLLER="${ROOT_DIR}/scripts/e2e-host-controller.sh"
ENV_FILE="${DENSE_MEM_E2E_ENV_FILE:-${HOME}/dense-mem-ci/.env}"
[[ "$BASE_SHA" =~ ^[0-9a-f]{40}$ && -f "$ENV_FILE" ]]
mkdir -p "$ARTIFACT_DIR"
chmod 700 "$ARTIFACT_DIR"
ARTIFACT_DIR="$(realpath "$ARTIFACT_DIR")"
export DENSE_MEM_CI_LOCAL=1 DENSE_MEM_E2E_EXPORT_PERFORMANCE=1
export DENSE_MEM_CI_ENV_FILE="$ENV_FILE"
export DENSE_MEM_CI_PROMETHEUS_FILE="${ROOT_DIR}/examples/prometheus.yml"
export DENSE_MEM_CI_JOB_DIR="${ARTIFACT_DIR}/job"
export DENSE_MEM_CI_REPOSITORY=local/dense-mem
export DENSE_MEM_E2E_SOURCE_ROOT="$ROOT_DIR"
export DENSE_MEM_CI_RUN_PLAYWRIGHT=0
mkdir -p "$DENSE_MEM_CI_JOB_DIR"
export DENSE_MEM_CI_TELEMETRY_TOKEN_FILE="${ARTIFACT_DIR}/telemetry-scrape-token"
node --input-type=module - "${ROOT_DIR}/scripts/e2e-redact-diagnostics.mjs" "$ENV_FILE" "$DENSE_MEM_CI_TELEMETRY_TOKEN_FILE" <<'NODE'
import fs from "node:fs";
import {pathToFileURL} from "node:url";
const [modulePath,envPath,destination]=process.argv.slice(2);
const {valueFromEnvFile}=await import(pathToFileURL(modulePath).href);
const token=valueFromEnvFile(envPath,"TELEMETRY_SCRAPE_TOKEN");
if(!token)throw new Error("telemetry scrape token is required");
fs.writeFileSync(destination,token,{mode:0o600});
NODE
base_id="$(docker image inspect "$BASE_IMAGE" --format '{{.Id}}')"
candidate_id="$(docker image inspect "$CANDIDATE_IMAGE" --format '{{.Id}}')"
[[ "$base_id" != "$candidate_id" ]]
python3 - "$ROOT_DIR" "$BASE_SHA" "$ARTIFACT_DIR" <<'PY_SOURCE'
import sys,json,hashlib,subprocess
from pathlib import Path
root=Path(sys.argv[1]); paths=subprocess.check_output(['git','-C',str(root),'ls-files','--cached','--others','--exclude-standard','-z']).decode().split('\0')
files={}
for name in sorted(set(paths)):
 p=root/name
 if name and p.is_file() and (name in ['go.mod','go.sum','Dockerfile','docker-entrypoint.sh'] or name.endswith('.sql') or name.endswith('.go') and not name.endswith('_test.go')):
  files[name]=hashlib.sha256(p.read_bytes()).hexdigest()
receipt={'base_sha':sys.argv[2],'candidate_working_tree_sha256':hashlib.sha256(json.dumps(files,sort_keys=True).encode()).hexdigest(),'runtime_files':files}
Path(sys.argv[3],'source-identity.json').write_text(json.dumps(receipt,indent=2)+'\n')
PY_SOURCE
PROJECT=""
cleanup_current() {
  local status=$?
  trap - EXIT INT TERM
  if [[ -n "$PROJECT" ]]; then "$CONTROLLER" stop "$PROJECT" > "${ARTIFACT_DIR}/last-cleanup.txt" || status=1; fi
  exit "$status"
}
trap cleanup_current EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
"$CONTROLLER" doctor > "${ARTIFACT_DIR}/doctor.txt"
ordinal=0
for state in disabled healthy slow unavailable; do
  export DENSE_MEM_E2E_EXPORT_PERF_STATE="$state"
  for repetition in 1 2 3 4 5; do
    export DENSE_MEM_E2E_EXPORT_PERF_REPETITION="$repetition"
    for variant in base candidate; do
      export DENSE_MEM_E2E_EXPORT_PERF_VARIANT="$variant"
      ordinal=$((ordinal+1))
      run_id="$(date -u +%s)$$${ordinal}"
      image="$BASE_IMAGE"
      [[ "$variant" == base ]] || image="$CANDIDATE_IMAGE"
      stem="${state}-${repetition}-${variant}"
      PROJECT="$("$CONTROLLER" start "$run_id" 1 exclusive enterprise_exports "$image" "$ROOT_DIR")"
      "$CONTROLLER" run "$run_id" 1 exclusive enterprise_exports enterprise_exports "$image" "$ROOT_DIR" > "${ARTIFACT_DIR}/${stem}.txt"
      cp "${DENSE_MEM_CI_JOB_DIR}/${run_id}-1/exclusive-enterprise_exports/performance-result.json" "${ARTIFACT_DIR}/${stem}-result.json"
      "$CONTROLLER" stop "$PROJECT" > "${ARTIFACT_DIR}/${stem}-cleanup.txt"
      PROJECT=""
      printf 'Measured %s repetition %s %s\n' "$state" "$repetition" "$variant"
    done
  done
done
node "${ROOT_DIR}/tests/eval/scripts/enterprise_exports_performance.mjs" "$ARTIFACT_DIR" "${ARTIFACT_DIR}/comparison.json"
node - "${ARTIFACT_DIR}/comparison.json" "$BASE_SHA" "$BASE_IMAGE" "$CANDIDATE_IMAGE" "$base_id" "$candidate_id" "$ARTIFACT_DIR" <<'NODE'
const fs=require("node:fs");
const [file,base,baseImage,candidateImage,baseID,candidateID,artifactDir]=process.argv.slice(2);
for(const name of fs.readdirSync(artifactDir).filter(name=>name.endsWith("-result.json"))){const run=JSON.parse(fs.readFileSync(`${artifactDir}/${name}`,"utf8"));if(run.server_image !== (run.variant==="base" ? baseID : candidateID)) throw new Error("performance source image changed");}
const receipt=JSON.parse(fs.readFileSync(file,"utf8"));
Object.assign(receipt,{source:JSON.parse(fs.readFileSync(`${artifactDir}/source-identity.json`,"utf8")),base_sha:base,base_image:baseImage,candidate_image:candidateImage,base_image_id:baseID,candidate_image_id:candidateID});
fs.writeFileSync(file,`${JSON.stringify(receipt,null,2)}\n`);
NODE
