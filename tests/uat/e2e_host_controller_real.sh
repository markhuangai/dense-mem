#!/usr/bin/env bash
set -euo pipefail

if [[ "${DENSE_MEM_E2E_REAL_DOCKER_TESTS:-0}" != "1" ]]; then
  printf '%s\n' 'real rootless Docker controller tests skipped (set DENSE_MEM_E2E_REAL_DOCKER_TESTS=1 to run)'
  exit 0
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTROLLER="${DENSE_MEM_CI_CONTROLLER:-${ROOT_DIR}/scripts/e2e-host-controller.sh}"
TEST_ROOT="$(mktemp -d "${RUNNER_TEMP:-/tmp}/dense-mem-controller-real.XXXXXX")"
CONFIG_DIR="${TEST_ROOT}/config"
JOB_DIR="${TEST_ROOT}/job"
COMPOSE_FILE="${TEST_ROOT}/docker-compose.yml"
ENV_FILE="${TEST_ROOT}/.env"
PROMETHEUS_FILE="${TEST_ROOT}/prometheus.yml"
mkdir -p "$CONFIG_DIR" "$JOB_DIR"

project_one=""
project_two=""
project_failed=""
stale_container=""
stale_network=""
stale_volume=""

fail() {
  printf 'real rootless controller test: %s\n' "$*" >&2
  exit 1
}

fixture_prefix="${GITHUB_RUN_ID:-${BASHPID}}"
fixture_attempt="${GITHUB_RUN_ATTEMPT:-1}"
[[ "$fixture_prefix" =~ ^[1-9][0-9]*$ ]] || fail "workflow run ID is invalid"
[[ "$fixture_attempt" =~ ^[1-9][0-9]*$ ]] || fail "workflow run attempt is invalid"
run_one="${fixture_prefix}1"
run_two="${fixture_prefix}2"
run_failed="${fixture_prefix}3"
run_stale="${fixture_prefix}4"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  for project in "$project_one" "$project_two" "$project_failed"; do
    [[ -n "$project" ]] && "$CONTROLLER" stop "$project" >/dev/null 2>&1 || true
  done
  [[ -n "$stale_container" ]] && docker rm -f "$stale_container" >/dev/null 2>&1 || true
  [[ -n "$stale_network" ]] && docker network rm "$stale_network" >/dev/null 2>&1 || true
  [[ -n "$stale_volume" ]] && docker volume rm "$stale_volume" >/dev/null 2>&1 || true
  rm -r -- "$TEST_ROOT"
  exit "$status"
}
trap cleanup EXIT INT TERM

[[ -x "$CONTROLLER" ]] || fail "controller is unavailable: $CONTROLLER"
command -v docker >/dev/null 2>&1 || fail "docker is unavailable"
command -v node >/dev/null 2>&1 || fail "node is unavailable"
command -v git >/dev/null 2>&1 || fail "git is unavailable"

security_options="$(docker info --format '{{json .SecurityOptions}}')"
[[ "$security_options" == *rootless* ]] || fail "the test requires a rootless Docker daemon"

printf '%s\n' 'CI_TEST=1' 'POSTGRES_PASSWORD=controller-test-password' 'AI_API_KEY=controller-test-key' 'CONTROL_PORTAL_TOKEN=controller-test-token' > "$ENV_FILE"
chmod 600 "$ENV_FILE"
printf '%s\n' 'dense-mem-ci-real-test-token' > "${TEST_ROOT}/telemetry-scrape-token"
chmod 600 "${TEST_ROOT}/telemetry-scrape-token"
cp "${ROOT_DIR}/examples/prometheus.yml" "$PROMETHEUS_FILE"
chmod 644 "${TEST_ROOT}/prometheus.yml"

export DENSE_MEM_CI_CONFIG_DIR="$CONFIG_DIR"
export DENSE_MEM_CI_JOB_DIR="$JOB_DIR"
export DENSE_MEM_CI_COMPOSE_FILE="$COMPOSE_FILE"
export DENSE_MEM_CI_ENV_FILE="$ENV_FILE"
export DENSE_MEM_CI_PROMETHEUS_FILE="$PROMETHEUS_FILE"
export DENSE_MEM_CI_TELEMETRY_TOKEN_FILE="${TEST_ROOT}/telemetry-scrape-token"
export DENSE_MEM_CI_REGISTRY_SCRIPT="${ROOT_DIR}/scripts/e2e-scenario-registry.mjs"
export DENSE_MEM_CI_REPOSITORY="${DENSE_MEM_CI_TEST_REPOSITORY:-markhuangai/dense-mem-controller-contract}"

