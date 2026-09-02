#!/usr/bin/env bash
set -euo pipefail
CONTROLLER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CONTRACT_VERSION="dense-mem-ci-e2e.v1"
CONFIG_DIR="${DENSE_MEM_CI_CONFIG_DIR:-${HOME}/dense-mem-ci}"
COMPOSE_FILE="${DENSE_MEM_CI_COMPOSE_FILE:-${CONTROLLER_DIR}/e2e-ci-compose.yml}"
PROMETHEUS_FILE="${DENSE_MEM_CI_PROMETHEUS_FILE:-${CONTROLLER_DIR}/../examples/prometheus.yml}"
ENV_FILE="${DENSE_MEM_CI_ENV_FILE:-${CONFIG_DIR}/.env}"
TELEMETRY_TOKEN_FILE="${DENSE_MEM_CI_TELEMETRY_TOKEN_FILE:-${CONFIG_DIR}/telemetry-scrape-token}"
JOB_DIR="${DENSE_MEM_CI_JOB_DIR:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/dense-mem-e2e}"
REPOSITORY="${DENSE_MEM_CI_REPOSITORY:-${GITHUB_REPOSITORY:-markhuangai/dense-mem}}"
REGISTRY_SCRIPT="${DENSE_MEM_CI_REGISTRY_SCRIPT:-${CONTROLLER_DIR}/e2e-scenario-registry.mjs}"
export DENSE_MEM_CI_ENV_FILE="$ENV_FILE"
DENSE_MEM_CI_COMPOSE_OVERLAY_FILE=""
DENSE_MEM_CI_HELPER_DIR=""
DENSE_MEM_CI_PRIVATE_DIR=""

fail() {
  printf 'dense-mem CI controller: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf '%s\n' \
    'usage:' \
    '  e2e-host-controller.sh doctor' \
    '  e2e-host-controller.sh start RUN_ID ATTEMPT PHASE SCENARIO IMAGE_REF SOURCE_DIR' \
    '  e2e-host-controller.sh run RUN_ID ATTEMPT PHASE STACK_SCENARIO SCENARIO IMAGE_REF SOURCE_DIR' \
    '  e2e-host-controller.sh stop PROJECT' \
    '  e2e-host-controller.sh stale-cleanup [MAX_AGE_SECONDS] [RUN_ID ATTEMPT PHASE]' \
    '  e2e-host-controller.sh precheck RUN_ID ATTEMPT IMAGE_REF SOURCE_DIR' >&2
  exit 2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

require_command docker
require_command node
require_command git

validate_decimal() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]] || fail "expected a positive decimal value: $1"
}

validate_digest() {
  [[ "$1" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid image digest"
}

validate_revision() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]] || fail "invalid source revision"
}

validate_project() {
  [[ "$1" =~ ^densemem-ci-[a-z0-9][a-z0-9-]{0,50}$ ]] || fail "refusing an unmanaged Compose project: $1"
}

