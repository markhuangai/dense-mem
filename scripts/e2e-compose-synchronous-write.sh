SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE=""

prepare_synchronous_write_files() {
  if [[ "$E2E_SCENARIO" != "synchronous_write" ]]; then
    return
  fi

  local slice="${DENSE_MEM_E2E_WRITE_SLICE:-legacy}"
  case "$slice" in
    legacy|remember|correction|conflict|dream|reconciliation|diagnostics|contract)
      ;;
    *)
      echo "DENSE_MEM_E2E_WRITE_SLICE must be one of legacy, remember, correction, conflict, dream, reconciliation, diagnostics, or contract." >&2
      return 1
      ;;
  esac

  local provider_dimensions
  provider_dimensions="$(compose_server_environment_value AI_API_EMBEDDING_DIMENSIONS)"
  if [[ -z "$provider_dimensions" || ! "$provider_dimensions" =~ ^[1-9][0-9]*$ ]]; then
    echo "AI_API_EMBEDDING_DIMENSIONS must be a positive integer for the synchronous-write provider fixture." >&2
    return 1
  fi

  SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE="${ROOT_DIR}/docker-compose.synchronous-write-e2e-${E2E_FILE_ID}.yml"
  node - "$SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE" "$slice" "$provider_dimensions" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");

const [destination, slice, providerDimensions, marker] = process.argv.slice(2);
const quote = (value) => JSON.stringify(value);
const providerURL = slice === "conflict" ? "http://conflict-provider:8081/v1" : "http://synchronous-write-provider:8787/v1";
const contents = `${marker}
services:
  server:
    build:
      target: e2e
    depends_on:
      synchronous-write-provider:
        condition: service_healthy
    environment:
      DENSE_MEM_E2E_WRITE_SLICE: ${quote(slice)}
      DENSE_MEM_E2E_PROVIDER_FAULT: "\${DENSE_MEM_E2E_PROVIDER_FAULT:-none}"
      AI_API_EMBEDDING_TIMEOUT_SECONDS: "2"
      AI_VERIFIER_TIMEOUT_SECONDS: "2"
      AI_API_URL: ${providerURL}
      AI_VERIFIER_API_URL: ${providerURL}
      AI_VERIFIER_API_KEY: dense-mem-e2e-verifier-key
  synchronous-write-provider:
    image: node:22-alpine
    working_dir: /e2e
    command: ["node", "/e2e/provider-fixture.mjs"]
    environment:
      DENSE_MEM_E2E_PROVIDER_FAULT: "\${DENSE_MEM_E2E_PROVIDER_FAULT:-none}"
      DENSE_MEM_E2E_WRITE_SLICE: ${quote(slice)}
      DENSE_MEM_E2E_PROVIDER_DIMENSIONS: ${quote(providerDimensions)}
      DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS: "5000"
    volumes:
      - e2e-synchronous-write-provider-files:/e2e
    healthcheck:
      test: ["CMD", "node", "-e", "fetch('http://127.0.0.1:8787/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"]
      interval: 1s
      timeout: 2s
      retries: 30
volumes:
  e2e-synchronous-write-provider-files:
`;
fs.writeFileSync(destination, contents);
NODE
}

prepare_synchronous_write_provider_fixture_volume() {
  if [[ "$E2E_SCENARIO" != "synchronous_write" ]]; then
    return
  fi

  local container_id
  container_id="$(compose create synchronous-write-provider >/dev/null && compose ps -aq synchronous-write-provider)"
  if [[ -z "$container_id" ]]; then
    echo "Failed to create the synchronous-write provider fixture volume." >&2
    return 1
  fi
  docker cp \
    "$ROOT_DIR/tests/uat/synchronous_write/provider-fixture.mjs" \
    "${container_id}:/e2e/provider-fixture.mjs"
}