write_compose() {
  local mode="$1"
  node - "$COMPOSE_FILE" "$mode" <<'NODE'
const fs = require("node:fs");
const [destination, mode] = process.argv.slice(2);
const serverCommand = mode === "fail" ? ["sh", "-c", "exit 1"] : ["sh", "-c", "while :; do sleep 3600; done"];
const lines = [
  "services:",
  "  postgres:",
  "    image: alpine:3.24",
  "    command: [\"sh\", \"-c\", \"while :; do sleep 3600; done\"]",
  "    networks: [ci]",
  "    labels: &ci_labels",
  "      io.dense-mem.ci.contract: ${DENSE_MEM_CI_CONTRACT:?required}",
  "      io.dense-mem.ci.repository: ${DENSE_MEM_CI_REPOSITORY:?required}",
  "      io.dense-mem.ci.run-id: ${DENSE_MEM_CI_RUN_ID:?required}",
  "      io.dense-mem.ci.run-attempt: ${DENSE_MEM_CI_RUN_ATTEMPT:?required}",
  "      io.dense-mem.ci.phase: ${DENSE_MEM_CI_PHASE:?required}",
  "      io.dense-mem.ci.scenario: ${DENSE_MEM_CI_SCENARIO:?required}",
  "      io.dense-mem.ci.image-digest: ${DENSE_MEM_CI_IMAGE_DIGEST:?required}",
  "      io.dense-mem.ci.created-at: ${DENSE_MEM_CI_CREATED_AT:?required}",
  "  redis:",
  "    image: alpine:3.24",
  "    command: [\"sh\", \"-c\", \"while :; do sleep 3600; done\"]",
  "    networks: [ci]",
  "    labels: *ci_labels",
  "  server:",
  "    image: alpine:3.24",
  "    command: " + JSON.stringify(serverCommand),
  "    networks: [ci]",
  "    labels: *ci_labels",
  "  prometheus:",
  "    image: alpine:3.24",
  "    command: [\"sh\", \"-c\", \"while :; do sleep 3600; done\"]",
  "    networks: [ci]",
  "    labels: *ci_labels",
  "  client-env:",
  "    image: alpine:3.24",
  "    command: [\"sh\", \"-c\", \"while :; do sleep 3600; done\"]",
  "    networks: [ci]",
  "    volumes:",
  "      - client-env:/client",
  "    labels: *ci_labels",
];
if (mode === "ports") lines.splice(lines.indexOf("    labels: *ci_labels", lines.indexOf("  server:")), 0, "    ports:", "      - \"127.0.0.1:0:8080\"");
lines.push(
  "networks:",
  "  ci:",
  "    name: ${DENSE_MEM_CI_NETWORK_NAME:?required}",
  "    labels: *ci_labels",
  "volumes:",
  "  client-env:",
  "    name: ${DENSE_MEM_CI_CLIENT_VOLUME_NAME:?required}",
  "    labels: *ci_labels",
  "",
);
fs.writeFileSync(destination, lines.join("\n") + "\n", { mode: 0o600 });
NODE
}

expect_failure() {
  local output="${TEST_ROOT}/expected-failure-$RANDOM.log"
  set +e
  "$@" > "$output" 2>&1
  local status=$?
  set -e
  ((status != 0)) || fail "expected command to fail: $*"
}

project_resources() {
  local project="$1"
  {
    docker ps -aq --filter "label=io.dense-mem.ci.contract=dense-mem-ci-e2e.v1" --filter "label=io.dense-mem.ci.repository=${DENSE_MEM_CI_REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
    docker ps -aq --filter "label=io.dense-mem.ci.contract=dense-mem-ci-e2e.v1" --filter "label=io.dense-mem.ci.repository=${DENSE_MEM_CI_REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
    docker network ls -q --filter "label=io.dense-mem.ci.contract=dense-mem-ci-e2e.v1" --filter "label=io.dense-mem.ci.repository=${DENSE_MEM_CI_REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
    docker network ls -q --filter "label=io.dense-mem.ci.contract=dense-mem-ci-e2e.v1" --filter "label=io.dense-mem.ci.repository=${DENSE_MEM_CI_REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
    docker volume ls -q --filter "label=io.dense-mem.ci.contract=dense-mem-ci-e2e.v1" --filter "label=io.dense-mem.ci.repository=${DENSE_MEM_CI_REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
    docker volume ls -q --filter "label=io.dense-mem.ci.contract=dense-mem-ci-e2e.v1" --filter "label=io.dense-mem.ci.repository=${DENSE_MEM_CI_REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
  } | sort -u
}

