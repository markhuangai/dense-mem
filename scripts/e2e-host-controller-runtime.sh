#!/usr/bin/env bash
# Sourced by e2e-host-controller.sh.

SCENARIO_TEST_IMAGE="mcr.microsoft.com/playwright:v1.62.1-noble"

docker_cli_paths() {
  local docker_bin
  docker_bin="$(readlink -f "$(command -v docker)")"
  [[ -f "$docker_bin" ]] || fail "the Docker CLI binary is unavailable"
  local compose_plugin=""
  for candidate in /usr/libexec/docker/cli-plugins/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose; do
    if [[ -f "$candidate" ]]; then
      compose_plugin="$(readlink -f "$candidate")"
      break
    fi
  done
  [[ -n "$compose_plugin" ]] || fail "the Docker Compose CLI plugin is unavailable"
  DENSE_MEM_CI_DOCKER_BIN="$docker_bin"
  DENSE_MEM_CI_COMPOSE_PLUGIN="$compose_plugin"
}

scenario_helpers() {
  local source_dir="$1" phase="$2" scenario="$3"
  if [[ "$phase" == "shared" ]]; then
    DENSE_MEM_E2E_SCENARIO_REGISTRY="$source_dir/scripts/e2e-scenarios.json" \
      node "$REGISTRY_SCRIPT" --helpers shared_team |
      node -e 'const fs=require("node:fs");process.stdout.write(JSON.parse(fs.readFileSync(0,"utf8")).join(","));'
  else
    DENSE_MEM_E2E_SCENARIO_REGISTRY="$source_dir/scripts/e2e-scenarios.json" \
      node "$REGISTRY_SCRIPT" --scenario "$scenario" |
      node -e 'const fs=require("node:fs");process.stdout.write((JSON.parse(fs.readFileSync(0,"utf8")).helper_profiles||[]).join(","));'
  fi
}

