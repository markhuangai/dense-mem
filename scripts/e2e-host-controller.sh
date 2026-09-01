#!/usr/bin/env bash
set -euo pipefail
CONTROLLER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

CONTRACT_VERSION="dense-mem-ci-e2e.v1"
CI_HOME="${DENSE_MEM_CI_HOME:-${HOME}/dense-mem-ci}"
COMPOSE_FILE="${DENSE_MEM_CI_COMPOSE_FILE:-${CI_HOME}/docker-compose.yml}"
ENV_FILE="${DENSE_MEM_CI_ENV_FILE:-${CI_HOME}/.env}"
LEASE_DIR="${DENSE_MEM_CI_LEASE_DIR:-${CI_HOME}/leases}"
RUN_DIR="${DENSE_MEM_CI_RUN_DIR:-${CI_HOME}/runs}"
REPOSITORY="${DENSE_MEM_CI_REPOSITORY:-${GITHUB_REPOSITORY:-markhuangai/dense-mem}}"
PROXY_SCRIPT="${DENSE_MEM_CI_PROXY_SCRIPT:-${CI_HOME}/e2e-docker-proxy.mjs}"
RUNTIME_ADAPTER_SCRIPT="${DENSE_MEM_CI_RUNTIME_ADAPTER_SCRIPT:-${CI_HOME}/e2e-runtime-adapter.mjs}"
REGISTRY_SCRIPT="${DENSE_MEM_CI_REGISTRY_SCRIPT:-${CI_HOME}/e2e-scenario-registry.mjs}"
DENSE_MEM_CI_COMPOSE_OVERLAY_FILE=""
DENSE_MEM_CI_HELPER_DIR=""
DENSE_MEM_CI_PRIVATE_DIR=""
E2E_SCENARIO_PROXY_PID=""
E2E_SCENARIO_PROXY_SOCKET=""
E2E_SCENARIO_PROXY_LOG=""

fail() {
  printf 'dense-mem CI controller: %s\n' "$*" >&2
  exit 1
}

