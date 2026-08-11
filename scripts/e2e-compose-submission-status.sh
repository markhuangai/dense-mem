E2E_GRAPH_ANCHOR_ENTITY_ID=""
E2E_GRAPH_ORIGINAL_OBJECT_ENTITY_ID=""
E2E_GRAPH_CORRECTED_OBJECT_ENTITY_ID=""
E2E_GRAPH_ORIGINAL_RELATIONSHIP_ID=""
E2E_GRAPH_SUCCESSOR_RELATIONSHIP_ID=""

set_submission_status_playwright_args() {
  test_args=(
    "tests-compose/compose-portal.spec.ts"
    -g
    "control overview contains every Top Signals diagnostic|user portal renders the corrected live graph"
  )
}

run_submission_status_e2e() {
  local team_id="$1"
  local submission_status_json

  echo "Running compose-backed submission status and public-contract e2e with the configured live verifier."
  submission_status_json="$(DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  node "$ROOT_DIR/tests/uat/submission_status_mcp_e2e.mjs")"
  printf '%s\n' "$submission_status_json"
  E2E_GRAPH_ANCHOR_ENTITY_ID="$(printf '%s' "$submission_status_json" | json_field graph_anchor_entity_id)"
  E2E_GRAPH_ORIGINAL_OBJECT_ENTITY_ID="$(printf '%s' "$submission_status_json" | json_field graph_original_object_entity_id)"
  E2E_GRAPH_CORRECTED_OBJECT_ENTITY_ID="$(printf '%s' "$submission_status_json" | json_field graph_corrected_object_entity_id)"
  E2E_GRAPH_ORIGINAL_RELATIONSHIP_ID="$(printf '%s' "$submission_status_json" | json_field relationship_id)"
  E2E_GRAPH_SUCCESSOR_RELATIONSHIP_ID="$(printf '%s' "$submission_status_json" | json_field successor_relationship_id)"
  if [[ -z "$E2E_GRAPH_ANCHOR_ENTITY_ID" || -z "$E2E_GRAPH_ORIGINAL_OBJECT_ENTITY_ID" ||
        -z "$E2E_GRAPH_CORRECTED_OBJECT_ENTITY_ID" || -z "$E2E_GRAPH_ORIGINAL_RELATIONSHIP_ID" ||
        -z "$E2E_GRAPH_SUCCESSOR_RELATIONSHIP_ID" ]]; then
    echo "submission-status UAT did not return required graph fixture identifiers" >&2
    return 1
  fi
  dream_statement="submission status e2e"
  if [[ "${DENSE_MEM_E2E_SKIP_PLAYWRIGHT:-0}" == "1" ]]; then
    echo "Skipping compose-backed submission-status Playwright tests by DENSE_MEM_E2E_SKIP_PLAYWRIGHT."
    return
  fi
  echo "Running compose-backed submission-status portal regressions."
  run_compose_playwright_tests submission_status
}
