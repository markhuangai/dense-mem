E2E_CONFLICT_PROVIDER_PORT=""
E2E_CONFLICT_REVIEW_DRIVER=""

append_conflict_e2e_environment() {
  if ! conflict_server_provider_required; then
    return
  fi
  printf '%s\n' \
    "AI_API_URL=http://conflict-provider:8081/v1" \
    "AI_API_KEY=dense-mem-conflict-e2e-key" \
    "AI_API_EMBEDDING_MODEL=dense-mem-conflict-e2e-embedding" \
    "AI_API_EMBEDDING_DIMENSIONS=1536" \
    "AI_VERIFIER_API_URL=http://conflict-provider:8081/v1" \
    "AI_VERIFIER_API_KEY=dense-mem-conflict-e2e-key" \
    "AI_VERIFIER_MODEL=dense-mem-conflict-e2e-verifier" \
    "AI_VERIFIER_DISABLE_TEMPERATURE=true" >> "$E2E_ENV_FILE"
}

prepare_conflict_provider_files() {
  if ! conflict_provider_required; then
    return
  fi
  E2E_COMPOSE_OVERLAY_FILE="${ROOT_DIR}/docker-compose.conflict-e2e-${E2E_FILE_ID}.yml"
  node - "$E2E_COMPOSE_OVERLAY_FILE" "$E2E_CONFLICT_PROVIDER_PORT" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");

const [destination, providerPort, marker] = process.argv.slice(2);
const contents = `${marker}
services:
  server:
    depends_on:
      conflict-provider:
        condition: service_healthy
  conflict-provider:
    image: node:24-alpine
    working_dir: /e2e
    command: ["node", "/e2e/conflict_openai_stub.mjs"]
    volumes:
      - e2e-conflict-provider:/e2e
    ports:
      - "127.0.0.1:${providerPort}:8081"
    healthcheck:
      test: ["CMD", "node", "-e", "fetch('http://127.0.0.1:8081/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"]
      interval: 1s
      timeout: 2s
      retries: 30
volumes:
  e2e-conflict-provider:
`;
fs.writeFileSync(destination, contents);
NODE
}

prepare_conflict_provider_volume() {
  local container_id
  if ! conflict_provider_required; then
    return
  fi
  compose create conflict-provider >/dev/null
  container_id="$(compose ps -aq conflict-provider)"
  if [[ -z "$container_id" ]]; then
    echo "Failed to create the E2E conflict provider volume." >&2
    return 1
  fi
  docker cp "${ROOT_DIR}/tests/uat/conflict_openai_stub.mjs" "${container_id}:/e2e/conflict_openai_stub.mjs"
}

prepare_conflict_review_driver() {
  if [[ "$E2E_SCENARIO" != "conflict" && "$E2E_SCENARIO" != "conflict_queue" && ! ( "$E2E_SCENARIO" == "synchronous_write" && ( -z "${DENSE_MEM_E2E_WRITE_CASE:-}" || "${DENSE_MEM_E2E_WRITE_CASE:-}" == "conflict" ) ) ]]; then
    return
  fi
  E2E_CONFLICT_REVIEW_DRIVER="${TEMP_DIR}/conflict-review-driver"
  go build -o "$E2E_CONFLICT_REVIEW_DRIVER" ./tests/uat/conflict_review_driver
}

conflict_provider_required() {
  [[ "$E2E_SCENARIO" == "conflict" || ( "$E2E_SCENARIO" == "synchronous_write" && ( -z "${DENSE_MEM_E2E_WRITE_CASE:-}" || "${DENSE_MEM_E2E_WRITE_CASE:-}" == "conflict" ) ) ]]
}

conflict_server_provider_required() {
  [[ "$E2E_SCENARIO" == "conflict" || ( "$E2E_SCENARIO" == "synchronous_write" && "${DENSE_MEM_E2E_WRITE_CASE:-}" == "conflict" ) ]]
}

