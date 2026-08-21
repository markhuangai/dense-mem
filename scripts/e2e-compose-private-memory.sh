run_private_memory_erasure_e2e() {
  local team_id="$1"
  local credential_id="$2"
  local api_key="$3"

  echo "Running compose-backed private-memory erasure, retention, legal-hold, and worker-recovery e2e."
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_CREDENTIAL_ID="$credential_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  node "$ROOT_DIR/tests/uat/private_memory_erasure_e2e.mjs"
}
