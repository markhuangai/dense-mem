#!/usr/bin/env bash
# Sourced by e2e-host-controller.sh.

docker_cli_mount_args() {
  local docker_socket="${DENSE_MEM_CI_DOCKER_SOCKET:-}"
  if [[ -z "$docker_socket" ]]; then
    case "${DOCKER_HOST:-}" in
      unix://*) docker_socket="${DOCKER_HOST#unix://}" ;;
      "") docker_socket="/var/run/docker.sock" ;;
      *) fail "the CI Docker host must use a Unix socket" ;;
    esac
  fi
  [[ -S "$docker_socket" ]] || fail "the CI Docker socket is unavailable"
  local docker_bin
  docker_bin="$(readlink -f "$(command -v docker)")"
  [[ -f "$docker_bin" ]] || fail "the Docker CLI binary is unavailable"
  local compose_plugin=""
  for candidate in /usr/libexec/docker/cli-plugins/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose; do
    if [[ -f "$candidate" ]]; then
      compose_plugin="$candidate"
      break
    fi
  done
  [[ -n "$compose_plugin" ]] || fail "the Docker Compose CLI plugin is unavailable"
  DENSE_MEM_CI_DOCKER_REAL_SOCKET="$docker_socket"
  DENSE_MEM_CI_DOCKER_MOUNT_ARGS=(
    --volume "${docker_bin}:/usr/local/bin/docker:ro"
    --volume "$(dirname "$compose_plugin"):/usr/libexec/docker/cli-plugins:ro"
  )
}