run_scenario() {
  local run_id="$1" attempt="$2" phase="$3" stack_scenario="$4" scenario="$5" image_ref="$6" source_dir="$7"
  validate_decimal "$run_id"
  validate_decimal "$attempt"
  validate_phase "$phase"
  validate_scenario "$scenario"
  validate_scenario "$stack_scenario"
  [[ "$phase" == "shared" && "$stack_scenario" == "shared" || "$phase" == "exclusive" && "$stack_scenario" == "$scenario" ]] ||
    fail "scenario stack identity is invalid"
  validate_image_ref "$image_ref"
  local digest="${image_ref##*@}"
  validate_digest "$digest"
  image_ref="${image_ref%@*}"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "scenario source directory must be an absolute directory"
  local tested_commit
  tested_commit="$(git -C "$source_dir" rev-parse HEAD 2>/dev/null || true)"
  validate_revision "$tested_commit"
  validate_registered_scenario "$source_dir" "$scenario" "$phase"

  local project
  project="$(managed_project_name "$run_id" "$attempt" "$phase" "$stack_scenario")"
  local helpers
  helpers="$(scenario_helpers "$source_dir" "$phase" "$scenario")" || fail "scenario helper profiles are unavailable"
  [[ "$helpers" =~ ^[a-z0-9_,]*$ ]] || fail "invalid helper profile list"
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  compose_base_env "$project" "$phase" "$stack_scenario" "$digest" "$run_id" "$attempt" "$created_at" "${image_ref}@${digest}"

  local run_root="${JOB_DIR}/${run_id}-${attempt}/${phase}-${stack_scenario}"
  local helper_dir="${JOB_DIR}/${run_id}-${attempt}/${phase}-${stack_scenario}-helpers"
  local helper_overlay=""
  if [[ -f "${helper_dir}/compose.yml" ]]; then
    helper_overlay="${helper_dir}/compose.yml"
  fi
  mkdir -p "${run_root}"
  chmod 700 "${run_root}"
  local runtime_compose_host="${run_root}/runtime-compose.yml"
  write_runtime_compose "$runtime_compose_host" "$project" "${image_ref}@${digest}"

  local test_image="$SCENARIO_TEST_IMAGE" control_token telemetry_token embedding_model embedding_dimensions postgres_user postgres_password postgres_db
  control_token="$(env_value CONTROL_PORTAL_TOKEN 2>/dev/null || true)"
  telemetry_token="$(env_value TELEMETRY_SCRAPE_TOKEN 2>/dev/null || cat "$TELEMETRY_TOKEN_FILE")"
  embedding_model="$(env_value AI_API_EMBEDDING_MODEL 2>/dev/null || true)"
  embedding_dimensions="$(env_value AI_API_EMBEDDING_DIMENSIONS 2>/dev/null || true)"
  if has_helper "$helpers" conflict_provider; then
    embedding_model="$CONFLICT_PROVIDER_EMBEDDING_MODEL"
    if has_helper "$helpers" synchronous_write; then
      embedding_dimensions="${embedding_dimensions:-1536}"
    else
      embedding_dimensions="1536"
    fi
  fi
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

  local -a conflict_driver_env=()
  if [[ -f "${helper_dir}/conflict-review-driver" ]]; then
    if [[ "$scenario" == "conflict" ]]; then
      conflict_driver_env=(
        "AI_API_URL=http://conflict-provider:8081/v1"
        "AI_API_KEY=dense-mem-conflict-e2e-key"
        "AI_API_EMBEDDING_MODEL=${embedding_model}"
        "AI_API_EMBEDDING_DIMENSIONS=${embedding_dimensions}"
        "AI_VERIFIER_API_URL=http://conflict-provider:8081/v1"
        "AI_VERIFIER_API_KEY=dense-mem-conflict-e2e-key"
        "AI_VERIFIER_MODEL=dense-mem-conflict-e2e-verifier"
        "AI_VERIFIER_DISABLE_TEMPERATURE=true"
      )
    else
      local provider_field provider_value
      for provider_field in \
        AI_API_URL AI_API_KEY AI_API_EMBEDDING_MODEL AI_API_EMBEDDING_DIMENSIONS \
        AI_VERIFIER_API_URL AI_VERIFIER_API_KEY AI_VERIFIER_MODEL \
        AI_VERIFIER_DISABLE_TEMPERATURE AI_API_EMBEDDING_TIMEOUT_SECONDS AI_VERIFIER_TIMEOUT_SECONDS; do
        if has_helper "$helpers" conflict_provider && [[ "$provider_field" == "AI_API_EMBEDDING_MODEL" ]]; then
          provider_value="$embedding_model"
        elif has_helper "$helpers" conflict_provider && [[ "$provider_field" == "AI_API_EMBEDDING_DIMENSIONS" ]]; then
          provider_value="$embedding_dimensions"
        else
          provider_value="$(env_value "$provider_field" 2>/dev/null || true)"
        fi
        if [[ -n "$provider_value" ]]; then
          conflict_driver_env+=("${provider_field}=${provider_value}")
        fi
      done
    fi
  fi

  docker_cli_paths
  local docker_socket
  docker_socket="$(docker_socket_path)"
  if [[ "$scenario" == "synchronous_write_primitives" ]]; then
    run_synchronous_primitives_driver "$source_dir" "$project" "$postgres_user" "$postgres_password" "$postgres_db" "$run_id" "$attempt" "$phase" "$scenario" "$digest" ||
      fail "synchronous-write primitive drivers failed"
  fi
  if [[ "$scenario" == "mcp_sdk_parity" ]]; then
    run_mcp_sdk_parity_driver "$source_dir" "$project" "$run_id" "$attempt" "$phase" "$scenario" "$digest" ||
      fail "MCP SDK parity driver failed"
  fi

  local team_name="Dense-Mem CI ${run_id}-${attempt}-${scenario}"
  local team_payload team_response team_id credential_payload credential_response api_key credential_id
  team_payload="$(node -e 'process.stdout.write(JSON.stringify({name: process.argv[1], description: "production-image E2E scenario"}))' "$team_name")"
  team_response="$(control_api_request "$project" "$phase" "$stack_scenario" "$digest" "$run_id" "$attempt" "$image_ref" "$helper_overlay" "$control_token" "http://server:8090/control/api/teams" "$team_payload")" || fail "control portal did not return a team"
  team_id="$(printf '%s' "$team_response" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input).data?.id;if(!value)process.exit(1);process.stdout.write(value);});')" || fail "control portal did not return a team"
  credential_payload='{"name":"production-image-e2e","role":"manager","scopes":["read","write"],"rate_limit":300}'
  credential_response="$(control_api_request "$project" "$phase" "$stack_scenario" "$digest" "$run_id" "$attempt" "$image_ref" "$helper_overlay" "$control_token" "http://server:8090/control/api/teams/${team_id}/credentials" "$credential_payload")" || fail "control portal did not return a credential"
  api_key="$(printf '%s' "$credential_response" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input).data?.api_key;if(!value)process.exit(1);process.stdout.write(value);});')" || fail "control portal did not return a credential"
  credential_id="$(printf '%s' "$credential_response" | node -e 'let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{const value=JSON.parse(input).data?.credential?.id;if(!value)process.exit(1);process.stdout.write(value);});')" || fail "control portal did not return a credential ID"

  local oauth_token="" oauth_harness="" helper_runtime="${helper_dir}/runtime.env"
  local oauth_session_token="oauth-session-${run_id}-${scenario}" oauth_csrf_token="oauth-csrf-${run_id}-${scenario}"
  if [[ -f "$helper_runtime" ]]; then
    oauth_token="$(helper_env_value "$helper_runtime" DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN 2>/dev/null || true)"
    oauth_harness="$(helper_env_value "$helper_runtime" DENSE_MEM_E2E_OAUTH_HARNESS_URL 2>/dev/null || true)"
  fi
  local result_file="/results/${scenario}-result.json"
  local conflict_live=0
  [[ "$scenario" == "conflict_queue" ]] && conflict_live=1
  local container_name="${project}-test-${scenario//_/-}"
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local -a docker_args=(
    create --name "$container_name"
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}"
    --label "io.dense-mem.ci.repository=${REPOSITORY}"
    --label "io.dense-mem.ci.run-id=${run_id}"
    --label "io.dense-mem.ci.run-attempt=${attempt}"
    --label "io.dense-mem.ci.phase=${phase}"
    --label "io.dense-mem.ci.scenario=${scenario}"
    --label "io.dense-mem.ci.image-digest=${digest}"
    --label "io.dense-mem.ci.created-at=${created_at}"
    --label "com.docker.compose.project=${project}"
    --network "${project}_ci"
    --mount "type=bind,source=${docker_socket},target=${docker_socket}"
    --volume "${project}_client-env:/client-env:ro"
    --workdir /workspace
    -e "DOCKER_HOST=unix://${docker_socket}"
    -e "HOME=/tmp/dense-mem-home"
    -e "DENSE_MEM_USER_URL=http://server:8080"
    -e "DENSE_MEM_CONTROL_URL=http://server:8090"
    -e "DENSE_MEM_PROMETHEUS_URL=http://prometheus:9090"
    -e "DENSE_MEM_E2E_PROMETHEUS_URL=http://prometheus:9090"
    -e "DENSE_MEM_E2E_NETWORK=${project}_ci"
    -e "DENSE_MEM_E2E_CLIENT_ENV=/client-env/runtime.env"
    -e "DENSE_MEM_E2E_COMPOSE_PROJECT=${project}"
    -e "DENSE_MEM_E2E_COMPOSE_FILE=/ci/runtime-compose.yml"
    -e "DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE=/ci/helper-compose.yml"
    -e "DENSE_MEM_E2E_SCENARIO=${scenario}"
    -e "DENSE_MEM_E2E_RUNTIME=production"
    -e "DENSE_MEM_E2E_COMMIT_SHA=${tested_commit}"
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
  if [[ -f "${helper_dir}/conflict-review-driver" ]]; then
    local provider_env
    for provider_env in "${conflict_driver_env[@]}"; do
      docker_args+=(-e "$provider_env")
    done
  fi
  if [[ -f "${helper_dir}/ca.pem" ]]; then
    docker_args+=(-e "NODE_EXTRA_CA_CERTS=/oauth/ca.pem")
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
  docker_args+=(
    "$test_image" bash -euc '
      set -euo pipefail
      mkdir -p /usr/local/lib/docker/cli-plugins /ci /helpers /oauth /results
      install -m 0755 /tmp/dense-mem-docker /usr/local/bin/docker
      install -m 0755 /tmp/dense-mem-compose /usr/local/lib/docker/cli-plugins/docker-compose
      if [[ -f /tmp/dense-mem-runtime-compose.yml ]]; then
        install -m 0600 /tmp/dense-mem-runtime-compose.yml /ci/runtime-compose.yml
      fi
      if [[ -f /tmp/dense-mem-helper-compose.yml ]]; then
        install -m 0600 /tmp/dense-mem-helper-compose.yml /ci/helper-compose.yml
      fi
      if [[ -f /tmp/dense-mem-conflict-review-driver ]]; then
        install -m 0700 /tmp/dense-mem-conflict-review-driver /helpers/conflict-review-driver
      fi
      if [[ -f /tmp/dense-mem-oauth-ca.pem ]]; then
        install -m 0644 /tmp/dense-mem-oauth-ca.pem /oauth/ca.pem
      fi
      docker version
      docker compose version
      exec bash /workspace/scripts/e2e-ci-scenario.sh "$1"
    ' bash "$scenario"
  )

  local container=""
  cleanup_scenario_container() {
    local status=$?
    trap - EXIT INT TERM
    if [[ -n "$container" ]]; then
      docker rm -f "$container" >/dev/null 2>&1 || status=1
    fi
    exit "$status"
  }
  trap cleanup_scenario_container EXIT INT TERM
  container="$(docker "${docker_args[@]}")"
  copy_git_source "$container" "$source_dir"
  copy_file_into_container "$container" "$runtime_compose_host" /tmp/dense-mem-runtime-compose.yml
  copy_file_into_container "$container" "$DENSE_MEM_CI_DOCKER_BIN" /tmp/dense-mem-docker
  copy_file_into_container "$container" "$DENSE_MEM_CI_COMPOSE_PLUGIN" /tmp/dense-mem-compose
  if [[ -f "$helper_overlay" ]]; then
    copy_file_into_container "$container" "$helper_overlay" /tmp/dense-mem-helper-compose.yml
  fi
  if [[ -f "${helper_dir}/conflict-review-driver" ]]; then
    copy_file_into_container "$container" "${helper_dir}/conflict-review-driver" /tmp/dense-mem-conflict-review-driver
  fi
  if [[ -f "${helper_dir}/ca.pem" ]]; then
    copy_file_into_container "$container" "${helper_dir}/ca.pem" /tmp/dense-mem-oauth-ca.pem
  fi

  set +e
  docker start --attach "$container" 2>&1 |
    redact_diagnostics "$ENV_FILE" "$control_token" "$telemetry_token" "$postgres_password" "$api_key" "$identity_upgrade_api_key" "$oauth_token"
  local -a scenario_pipeline_status=("${PIPESTATUS[@]}")
  set -e
  ((scenario_pipeline_status[1] == 0)) || fail "diagnostic redaction failed"
  local scenario_status="${scenario_pipeline_status[0]}"
  trap - EXIT INT TERM
  docker rm "$container" >/dev/null
  container=""
  return "$scenario_status"
}