assert_no_project_resources() {
  local project="$1"
  [[ -z "$(project_resources "$project")" ]] || fail "resources remain for $project"
}

check_dns() {
  local network="$1"
  docker run --rm --network "$network" alpine:3.24 sh -ec '
    for service in server postgres redis prometheus; do
      getent hosts "$service" >/dev/null 2>&1 || nslookup "$service" >/dev/null 2>&1 || exit 1
    done
  '
}

assert_container_labels() {
  local project="$1" run_id="$2"
  local container labels
  local inspected=0
  while IFS= read -r container; do
    [[ -n "$container" ]] || continue
    inspected=$((inspected + 1))
    labels="$(docker inspect --format '{{json .Config.Labels}}' "$container")"
    node - "$labels" "$project" "$run_id" "$fixture_attempt" "$digest" <<'NODE'
const [raw, project, runId, attempt, digest] = process.argv.slice(2);
const labels = JSON.parse(raw);
for (const [key, value] of Object.entries({
  "io.dense-mem.ci.contract": "dense-mem-ci-e2e.v1",
  "io.dense-mem.ci.repository": process.env.DENSE_MEM_CI_REPOSITORY,
  "io.dense-mem.ci.run-id": runId,
  "io.dense-mem.ci.run-attempt": attempt,
  "io.dense-mem.ci.phase": "shared",
  "io.dense-mem.ci.scenario": "shared",
  "io.dense-mem.ci.image-digest": digest,
  "com.docker.compose.project": project,
})) if (labels[key] !== value) process.exit(1);
if (typeof labels["io.dense-mem.ci.created-at"] !== "string" || labels["io.dense-mem.ci.created-at"].length === 0) process.exit(1);
NODE
  done < <(docker ps -q --filter "label=com.docker.compose.project=${project}")
  ((inspected > 0)) || fail "no containers were inspected for $project"
}

digest="sha256:$(printf '%064d' 1)"
image="ghcr.io/markhuangai/dense-mem:controller-contract-test"

write_compose normal
"$CONTROLLER" doctor >/dev/null
chmod 644 "$ENV_FILE"
expect_failure "$CONTROLLER" doctor
chmod 600 "$ENV_FILE"
write_compose ports
expect_failure "$CONTROLLER" doctor
write_compose normal

project_one="$("$CONTROLLER" start "$run_one" "$fixture_attempt" shared shared "${image}@${digest}" "$ROOT_DIR")"
network_one="${project_one}_ci"
check_dns "$network_one"
assert_container_labels "$project_one" "$run_one"
server_one="$(docker ps -q --filter "label=com.docker.compose.project=${project_one}" --filter "label=com.docker.compose.service=server")"
[[ -n "$server_one" ]] || fail "the first stack server container is missing"
docker restart "$server_one" >/dev/null
check_dns "$network_one"

project_two="$("$CONTROLLER" start "$run_two" "$fixture_attempt" shared shared "${image}@${digest}" "$ROOT_DIR")"
network_two="${project_two}_ci"
[[ "$project_one" != "$project_two" && "$network_one" != "$network_two" ]] || fail "concurrent stacks reused a project or network"
check_dns "$network_two"
ids_one="$(docker ps -aq --filter "label=com.docker.compose.project=${project_one}" | sort)"
ids_two="$(docker ps -aq --filter "label=com.docker.compose.project=${project_two}" | sort)"
[[ -z "$(comm -12 <(printf '%s\n' "$ids_one") <(printf '%s\n' "$ids_two"))" ]] || fail "concurrent stacks share containers"

"$CONTROLLER" stop "$project_one"
"$CONTROLLER" stop "$project_one"
assert_no_project_resources "$project_one"
"$CONTROLLER" stop "$project_two"
"$CONTROLLER" stop "$project_two"
assert_no_project_resources "$project_two"

write_compose fail
project_failed="densemem-ci-${run_failed}-${fixture_attempt}-shared-shared"
expect_failure "$CONTROLLER" start "$run_failed" "$fixture_attempt" shared shared "${image}@${digest}" "$ROOT_DIR"
write_compose normal
assert_no_project_resources "$project_failed"

