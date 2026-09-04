#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="${DENSE_MEM_E2E_SOURCE_ROOT:-/workspace}"
SCENARIO="${1:-${DENSE_MEM_E2E_SCENARIO:-}}"

fail() {
  printf 'dense-mem CI scenario: %s\n' "$*" >&2
  exit 1
}

log() {
  printf 'dense-mem CI scenario [%s]: %s\n' "$SCENARIO" "$*" >&2
}

[[ "$SCENARIO" =~ ^[a-z0-9_]+$ ]] || fail "a scenario name is required"
scenario_info="$(node "${ROOT_DIR}/scripts/e2e-scenario-registry.mjs" --scenario "$SCENARIO")" || fail "scenario is not in the registry"
node - "$scenario_info" <<'NODE' || fail "scenario is not audited in the production registry"
const scenario = JSON.parse(process.argv[2]);
if (scenario.audited !== true || scenario.runtime !== "production") process.exit(1);
NODE

: "${DENSE_MEM_USER_URL:?DENSE_MEM_USER_URL is required}"
: "${DENSE_MEM_CONTROL_URL:?DENSE_MEM_CONTROL_URL is required}"
: "${DENSE_MEM_CONTROL_TOKEN:?DENSE_MEM_CONTROL_TOKEN is required}"
: "${DENSE_MEM_E2E_TEAM_ID:?DENSE_MEM_E2E_TEAM_ID is required}"
: "${DENSE_MEM_E2E_TEAM_NAME:?DENSE_MEM_E2E_TEAM_NAME is required}"
: "${DENSE_MEM_E2E_API_KEY:?DENSE_MEM_E2E_API_KEY is required}"
log "starting"

wait_for_url() {
  local label="$1" url="$2" ca_file="${3:-}"
  log "waiting for ${label}"
  for _ in $(seq 1 90); do
    if [[ -n "$ca_file" ]]; then
      if curl --connect-timeout 5 --max-time 10 --cacert "$ca_file" -fsS "$url" >/dev/null 2>&1; then
        log "${label} is ready"
        return 0
      fi
    elif curl --connect-timeout 5 --max-time 10 -fsS "$url" >/dev/null 2>&1; then
      log "${label} is ready"
      return 0
    fi
    sleep 2
  done
  fail "timed out waiting for ${label}"
}

wait_for_url "Dense-Mem readiness" "${DENSE_MEM_USER_URL%/}/ready"
curl --connect-timeout 5 --max-time 10 -fsS -H "Authorization: Bearer ${DENSE_MEM_CONTROL_TOKEN}" \
  "${DENSE_MEM_CONTROL_URL%/}/control/api/session" >/dev/null || fail "control portal is not ready"
if [[ -n "${DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN:-}" ]]; then
  wait_for_url "OAuth provider mock" "${DENSE_MEM_E2E_OAUTH_MOCK_URL%/}/health" "${NODE_EXTRA_CA_CERTS:-}"
  if [[ -n "${DENSE_MEM_E2E_OAUTH_HARNESS_URL:-}" ]]; then
    wait_for_url "OAuth compatibility harness" "${DENSE_MEM_E2E_OAUTH_HARNESS_URL%/}/health" "${NODE_EXTRA_CA_CERTS:-}"
  fi
  if [[ -n "${DENSE_MEM_ENTRA_MOCK_URL:-}" ]]; then
    wait_for_url "Entra OIDC mock" "${DENSE_MEM_ENTRA_MOCK_URL%/}/.well-known/openid-configuration" "${NODE_EXTRA_CA_CERTS:-}"
  fi
fi
if [[ "$SCENARIO" == "conflict" ]]; then
  conflict_health_url="${DENSE_MEM_E2E_CONFLICT_PROVIDER_URL%/}"
  conflict_health_url="${conflict_health_url%/v1}/health"
  wait_for_url "conflict provider" "$conflict_health_url"
elif [[ "$SCENARIO" == "synchronous_write" || "$SCENARIO" == "synchronous_write_primitives" ]]; then
  wait_for_url "synchronous-write provider" "http://synchronous-write-provider:8787/health"
fi