control_api_request() {
  local project="$1" phase="$2" scenario="$3" digest="$4" run_id="$5" attempt="$6" image_ref="$7" overlay="$8" token="$9" url="${10}" payload="${11}"
  (
    compose_base_env "$project" "$phase" "$scenario" "$digest" "$run_id" "$attempt" "1970-01-01T00:00:00Z" "${image_ref}@${digest}"
    DENSE_MEM_CI_COMPOSE_OVERLAY_FILE="$overlay" \
      ci_compose --profile client_env exec -T -e "DENSE_MEM_CI_CONTROL_TOKEN=${token}" client-env \
      sh -ec 'wget -q -O - --header="Authorization: Bearer ${DENSE_MEM_CI_CONTROL_TOKEN}" --header="Content-Type: application/json" --post-data="$1" "$2"' \
      sh "$payload" "$url"
  )
}

stop_stack() {
  local project="$1"
  validate_project "$project"
  local cleanup_failed=0 resource
  while IFS= read -r resource; do
    [[ -n "$resource" ]] || continue
    if ! docker rm -f "$resource" >/dev/null 2>&1 && docker inspect "$resource" >/dev/null 2>&1; then
      cleanup_failed=1
    fi
  done < <(
    {
      docker ps -aq --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
      docker ps -aq --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
    } | sort -u
  )
  while IFS= read -r resource; do
    [[ -n "$resource" ]] || continue
    if ! docker network rm "$resource" >/dev/null 2>&1 && docker network inspect "$resource" >/dev/null 2>&1; then
      cleanup_failed=1
    fi
  done < <(
    {
      docker network ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
      docker network ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
    } | sort -u
  )
  while IFS= read -r resource; do
    [[ -n "$resource" ]] || continue
    if ! docker volume rm "$resource" >/dev/null 2>&1 && docker volume inspect "$resource" >/dev/null 2>&1; then
      cleanup_failed=1
    fi
  done < <(
    {
      docker volume ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
      docker volume ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
    } | sort -u
  )
  while IFS= read -r resource; do
    [[ -n "$resource" ]] || continue
    if ! docker image rm "$resource" >/dev/null 2>&1 && docker image inspect "$resource" >/dev/null 2>&1; then
      cleanup_failed=1
    fi
  done < <(
    {
      docker image ls -q --no-trunc --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=com.docker.compose.project=${project}"
      docker image ls -q --no-trunc --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}" --filter "label=io.dense-mem.ci.compose-project=${project}"
    } | sort -u
  )
  if ((cleanup_failed)); then
    printf 'dense-mem CI controller: failed to remove every resource for project %s\n' "$project" >&2
    return 1
  fi
}