stale_project="densemem-ci-${run_stale}-${fixture_attempt}-shared-stale"
stale_container="dense-mem-controller-stale-${fixture_prefix}-${fixture_attempt}-$$"
stale_network="${stale_project}_ci"
stale_volume="${stale_project}_data"
docker run -d --name "$stale_container" \
  --label io.dense-mem.ci.contract=dense-mem-ci-e2e.v1 \
  --label io.dense-mem.ci.repository="${DENSE_MEM_CI_REPOSITORY}" \
  --label io.dense-mem.ci.run-id="$run_stale" \
  --label io.dense-mem.ci.run-attempt="$fixture_attempt" \
  --label io.dense-mem.ci.phase=shared \
  --label io.dense-mem.ci.scenario=stale \
  --label io.dense-mem.ci.image-digest="$digest" \
  --label io.dense-mem.ci.created-at=2000-01-01T00:00:00Z \
  --label io.dense-mem.ci.compose-project="$stale_project" \
  alpine:3.24 sh -c 'while :; do sleep 3600; done' >/dev/null
docker network create \
  --label io.dense-mem.ci.contract=dense-mem-ci-e2e.v1 \
  --label io.dense-mem.ci.repository="${DENSE_MEM_CI_REPOSITORY}" \
  --label io.dense-mem.ci.run-id="$run_stale" \
  --label io.dense-mem.ci.run-attempt="$fixture_attempt" \
  --label io.dense-mem.ci.phase=shared \
  --label io.dense-mem.ci.scenario=stale \
  --label io.dense-mem.ci.image-digest="$digest" \
  --label io.dense-mem.ci.created-at=2000-01-01T00:00:00Z \
  --label io.dense-mem.ci.compose-project="$stale_project" \
  "$stale_network" >/dev/null
docker volume create \
  --label io.dense-mem.ci.contract=dense-mem-ci-e2e.v1 \
  --label io.dense-mem.ci.repository="${DENSE_MEM_CI_REPOSITORY}" \
  --label io.dense-mem.ci.run-id="$run_stale" \
  --label io.dense-mem.ci.run-attempt="$fixture_attempt" \
  --label io.dense-mem.ci.phase=shared \
  --label io.dense-mem.ci.scenario=stale \
  --label io.dense-mem.ci.image-digest="$digest" \
  --label io.dense-mem.ci.created-at=2000-01-01T00:00:00Z \
  --label io.dense-mem.ci.compose-project="$stale_project" \
  "$stale_volume" >/dev/null
"$CONTROLLER" stale-cleanup 1 >/dev/null
if docker inspect "$stale_container" >/dev/null 2>&1; then
  fail "stale controller container was not reclaimed"
fi
stale_container=""
if docker network inspect "$stale_network" >/dev/null 2>&1; then
  fail "stale controller network was not reclaimed"
fi
stale_network=""
if docker volume inspect "$stale_volume" >/dev/null 2>&1; then
  fail "stale controller volume was not reclaimed"
fi
stale_volume=""

helper_image="${stale_project}-oauth-compat-harness:latest"
printf '%s\n' 'FROM alpine:3.24' > "${TEST_ROOT}/Dockerfile"
docker build \
  --label io.dense-mem.ci.contract=dense-mem-ci-e2e.v1 \
  --label io.dense-mem.ci.repository="${DENSE_MEM_CI_REPOSITORY}" \
  --label io.dense-mem.ci.run-id="$run_stale" \
  --label io.dense-mem.ci.run-attempt="$fixture_attempt" \
  --label io.dense-mem.ci.phase=shared \
  --label io.dense-mem.ci.scenario=stale \
  --label io.dense-mem.ci.image-digest="$digest" \
  --label io.dense-mem.ci.created-at=2000-01-01T00:00:00Z \
  --label com.docker.compose.project="$stale_project" \
  --tag "$helper_image" "${TEST_ROOT}" >/dev/null
"$CONTROLLER" stale-cleanup 1 >/dev/null
if docker image inspect "$helper_image" >/dev/null 2>&1; then
  fail "stale helper image was not reclaimed"
fi

[[ ! -e "${TEST_ROOT}/leases" && ! -e "${TEST_ROOT}/runs" ]] || fail "controller created persistent lease/run state"
printf '%s\n' 'real rootless Docker controller tests passed'