run_synchronous_write_e2e() {
  local team_id="$1"
  local api_key="$2"
  local slice="${DENSE_MEM_E2E_WRITE_SLICE:-legacy}"
  local configured_fault="${DENSE_MEM_E2E_PROVIDER_FAULT:-none}"
  local runtime_postgres_user
  local runtime_postgres_password
  local runtime_postgres_database
  echo "Running compose-backed synchronous-write contract cases with slice ${slice}."
  if [[ "$slice" == "conflict" ]]; then
    runtime_postgres_user="$(compose_server_environment_value POSTGRES_USER)"
    runtime_postgres_password="$(compose_server_environment_value POSTGRES_PASSWORD)"
    runtime_postgres_database="$(compose_server_environment_value POSTGRES_DB)"
    DENSE_MEM_E2E_CONFLICT_REVIEW_DRIVER="$E2E_CONFLICT_REVIEW_DRIVER" \
    DENSE_MEM_E2E_CONFLICT_PROVIDER_URL="http://127.0.0.1:${E2E_CONFLICT_PROVIDER_PORT}/v1" \
    DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
    DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
    DENSE_MEM_E2E_POSTGRES_HOST="127.0.0.1" \
    DENSE_MEM_E2E_POSTGRES_PORT="$POSTGRES_HOST_PORT" \
    DENSE_MEM_E2E_POSTGRES_USER="$runtime_postgres_user" \
    DENSE_MEM_E2E_POSTGRES_PASSWORD="$runtime_postgres_password" \
    DENSE_MEM_E2E_POSTGRES_DB="$runtime_postgres_database" \
    AI_API_URL="http://127.0.0.1:${E2E_CONFLICT_PROVIDER_PORT}/v1" \
    AI_API_KEY="dense-mem-conflict-e2e-key" \
    AI_API_EMBEDDING_MODEL="dense-mem-conflict-e2e-embedding" \
    AI_API_EMBEDDING_DIMENSIONS="1536" \
    AI_API_EMBEDDING_TIMEOUT_SECONDS="10" \
    AI_VERIFIER_API_URL="http://127.0.0.1:${E2E_CONFLICT_PROVIDER_PORT}/v1" \
    AI_VERIFIER_API_KEY="dense-mem-conflict-e2e-key" \
    AI_VERIFIER_MODEL="dense-mem-conflict-e2e-verifier" \
    AI_VERIFIER_TIMEOUT_SECONDS="2" \
    AI_VERIFIER_DISABLE_TEMPERATURE="true" \
    DENSE_MEM_USER_URL="$USER_URL" \
    DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
    DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
    DENSE_MEM_E2E_TEAM_ID="$team_id" \
    DENSE_MEM_E2E_API_KEY="$api_key" \
    DENSE_MEM_E2E_WRITE_CASE="$slice" \
    node "$ROOT_DIR/tests/uat/synchronous_write/runner.mjs"
    return
  fi

  run_case() {
    local fault="$1"
    echo "Running synchronous-write ${slice} case with boot fault ${fault}."
    DENSE_MEM_E2E_PROVIDER_FAULT="$fault" \
      compose up -d --no-build --force-recreate synchronous-write-provider server >/dev/null
    wait_for_url "synchronous-write server readiness (${fault})" "${USER_URL}/ready"
    DENSE_MEM_USER_URL="$USER_URL" \
    DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
    DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
    DENSE_MEM_E2E_TEAM_ID="$team_id" \
    DENSE_MEM_E2E_API_KEY="$api_key" \
    DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
    DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
    DENSE_MEM_E2E_PROVIDER_FAULT="$fault" \
    DENSE_MEM_E2E_WRITE_CASE="$slice" \
    node "$ROOT_DIR/tests/uat/synchronous_write/runner.mjs"
  }

  if [[ "$slice" == "remember" && "$configured_fault" == "none" ]]; then
    run_case none
    run_case search-generation-rotation
    run_case supersession-rotation
    return
  fi
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  DENSE_MEM_E2E_PROVIDER_FAULT="$configured_fault" \
  DENSE_MEM_E2E_WRITE_CASE="$slice" \
  node "$ROOT_DIR/tests/uat/synchronous_write/runner.mjs"
}