run_scenario() {
  local manifest="$1" source_dir="$2" scenario="$3"
  local scenario_prev_traps
  scenario_prev_traps="$(trap -p EXIT INT TERM)"
  [[ -f "$manifest" ]] || fail "missing runtime manifest: $manifest"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "scenario source directory must be an absolute directory"
  validate_runtime_manifest "$manifest"
  validate_scenario "$scenario"
  validate_registered_scenario "$source_dir" "$scenario" "$(node -e 'const m=require("node:fs").readFileSync(process.argv[1], "utf8"); process.stdout.write(JSON.parse(m).phase);' "$manifest")"
  local values
  values="$(node - "$manifest" <<'NODE'
const fs = require("node:fs");
const manifest = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
if (manifest.contract_version !== "dense-mem-ci-e2e.v1" || manifest.runtime !== "production") process.exit(1);
for (const key of ["image", "compose_project", "network", "urls", "run_id", "run_attempt", "phase", "scenario", "source_revision", "image_digest", "helper_profiles", "client_env_volume"]) {
  if (manifest[key] === undefined) process.exit(1);
}
process.stdout.write([
  manifest.image,
  manifest.compose_project,
  manifest.network,
  manifest.urls.user,
  manifest.urls.control,
  manifest.urls.prometheus,
  manifest.run_id,
  manifest.run_attempt,
  manifest.phase,
  manifest.scenario,
  manifest.source_revision,
  manifest.image_digest,
  manifest.client_env_volume,
  (manifest.helper_profiles || []).join(","),
].join("\t"));
NODE
)" || fail "invalid runtime manifest"
  local image_ref project network user_url control_url prometheus_url run_id attempt manifest_phase manifest_scenario source_revision digest client_volume helpers
  IFS=$'\t' read -r image_ref project network user_url control_url prometheus_url run_id attempt manifest_phase manifest_scenario source_revision digest client_volume helpers <<<"$values"
  image_ref="$(canonical_image_ref "$image_ref")"
  validate_project "$project"
  validate_phase "$manifest_phase"
  validate_scenario "$manifest_scenario"
  if [[ "$manifest_phase" == "shared" ]]; then
    [[ "$manifest_scenario" == "shared" ]] || fail "runtime manifest scenario does not match the requested scenario"
  else
    [[ "$manifest_scenario" == "$scenario" ]] || fail "runtime manifest scenario does not match the requested scenario"
  fi
  validate_digest "$digest"
  [[ "$network" == "${project}_ci" ]] || fail "runtime manifest network is not owned by its project"
  [[ "$client_volume" == "${project}_client-env" ]] || fail "runtime manifest client volume is not run-scoped"
  [[ "$(git -C "$source_dir" rev-parse HEAD 2>/dev/null || true)" == "$source_revision" ]] || fail "scenario checkout does not match the runtime manifest revision"

  local run_root="${RUN_DIR}/${run_id}-${attempt}"
  local runtime_compose_host="${run_root}/${manifest_phase}-${manifest_scenario}.runtime-compose.yml"
  local helper_dir="${run_root}/${manifest_phase}-${manifest_scenario}-helpers"
  local helper_overlay=""
  if [[ -f "${helper_dir}/compose.yml" ]]; then
    helper_overlay="${helper_dir}/compose.yml"
  fi
  [[ -f "$runtime_compose_host" ]] || fail "missing non-secret runtime Compose view"
  if [[ -n "$helpers" ]]; then
    [[ -f "${helper_dir}/compose.yml" ]] || fail "missing helper Compose view"
  fi

  local test_image control_token telemetry_token embedding_model embedding_dimensions postgres_user postgres_password postgres_db
  test_image="$(env_value DENSE_MEM_CI_TEST_IMAGE 2>/dev/null || printf '%s' node:24-bookworm)"
  control_token="$(env_value CONTROL_PORTAL_TOKEN 2>/dev/null || true)"
  telemetry_token="$(env_value TELEMETRY_SCRAPE_TOKEN 2>/dev/null || true)"
  embedding_model="$(env_value AI_API_EMBEDDING_MODEL 2>/dev/null || true)"
  embedding_dimensions="$(env_value AI_API_EMBEDDING_DIMENSIONS 2>/dev/null || true)"
  postgres_user="$(env_value POSTGRES_USER 2>/dev/null || true)"
  postgres_password="$(env_value POSTGRES_PASSWORD 2>/dev/null || true)"
  postgres_db="$(env_value POSTGRES_DB 2>/dev/null || true)"
  [[ "$test_image" != *$'\n'* && "$test_image" != *$'\r'* ]] || fail "invalid scenario test image"
  [[ -n "$control_token" ]] || fail "control portal token is missing"
  local identity_upgrade_team_id="" identity_upgrade_profile_id="" identity_upgrade_api_key=""
  if [[ "$scenario" == "identity_cleanup" ]]; then
    local identity_file="${helper_dir}/identity.env"
    [[ -f "$identity_file" ]] || fail "identity cleanup seed handoff is missing"
    identity_upgrade_team_id="$(helper_env_value "$identity_file" DENSE_MEM_E2E_UPGRADE_TEAM_ID)" || fail "identity cleanup seed team is missing"
    identity_upgrade_profile_id="$(helper_env_value "$identity_file" DENSE_MEM_E2E_UPGRADE_PROFILE_ID)" || fail "identity cleanup seed profile is missing"
    identity_upgrade_api_key="$(helper_env_value "$identity_file" DENSE_MEM_E2E_UPGRADE_API_KEY)" || fail "identity cleanup seed credential is missing"
  fi
  mkdir -p "${run_root}/results-${scenario}"
  chmod 700 "${run_root}/results-${scenario}"
  docker_cli_mount_args
  if [[ "$scenario" == "synchronous_write_primitives" ]]; then
    run_synchronous_primitives_driver "$source_dir" "$project" "$postgres_user" "$postgres_password" "$postgres_db" "$run_id" "$attempt" "$manifest_phase" "$scenario" "$digest"
  fi
  if [[ "$scenario" == "mcp_sdk_parity" ]]; then
    run_mcp_sdk_parity_driver "$source_dir" "$project" "$run_id" "$attempt" "$manifest_phase" "$scenario" "$digest"
  fi
  local team_name="Dense-Mem CI ${run_id}-${attempt}-${scenario}"
  local team_payload team_response team_id credential_payload credential_response api_key credential_id
  team_payload="$(node -e 'process.stdout.write(JSON.stringify({name: process.argv[1], description: "production-image E2E scenario"}))' "$team_name")"
  team_response="$(control_api_request "$project" "$manifest_phase" "$manifest_scenario" "$digest" "$run_id" "$attempt" "$image_ref" "$helper_overlay" "$control_token" "${control_url%/}/control/api/teams" "$team_payload")" || fail "control portal did not return a team"
  team_id="$(printf '%s' "$team_response" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input).data?.id;if(!value)process.exit(1);process.stdout.write(value);});')" || fail "control portal did not return a team"
  credential_payload='{"name":"production-image-e2e","role":"manager","scopes":["read","write"],"rate_limit":300}'
  credential_response="$(control_api_request "$project" "$manifest_phase" "$manifest_scenario" "$digest" "$run_id" "$attempt" "$image_ref" "$helper_overlay" "$control_token" "${control_url%/}/control/api/teams/${team_id}/credentials" "$credential_payload")" || fail "control portal did not return a credential"
  api_key="$(printf '%s' "$credential_response" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input).data?.api_key;if(!value)process.exit(1);process.stdout.write(value);});')" || fail "control portal did not return a credential"
  credential_id="$(printf '%s' "$credential_response" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input).data?.credential?.id;if(!value)process.exit(1);process.stdout.write(value);});')" || fail "control portal did not return a credential ID"

  E2E_SCENARIO_PROXY_SOCKET="${run_root}/${manifest_phase}-${manifest_scenario}-${scenario}.docker.sock"
  E2E_SCENARIO_PROXY_LOG="${run_root}/${manifest_phase}-${manifest_scenario}-${scenario}.docker-proxy.log"
  [[ -r "$PROXY_SCRIPT" ]] || fail "missing trusted Docker proxy: $PROXY_SCRIPT"
  node "$PROXY_SCRIPT" \
    --listen "$E2E_SCENARIO_PROXY_SOCKET" \
    --target "$DENSE_MEM_CI_DOCKER_REAL_SOCKET" \
    --project "$project" \
    --contract "$CONTRACT_VERSION" \
    --repository "$REPOSITORY" \
    --run-id "$run_id" \
    --attempt "$attempt" \
    --phase "$manifest_phase" \
    --scenario "$scenario" \
    --image-digest "$digest" >"$E2E_SCENARIO_PROXY_LOG" 2>&1 &
  E2E_SCENARIO_PROXY_PID=$!
  trap cleanup_scenario_proxy_on_exit EXIT INT TERM
  for _ in $(seq 1 100); do
    if [[ -S "$E2E_SCENARIO_PROXY_SOCKET" ]]; then break; fi
    sleep 0.1
  done
  if [[ ! -S "$E2E_SCENARIO_PROXY_SOCKET" ]]; then
    fail "the restricted Docker proxy did not start"
  fi
  local proxy_socket_gid
  proxy_socket_gid="$(stat -c '%g' "$E2E_SCENARIO_PROXY_SOCKET" 2>/dev/null || stat -f '%g' "$E2E_SCENARIO_PROXY_SOCKET")"

  local helper_runtime="${helper_dir}/runtime.env"
  local oauth_token="" oauth_harness=""
  local oauth_session_token="oauth-session-${run_id}-${scenario}"
  local oauth_csrf_token="oauth-csrf-${run_id}-${scenario}"
  if [[ -f "$helper_runtime" ]]; then
    oauth_token="$(helper_env_value "$helper_runtime" DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN 2>/dev/null || true)"
    oauth_harness="$(helper_env_value "$helper_runtime" DENSE_MEM_E2E_OAUTH_HARNESS_URL 2>/dev/null || true)"
  fi
  local result_file="/results/${scenario}-result.json"
  local conflict_live=0
  [[ "$scenario" == "conflict_queue" ]] && conflict_live=1
  local container_name="${project}-test-${scenario//_/-}"
  local -a docker_args=(
    run --rm --name "$container_name"
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}"
    --label "io.dense-mem.ci.repository=${REPOSITORY}"
    --label "io.dense-mem.ci.run-id=${run_id}"
    --label "io.dense-mem.ci.run-attempt=${attempt}"
    --label "io.dense-mem.ci.phase=${manifest_phase}"
    --label "io.dense-mem.ci.scenario=${scenario}"
    --label "io.dense-mem.ci.image-digest=${digest}"
    --label "io.dense-mem.ci.created-at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    --label "com.docker.compose.project=${project}"
    --network "$network"
    --user "$(id -u):$(id -g)"
    --group-add "$proxy_socket_gid"
    "${DENSE_MEM_CI_DOCKER_MOUNT_ARGS[@]}"
    --volume "${E2E_SCENARIO_PROXY_SOCKET}:/var/run/docker.sock"
    --volume "${source_dir}:/workspace:ro"
    --volume "${runtime_compose_host}:/ci/runtime-compose.yml:ro"
    --volume "${client_volume}:/client-env:ro"
    --volume "${run_root}/results-${scenario}:/results"
    --workdir /workspace
    -e "DOCKER_HOST=unix:///var/run/docker.sock"
    -e "HOME=/tmp/dense-mem-home"
    -e "DENSE_MEM_USER_URL=${user_url}"
    -e "DENSE_MEM_CONTROL_URL=${control_url}"
    -e "DENSE_MEM_PROMETHEUS_URL=${prometheus_url}"
    -e "DENSE_MEM_E2E_PROMETHEUS_URL=${prometheus_url}"
    -e "DENSE_MEM_E2E_NETWORK=${network}"
    -e "DENSE_MEM_E2E_CLIENT_ENV=/client-env/runtime.env"
    -e "DENSE_MEM_E2E_COMPOSE_PROJECT=${project}"
    -e "DENSE_MEM_E2E_COMPOSE_FILE=/ci/runtime-compose.yml"
    -e "DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=/ci/helper-compose.yml"
    -e "DENSE_MEM_E2E_SCENARIO=${scenario}"
    -e "DENSE_MEM_E2E_RUNTIME=production"
    -e "DENSE_MEM_E2E_SOURCE_REVISION=${source_revision}"
    -e "DENSE_MEM_E2E_COMMIT_SHA=${source_revision}"
    -e "DENSE_MEM_E2E_RUN_ID=${run_id}"
    -e "DENSE_MEM_E2E_RUN_ATTEMPT=${attempt}"
    -e "DENSE_MEM_E2E_RUN_PLAYWRIGHT=${DENSE_MEM_CI_RUN_PLAYWRIGHT:-0}"
    -e "DENSE_MEM_E2E_SDK_PARITY_GO_TESTED=$(if [[ "$scenario" == "mcp_sdk_parity" ]]; then printf 1; else printf 0; fi)"
    -e "DENSE_MEM_E2E_TEAM_ID=${team_id}"
    -e "DENSE_MEM_E2E_TEAM_NAME=${team_name}"
    -e "DENSE_MEM_E2E_API_KEY=${api_key}"
    -e "DENSE_MEM_E2E_CREDENTIAL_ID=${credential_id}"
    -e "DENSE_MEM_E2E_UPGRADE_TEAM_ID=${identity_upgrade_team_id}"
    -e "DENSE_MEM_E2E_UPGRADE_PROFILE_ID=${identity_upgrade_profile_id}"
    -e "DENSE_MEM_E2E_UPGRADE_API_KEY=${identity_upgrade_api_key}"
    -e "DENSE_MEM_E2E_TELEMETRY_TOKEN=${telemetry_token}"
    -e "DENSE_MEM_E2E_POSTGRES_HOST=postgres"
    -e "DENSE_MEM_E2E_POSTGRES_PORT=5432"
    -e "DENSE_MEM_E2E_POSTGRES_USER=${postgres_user}"
    -e "DENSE_MEM_E2E_POSTGRES_PASSWORD=${postgres_password}"
    -e "DENSE_MEM_E2E_POSTGRES_DB=${postgres_db}"
    -e "DENSE_MEM_E2E_CONFLICT_PROVIDER_URL=http://conflict-provider:8081/v1"
    -e "DENSE_MEM_E2E_CONFLICT_REVIEW_DRIVER=/helpers/conflict-review-driver"
    -e "DENSE_MEM_E2E_SERVER_EMBEDDING_MODEL=${embedding_model}"
    -e "DENSE_MEM_E2E_SERVER_EMBEDDING_DIMENSIONS=${embedding_dimensions}"
    -e "DENSE_MEM_E2E_RESULT_FILE=${result_file}"
    -e "DENSE_MEM_E2E_SSO_SESSION_TOKEN=${oauth_session_token}"
    -e "DENSE_MEM_E2E_SSO_CSRF_TOKEN=${oauth_csrf_token}"
    -e "DENSE_MEM_E2E_MEMORY_SCENARIO=${scenario}"
    -e "DENSE_MEM_E2E_CONFLICT_REVIEW_LIVE=${conflict_live}"
    -e "DENSE_MEM_CONTROL_TOKEN=${control_token}"
  )
  if [[ -f "$helper_overlay" ]]; then
    docker_args+=(--volume "${helper_overlay}:/ci/helper-compose.yml:ro")
  fi
  if [[ -f "${helper_dir}/conflict-review-driver" ]]; then
    docker_args+=(--volume "${helper_dir}/conflict-review-driver:/helpers/conflict-review-driver:ro")
  fi
  if [[ -f "${helper_dir}/ca.pem" ]]; then
    docker_args+=(--volume "${helper_dir}/ca.pem:/oauth/ca.pem:ro" -e "NODE_EXTRA_CA_CERTS=/oauth/ca.pem")
  fi
  if [[ -n "$oauth_token" ]]; then
    docker_args+=(
      -e "DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN=${oauth_token}"
      -e "DENSE_MEM_E2E_OAUTH_INTERNAL_URL=https://oauth-provider-mock:9444"
      -e "DENSE_MEM_E2E_OAUTH_MOCK_URL=https://oauth-provider-mock:9444"
      -e "DENSE_MEM_E2E_OAUTH_ISSUER_BASE=https://oauth-provider-mock:9444"
    )
    if [[ -n "$oauth_harness" ]]; then
      docker_args+=(
        -e "DENSE_MEM_E2E_OAUTH_HARNESS_URL=${oauth_harness}"
        -e "DENSE_MEM_E2E_OAUTH_COMPOSE_FILE=/ci/helper-compose.yml"
      )
    fi
  fi
  docker_args+=("$test_image" bash /workspace/scripts/e2e-ci-scenario.sh "$scenario")
  set +e
  docker "${docker_args[@]}" 2>&1 |
    redact_diagnostics "$ENV_FILE" "$control_token" "$telemetry_token" "$postgres_password" "$api_key" "$identity_upgrade_api_key" "$oauth_token"
  local -a scenario_pipeline_status=("${PIPESTATUS[@]}")
  set -e
  local scenario_status="${scenario_pipeline_status[0]}"
  if ((scenario_pipeline_status[1] != 0)); then
    fail "diagnostic redaction failed"
  fi
  stop_scenario_proxy
  trap - EXIT INT TERM
  if [[ -n "$scenario_prev_traps" ]]; then
    eval "$scenario_prev_traps"
  fi
  return "$scenario_status"
}

