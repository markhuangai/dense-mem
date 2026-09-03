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
project_adverse=""
stale_container=""
protected_container=""
protected_phase_container=""
stale_network=""
stale_volume=""
network_blocker=""
network_release_pid=""

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
  for project in "$project_one" "$project_two" "$project_failed" "$project_adverse"; do
    [[ -n "$project" ]] && "$CONTROLLER" stop "$project" >/dev/null 2>&1 || true
  done
  [[ -n "$stale_container" ]] && docker rm -f "$stale_container" >/dev/null 2>&1 || true
  [[ -n "$protected_container" ]] && docker rm -f "$protected_container" >/dev/null 2>&1 || true
  [[ -n "$protected_phase_container" ]] && docker rm -f "$protected_phase_container" >/dev/null 2>&1 || true
  if [[ -n "$network_release_pid" ]]; then
    kill "$network_release_pid" >/dev/null 2>&1 || true
    wait "$network_release_pid" >/dev/null 2>&1 || true
  fi
  [[ -n "$network_blocker" ]] && docker rm -f "$network_blocker" >/dev/null 2>&1 || true
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

printf '%s\n' 'CI_TEST=1' 'POSTGRES_USER=densemem' 'POSTGRES_DB=densemem' 'POSTGRES_PASSWORD=controller-test-password' 'AI_API_KEY=controller-test-key' 'CONTROL_PORTAL_TOKEN=controller-test-token' 'DOCKER_HOST=unix:///run/user/1001/docker.sock' > "$ENV_FILE"
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
const runtimeUser = mode === "bootstrap-role"
  ? "densemem_e2e_bootstrap"
  : mode === "owner-role"
    ? "densemem_e2e_database_owner"
    : "densemem";
const gooseStatements = [
  "CREATE TABLE IF NOT EXISTS public.goose_db_version (version_id bigint PRIMARY KEY, is_applied boolean NOT NULL);",
  "INSERT INTO public.goose_db_version (version_id, is_applied) VALUES (2026080603, TRUE) ON CONFLICT (version_id) DO UPDATE SET is_applied = EXCLUDED.is_applied;",
];
const rlsStatements = [
  "CREATE TABLE IF NOT EXISTS public.user_portal_sessions (id integer PRIMARY KEY, key_id text NOT NULL);",
  "ALTER TABLE public.user_portal_sessions ENABLE ROW LEVEL SECURITY;",
  "ALTER TABLE public.user_portal_sessions FORCE ROW LEVEL SECURITY;",
  "DROP POLICY IF EXISTS user_portal_sessions_system_access ON public.user_portal_sessions;",
  "CREATE POLICY user_portal_sessions_system_access ON public.user_portal_sessions AS PERMISSIVE FOR ALL TO PUBLIC USING (current_setting('app.tx_mode', true) = 'system') WITH CHECK (current_setting('app.tx_mode', true) = 'system');",
];
const stateStatements = mode === "missing-state"
  ? ["SELECT 1;"]
  : mode === "missing-migration"
    ? rlsStatements
    : mode === "missing-rls"
      ? gooseStatements
      : [...gooseStatements, ...rlsStatements];
