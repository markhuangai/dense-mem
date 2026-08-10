E2E_EMBEDDING_PROXY_PORT=""; E2E_EMBEDDING_PROXY_OVERLAY_FILE=""; E2E_EMBEDDING_PROXY_SCRIPT=""

append_embedding_reconciliation_environment() {
  if [[ "$E2E_SCENARIO" != "embedding_reconciliation" ]]; then
    return
  fi
  local live_api_url
  local live_api_key
  live_api_url="$(require_env_value AI_API_URL)"
  live_api_key="$(require_env_value AI_API_KEY)"
  printf '%s\n' \
    "AI_API_URL=http://embedding-fault-proxy:8081/v1" \
    "AI_API_KEY=dense-mem-embedding-proxy-key" \
    "EMBEDDING_BATCH_SIZE=256" \
    "EMBEDDING_JOB_POLL_SECONDS=5" \
    "EMBEDDING_PROXY_UPSTREAM_URL=${live_api_url}" \
    "EMBEDDING_PROXY_UPSTREAM_KEY=${live_api_key}" >> "$E2E_ENV_FILE"
}

is_generated_proxy_file() {
  local path="$1"
  local first_line=""
  if [[ ! -f "$path" ]]; then
    return 1
  fi
  IFS= read -r first_line < "$path" || true
  [[ "$first_line" == "// ${E2E_MARKER}" ]]
}

prepare_embedding_proxy_files() {
  if [[ "$E2E_SCENARIO" != "embedding_reconciliation" ]]; then
    return
  fi
  E2E_EMBEDDING_PROXY_PORT="${DENSE_MEM_E2E_EMBEDDING_PROXY_PORT:-$(pick_ports 1)}"
  E2E_EMBEDDING_PROXY_OVERLAY_FILE="${ROOT_DIR}/docker-compose.embedding-reconciliation-e2e-${E2E_FILE_ID}.yml"
  E2E_EMBEDDING_PROXY_SCRIPT="${E2E_BIND_DIR}/densemem-embedding-fault-proxy-${E2E_FILE_ID}.mjs"
  node - "$E2E_EMBEDDING_PROXY_SCRIPT" "$ROOT_DIR/tests/uat/embedding_fault_proxy.mjs" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");
const [destination, source, marker] = process.argv.slice(2);
const sourceText = fs.readFileSync(source, "utf8").replace(/^#![^\n]*\n/, "");
fs.writeFileSync(destination, `// ${marker}\n${sourceText}`);
NODE
  node - "$E2E_EMBEDDING_PROXY_OVERLAY_FILE" "$E2E_EMBEDDING_PROXY_SCRIPT" "$E2E_EMBEDDING_PROXY_PORT" "$E2E_ENV_FILE" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");

const [destination, proxyScript, proxyPort, envFile, marker] = process.argv.slice(2);
const quote = (value) => JSON.stringify(value);
const mount = quote(`${proxyScript}:/e2e/embedding_fault_proxy.mjs:ro`);
const contents = `${marker}
services:
  server:
    depends_on:
      embedding-fault-proxy:
        condition: service_healthy
  embedding-fault-proxy:
    image: node:24-alpine
    working_dir: /e2e
    command: ["node", "/e2e/embedding_fault_proxy.mjs"]
    env_file:
      - ${quote(envFile)}
    environment:
      EMBEDDING_PROXY_MODE: quota
    volumes:
      - ${mount}
    ports:
      - "127.0.0.1:${proxyPort}:8081"
    healthcheck:
      test: ["CMD", "node", "-e", "fetch('http://127.0.0.1:8081/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"]
      interval: 1s
      timeout: 2s
      retries: 30
`;
fs.writeFileSync(destination, contents);
NODE
}

append_embedding_proxy_compose_args() {
  local -n compose_args_ref="$1"
  if [[ -n "$E2E_EMBEDDING_PROXY_OVERLAY_FILE" ]]; then compose_args_ref+=(-f "$E2E_EMBEDDING_PROXY_OVERLAY_FILE"); fi
}

cleanup_embedding_proxy_files() {
  if [[ -n "$E2E_EMBEDDING_PROXY_OVERLAY_FILE" && -f "$E2E_EMBEDDING_PROXY_OVERLAY_FILE" ]] && is_generated_marker_file "$E2E_EMBEDDING_PROXY_OVERLAY_FILE"; then rm -- "$E2E_EMBEDDING_PROXY_OVERLAY_FILE"; fi
  if [[ -n "$E2E_EMBEDDING_PROXY_SCRIPT" && -f "$E2E_EMBEDDING_PROXY_SCRIPT" ]] && is_generated_proxy_file "$E2E_EMBEDDING_PROXY_SCRIPT"; then rm -- "$E2E_EMBEDDING_PROXY_SCRIPT"; fi
}

run_embedding_reconciliation_e2e() {
  local team_id="$1"
  echo "Running compose-backed daily embedding reconciliation e2e."
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  DENSE_MEM_PROMETHEUS_URL="$PROMETHEUS_URL" \
  DENSE_MEM_E2E_EMBEDDING_PROXY_URL="http://127.0.0.1:${E2E_EMBEDDING_PROXY_PORT}" \
  node "$ROOT_DIR/tests/uat/embedding_reconciliation_e2e.mjs"
}