stale_cleanup() {
  local max_age="${1:-86400}"
  local target_run_id="${2:-}" target_attempt="${3:-}" target_phase="${4:-}"
  validate_decimal "$max_age"
  if [[ -n "$target_run_id" || -n "$target_attempt" || -n "$target_phase" ]]; then
    [[ -n "$target_run_id" && -n "$target_attempt" && -n "$target_phase" ]] || fail "targeted stale cleanup requires run ID, attempt, and phase"
    validate_decimal "$target_run_id"
    validate_decimal "$target_attempt"
    validate_cleanup_phase "$target_phase"
  fi
  local now cutoff resource metadata created_at project managed created_epoch
  now="$(date +%s)"
  cutoff=$((now - max_age))
  declare -A stale_projects=()
  while IFS= read -r resource; do
    [[ -n "$resource" ]] || continue
    metadata="$(docker inspect --format '{{json .}}' "$resource" 2>/dev/null | node -e '
let input="";process.stdin.on("data",c=>input+=c);process.stdin.on("end",()=>{try{const value=JSON.parse(input);const labels=value.Config?.Labels||value.Labels||{};const project=labels["com.docker.compose.project"]||labels["io.dense-mem.ci.compose-project"]||"";const managed=labels["io.dense-mem.ci.contract"]===process.argv[1]&&labels["io.dense-mem.ci.repository"]===process.argv[2]&&/^[1-9][0-9]*$/.test(labels["io.dense-mem.ci.run-id"]||"")&&/^[1-9][0-9]*$/.test(labels["io.dense-mem.ci.run-attempt"]||"")&&/^(precheck|exclusive|shared)$/.test(labels["io.dense-mem.ci.phase"]||"")&&/^[a-z0-9_]+$/.test(labels["io.dense-mem.ci.scenario"]||"")&&/^sha256:[0-9a-f]{64}$/.test(labels["io.dense-mem.ci.image-digest"]||"")&&typeof labels["io.dense-mem.ci.created-at"]==="string"&&/^densemem-ci-[a-z0-9][a-z0-9-]{0,50}$/.test(project)&&(!process.argv[3]||labels["io.dense-mem.ci.run-id"]===process.argv[3])&&(!process.argv[4]||labels["io.dense-mem.ci.run-attempt"]===process.argv[4])&&(!process.argv[5]||labels["io.dense-mem.ci.phase"]===process.argv[5]);process.stdout.write(`${labels["io.dense-mem.ci.created-at"]||""}\t${project}\t${managed?"1":"0"}`)}catch{}});' "$CONTRACT_VERSION" "$REPOSITORY" "$target_run_id" "$target_attempt" "$target_phase" || true)"
    IFS=$'\t' read -r created_at project managed <<<"$metadata"
    [[ "$managed" == "1" ]] || continue
    created_epoch="$(date -d "$created_at" +%s 2>/dev/null || true)"
    if [[ "$created_epoch" =~ ^[0-9]+$ ]] && ((created_epoch < cutoff)); then
      stale_projects["$project"]=1
    fi
  done < <(
    {
      docker ps -aq --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}"
      docker network ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}"
      docker volume ls -q --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}"
      docker image ls -q --no-trunc --filter "label=io.dense-mem.ci.contract=${CONTRACT_VERSION}" --filter "label=io.dense-mem.ci.repository=${REPOSITORY}"
    } | sort -u
  )
  local stale_failed=0
  for project in "${!stale_projects[@]}"; do
    stop_stack "$project" || stale_failed=1
  done
  if ((stale_failed)); then
    printf '%s\n' "stale CI cleanup completed with resource failures" >&2
    return 1
  fi
  printf '%s\n' "stale CI cleanup complete"
}
