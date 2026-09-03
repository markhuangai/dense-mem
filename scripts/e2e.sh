#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTROLLER="${DENSE_MEM_E2E_CONTROLLER:-${ROOT_DIR}/scripts/e2e-host-controller.sh}"
SCENARIO="${1:-}"
CONFIG_DIR="${DENSE_MEM_CI_CONFIG_DIR:-${HOME}/dense-mem-ci}"
ENV_FILE="${DENSE_MEM_E2E_ENV_FILE:-${CONFIG_DIR}/.env}"
TOKEN_FILE="${DENSE_MEM_E2E_TELEMETRY_TOKEN_FILE:-${CONFIG_DIR}/telemetry-scrape-token}"
PROMETHEUS_FILE="${DENSE_MEM_E2E_PROMETHEUS_FILE:-${ROOT_DIR}/examples/prometheus.yml}"
JOB_DIR=""
TEMP_DIR=""
GENERATED_TOKEN_FILE=""
IMAGE_TAG=""
PROJECT=""

fail() {
  printf 'dense-mem local e2e: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf '%s\n' 'usage: scripts/e2e.sh SCENARIO' >&2
  exit 2
}

[[ "$SCENARIO" =~ ^[a-z0-9_]+$ ]] || usage
[[ -x "$CONTROLLER" ]] || fail "controller is unavailable: $CONTROLLER"
command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
command -v node >/dev/null 2>&1 || fail "node is unavailable"
command -v git >/dev/null 2>&1 || fail "git is unavailable"

if [[ -z "${DENSE_MEM_E2E_ENV_FILE:-}" && ! -f "$ENV_FILE" && -f "${ROOT_DIR}/.env" ]]; then
  ENV_FILE="${ROOT_DIR}/.env"
fi
[[ -f "$ENV_FILE" ]] || fail "missing environment file: $ENV_FILE"
[[ -f "$PROMETHEUS_FILE" ]] || fail "missing Prometheus configuration: $PROMETHEUS_FILE"

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/dense-mem-e2e-local.XXXXXX")"
JOB_DIR="${TEMP_DIR}/job"
mkdir -p "$JOB_DIR"

env_value_from_file() {
  local field="$1"
  node --input-type=module - "${ROOT_DIR}/scripts/e2e-redact-diagnostics.mjs" "$ENV_FILE" "$field" <<'NODE'
import { pathToFileURL } from "node:url";
const [modulePath, envPath, field] = process.argv.slice(2);
const { valueFromEnvFile } = await import(pathToFileURL(modulePath).href);
const value = valueFromEnvFile(envPath, field);
if (typeof value !== "string" || value.length === 0) process.exit(1);
process.stdout.write(value);
NODE
}

if [[ ! -f "$TOKEN_FILE" ]]; then
  token_value="$(env_value_from_file TELEMETRY_SCRAPE_TOKEN 2>/dev/null || true)"
  [[ -n "$token_value" ]] || fail "missing telemetry scrape token: $TOKEN_FILE"
  GENERATED_TOKEN_FILE="${TEMP_DIR}/telemetry-scrape-token"
  printf '%s\n' "$token_value" > "$GENERATED_TOKEN_FILE"
  chmod 600 "$GENERATED_TOKEN_FILE"
  TOKEN_FILE="$GENERATED_TOKEN_FILE"
fi

classification="$(
  DENSE_MEM_E2E_SCENARIO_REGISTRY="${ROOT_DIR}/scripts/e2e-scenarios.json" \
    node "${ROOT_DIR}/scripts/e2e-scenario-registry.mjs" --scenario "$SCENARIO"
)" || fail "scenario is not registered: $SCENARIO"
scenario_configuration="$(
  node - "$classification" <<'NODE'
const scenario = JSON.parse(process.argv[2]);
if (scenario.audited !== true || scenario.runtime !== "production") process.exit(1);
const phase = scenario.isolation === "shared_team" ? "shared" : "exclusive";
process.stdout.write(`${phase} ${scenario.playwright ? "1" : "0"}`);
NODE
)" || fail "scenario is not audited for local production execution: $SCENARIO"
read -r phase playwright <<< "$scenario_configuration"
[[ "$playwright" == "0" || "$playwright" == "1" ]] || fail "scenario has invalid Playwright configuration: $SCENARIO"
stack_scenario="$([[ "$phase" == "shared" ]] && printf '%s' shared || printf '%s' "$SCENARIO")"

run_id="$(date -u +%s)$$"
attempt=1
IMAGE_TAG="ghcr.io/markhuangai/dense-mem:e2e-local-${run_id}"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  if [[ -n "$PROJECT" ]]; then
    "$CONTROLLER" stop "$PROJECT" >/dev/null 2>&1 || status=1
  fi
  if [[ -n "$IMAGE_TAG" ]]; then
    docker image rm "$IMAGE_TAG" >/dev/null 2>&1 || true
  fi
  if [[ -n "$TEMP_DIR" && -d "$TEMP_DIR" ]]; then
    rm -r -- "$TEMP_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

export DENSE_MEM_CI_LOCAL=1
export DENSE_MEM_CI_ENV_FILE="$ENV_FILE"
export DENSE_MEM_CI_TELEMETRY_TOKEN_FILE="$TOKEN_FILE"
export DENSE_MEM_CI_PROMETHEUS_FILE="$PROMETHEUS_FILE"
export DENSE_MEM_CI_JOB_DIR="$JOB_DIR"
export DENSE_MEM_CI_REPOSITORY="${DENSE_MEM_CI_REPOSITORY:-local/dense-mem}"
export DENSE_MEM_E2E_SOURCE_ROOT="$ROOT_DIR"
export DENSE_MEM_CI_RUN_PLAYWRIGHT="$playwright"

REVISION="$(git -C "$ROOT_DIR" rev-parse HEAD)" || fail "unable to resolve the git revision"
[[ "$REVISION" =~ ^[0-9a-f]{40}$ ]] || fail "git revision is invalid"

printf 'Building local production image %s\n' "$IMAGE_TAG"
docker build --target production \
  --tag "$IMAGE_TAG" \
  --build-arg "IMAGE_VERSION=e2e-local-${run_id}" \
  --build-arg "IMAGE_REVISION=${REVISION}" \
  "$ROOT_DIR" >/dev/null
docker image inspect "$IMAGE_TAG" >/dev/null || fail "local image was not registered"
IMAGE_REF="$IMAGE_TAG"

"$CONTROLLER" doctor >/dev/null
PROJECT="$("$CONTROLLER" start "$run_id" "$attempt" "$phase" "$stack_scenario" "$IMAGE_REF" "$ROOT_DIR")"
"$CONTROLLER" run "$run_id" "$attempt" "$phase" "$stack_scenario" "$SCENARIO" "$IMAGE_REF" "$ROOT_DIR"
printf '%s\n' "local ${SCENARIO} e2e passed"
