#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
UAT_DIR="${ROOT_DIR}/tests/uat"
BASE_URL="${BASE_URL:-http://localhost:8080}"
SUFFIX="$(date -u +%Y%m%d%H%M%S)-$$"
TEAM_A_NAME="${UAT_TEAM_A_NAME:-uat-e2e-a-${SUFFIX}}"
TEAM_B_NAME="${UAT_TEAM_B_NAME:-uat-e2e-b-${SUFFIX}}"

if ! (cd "${ROOT_DIR}" && docker compose exec -T server /bin/sh -c 'test -x /app/provision-team') >/dev/null 2>&1; then
  {
    echo "Dense-Mem compose server is not ready."
    echo "Start it first, for example: docker compose up -d --build"
  } >&2
  exit 1
fi

parse_field() {
  local json_input="$1"
  local field="$2"
  JSON_INPUT="${json_input}" FIELD="${field}" node <<'NODE'
const data = JSON.parse(process.env.JSON_INPUT || "{}");
const field = process.env.FIELD || "";
const value = data[field];
if (typeof value !== "string" || value.length === 0) {
  console.error(`provision-team output missing ${field}`);
  process.exit(1);
}
process.stdout.write(value);
NODE
}

provision_team() {
  local name="$1"
  (cd "${ROOT_DIR}" && docker compose exec -T server /app/provision-team --name "${name}")
}

primary_json="$(provision_team "${TEAM_A_NAME}")"
secondary_json="$(provision_team "${TEAM_B_NAME}")"

primary_key="$(parse_field "${primary_json}" "api_key")"
secondary_key="$(parse_field "${secondary_json}" "api_key")"
primary_team_id="$(parse_field "${primary_json}" "team_id")"

if [ "$#" -eq 0 ]; then
  set -- e2e-journey.spec.ts
fi

(
  cd "${UAT_DIR}"
  BASE_URL="${BASE_URL}" \
  API_KEY="${primary_key}" \
  DENSE_MEM_API_KEY="${primary_key}" \
  API_KEY_B="${secondary_key}" \
  PROFILE_ID="${primary_team_id}" \
  REQUIRE_API_KEY_B=1 \
    npx playwright test "$@"
)