usage() {
  printf '%s\n' \
    'usage:' \
    '  e2e-stack.sh doctor' \
    '  e2e-stack.sh acquire RUN_ID ATTEMPT IMAGE_REF DIGEST' \
    '  e2e-stack.sh start RUN_ID ATTEMPT PHASE SCENARIO IMAGE_REF DIGEST SOURCE_REVISION [HELPERS] [SOURCE_DIR]' \
    '  e2e-stack.sh run MANIFEST SOURCE_DIR SCENARIO' \
    '  e2e-stack.sh stop PROJECT' \
    '  e2e-stack.sh release LEASE_FILE' \
    '  e2e-stack.sh stale-cleanup [MAX_AGE_SECONDS]' \
    '  e2e-stack.sh cleanup-run RUN_ID ATTEMPT' \
    '  e2e-stack.sh precheck RUN_ID ATTEMPT SOURCE_REVISION IMAGE_DIGEST SOURCE_DIR' \
    '  e2e-stack.sh validate MANIFEST' >&2
  exit 2
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

require_command docker
require_command node
require_command git
require_command flock

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

validate_scenario() {
  [[ "$1" =~ ^[a-z0-9_]+$ ]] || fail "invalid scenario: $1"
}

validate_registered_scenario() {
  local source_dir="$1" scenario="$2" phase="$3"
  [[ -f "$source_dir/scripts/e2e-scenario-registry.mjs" ]] || fail "scenario registry is unavailable in the tested source"
  [[ -r "$REGISTRY_SCRIPT" ]] || fail "trusted scenario registry is unavailable: $REGISTRY_SCRIPT"
  local classification
  classification="$(node "$REGISTRY_SCRIPT" --scenario "$scenario")" || fail "scenario is not in the trusted registry: $scenario"
  node - "$classification" "$phase" <<'NODE' || fail "scenario is not audited for the requested production phase"
const classification = JSON.parse(process.argv[2]);
const phase = process.argv[3];
const expectedIsolation = phase === "shared" ? "shared_team" : "exclusive";
if (classification.audited !== true || classification.runtime !== "production" || classification.isolation !== expectedIsolation) process.exit(1);
NODE
}

validate_image_ref() {
  [[ "$1" =~ ^ghcr\.io/[a-z0-9_.-]+/[a-z0-9_.-]+((:[A-Za-z0-9_.-]+)|(@sha256:[0-9a-f]{64}))?$ ]] || fail "image must be a GHCR repository reference"
}

canonical_image_ref() {
  local image_ref="$1"
  validate_image_ref "$image_ref"
  printf '%s\n' "${image_ref%@*}"
}

validate_bundle() {
  [[ -f "$COMPOSE_FILE" ]] || fail "missing Compose bundle: $COMPOSE_FILE"
  [[ -f "$ENV_FILE" ]] || fail "missing CI environment file: $ENV_FILE"
  local bundle_dir="$(dirname "$COMPOSE_FILE")"
  [[ -f "${bundle_dir}/prometheus.yml" ]] || fail "missing Prometheus configuration"
  [[ -f "${bundle_dir}/telemetry-scrape-token" ]] || fail "missing telemetry scrape token"
  local env_mode env_owner
  env_mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE")"
  [[ "$env_mode" == "600" ]] || fail "CI environment file must have mode 0600"
  env_owner="$(stat -c '%u' "$ENV_FILE" 2>/dev/null || stat -f '%u' "$ENV_FILE")"
  [[ "$env_owner" == "$(id -u)" ]] || fail "CI environment file is not owned by the runner user"
}

env_value() {
  local field="$1"
  node - "$ENV_FILE" "$field" <<'NODE'
const fs = require("node:fs");
const [path, field] = process.argv.slice(2);
let found;
for (const line of fs.readFileSync(path, "utf8").split(/\r?\n/)) {
  const match = line.match(/^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/);
  if (!match || match[1] !== field) continue;
  let value = match[2].trim();
  if ((value.startsWith("\"") && value.endsWith("\"")) || (value.startsWith("'") && value.endsWith("'"))) value = value.slice(1, -1);
  found = value;
}

if (!found) process.exit(1);
process.stdout.write(found);
NODE
}

redact_diagnostics() {
  local env_file="$1"
  shift
  DENSE_MEM_CI_REDACT_ENV_FILE="$env_file" \
    DENSE_MEM_CI_REDACT_EXTRA_VALUES="$(printf '%s\n' "$@")" \
    node <<'NODE'
const fs = require("node:fs");
const envFile = process.env.DENSE_MEM_CI_REDACT_ENV_FILE;
const values = [];
const add = (value) => {
  if (typeof value !== "string" || value.length < 4 || /[\r\n]/.test(value)) return;
  values.push(value);
};
try {
  for (const line of fs.readFileSync(envFile, "utf8").split(/\r?\n/)) {
    const match = line.match(/^\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*\s*=\s*(.*)$/);
    if (!match) continue;
    let value = match[1].trim();
    if ((value.startsWith("\"") && value.endsWith("\"")) || (value.startsWith("'") && value.endsWith("'"))) value = value.slice(1, -1);
    add(value);
  }
} catch {}
for (const value of (process.env.DENSE_MEM_CI_REDACT_EXTRA_VALUES || "").split("\n")) add(value);
const patterns = [...new Set(values)].sort((left, right) => right.length - left.length).map((value) => new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g"));
const maxPatternLength = values.reduce((max, value) => Math.max(max, value.length), 0);
let carry = "";
const redact = (input) => {
  for (const pattern of patterns) input = input.replace(pattern, "[REDACTED]");
  return input;
};
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => {
  const input = carry + chunk;
  const cutoff = Math.max(0, input.length - Math.max(0, maxPatternLength - 1));
  if (cutoff > 0) process.stdout.write(redact(input.slice(0, cutoff)));
  carry = input.slice(cutoff);
});
process.stdin.on("end", () => {
  process.stdout.write(redact(carry));
});
NODE
}

stop_scenario_proxy() {
  set +e
  if [[ -n "$E2E_SCENARIO_PROXY_PID" ]]; then
    kill -TERM "$E2E_SCENARIO_PROXY_PID" >/dev/null 2>&1 || true
    wait "$E2E_SCENARIO_PROXY_PID" >/dev/null 2>&1 || true
  fi
  rm -f -- "$E2E_SCENARIO_PROXY_SOCKET" "$E2E_SCENARIO_PROXY_LOG"
  E2E_SCENARIO_PROXY_PID=""
  E2E_SCENARIO_PROXY_SOCKET=""
  E2E_SCENARIO_PROXY_LOG=""
  set -e
}

cleanup_scenario_proxy_on_exit() {
  local status=$?
  trap - EXIT INT TERM
  stop_scenario_proxy
  exit "$status"
}

terminate_e2e_process_groups() {
  local pid
  local any_alive
  local deadline=$((SECONDS + 60))
  for pid in "$@"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    fi
  done
  while ((SECONDS < deadline)); do
    any_alive=0
    for pid in "$@"; do
      if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
        any_alive=1
        break
      fi
    done
    ((any_alive == 0)) && break
    sleep 0.2
  done
  for pid in "$@"; do
    if [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null; then
      kill -KILL -- "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
    fi
  done
  for pid in "$@"; do
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] && wait "$pid" 2>/dev/null || true
  done
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
  export DENSE_MEM_CI_NETWORK_NAME="${project}_ci"
  export DENSE_MEM_CI_POSTGRES_VOLUME_NAME="${project}_postgres-data"
  export DENSE_MEM_CI_REDIS_VOLUME_NAME="${project}_redis-data"
  export DENSE_MEM_CI_PROMETHEUS_VOLUME_NAME="${project}_prometheus-data"
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

validate_runtime_manifest() {
  local manifest="$1"
  [[ -f "$manifest" ]] || fail "missing runtime manifest: $manifest"
  [[ -r "$RUNTIME_ADAPTER_SCRIPT" ]] || fail "missing trusted runtime adapter: $RUNTIME_ADAPTER_SCRIPT"
  node "$RUNTIME_ADAPTER_SCRIPT" --validate "$manifest"
}

precheck() {
  local run_id="$1" attempt="$2" source_revision="$3" image_digest="$4" source_dir="$5"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_revision "$source_revision"
  validate_digest "$image_digest"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "precheck source directory must be absolute"
  [[ "$(git -C "$source_dir" rev-parse HEAD 2>/dev/null || true)" == "$source_revision" ]] || fail "precheck checkout does not match the requested revision"
  validate_bundle
  doctor >/dev/null

  local run_root="${RUN_DIR}/${run_id}-${attempt}"
  local marker="${run_root}/precheck.ok"
  mkdir -p "$run_root"
  chmod 700 "$run_root"
  if [[ -f "$marker" ]]; then
    local marker_value
    marker_value="$(cat "$marker")"
    [[ "$marker_value" == "${CONTRACT_VERSION}|${source_revision}" ]] || fail "precheck marker does not match the requested revision"
    printf '%s\n' "precheck already passed"
    return 0
  fi

  local proxy_pid=""
  local proxy_socket=""
  local proxy_log=""
  stop_precheck_proxy() {
    set +e
    if [[ -n "$proxy_pid" ]]; then
      kill -TERM "$proxy_pid" >/dev/null 2>&1 || true
      wait "$proxy_pid" >/dev/null 2>&1 || true
    fi
    if [[ -n "$proxy_socket" || -n "$proxy_log" ]]; then
      rm -f -- "$proxy_socket" "$proxy_log"
    fi
    set -e
  }
  cleanup_precheck() {
    local status=$?
    trap - EXIT INT TERM
    stop_precheck_proxy
    exit "$status"
  }
  trap cleanup_precheck EXIT INT TERM

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
  proxy_socket="${run_root}/precheck.docker.sock"
  proxy_log="${run_root}/precheck.docker-proxy.log"
  node "$PROXY_SCRIPT" \
    --listen "$proxy_socket" \
    --target "$docker_socket" \
    --project "$project" \
    --contract "$CONTRACT_VERSION" \
    --repository "$REPOSITORY" \
    --mode precheck \
    --run-id "$run_id" \
    --attempt "$attempt" \
    --phase precheck \
    --scenario precheck \
    --image-digest "$image_digest" \
    --network "$precheck_network" >"$proxy_log" 2>&1 &
  proxy_pid=$!
  for _ in $(seq 1 100); do
    if [[ -S "$proxy_socket" ]]; then break; fi
    sleep 0.1
  done
  if [[ ! -S "$proxy_socket" ]]; then
    fail "the precheck Docker proxy did not start"
  fi

  DOCKER_HOST="unix://${proxy_socket}" docker network create \
    --driver bridge \
    --attachable \
    "$precheck_network" >/dev/null

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
    --volume "${proxy_socket}:/var/run/docker.sock" \
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
      --volume "${proxy_socket}:/var/run/docker.sock" \
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
  stop_precheck_proxy
  trap - EXIT INT TERM
  if ((test_status != 0)); then
    return "$test_status"
  fi
  printf '%s\n' "${CONTRACT_VERSION}|${source_revision}" > "$marker"
  chmod 600 "$marker"
  printf '%s\n' "precheck passed"
}

doctor() {
  validate_bundle
  [[ -r "$PROXY_SCRIPT" ]] || fail "missing trusted Docker proxy: $PROXY_SCRIPT"
  [[ -r "$RUNTIME_ADAPTER_SCRIPT" ]] || fail "missing trusted runtime adapter: $RUNTIME_ADAPTER_SCRIPT"
  [[ -r "$REGISTRY_SCRIPT" ]] || fail "missing trusted scenario registry: $REGISTRY_SCRIPT"
  docker info >/dev/null 2>&1 || fail "the rootless Docker daemon is unavailable"
  local security_options expected_daemon_id actual_daemon_id
  security_options="$(docker info --format '{{json .SecurityOptions}}' 2>/dev/null || true)"
  [[ "$security_options" == *rootless* ]] || fail "Docker daemon is not rootless"
  expected_daemon_id="$(env_value DENSE_MEM_CI_DAEMON_ID 2>/dev/null || true)"
  [[ "$expected_daemon_id" =~ ^[A-Za-z0-9:_-]{8,128}$ ]] || fail "DENSE_MEM_CI_DAEMON_ID is missing or invalid"
  actual_daemon_id="$(docker info --format '{{.ID}}' 2>/dev/null || true)"
  [[ "$actual_daemon_id" == "$expected_daemon_id" ]] || fail "runner is attached to an unexpected Docker daemon"
  local project="densemem-ci-doctor-${BASHPID}"
  validate_project "$project"
  compose_base_env "$project"
  assert_no_host_ports || fail "Compose bundle publishes a host port"
  docker compose --env-file "$ENV_FILE" --project-name "$project" --file "$COMPOSE_FILE" config --quiet || fail "Compose bundle validation failed"
  printf '%s\n' "$CONTRACT_VERSION"
}

lease_path() {
  local digest_key="${1#sha256:}"
  printf '%s/%s.%s.%s.lease\n' "$LEASE_DIR" "$digest_key" "$2" "$3"
}

acquire() {
  local run_id="$1" attempt="$2" image_ref="$3" digest="$4"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_digest "$digest"
  local requested_image_ref="$image_ref"
  image_ref="$(canonical_image_ref "$requested_image_ref")"
  if [[ "$requested_image_ref" == *@* && "${requested_image_ref##*@}" != "$digest" ]]; then
    fail "image digest does not match the requested digest"
  fi
  validate_bundle
  mkdir -p "$LEASE_DIR"
  chmod 700 "$LEASE_DIR"
  umask 077
  exec 9>"${LEASE_DIR}/.lock"
  flock -x 9
  local lease
  lease="$(lease_path "$digest" "$run_id" "$attempt")"
  if [[ -e "$lease" ]]; then
    local existing_contract existing_repository existing_run_id existing_attempt existing_image existing_digest
    existing_contract="$(sed -n 's/^contract=//p' "$lease")"
    existing_repository="$(sed -n 's/^repository=//p' "$lease")"
    existing_run_id="$(sed -n 's/^run_id=//p' "$lease")"
    existing_attempt="$(sed -n 's/^run_attempt=//p' "$lease")"
    existing_image="$(sed -n 's/^image=//p' "$lease")"
    existing_digest="$(sed -n 's/^digest=//p' "$lease")"
    if [[ "$existing_contract" != "$CONTRACT_VERSION" ||
      "$existing_repository" != "$REPOSITORY" ||
      "$existing_run_id" != "$run_id" ||
      "$existing_attempt" != "$attempt" ||
      "$existing_image" != "$image_ref" ||
      "$existing_digest" != "$digest" ]]; then
      flock -u 9
      exec 9>&-
      fail "an incompatible digest lease already exists for this run and attempt"
    fi
    if ! docker image inspect "${image_ref}@${digest}" >/dev/null 2>&1; then
      docker pull "${image_ref}@${digest}" >/dev/null || {
        flock -u 9
        exec 9>&-
        fail "failed to reacquire the leased candidate image"
      }
    fi
    flock -u 9
    exec 9>&-
    printf '%s\n' "$lease"
    return 0
  fi
  local temporary
  temporary="$(mktemp "${LEASE_DIR}/.lease.XXXXXX")"
  printf 'contract=%s\nrepository=%s\nrun_id=%s\nrun_attempt=%s\nimage=%s\ndigest=%s\ncreated_at=%s\n' "$CONTRACT_VERSION" "$REPOSITORY" "$run_id" "$attempt" "$image_ref" "$digest" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$temporary"
  if ! docker pull "${image_ref}@${digest}" >/dev/null; then
    rm -f -- "$temporary"
    flock -u 9
    exec 9>&-
    fail "failed to acquire candidate image"
  fi
  mv -- "$temporary" "$lease"
  flock -u 9
  exec 9>&-
  printf '%s\n' "$lease"
}

start_stack() {
  local run_id="$1" attempt="$2" phase="$3" scenario="$4" image_ref="$5" digest="$6" source_revision="$7" helpers="${8:-}" source_dir="${9:-${PWD}}"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_phase "$phase"
  validate_scenario "$scenario"
  local requested_image_ref="$image_ref"
  image_ref="$(canonical_image_ref "$requested_image_ref")"
  validate_digest "$digest"
  if [[ "$requested_image_ref" == *@* && "${requested_image_ref##*@}" != "$digest" ]]; then
    fail "image digest does not match the requested digest"
  fi
  validate_revision "$source_revision"
  [[ "$helpers" =~ ^[a-z0-9_,]*$ ]] || fail "invalid helper profile list"
  validate_bundle

  local project
  project="$(managed_project_name "$run_id" "$attempt" "$phase" "$scenario")"
  if [[ "$scenario" != "shared" ]]; then
    validate_registered_scenario "$source_dir" "$scenario" "$phase"
  fi
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  compose_base_env "$project" "$phase" "$scenario" "$digest" "$run_id" "$attempt" "$created_at" "${image_ref}@${digest}"
  export DENSE_MEM_CI_HELPERS="$helpers"
  mkdir -p "$RUN_DIR/${run_id}-${attempt}"
  chmod 700 "$RUN_DIR" "$RUN_DIR/${run_id}-${attempt}"
  local manifest_path="${RUN_DIR}/${run_id}-${attempt}/${phase}-${scenario}.json"
  if [[ -e "$manifest_path" ]]; then
    node - "$manifest_path" "$project" "$phase" "$scenario" "$image_ref" "$digest" "$source_revision" "$helpers" <<'NODE' || fail "existing runtime manifest does not match the requested start"
const fs = require("node:fs");
const [path, project, phase, scenario, image, digest, revision, helpers] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(path, "utf8"));
const expectedHelpers = helpers ? helpers.split(",").filter(Boolean) : [];
if (
  manifest.contract_version !== "dense-mem-ci-e2e.v1" ||
  manifest.runtime !== "production" ||
  manifest.compose_project !== project ||
  manifest.phase !== phase ||
  manifest.scenario !== scenario ||
  manifest.image !== image ||
  manifest.image_digest !== digest ||
  manifest.source_revision !== revision ||
  JSON.stringify(manifest.helper_profiles || []) !== JSON.stringify(expectedHelpers)
) process.exit(1);
process.stdout.write(`${JSON.stringify(manifest)}\n`);
NODE
    return 0
  fi

  local runtime_compose_path="${RUN_DIR}/${run_id}-${attempt}/${phase}-${scenario}.runtime-compose.yml"
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
  if ! ci_compose exec -T \
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

  node - "$manifest_path" "$project" "$run_id" "$attempt" "$phase" "$scenario" "$image_ref" "$digest" "$source_revision" "$helpers" "$created_at" "$runtime_compose_path" "${DENSE_MEM_CI_COMPOSE_OVERLAY_FILE:-}" <<'NODE'
const fs = require("node:fs");
const [path, project, runId, attempt, phase, scenario, image, digest, revision, helpers, createdAt, runtimeCompose, helperOverlay] = process.argv.slice(2);
const manifest = {
  contract_version: "dense-mem-ci-e2e.v1",
  repository: process.env.DENSE_MEM_CI_REPOSITORY,
  run_id: Number(runId),
  run_attempt: Number(attempt),
  phase,
  scenario,
  runtime: "production",
  image,
  image_digest: digest,
  source_revision: revision,
  compose_project: project,
  network: `${project}_ci`,
  urls: {
    user: "http://server:8080",
    control: "http://server:8090",
    prometheus: "http://prometheus:9090",
    postgres: "postgres:5432",
  },
  client_env_volume: `${project}_client-env`,
  compose_file: runtimeCompose ? "runtime-compose.yml" : "",
  helper_overlay: helperOverlay ? "helper-compose.yml" : "",
  helper_profiles: helpers ? helpers.split(",").filter(Boolean) : [],
  created_at: createdAt,
};
fs.writeFileSync(path, `${JSON.stringify(manifest)}\n`, { mode: 0o600 });
process.stdout.write(`${JSON.stringify(manifest)}\n`);
NODE
  stack_started=0
  trap - EXIT
}


source "${CONTROLLER_DIR}/e2e-host-controller-runtime.sh"

case "${1:-}" in
  doctor)
    [[ "$#" -eq 1 ]] || usage
    doctor
    ;;
  acquire)
    [[ "$#" -eq 5 ]] || usage
    acquire "$2" "$3" "$4" "$5"
    ;;
  start)
    [[ "$#" -ge 8 && "$#" -le 10 ]] || usage
    start_stack "$2" "$3" "$4" "$5" "$6" "$7" "$8" "${9:-}" "${10:-}"
    ;;
  run)
    [[ "$#" -eq 4 ]] || usage
    run_scenario "$2" "$3" "$4"
    ;;
  stop)
    [[ "$#" -eq 2 ]] || usage
    stop_stack "$2"
    ;;
  release)
    [[ "$#" -eq 2 ]] || usage
    release "$2"
    ;;
  stale-cleanup)
    [[ "$#" -le 2 ]] || usage
    stale_cleanup "${2:-86400}"
    ;;
  cleanup-run)
    [[ "$#" -eq 3 ]] || usage
    cleanup_run "$2" "$3"
    ;;
  precheck)
    [[ "$#" -eq 6 ]] || usage
    precheck "$2" "$3" "$4" "$5" "$6"
    ;;
  validate)
    [[ "$#" -eq 2 ]] || usage
    validate_runtime_manifest "$2"
    ;;
  local-all)
    [[ "$#" -eq 7 ]] || usage
    local_all "$2" "$3" "$4" "$5" "$6" "$7"
    ;;
  *)
    usage
    ;;
esac