managed_project_name() {
  local run_id="$1" attempt="$2" phase="$3" scenario="$4"
  local project="densemem-ci-${run_id}-${attempt}-${phase}-${scenario//_/-}"
  if (( ${#project} > 63 )); then
    local suffix
    suffix="$(node -e 'process.stdout.write(require("node:crypto").createHash("sha256").update(process.argv[1]).digest("hex").slice(0,8))' "$project")"
    project="${project:0:$((63 - ${#suffix} - 1))}-${suffix}"
  fi
  validate_project "$project"
  printf '%s\n' "$project"
}

validate_phase() {
  [[ "$1" == "exclusive" || "$1" == "shared" ]] || fail "invalid phase: $1"
}

validate_cleanup_phase() {
  [[ "$1" == "precheck" || "$1" == "exclusive" || "$1" == "shared" ]] || fail "invalid cleanup phase: $1"
}

validate_scenario() {
  [[ "$1" =~ ^[a-z0-9_]+$ ]] || fail "invalid scenario: $1"
}

validate_registered_scenario() {
  local source_dir="$1" scenario="$2" phase="$3"
  [[ -f "$source_dir/scripts/e2e-scenario-registry.mjs" ]] || fail "scenario registry is unavailable in the tested source"
  [[ -f "$source_dir/scripts/e2e-scenarios.json" ]] || fail "scenario registry data is unavailable in the tested source"
  [[ -r "$REGISTRY_SCRIPT" ]] || fail "trusted scenario registry is unavailable: $REGISTRY_SCRIPT"
  local classification
  classification="$(DENSE_MEM_E2E_SCENARIO_REGISTRY="$source_dir/scripts/e2e-scenarios.json" node "$REGISTRY_SCRIPT" --scenario "$scenario")" || fail "scenario is not in the tested registry: $scenario"
  node - "$classification" "$phase" <<'NODE' || fail "scenario is not audited for the requested production phase"
const classification = JSON.parse(process.argv[2]);
const phase = process.argv[3];
const expectedIsolation = phase === "shared" ? "shared_team" : "exclusive";
if (classification.audited !== true || classification.runtime !== "production" || classification.isolation !== expectedIsolation) process.exit(1);
NODE
}

validate_image_ref() {
  [[ "$1" =~ ^ghcr\.io/[a-z0-9_.-]+/[a-z0-9_.-]+(:[A-Za-z0-9][A-Za-z0-9_.-]{0,127})?(@sha256:[0-9a-f]{64})?$ ]] || fail "image must be a GHCR repository reference"
}

validate_bundle() {
  [[ -f "$COMPOSE_FILE" ]] || fail "missing Compose bundle: $COMPOSE_FILE"
  [[ -f "$ENV_FILE" ]] || fail "missing CI environment file: $ENV_FILE"
  [[ -f "$PROMETHEUS_FILE" ]] || fail "missing Prometheus configuration: $PROMETHEUS_FILE"
  [[ -f "$TELEMETRY_TOKEN_FILE" ]] || fail "missing telemetry scrape token: $TELEMETRY_TOKEN_FILE"
  local env_mode env_owner
  env_mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE")"
  [[ "$env_mode" == "600" ]] || fail "CI environment file must have mode 0600"
  env_owner="$(stat -c '%u' "$ENV_FILE" 2>/dev/null || stat -f '%u' "$ENV_FILE")"
  [[ "$env_owner" == "$(id -u)" ]] || fail "CI environment file is not owned by the runner user"
  local token_mode token_owner
  token_mode="$(stat -c '%a' "$TELEMETRY_TOKEN_FILE" 2>/dev/null || stat -f '%Lp' "$TELEMETRY_TOKEN_FILE")"
  [[ "$token_mode" == "600" ]] || fail "telemetry scrape token must have mode 0600"
  token_owner="$(stat -c '%u' "$TELEMETRY_TOKEN_FILE" 2>/dev/null || stat -f '%u' "$TELEMETRY_TOKEN_FILE")"
  [[ "$token_owner" == "$(id -u)" ]] || fail "telemetry scrape token is not owned by the runner user"
  local database_field database_value secret_field secret_value telemetry_secret verifier_url
  for database_field in POSTGRES_USER POSTGRES_DB; do
    database_value="$(env_value "$database_field" 2>/dev/null || true)"
    [[ -n "$database_value" ]] || fail "${database_field} must be configured"
  done
  for secret_field in POSTGRES_PASSWORD AI_API_KEY CONTROL_PORTAL_TOKEN; do
    secret_value="$(env_value "$secret_field" 2>/dev/null || true)"
    [[ ${#secret_value} -ge 2 ]] || fail "${secret_field} must contain at least two characters"
  done
  for secret_field in REDIS_PASSWORD AI_VERIFIER_API_KEY; do
    secret_value="$(env_value "$secret_field" 2>/dev/null || true)"
    [[ -z "$secret_value" || ${#secret_value} -ge 2 ]] || fail "${secret_field} must contain at least two characters"
  done
  verifier_url="$(env_value AI_VERIFIER_API_URL 2>/dev/null || true)"
  if [[ -n "$verifier_url" ]]; then
    secret_value="$(env_value AI_VERIFIER_API_KEY 2>/dev/null || true)"
    [[ ${#secret_value} -ge 2 ]] || fail "AI_VERIFIER_API_KEY must contain at least two characters"
  fi
  telemetry_secret="$(cat "$TELEMETRY_TOKEN_FILE")"
  [[ ${#telemetry_secret} -ge 2 ]] || fail "telemetry scrape token must contain at least two characters"
}

env_value() {
  local field="$1"
  node --input-type=module - "$CONTROLLER_DIR/e2e-redact-diagnostics.mjs" "$ENV_FILE" "$field" <<'NODE'
import { pathToFileURL } from "node:url";

const [modulePath, envPath, field] = process.argv.slice(2);
const { valueFromEnvFile } = await import(pathToFileURL(modulePath).href);
const value = valueFromEnvFile(envPath, field);
if (typeof value !== "string" || value.length === 0) process.exit(1);
process.stdout.write(value);
NODE
}

redact_diagnostics() {
  local env_file="$1"
  shift
  DENSE_MEM_CI_REDACT_ENV_FILE="$env_file" \
    DENSE_MEM_CI_REDACT_ALLOW_SHORT=1 \
    DENSE_MEM_CI_REDACT_EXTRA_VALUES="$(printf '%s\n' "$@")" \
    node "${CONTROLLER_DIR}/e2e-redact-diagnostics.mjs"
}

compose_base_env() {
  local project="$1"
  local phase="${2:-doctor}"
  local scenario="${3:-doctor}"
  local digest="${4:-sha256:0000000000000000000000000000000000000000000000000000000000000000}"
  local run_id="${5:-1}"
  local attempt="${6:-1}"
  local created_at="${7:-1970-01-01T00:00:00Z}"
  local image="${8:-ghcr.io/markhuangai/dense-mem:controller-doctor}"
  export COMPOSE_PROJECT_NAME="$project"
  export DENSE_MEM_IMAGE="$image"
  export DENSE_MEM_CI_CONTRACT="$CONTRACT_VERSION"
  export DENSE_MEM_CI_REPOSITORY="$REPOSITORY"
  export DENSE_MEM_CI_RUN_ID="$run_id"
  export DENSE_MEM_CI_RUN_ATTEMPT="$attempt"
  export DENSE_MEM_CI_PHASE="$phase"
  export DENSE_MEM_CI_SCENARIO="$scenario"
  export DENSE_MEM_CI_IMAGE_DIGEST="$digest"
  export DENSE_MEM_CI_CREATED_AT="$created_at"
  export DENSE_MEM_CI_PROMETHEUS_FILE="$PROMETHEUS_FILE"
  export DENSE_MEM_CI_TELEMETRY_TOKEN_FILE="$TELEMETRY_TOKEN_FILE"
  DENSE_MEM_CI_TELEMETRY_SCRAPE_TOKEN="$(cat "$TELEMETRY_TOKEN_FILE")"
  export DENSE_MEM_CI_TELEMETRY_SCRAPE_TOKEN
  export DENSE_MEM_CI_NETWORK_NAME="${project}_ci"
  export DENSE_MEM_CI_POSTGRES_VOLUME_NAME="${project}_postgres-data"
  export DENSE_MEM_CI_REDIS_VOLUME_NAME="${project}_redis-data"
  export DENSE_MEM_CI_PROMETHEUS_VOLUME_NAME="${project}_prometheus-data"
  export DENSE_MEM_CI_PROMETHEUS_CONFIG_VOLUME_NAME="${project}_prometheus-config"
  export DENSE_MEM_CI_TELEMETRY_TOKEN_VOLUME_NAME="${project}_telemetry-scrape-token"
  export DENSE_MEM_CI_CLIENT_VOLUME_NAME="${project}_client-env"
  export DENSE_MEM_CI_CONFLICT_VOLUME_NAME="${project}_conflict-provider-files"
  export DENSE_MEM_CI_OAUTH_VOLUME_NAME="${project}_oauth-provider-files"
  export DENSE_MEM_CI_SYNCHRONOUS_WRITE_VOLUME_NAME="${project}_synchronous-write-provider-files"
}

ci_compose() {
  local -a compose_args=(--env-file "$ENV_FILE" --project-name "$COMPOSE_PROJECT_NAME" --file "$COMPOSE_FILE")
  if [[ -n "$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE" ]]; then
    compose_args+=(--file "$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE")
  fi
  docker compose "${compose_args[@]}" "$@"
}

assert_no_host_ports() {
  ci_compose config --format json |
    node -e '
let input = "";
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  const config = JSON.parse(input);
  const ports = Object.entries(config.services || {}).flatMap(([name, service]) =>
    (service.ports || []).map((port) => ({ name, port })));
  const hostNetwork = Object.entries(config.services || {})
    .filter(([, service]) => service.network_mode === "host")
    .map(([name]) => name);
  if (ports.length > 0 || hostNetwork.length > 0) {
    if (hostNetwork.length > 0) ports.push({ network_mode: "host", services: hostNetwork });
    process.stderr.write(`host port bindings are forbidden: ${JSON.stringify(ports)}\n`);
    process.exit(1);
  }
});
'
}

has_helper() {
  local helpers=",$1,"
  [[ "$helpers" == *,"$2",* ]]
}

helper_env_value() {
  local path="$1"
  local field="$2"
  node - "$path" "$field" <<'NODE'
const fs = require("node:fs");
const [path, field] = process.argv.slice(2);
for (const line of fs.readFileSync(path, "utf8").split(/\r?\n/)) {
  const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
  if (!match || match[1] !== field) continue;
  process.stdout.write(match[2]);
  process.exit(0);
}
process.exit(1);
NODE
}


source "${CONTROLLER_DIR}/e2e-host-controller-stack.sh"

precheck() {
  local run_id="$1" attempt="$2" image_ref="$3" source_dir="$4"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_image_ref "$image_ref"
  local image_digest="${image_ref##*@}"
  validate_digest "$image_digest"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "precheck source directory must be absolute"
  validate_bundle
  doctor >/dev/null

  local docker_socket="${DENSE_MEM_CI_DOCKER_SOCKET:-}"
  if [[ -z "$docker_socket" ]]; then
    case "${DOCKER_HOST:-}" in
      unix://*) docker_socket="${DOCKER_HOST#unix://}" ;;
      "") docker_socket="/var/run/docker.sock" ;;
      *) fail "the CI Docker host must use a Unix socket" ;;
    esac
  fi
  [[ -S "$docker_socket" ]] || fail "the CI Docker socket is unavailable"
  local project="densemem-ci-${run_id}-${attempt}-precheck"
  validate_project "$project"
  local precheck_network="$project"
  docker network rm "$precheck_network" >/dev/null 2>&1 || true
  docker network create \
    --driver bridge \
    --attachable \
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
    --label "io.dense-mem.ci.repository=${REPOSITORY}" \
    --label "io.dense-mem.ci.run-id=${run_id}" \
    --label "io.dense-mem.ci.run-attempt=${attempt}" \
    --label "io.dense-mem.ci.phase=precheck" \
    --label "io.dense-mem.ci.scenario=precheck" \
    --label "io.dense-mem.ci.image-digest=${image_digest}" \
    --label "io.dense-mem.ci.created-at=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --label "io.dense-mem.ci.compose-project=${project}" \
    "$precheck_network" >/dev/null

  cleanup_precheck() {
    local status=$?
    trap - EXIT INT TERM
    docker network rm "$precheck_network" >/dev/null 2>&1 || true
    exit "$status"
  }
  trap cleanup_precheck EXIT INT TERM

  local test_image
  test_image="$(env_value DENSE_MEM_CI_GO_TEST_IMAGE 2>/dev/null || printf '%s' golang:1.26.6-bookworm)"
  [[ "$test_image" =~ ^[A-Za-z0-9._/:@-]+$ ]] || fail "invalid precheck Go test image"
  set +e
  docker run --rm \
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
    --label "io.dense-mem.ci.repository=${REPOSITORY}" \
    --label "io.dense-mem.ci.run-id=${run_id}" \
    --label "io.dense-mem.ci.run-attempt=${attempt}" \
    --label "io.dense-mem.ci.phase=precheck" \
    --label "io.dense-mem.ci.scenario=precheck" \
    --label "io.dense-mem.ci.image-digest=${image_digest}" \
    --label "io.dense-mem.ci.created-at=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --label "io.dense-mem.ci.compose-project=${project}" \
    --network "$precheck_network" \
    --volume "${docker_socket}:/var/run/docker.sock" \
    --volume "${source_dir}:/workspace:ro" \
    --workdir /workspace \
    -e DOCKER_HOST=unix:///var/run/docker.sock \
    -e TESTCONTAINERS_RYUK_DISABLED=true \
    -e DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 \
    -e DENSE_MEM_CI_PRECHECK_CONTRACT="$CONTRACT_VERSION" \
    -e DENSE_MEM_CI_PRECHECK_REPOSITORY="$REPOSITORY" \
    -e DENSE_MEM_CI_PRECHECK_RUN_ID="$run_id" \
      -e DENSE_MEM_CI_PRECHECK_RUN_ATTEMPT="$attempt" \
      -e DENSE_MEM_CI_PRECHECK_PROJECT="$project" \
      -e DENSE_MEM_CI_PRECHECK_NETWORK="$precheck_network" \
      -e DENSE_MEM_CI_PRECHECK_IMAGE_DIGEST="$image_digest" \
      "$test_image" \
    go test ./internal/repository \
      -run '^(TestSSORuntimeEntitlementsExcludeArchivedTeams|TestDreamControlRepositoryIsTeamScopedAndAuditsAtomicRefresh|TestDreamRepositoryPersistsEvidenceGroundedHypothesisAndPathAssessment|TestScheduledDreamRecoveryFencesExpiredLease|TestScheduledDreamsAreTeamOwnedAndFeedbackIsActorAudited)$' \
      -count=1
  local repository_status=$?
  if ((repository_status == 0)); then
    docker run --rm \
      --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
      --label "io.dense-mem.ci.repository=${REPOSITORY}" \
      --label "io.dense-mem.ci.run-id=${run_id}" \
      --label "io.dense-mem.ci.run-attempt=${attempt}" \
      --label "io.dense-mem.ci.phase=precheck" \
      --label "io.dense-mem.ci.scenario=precheck" \
      --label "io.dense-mem.ci.image-digest=${image_digest}" \
      --label "io.dense-mem.ci.created-at=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --label "io.dense-mem.ci.compose-project=${project}" \
      --network "$precheck_network" \
      --volume "${docker_socket}:/var/run/docker.sock" \
      --volume "${source_dir}:/workspace:ro" \
      --workdir /workspace \
      -e DOCKER_HOST=unix:///var/run/docker.sock \
      -e TESTCONTAINERS_RYUK_DISABLED=true \
      -e DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 \
      -e DENSE_MEM_CI_PRECHECK_CONTRACT="$CONTRACT_VERSION" \
      -e DENSE_MEM_CI_PRECHECK_REPOSITORY="$REPOSITORY" \
      -e DENSE_MEM_CI_PRECHECK_RUN_ID="$run_id" \
      -e DENSE_MEM_CI_PRECHECK_RUN_ATTEMPT="$attempt" \
      -e DENSE_MEM_CI_PRECHECK_PROJECT="$project" \
      -e DENSE_MEM_CI_PRECHECK_NETWORK="$precheck_network" \
      -e DENSE_MEM_CI_PRECHECK_IMAGE_DIGEST="$image_digest" \
      "$test_image" \
      go test ./internal/http \
        -run '^TestSSOOIDCCallbackSkipsArchivedTeamMappingIntegration$' \
        -count=1
  fi
  local test_status=$?
  if ((repository_status != 0)); then
    test_status=$repository_status
  fi
  set -e
  trap - EXIT INT TERM
  docker network rm "$precheck_network" >/dev/null 2>&1 || true
  if ((test_status != 0)); then
    return "$test_status"
  fi
  printf '%s\n' "precheck passed"
}

doctor() {
  validate_bundle
  [[ -r "$REGISTRY_SCRIPT" ]] || fail "missing trusted scenario registry: $REGISTRY_SCRIPT"
  docker info >/dev/null 2>&1 || fail "the rootless Docker daemon is unavailable"
  local security_options
  security_options="$(docker info --format '{{json .SecurityOptions}}' 2>/dev/null || true)"
  [[ "$security_options" == *rootless* ]] || fail "Docker daemon is not rootless"
  local project="densemem-ci-doctor-${BASHPID}"
  validate_project "$project"
  compose_base_env "$project"
  assert_no_host_ports || fail "Compose bundle publishes a host port"
  docker compose --env-file "$ENV_FILE" --project-name "$project" --file "$COMPOSE_FILE" config --quiet || fail "Compose bundle validation failed"
  printf '%s\n' "$CONTRACT_VERSION"
}

start_stack() {
  local run_id="$1" attempt="$2" phase="$3" scenario="$4" image_ref="$5" source_dir="${6:-${PWD}}"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_phase "$phase"
  validate_scenario "$scenario"
  validate_image_ref "$image_ref"
  local digest="${image_ref##*@}"
  validate_digest "$digest"
  image_ref="${image_ref%@*}"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "stack source directory must be absolute"
  validate_bundle

  local project
  project="$(managed_project_name "$run_id" "$attempt" "$phase" "$scenario")"
  if [[ "$scenario" != "shared" ]]; then
    validate_registered_scenario "$source_dir" "$scenario" "$phase"
  fi
  local helpers
  helpers="$(scenario_helpers "$source_dir" "$phase" "$scenario")" || fail "scenario helper profiles are unavailable"
  [[ "$helpers" =~ ^[a-z0-9_,]*$ ]] || fail "invalid helper profile list"
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  compose_base_env "$project" "$phase" "$scenario" "$digest" "$run_id" "$attempt" "$created_at" "${image_ref}@${digest}"
  local run_root="${JOB_DIR}/${run_id}-${attempt}/${phase}-${scenario}"
  mkdir -p "$run_root"
  chmod 700 "$run_root"
  local runtime_compose_path="${run_root}/runtime-compose.yml"
  local stack_started=0
  cleanup_failed_start() {
    local status=$?
    trap - EXIT INT TERM
    set +e
    if ((stack_started)); then
      local cleanup_overlay="$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE"
      DENSE_MEM_CI_COMPOSE_OVERLAY_FILE=""
      stop_stack "$project" || true
      DENSE_MEM_CI_COMPOSE_OVERLAY_FILE="$cleanup_overlay"
    fi
    if [[ -n "$runtime_compose_path" && -e "$runtime_compose_path" ]]; then
      rm -f -- "$runtime_compose_path"
    fi
    if [[ -n "$DENSE_MEM_CI_HELPER_DIR" && -e "$DENSE_MEM_CI_HELPER_DIR" ]]; then
      rm -r -- "$DENSE_MEM_CI_HELPER_DIR"
    fi
    if [[ -n "$DENSE_MEM_CI_PRIVATE_DIR" && -e "$DENSE_MEM_CI_PRIVATE_DIR" ]]; then
      rm -r -- "$DENSE_MEM_CI_PRIVATE_DIR"
    fi
    if docker image inspect "${project}-oauth-compat-harness:latest" >/dev/null 2>&1; then
      docker image rm "${project}-oauth-compat-harness:latest" >/dev/null 2>&1 || true
    fi
    exit "$status"
  }
  trap cleanup_failed_start EXIT INT TERM

  prepare_stack_helpers "$project" "$source_dir" "$helpers" "$run_id" "$attempt" "$phase" "$scenario" >/dev/null
  assert_no_host_ports || fail "Compose bundle publishes a host port"

  local -a profiles=(--profile client_env)
  if [[ -n "$helpers" ]]; then
    local helper
    local -a helper_values
    IFS=',' read -r -a helper_values <<< "$helpers"
    for helper in "${helper_values[@]}"; do
      [[ -n "$helper" ]] || continue
      case "$helper" in
        conflict_provider|conflict_review|oauth|oauth_compatibility|playwright|synchronous_write|verifier) ;;
        *) fail "unknown helper profile: $helper" ;;
      esac
      profiles+=(--profile "$helper")
    done
  fi
  if [[ "$scenario" == "identity_cleanup" ]]; then
    local identity_postgres_user identity_postgres_password identity_postgres_db
    identity_postgres_user="$(env_value POSTGRES_USER 2>/dev/null || true)"
    identity_postgres_password="$(env_value POSTGRES_PASSWORD 2>/dev/null || true)"
    identity_postgres_db="$(env_value POSTGRES_DB 2>/dev/null || true)"
    [[ -n "$identity_postgres_user" && -n "$identity_postgres_password" && -n "$identity_postgres_db" ]] || fail "identity cleanup seed requires PostgreSQL credentials"
    stack_started=1
    if ! ci_compose "${profiles[@]}" up -d --wait --wait-timeout 300 postgres >/dev/null; then
      ci_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
      fail "PostgreSQL failed to start for identity cleanup seed"
    fi
    if ! run_identity_cleanup_seed "$source_dir" "$project" "$identity_postgres_user" "$identity_postgres_password" "$identity_postgres_db"; then
      ci_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
      fail "identity cleanup seed failed"
    fi
  fi
  stack_started=1
  if ! ci_compose "${profiles[@]}" up -d --wait --wait-timeout 300 >/dev/null; then
    ci_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
    fail "Compose stack failed to start"
  fi
  if ! ci_compose --profile client_env exec -T \
    -e "DENSE_MEM_CI_USER_URL=http://server:8080" \
    -e "DENSE_MEM_CI_CONTROL_URL=http://server:8090" \
    -e "DENSE_MEM_CI_PROMETHEUS_URL=http://prometheus:9090" \
    -e "DENSE_MEM_CI_NETWORK=${project}_ci" \
    -e "DENSE_MEM_CI_SCENARIO=${scenario}" \
    client-env sh -ec 'umask 077; printf "DENSE_MEM_USER_URL=%s\\nDENSE_MEM_CONTROL_URL=%s\\nDENSE_MEM_PROMETHEUS_URL=%s\\nDENSE_MEM_E2E_NETWORK=%s\\nDENSE_MEM_E2E_SCENARIO=%s\\n" "$DENSE_MEM_CI_USER_URL" "$DENSE_MEM_CI_CONTROL_URL" "$DENSE_MEM_CI_PROMETHEUS_URL" "$DENSE_MEM_CI_NETWORK" "$DENSE_MEM_CI_SCENARIO" > /client/runtime.env' >/dev/null; then
    ci_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
    fail "failed to write the non-secret client runtime environment"
  fi

  start_stack_helpers "$project" "$source_dir" "$helpers"
  write_runtime_compose "$runtime_compose_path" "$project" "${image_ref}@${digest}"

  printf '%s\n' "$project"
  stack_started=0
  trap - EXIT INT TERM
}


source "${CONTROLLER_DIR}/e2e-host-controller-runtime.sh"

case "${1:-}" in
  doctor)
    [[ "$#" -eq 1 ]] || usage
    doctor
    ;;
  start)
    [[ "$#" -eq 7 ]] || usage
    start_stack "$2" "$3" "$4" "$5" "$6" "$7"
    ;;
  run)
    [[ "$#" -eq 8 ]] || usage
    run_scenario "$2" "$3" "$4" "$5" "$6" "$7" "$8"
    ;;
  stop)
    [[ "$#" -eq 2 ]] || usage
    stop_stack "$2"
    ;;
  stale-cleanup)
    [[ "$#" -le 5 ]] || usage
    stale_cleanup "${2:-86400}" "${3:-}" "${4:-}" "${5:-}"
    ;;
  precheck)
    [[ "$#" -eq 5 ]] || usage
    precheck "$2" "$3" "$4" "$5"
    ;;
  *)
    usage
    ;;
esac