run_conflict_e2e() {
  local team_id="$1"
  local runtime_postgres_user
  local runtime_postgres_password
  local runtime_postgres_database

  echo "Running deterministic compose-backed MCP conflict e2e."
  runtime_postgres_user="$(compose_server_environment_value POSTGRES_USER)"
  runtime_postgres_password="$(compose_server_environment_value POSTGRES_PASSWORD)"
  runtime_postgres_database="$(compose_server_environment_value POSTGRES_DB)"
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  DENSE_MEM_E2E_CONFLICT_REVIEW_DRIVER="$E2E_CONFLICT_REVIEW_DRIVER" \
  DENSE_MEM_E2E_CONFLICT_PROVIDER_URL="http://127.0.0.1:${E2E_CONFLICT_PROVIDER_PORT}/v1" \
  AI_API_URL="http://127.0.0.1:${E2E_CONFLICT_PROVIDER_PORT}/v1" \
  AI_API_KEY="dense-mem-conflict-e2e-key" \
  AI_API_EMBEDDING_MODEL="dense-mem-conflict-e2e-embedding" \
  AI_API_EMBEDDING_DIMENSIONS="1536" \
  AI_API_EMBEDDING_TIMEOUT_SECONDS="10" \
  AI_VERIFIER_API_URL="http://127.0.0.1:${E2E_CONFLICT_PROVIDER_PORT}/v1" \
  AI_VERIFIER_API_KEY="dense-mem-conflict-e2e-key" \
  AI_VERIFIER_MODEL="dense-mem-conflict-e2e-verifier" \
  AI_VERIFIER_DISABLE_TEMPERATURE="true" \
  DENSE_MEM_E2E_POSTGRES_HOST="127.0.0.1" \
  DENSE_MEM_E2E_POSTGRES_PORT="$POSTGRES_HOST_PORT" \
  DENSE_MEM_E2E_POSTGRES_USER="$runtime_postgres_user" \
  DENSE_MEM_E2E_POSTGRES_PASSWORD="$runtime_postgres_password" \
  DENSE_MEM_E2E_POSTGRES_DB="$runtime_postgres_database" \
  node "$ROOT_DIR/tests/uat/conflict_mcp_e2e.mjs"
}

run_conflict_queue_e2e() {
  local team_id="$1"
  local runtime_postgres_user
  local runtime_postgres_password
  local runtime_postgres_database

  echo "Running live-credential compose-backed MCP conflict queue e2e."
  runtime_postgres_user="$(compose_server_environment_value POSTGRES_USER)"
  runtime_postgres_password="$(compose_server_environment_value POSTGRES_PASSWORD)"
  runtime_postgres_database="$(compose_server_environment_value POSTGRES_DB)"
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  DENSE_MEM_E2E_CONFLICT_REVIEW_DRIVER="$E2E_CONFLICT_REVIEW_DRIVER" \
  DENSE_MEM_E2E_PROMETHEUS_URL="$PROMETHEUS_URL" \
  DENSE_MEM_E2E_POSTGRES_HOST="127.0.0.1" \
  DENSE_MEM_E2E_POSTGRES_PORT="$POSTGRES_HOST_PORT" \
  DENSE_MEM_E2E_POSTGRES_USER="$runtime_postgres_user" \
  DENSE_MEM_E2E_POSTGRES_PASSWORD="$runtime_postgres_password" \
  DENSE_MEM_E2E_POSTGRES_DB="$runtime_postgres_database" \
  DENSE_MEM_E2E_CONFLICT_REVIEW_LIVE=1 \
  AI_API_URL="$(env_file_value AI_API_URL)" \
  AI_API_KEY="$(env_file_value AI_API_KEY)" \
  AI_API_EMBEDDING_MODEL="$(env_file_value AI_API_EMBEDDING_MODEL)" \
  AI_API_EMBEDDING_DIMENSIONS="$(env_file_value AI_API_EMBEDDING_DIMENSIONS)" \
  AI_API_EMBEDDING_TIMEOUT_SECONDS="$(env_file_value AI_API_EMBEDDING_TIMEOUT_SECONDS)" \
  AI_VERIFIER_API_URL="$(env_file_value AI_VERIFIER_API_URL)" \
  AI_VERIFIER_API_KEY="$(env_file_value AI_VERIFIER_API_KEY)" \
  AI_VERIFIER_MODEL="$(env_file_value AI_VERIFIER_MODEL)" \
  node "$ROOT_DIR/tests/uat/conflict_queue_e2e.mjs"
  if [[ "${DENSE_MEM_E2E_SKIP_PLAYWRIGHT:-0}" == "1" ]]; then
    echo "Skipping compose-backed conflict queue Playwright tests by DENSE_MEM_E2E_SKIP_PLAYWRIGHT."
  else
    echo "Running compose-backed conflict queue Playwright tests."
    run_compose_playwright_tests conflict_queue
  fi
}