export DENSE_MEM_E2E_COMPOSE_PROJECT="${DENSE_MEM_E2E_COMPOSE_PROJECT:-}"
export DENSE_MEM_E2E_COMPOSE_FILE="${DENSE_MEM_E2E_COMPOSE_FILE:-}"
export DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE="${DENSE_MEM_E2E_COMPOSE_OVERLAY_FILE:-}"
export DENSE_MEM_E2E_CONFLICT_PROVIDER_URL="${DENSE_MEM_E2E_CONFLICT_PROVIDER_URL:-http://conflict-provider:8081/v1}"
export DENSE_MEM_E2E_OAUTH_INTERNAL_URL="${DENSE_MEM_E2E_OAUTH_INTERNAL_URL:-https://oauth-provider-mock:9444}"
export DENSE_MEM_E2E_SSO_SESSION_TOKEN="${DENSE_MEM_E2E_SSO_SESSION_TOKEN:-oauth-session-${DENSE_MEM_E2E_RUN_ID:-unknown}}"
export DENSE_MEM_E2E_SSO_CSRF_TOKEN="${DENSE_MEM_E2E_SSO_CSRF_TOKEN:-oauth-csrf-${DENSE_MEM_E2E_RUN_ID:-unknown}}"

run_node_case() {
  local script="$1"
  local status
  [[ -f "${ROOT_DIR}/${script}" ]] || fail "missing scenario script: ${script}"
  log "running ${script}"
  if node "${ROOT_DIR}/${script}"; then
    log "passed ${script}"
  else
    status=$?
    log "failed ${script} (exit ${status})"
    return "$status"
  fi
}

parse_json_root_field() {
  local field="$1"
  node -e '
const field = process.argv[1];
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  let value;
  try {
    value = JSON.parse(input)?.[field];
  } catch {
    process.exitCode = 1;
    return;
  }
  if (typeof value !== "string" || value.length === 0 || /[\r\n]/.test(value)) {
    process.exitCode = 1;
    return;
  }
  process.stdout.write(value);
});
' "$field"
}

run_playwright() {
  if [[ "${DENSE_MEM_E2E_RUN_PLAYWRIGHT:-0}" != "1" ]]; then
    log "Playwright checks are disabled"
    return 0
  fi
  [[ -f "${ROOT_DIR}/web/package.json" ]] || fail "missing web package for Playwright"
  local diagnostics_attempt_id=""
  case "$SCENARIO" in
    mcp_oauth) [[ -n "${DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID:-}" ]] || fail "OAuth Playwright handoff is missing" ;;
    synchronous_write)
      local fixture_file="${DENSE_MEM_E2E_DIAGNOSTICS_FIXTURE_FILE:-}"
      [[ -n "$fixture_file" && -f "$fixture_file" ]] || fail "diagnostics Playwright handoff is missing"
      diagnostics_attempt_id="$(node -e 'const fs=require("node:fs");const value=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));process.stdout.write(value.failed_attempt_id||"");' "$fixture_file")"
      [[ -n "$diagnostics_attempt_id" ]] || fail "diagnostics Playwright attempt is missing"
      export DENSE_MEM_E2E_DIAGNOSTIC_ATTEMPT_ID="$diagnostics_attempt_id"
      ;;
    full) [[ -n "${DENSE_MEM_E2E_DREAM_STATEMENT:-}" ]] || fail "Dream Playwright handoff is missing" ;;
  esac
  local -a specs=("tests-compose/search-convergence.spec.ts" "tests-compose/compose-portal.spec.ts")
  case "$SCENARIO" in
    synchronous_write) specs=("tests-compose/remember-attempts.spec.ts") ;;
    community) specs=("tests-compose/community-recall.spec.ts") ;;
    conflict_queue) specs=("tests-compose/compose-conflict-queue.spec.ts") ;;
    mcp_oauth) specs=("tests-compose/oauth-team-resource.spec.ts") ;;
  esac
  if [[ "$SCENARIO" == "synchronous_write" ]]; then
    specs+=("tests-compose/compose-evidence-conflict.spec.ts")
  fi
  log "running Playwright specs: ${specs[*]}"
  local web_dir="/tmp/dense-mem-web-${SCENARIO}-$$" status
  if [[ -e "$web_dir" ]]; then rm -r -- "$web_dir"; fi
  mkdir -p "$web_dir"
  cp -a "${ROOT_DIR}/web/." "$web_dir/"
  if (cd "$web_dir" && npm ci --ignore-scripts && npx playwright test --config playwright.compose.config.ts "${specs[@]}"); then
    status=0
  else
    status=$?
  fi
  rm -r -- "$web_dir"
  if ((status != 0)); then
    log "failed Playwright specs: ${specs[*]} (exit ${status})"
    return "$status"
  fi
  log "passed Playwright specs: ${specs[*]}"
}

