run_memory_spaces_e2e() {
  local team_id="$1"
  local api_key="$2"
  echo "Running compose-backed ${E2E_SCENARIO} memory-space e2e."
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_MEMORY_SCENARIO="$E2E_SCENARIO" \
  node "$ROOT_DIR/tests/uat/memory_spaces_e2e.mjs"
}
