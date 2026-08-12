run_infrastructure_credentials_e2e() {
  echo "Running compose-backed infrastructure credential and socket-boundary e2e."
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  node "$ROOT_DIR/tests/uat/infrastructure_credentials_e2e.mjs"
}

run_security_runtime_e2e() {
  echo "Running compose-backed runtime security boundary e2e."
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_E2E_TELEMETRY_TOKEN="$TELEMETRY_SCRAPE_TOKEN" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  node "$ROOT_DIR/tests/uat/security_runtime_e2e.mjs"
}