case "$SCENARIO" in
  mcp_boundaries) run_node_case tests/uat/mcp_boundaries_e2e.mjs ;;
  oauth_provider_compatibility)
    run_node_case tests/uat/oauth_provider_compatibility_e2e.mjs
    if [[ -n "${DENSE_MEM_ENTRA_MOCK_URL:-}" ]]; then
      run_node_case tests/uat/entra_scim_e2e.mjs
    fi
    ;;
  mcp_oauth) run_node_case tests/uat/oauth_mcp_e2e.mjs ;;
  private_memory_erasure) run_node_case tests/uat/private_memory_erasure_e2e.mjs ;;
  synchronous_write)
    run_node_case tests/uat/synchronous_write/runner.mjs || fail "synchronous-write scenario failed"
    ;;
  synchronous_write_primitives)
    DENSE_MEM_E2E_WRITE_CASE=remember run_node_case tests/uat/synchronous_write/runner.mjs
    ;;
  identity_cleanup) run_node_case tests/uat/identity_cleanup_e2e.mjs ;;
  community) run_node_case tests/uat/community_recall_mcp_e2e.mjs ;;
  conflict) run_node_case tests/uat/conflict_mcp_e2e.mjs ;;
  conflict_queue) run_node_case tests/uat/conflict_queue_e2e.mjs ;;
  mcp_sdk_parity) run_node_case tests/uat/mcp_sdk_parity_e2e.mjs ;;
  mcp_sdk_transport) run_node_case tests/uat/mcp_sdk_transport_e2e.mjs ;;
  security_runtime) run_node_case tests/uat/security_runtime_e2e.mjs ;;
  infrastructure_credentials) run_node_case tests/uat/infrastructure_credentials_e2e.mjs ;;
  submission_terminal_errors) run_node_case tests/uat/submission_terminal_errors_e2e.mjs ;;
  security_intake) run_node_case tests/uat/security_intake_mcp_e2e.mjs ;;
  memory_space_backfill|memory_space_isolation|space_aware_recall|credential_memory_binding)
    run_node_case tests/uat/memory_spaces_e2e.mjs
    ;;
  full)
    dreaming_output="$(run_node_case tests/uat/team_dreaming_e2e.mjs)" || fail "team-dreaming scenario failed"
    printf '%s\n' "${dreaming_output}"
    DENSE_MEM_E2E_DREAM_STATEMENT="$(printf '%s' "${dreaming_output}" | parse_json_root_field statement)" || fail "team-dreaming scenario did not produce the Dream Playwright handoff"
    export DENSE_MEM_E2E_DREAM_STATEMENT
    run_node_case tests/uat/telemetry_mcp_e2e.mjs
    ;;
  *) fail "unsupported scenario: ${SCENARIO}" ;;
esac

if [[ "$SCENARIO" == "mcp_oauth" && "${DENSE_MEM_E2E_RUN_PLAYWRIGHT:-0}" == "1" ]]; then
  result_file="${DENSE_MEM_E2E_RESULT_FILE:-/results/${SCENARIO}-result.json}"
  [[ -f "$result_file" ]] || fail "OAuth scenario result handoff is missing"
  DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID="$(parse_json_root_field second_team_id < "$result_file")" || fail "OAuth scenario did not produce the Playwright team handoff"
  export DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID
fi

case "$SCENARIO" in
  mcp_oauth|community|conflict_queue|synchronous_write|full) run_playwright ;;
esac

log "completed"
printf '%s\n' "${SCENARIO} passed"