control_api_request() {
  local project="$1" phase="$2" scenario="$3" digest="$4" run_id="$5" attempt="$6" image_ref="$7" overlay="$8" token="$9" url="${10}" payload="${11}"
  (
    compose_base_env "$project" "$phase" "$scenario" "$digest" "$run_id" "$attempt" "1970-01-01T00:00:00Z" "${image_ref}@${digest}"
    DENSE_MEM_CI_COMPOSE_OVERLAY_FILE="$overlay"
    ci_compose exec -T -e "DENSE_MEM_CI_CONTROL_TOKEN=${token}" client-env \
      sh -ec 'wget -q -O - --header="Authorization: Bearer ${DENSE_MEM_CI_CONTROL_TOKEN}" --header="Content-Type: application/json" --post-data="$1" "$2"' \
      sh "$payload" "$url"
  )
}

stop_stack() {
  local project="$1"
  validate_project "$project"
  validate_bundle
  compose_base_env "$project"
  local cleanup_failed=0
  while IFS= read -r container; do
    [[ -n "$container" ]] || continue
    docker stop --time 30 "$container" >/dev/null 2>&1 || true
    if ! docker rm "$container" >/dev/null 2>&1; then
      docker inspect "$container" >/dev/null 2>&1 && cleanup_failed=1
    fi
  done < <(
    {
      docker ps -aq \
        --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
        --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" \
        --filter "label=com.docker.compose.project=${project}"
      docker ps -aq \
        --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
        --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" \
        --filter "label=io.dense-mem.ci.compose-project=${project}"
    } | sort -u
  )
  if ! ci_compose down --volumes --remove-orphans >/dev/null 2>&1; then
    cleanup_failed=1
  fi
  while IFS= read -r network; do
    [[ -n "$network" ]] || continue
    if ! docker network rm "$network" >/dev/null 2>&1; then
      docker network inspect "$network" >/dev/null 2>&1 && cleanup_failed=1
    fi
  done < <(
    {
      docker network ls -q \
        --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
        --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" \
        --filter "label=com.docker.compose.project=${project}"
      docker network ls -q \
        --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
        --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" \
        --filter "label=io.dense-mem.ci.compose-project=${project}"
    } | sort -u
  )
  if docker image inspect "${project}-oauth-compat-harness:latest" >/dev/null 2>&1; then
    if ! docker image rm "${project}-oauth-compat-harness:latest" >/dev/null 2>&1; then
      cleanup_failed=1
    fi
  fi
  if [[ -d "$RUN_DIR" ]]; then
    while IFS= read -r -d '' manifest; do
      if node - "$manifest" "$project" <<'NODE'
const fs = require("node:fs");
const [path, project] = process.argv.slice(2);
try {
  process.exit(JSON.parse(fs.readFileSync(path, "utf8")).compose_project === project ? 0 : 1);
} catch {
  process.exit(1);
}
NODE
      then
        manifest_dir="$(dirname "$manifest")"
        manifest_scenario="$(node -e 'const value=JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8")); process.stdout.write(value.scenario || "");' "$manifest")"
        rm -- "$manifest"
        for generated in \
          "${manifest_dir}/$(basename "${manifest%.json}").runtime-compose.yml" \
          "${manifest_dir}/$(basename "${manifest%.json}")-helpers" \
          "${manifest_dir}/$(basename "${manifest%.json}")-private"; do
          if [[ -e "$generated" ]]; then rm -r -- "$generated"; fi
        done
        if [[ "$manifest_scenario" == "shared" ]]; then
          while IFS= read -r -d '' result_dir; do
            rm -r -- "$result_dir"
          done < <(find "$manifest_dir" -maxdepth 1 -type d -name 'results-*' -print0)
        elif [[ -n "$manifest_scenario" && -d "${manifest_dir}/results-${manifest_scenario}" ]]; then
          rm -r -- "${manifest_dir}/results-${manifest_scenario}"
        fi
      fi
    done < <(find "$RUN_DIR" -type f -name '*.json' -print0)
  fi
  if ((cleanup_failed)); then
    printf 'dense-mem CI controller: failed to remove every resource for project %s\n' "$project" >&2
    return 1
  fi
}

