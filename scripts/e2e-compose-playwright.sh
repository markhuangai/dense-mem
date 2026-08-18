remove_e2e_playwright_container() {
  if [[ -n "$E2E_PLAYWRIGHT_CONTAINER" ]] && docker container inspect "$E2E_PLAYWRIGHT_CONTAINER" >/dev/null 2>&1; then
    if ! docker rm -f "$E2E_PLAYWRIGHT_CONTAINER" >/dev/null; then
      echo "Failed to remove e2e Playwright container ${E2E_PLAYWRIGHT_CONTAINER}." >&2
    fi
  fi
}

run_compose_playwright_tests() {
  local image
  local test_args=("tests-compose/compose-portal.spec.ts")
  if [[ "${1:-}" == "portal" ]]; then
    test_args=(
      "tests-compose/compose-portal.spec.ts"
      -g
      "remembered API-key login uses a seven-day server session"
    )
  elif [[ "${1:-}" == "submission_status" ]]; then
    set_submission_status_playwright_args
  elif [[ "${1:-}" == "community" ]]; then test_args=("tests-compose/community-recall.spec.ts");
  elif [[ "${1:-}" == "conflict_queue" ]]; then test_args=("tests-compose/compose-conflict-queue.spec.ts");
  elif [[ "${1:-}" == "oauth" ]]; then test_args=("tests-compose/oauth-team-resource.spec.ts");
  fi
  image="${DENSE_MEM_E2E_PLAYWRIGHT_IMAGE:-mcr.microsoft.com/playwright:v1.62.1-noble}"
  E2E_PLAYWRIGHT_CONTAINER="densemem-e2e-${E2E_FILE_ID}-playwright"
  if docker container inspect "$E2E_PLAYWRIGHT_CONTAINER" >/dev/null 2>&1; then
    echo "Container ${E2E_PLAYWRIGHT_CONTAINER} already exists; choose another DENSE_MEM_E2E_RUN_ID." >&2
    return 1
  fi

  docker create --name "$E2E_PLAYWRIGHT_CONTAINER" --network host "$image" sleep infinity >/dev/null
  docker start "$E2E_PLAYWRIGHT_CONTAINER" >/dev/null
  docker exec "$E2E_PLAYWRIGHT_CONTAINER" mkdir -p /tmp/web
  tar \
    --exclude='./node_modules' \
    --exclude='./node_modules/*' \
    --exclude='./dist' \
    --exclude='./dist/*' \
    -C "$ROOT_DIR/web" \
    -cf - . | docker cp - "${E2E_PLAYWRIGHT_CONTAINER}:/tmp/web"
  docker exec \
    -e "PLAYWRIGHT_BROWSERS_PATH=/ms-playwright" \
    -e "DENSE_MEM_CONTROL_URL=$CONTROL_URL" \
    -e "DENSE_MEM_USER_URL=$USER_URL" \
    -e "DENSE_MEM_CONTROL_TOKEN=$CONTROL_TOKEN" \
    -e "DENSE_MEM_E2E_TEAM_ID=$team_id" \
    -e "DENSE_MEM_E2E_TEAM_NAME=E2E Team" \
    -e "DENSE_MEM_E2E_API_KEY=$api_key" \
    -e "DENSE_MEM_E2E_DREAM_STATEMENT=${dream_statement:-}" \
    -e "DENSE_MEM_PROMETHEUS_URL=$PROMETHEUS_URL" \
    -e "DENSE_MEM_E2E_GRAPH_ANCHOR_ENTITY_ID=$E2E_GRAPH_ANCHOR_ENTITY_ID" \
    -e "DENSE_MEM_E2E_GRAPH_ORIGINAL_OBJECT_ENTITY_ID=$E2E_GRAPH_ORIGINAL_OBJECT_ENTITY_ID" \
    -e "DENSE_MEM_E2E_GRAPH_CORRECTED_OBJECT_ENTITY_ID=$E2E_GRAPH_CORRECTED_OBJECT_ENTITY_ID" \
    -e "DENSE_MEM_E2E_GRAPH_ORIGINAL_RELATIONSHIP_ID=$E2E_GRAPH_ORIGINAL_RELATIONSHIP_ID" \
    -e "DENSE_MEM_E2E_GRAPH_SUCCESSOR_RELATIONSHIP_ID=$E2E_GRAPH_SUCCESSOR_RELATIONSHIP_ID" \
    -e "DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID=$E2E_OAUTH_SECOND_TEAM_ID" \
    -e "DENSE_MEM_E2E_SSO_SESSION_TOKEN=$E2E_OAUTH_SESSION_TOKEN" \
    -e "DENSE_MEM_E2E_SSO_CSRF_TOKEN=$E2E_OAUTH_CSRF_TOKEN" \
    "$E2E_PLAYWRIGHT_CONTAINER" \
    sh -ec 'cd /tmp/web && npm ci && ./node_modules/.bin/playwright test --config playwright.compose.config.ts "$@"' \
    sh "${test_args[@]}"
  remove_e2e_playwright_container
}