const postgresInitCommand = [
  "sh",
  "-ec",
  [
    'until pg_isready -h "$${POSTGRES_HOST}" -U "$${POSTGRES_BOOTSTRAP_USER}" -d "$${POSTGRES_BOOTSTRAP_DB}"; do sleep 1; done',
    'PGPASSWORD="$${POSTGRES_RUNTIME_PASSWORD}" psql -h "$${POSTGRES_HOST}" -U "$${POSTGRES_RUNTIME_USER}" -d "$${POSTGRES_RUNTIME_DB}" -v ON_ERROR_STOP=1 <<\'SQL\'',
    ...stateStatements,
    "SQL",
  ].join("\n"),
];
const lines = [
  "services:",
  "  postgres:",
  "    image: pgvector/pgvector:0.8.2-pg18-trixie",
  "    environment:",
  "      POSTGRES_USER: densemem_e2e_bootstrap",
  "      POSTGRES_PASSWORD: dense-mem-e2e-bootstrap-password",
  "      POSTGRES_DB: densemem",
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
  "    healthcheck:",
  "      test: [\"CMD-SHELL\", \"pg_isready -U \\\"$${POSTGRES_USER}\\\" -d \\\"$${POSTGRES_DB}\\\"\"]",
  "      interval: 1s",
  "      timeout: 5s",
  "      retries: 30",
  "      start_period: 1s",
  "  postgres-init:",
  "    image: pgvector/pgvector:0.8.2-pg18-trixie",
  "    environment:",
  "      POSTGRES_HOST: postgres",
  "      POSTGRES_BOOTSTRAP_USER: densemem_e2e_bootstrap",
  "      POSTGRES_BOOTSTRAP_DB: densemem",
  "      POSTGRES_RUNTIME_USER: " + runtimeUser,
  "      POSTGRES_RUNTIME_PASSWORD: controller-test-password",
  "      POSTGRES_RUNTIME_DB: densemem",
  "    command: " + JSON.stringify(postgresInitCommand),
  "    depends_on:",
  "      postgres:",
  "        condition: service_healthy",
  "    networks: [ci]",
  "    labels: *ci_labels",
  "  redis:",
  "    image: alpine:3.24",
  "    command: [\"sh\", \"-c\", \"while :; do sleep 3600; done\"]",
  "    networks: [ci]",
  "    labels: *ci_labels",
  "  server:",
  "    image: alpine:3.24",
  "    command: " + JSON.stringify(serverCommand),
  "    environment:",
  "      POSTGRES_USER: " + runtimeUser,
  "      POSTGRES_PASSWORD: controller-test-password",
  "      POSTGRES_DB: densemem",
  "    depends_on:",
  "      postgres-init:",
  "        condition: service_completed_successfully",
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

adverse_index=6
for adverse_mode in bootstrap-role owner-role missing-state missing-migration missing-rls; do
  run_adverse="${fixture_prefix}${adverse_index}"
  project_adverse="densemem-ci-${run_adverse}-${fixture_attempt}-shared-shared"
  write_compose "$adverse_mode"
  expect_failure "$CONTROLLER" start "$run_adverse" "$fixture_attempt" shared shared "${image}@${digest}" "$ROOT_DIR"
  assert_no_project_resources "$project_adverse"
  project_adverse=""
  adverse_index=$((adverse_index + 1))
done
write_compose normal

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
network_blocker="dense-mem-controller-network-blocker-${fixture_prefix}-${fixture_attempt}-$$"
docker run -d --name "$network_blocker" --network "$stale_network" \
  alpine:3.24 sh -c 'while :; do sleep 3600; done' >/dev/null
(
  sleep 3
  docker rm -f "$network_blocker" >/dev/null
) &
network_release_pid=$!
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
protected_run="${fixture_prefix}5"
protected_project="densemem-ci-${protected_run}-${fixture_attempt}-shared-protected"
protected_container="dense-mem-controller-protected-${fixture_prefix}-${fixture_attempt}-$$"
docker run -d --name "$protected_container" \
  --label io.dense-mem.ci.contract=dense-mem-ci-e2e.v1 \
  --label io.dense-mem.ci.repository="${DENSE_MEM_CI_REPOSITORY}" \
  --label io.dense-mem.ci.run-id="$protected_run" \
  --label io.dense-mem.ci.run-attempt="$fixture_attempt" \
  --label io.dense-mem.ci.phase=shared \
  --label io.dense-mem.ci.scenario=protected \
  --label io.dense-mem.ci.image-digest="$digest" \
  --label io.dense-mem.ci.created-at=2000-01-01T00:00:00Z \
  --label io.dense-mem.ci.compose-project="$protected_project" \
  alpine:3.24 sh -c 'while :; do sleep 3600; done' >/dev/null
protected_phase_container="dense-mem-controller-protected-phase-${fixture_prefix}-${fixture_attempt}-$$"
docker run -d --name "$protected_phase_container" \
  --label io.dense-mem.ci.contract=dense-mem-ci-e2e.v1 \
  --label io.dense-mem.ci.repository="${DENSE_MEM_CI_REPOSITORY}" \
  --label io.dense-mem.ci.run-id="$run_stale" \
  --label io.dense-mem.ci.run-attempt="$fixture_attempt" \
  --label io.dense-mem.ci.phase=exclusive \
  --label io.dense-mem.ci.scenario=protected_phase \
  --label io.dense-mem.ci.image-digest="$digest" \
  --label io.dense-mem.ci.created-at=2000-01-01T00:00:00Z \
  --label io.dense-mem.ci.compose-project="densemem-ci-${run_stale}-${fixture_attempt}-exclusive-protected" \
  alpine:3.24 sh -c 'while :; do sleep 3600; done' >/dev/null
"$CONTROLLER" stale-cleanup 1 "$run_stale" "$fixture_attempt" shared >/dev/null
wait "$network_release_pid"
network_release_pid=""
network_blocker=""
if docker inspect "$stale_container" >/dev/null 2>&1; then
  fail "targeted stale cleanup did not reclaim the selected run"
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
if ! docker inspect "$protected_container" >/dev/null 2>&1; then
  fail "targeted stale cleanup reclaimed a different run"
fi
docker rm -f "$protected_container" >/dev/null
protected_container=""
if ! docker inspect "$protected_phase_container" >/dev/null 2>&1; then
  fail "targeted stale cleanup reclaimed a different phase"
fi
docker rm -f "$protected_phase_container" >/dev/null
protected_phase_container=""

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

for state_dir in "$JOB_DIR" "$CONFIG_DIR"; do
  [[ ! -e "${state_dir}/leases" && ! -e "${state_dir}/runs" ]] || fail "controller created persistent lease/run state in ${state_dir}"
done
printf '%s\n' 'real rootless Docker controller tests passed'