release() {
  local lease="$1"
  mkdir -p "$LEASE_DIR"
  umask 077
  exec 9>"${LEASE_DIR}/.lock"
  flock -x 9
  local lease_dir_real lease_parent lease_real
  lease_dir_real="$(cd "$LEASE_DIR" && pwd)"
  lease_parent="$(cd "$(dirname "$lease")" 2>/dev/null && pwd)" || fail "invalid lease path"
  lease_real="${lease_parent}/$(basename "$lease")"
  [[ "$lease_real" == "$lease_dir_real"/* ]] || fail "refusing a lease outside the CI lease directory"
  if [[ ! -f "$lease_real" ]]; then
    flock -u 9
    exec 9>&-
    return 0
  fi
  local contract image digest
  contract="$(sed -n 's/^contract=//p' "$lease_real")"
  image="$(sed -n 's/^image=//p' "$lease_real")"
  digest="$(sed -n 's/^digest=//p' "$lease_real")"
  [[ "$contract" == "$CONTRACT_VERSION" ]] || fail "lease contract mismatch"
  validate_digest "$digest"
  local other_lease=0
  if find "$LEASE_DIR" -maxdepth 1 -type f -name "*${digest#sha256:}.*.lease" ! -samefile "$lease_real" -print -quit | grep -q .; then
    other_lease=1
  fi
  if ((other_lease == 0)); then
    if ! remove_candidate_image "$image" "$digest"; then
      flock -u 9
      exec 9>&-
      fail "failed to remove candidate image ${image}@${digest}; retaining lease for retry"
    fi
  fi
  rm -- "$lease_real"
  flock -u 9
  exec 9>&-
}

remove_candidate_image() {
  local image="$1" digest="$2"
  local image_id image_inspect_error
  if image_id="$(docker image inspect "${image}@${digest}" --format '{{.Id}}' 2>&1)"; then
    :
  else
    image_inspect_error="$image_id"
    case "$image_inspect_error" in
      *"No such image"*|*"No such object"*) image_id="" ;;
      *) return 1 ;;
    esac
  fi
  if ! docker image rm "${image}@${digest}" >/dev/null 2>&1; then
    if image_inspect_error="$(docker image inspect "${image}@${digest}" 2>&1 >/dev/null)"; then
      return 1
    fi
    case "$image_inspect_error" in
      *"No such image"*|*"No such object"*) ;;
      *) return 1 ;;
    esac
  fi
  if [[ -n "$image_id" ]]; then
    local tag current_id
    while IFS= read -r tag; do
      [[ -n "$tag" && ( "$tag" == "$image" || "$tag" == "$image":* ) ]] || continue
      current_id="$(docker image inspect "$tag" --format '{{.Id}}' 2>/dev/null || true)"
      [[ "$current_id" == "$image_id" ]] || continue
      docker image rm "$tag" >/dev/null 2>&1 || return 1
    done < <(docker image inspect "$image_id" --format '{{range .RepoTags}}{{println .}}{{end}}' 2>/dev/null || true)
  fi
  if docker image inspect "${image}@${digest}" >/dev/null 2>&1; then
    return 1
  fi
  image_inspect_error="$(docker image inspect "${image}@${digest}" 2>&1 >/dev/null || true)"
  case "$image_inspect_error" in
    *"No such image"*|*"No such object"*) return 0 ;;
    *) return 1 ;;
  esac
}

stale_cleanup() {
  local max_age="${1:-86400}"
  validate_decimal "$max_age"
  validate_bundle
  mkdir -p "$LEASE_DIR" "$RUN_DIR"
  local now cutoff path created_epoch
  now="$(date +%s)"
  cutoff=$((now - max_age))

  declare -A stale_projects=()
  local resource resource_metadata created_at project
  while IFS= read -r resource; do
    [[ -n "$resource" ]] || continue
    resource_metadata="$(docker inspect --format '{{json .}}' "$resource" 2>/dev/null | node -e '
let input = "";
const [contract, repository] = process.argv.slice(1);
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  try {
    const value = JSON.parse(input);
    const labels = value.Config?.Labels || value.Labels || {};
    const project = labels["com.docker.compose.project"] || labels["io.dense-mem.ci.compose-project"] || "";
    const managed = labels["io.dense-mem.ci.contract"] === contract &&
      labels["io.dense-mem.ci.repository"] === repository &&
      /^[1-9][0-9]*$/.test(labels["io.dense-mem.ci.run-id"] || "") &&
      /^[1-9][0-9]*$/.test(labels["io.dense-mem.ci.run-attempt"] || "") &&
      /^(precheck|exclusive|shared)$/.test(labels["io.dense-mem.ci.phase"] || "") &&
      /^[a-z0-9_]+$/.test(labels["io.dense-mem.ci.scenario"] || "") &&
      /^sha256:[0-9a-f]{64}$/.test(labels["io.dense-mem.ci.image-digest"] || "") &&
      typeof labels["io.dense-mem.ci.created-at"] === "string" &&
      labels["io.dense-mem.ci.created-at"].length > 0 &&
      /^densemem-ci-[a-z0-9][a-z0-9-]{0,50}$/.test(project);
    process.stdout.write(`${labels["io.dense-mem.ci.created-at"] || ""}\t${project}\t${managed ? "1" : "0"}`);
  } catch {}
});
' "$CONTRACT_VERSION" "$REPOSITORY" || true)"
    IFS=$'\t' read -r created_at project managed <<<"$resource_metadata"
    [[ "$managed" == "1" ]] || continue
    [[ "$project" =~ ^densemem-ci-[a-z0-9][a-z0-9-]{0,50}$ ]] || continue
    created_epoch="$(date -d "$created_at" +%s 2>/dev/null || true)"
    if [[ "$created_epoch" =~ ^[0-9]+$ ]] && ((created_epoch < cutoff)); then
      stale_projects["$project"]=1
    fi
  done < <(
    {
      docker ps -aq --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}";
      docker network ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}";
      docker volume ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}";
      docker image ls -q --no-trunc --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}";
    } | sort -u
  )
  local stale_failed=0
  for project in "${!stale_projects[@]}"; do
    stop_stack "$project" || stale_failed=1
  done

  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    created_epoch="$(stat -c '%Y' "$path" 2>/dev/null || stat -f '%m' "$path")"
    if ((created_epoch < cutoff)); then
      release "$path"
    fi
  done < <(find "$LEASE_DIR" -maxdepth 1 -type f -name '*.lease' -print)
  if [[ -d "$RUN_DIR" ]]; then
    while IFS= read -r -d '' path; do
      local run_name
      run_name="$(basename "$path")"
      [[ "$run_name" =~ ^[1-9][0-9]*-[1-9][0-9]*$ ]] || continue
      created_epoch="$(stat -c '%Y' "$path" 2>/dev/null || stat -f '%m' "$path")"
      if ((created_epoch < cutoff)); then
        rm -r -- "$path"
      fi
    done < <(find "$RUN_DIR" -mindepth 1 -maxdepth 1 -type d -print0)
  fi
  if ((stale_failed)); then
    printf '%s\n' "stale lease cleanup completed with resource failures" >&2
    return 1
  fi
  printf '%s\n' "stale lease cleanup complete"
}

cleanup_run() {
  local run_id="$1" attempt="$2"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  mkdir -p "$RUN_DIR"
  local run_dir_real run_root
  run_dir_real="$(cd "$RUN_DIR" && pwd)"
  run_root="${run_dir_real}/${run_id}-${attempt}"
  [[ "$run_root" == "${run_dir_real}/${run_id}-${attempt}" ]] || fail "invalid run directory"
  if [[ -d "$run_root" ]]; then
    rm -r -- "$run_root"
  fi
  printf '%s\n' "run directory cleanup complete"
}

local_all() {
  local run_id="$1" attempt="$2" image_ref="$3" digest="$4" source_revision="$5" source_dir="$6"
  if (( BASH_VERSINFO[0] < 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] < 1) )); then
    fail "local-all requires Bash 5.1 or newer"
  fi
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_digest "$digest"
  local requested_image_ref="$image_ref"
  image_ref="$(canonical_image_ref "$requested_image_ref")"
  if [[ "$requested_image_ref" == *@* && "${requested_image_ref##*@}" != "$digest" ]]; then
    fail "image digest does not match the requested digest"
  fi
  validate_revision "$source_revision"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "local matrix source directory must be absolute"
  [[ "$(git -C "$source_dir" rev-parse HEAD 2>/dev/null || true)" == "$source_revision" ]] || fail "local matrix source does not match the requested revision"
  validate_bundle
  doctor >/dev/null

  local registry_script="$REGISTRY_SCRIPT"
  [[ -r "$registry_script" ]] || fail "trusted scenario registry is unavailable"
  [[ -f "${source_dir}/scripts/e2e-scenario-registry.mjs" ]] || fail "scenario registry is unavailable in the tested source"
  [[ -f "${source_dir}/scripts/e2e-scenarios.json" ]] || fail "scenario registry data is unavailable in the tested source"
  require_command setsid
  local controller_path
  controller_path="$(readlink -f "$0")"
  [[ -x "$controller_path" ]] || fail "local matrix controller path is unavailable"
  local run_root="${RUN_DIR}/${run_id}-${attempt}"
  local log_dir="${RUN_DIR}/local-matrix-${run_id}-${attempt}"
  mkdir -p "$log_dir"
  chmod 700 "$log_dir"
  local lease=""
  local active_project=""
  local cleanup_status
  local -a active_pids=()
  cleanup_local_matrix() {
    cleanup_status=$?
    trap - EXIT INT TERM
    set +e
    terminate_e2e_process_groups "${active_pids[@]}"
    if [[ -n "$active_project" ]]; then stop_stack "$active_project" || true; fi
    if [[ -n "$lease" ]]; then release "$lease" || cleanup_status=1; fi
    if [[ -d "$run_root" ]]; then rm -r -- "$run_root"; fi
    exit "$cleanup_status"
  }
  trap cleanup_local_matrix EXIT INT TERM
  lease="$(acquire "$run_id" "$attempt" "$image_ref" "$digest")"

  local matrix_status=0
  local scenario helpers playwright manifest project scenario_log
  while IFS=$'\t' read -r scenario playwright; do
    [[ -n "$scenario" ]] || continue
    [[ "$playwright" == "0" || "$playwright" == "1" ]] || fail "invalid Playwright flag for local scenario: ${scenario}"
    helpers="$(DENSE_MEM_E2E_SCENARIO_REGISTRY="${source_dir}/scripts/e2e-scenarios.json" node "$registry_script" --scenario "$scenario" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input);if(value.audited!==true)process.exit(1);process.stdout.write((value.helper_profiles||[]).join(","));});')" || fail "unregistered scenario cannot enter the local matrix: ${scenario}"
    project="$(managed_project_name "$run_id" "$attempt" exclusive "$scenario")"
    active_project="$project"
    scenario_log="${log_dir}/exclusive-${scenario}.log"
    printf 'exclusive %s\n' "$scenario"
    manifest="$(start_stack "$run_id" "$attempt" exclusive "$scenario" "$image_ref" "$digest" "$source_revision" "$helpers" "$source_dir")"
    printf '%s\n' "$manifest" > "${run_root}/exclusive-${scenario}.manifest.json"
    export DENSE_MEM_CI_RUN_PLAYWRIGHT="$playwright"
    if run_scenario "${run_root}/exclusive-${scenario}.json" "$source_dir" "$scenario" >"$scenario_log" 2>&1; then
      :
    else
      matrix_status=$?
      stop_stack "$project" || true
      printf 'exclusive %s failed; shared matrix was not started\n' "$scenario" >&2
      exit "$matrix_status"
    fi
    unset DENSE_MEM_CI_RUN_PLAYWRIGHT
    stop_stack "$project"
    active_project=""
  done < <(DENSE_MEM_E2E_SCENARIO_REGISTRY="${source_dir}/scripts/e2e-scenarios.json" node "$registry_script" --matrix exclusive | node -e 'const fs=require("node:fs");const value=JSON.parse(fs.readFileSync(0,"utf8"));for(const row of value.include||[])process.stdout.write(`${row.name}\t${row.playwright ? 1 : 0}\n`);')

  local shared_helpers
  shared_helpers="$(DENSE_MEM_E2E_SCENARIO_REGISTRY="${source_dir}/scripts/e2e-scenarios.json" node "$registry_script" --helpers shared_team | node -e 'const fs=require("node:fs");process.stdout.write(JSON.parse(fs.readFileSync(0,"utf8")).join(","));')"
  local shared_scenario="shared"
  local shared_project
  shared_project="$(managed_project_name "$run_id" "$attempt" shared shared)"
  active_project="$shared_project"
  printf 'shared matrix (max-parallel=4)\n'
  manifest="$(start_stack "$run_id" "$attempt" shared "$shared_scenario" "$image_ref" "$digest" "$source_revision" "$shared_helpers" "$source_dir")"
  printf '%s\n' "$manifest" > "${run_root}/shared-shared.manifest.json"

  local -a shared_names=()
  local -A shared_playwright=()
  while IFS=$'\t' read -r scenario playwright; do
    [[ -n "$scenario" ]] || continue
    [[ "$playwright" == "0" || "$playwright" == "1" ]] || fail "invalid Playwright flag for shared scenario: ${scenario}"
    shared_names+=("$scenario")
    shared_playwright["$scenario"]="$playwright"
  done < <(DENSE_MEM_E2E_SCENARIO_REGISTRY="${source_dir}/scripts/e2e-scenarios.json" node "$registry_script" --matrix shared_team | node -e 'const fs=require("node:fs");const value=JSON.parse(fs.readFileSync(0,"utf8"));for(const row of value.include||[])process.stdout.write(`${row.name}\t${row.playwright ? 1 : 0}\n`);')
  local next_index=0
  local finished_pid=""
  local finished_status=0
  local pid
  local -A pid_scenario=()
  while ((next_index < ${#shared_names[@]} || ${#active_pids[@]} > 0)); do
    while ((next_index < ${#shared_names[@]} && ${#active_pids[@]} < 4)); do
      scenario="${shared_names[$next_index]}"
      scenario_log="${log_dir}/shared-${scenario}.log"
      DENSE_MEM_CI_RUN_PLAYWRIGHT="${shared_playwright[$scenario]}" \
        setsid --wait "$controller_path" run "${run_root}/shared-shared.json" "$source_dir" "$scenario" >"$scenario_log" 2>&1 &
      pid=$!
      active_pids+=("$pid")
      pid_scenario["$pid"]="$scenario"
      next_index=$((next_index + 1))
    done
    if wait -n -p finished_pid "${active_pids[@]}"; then finished_status=0; else finished_status=$?; fi
    local -a remaining_pids=()
    for pid in "${active_pids[@]}"; do
      if [[ "$pid" != "$finished_pid" ]]; then
        remaining_pids+=("$pid")
      fi
    done
    active_pids=("${remaining_pids[@]}")
    if ((finished_status != 0)); then
      printf 'shared scenario %s failed; stopping remaining rows\n' "${pid_scenario[$finished_pid]-unknown}" >&2
      terminate_e2e_process_groups "${active_pids[@]}"
      if stop_stack "$shared_project"; then active_project=""; fi
      exit "$finished_status"
    fi
  done
  stop_stack "$shared_project"
  active_project=""
  printf 'local production E2E matrix passed\n'
}
