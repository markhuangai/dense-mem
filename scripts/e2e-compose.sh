#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
CONTROL_URL="${DENSE_MEM_CONTROL_URL:-}"
USER_URL="${DENSE_MEM_USER_URL:-}"
TEMP_DOCKER_CONFIG=""

read_dotenv_value() {
  local key="$1"
  node - "$key" "${ROOT_DIR}/.env" <<'NODE'
const fs = require("node:fs");
const key = process.argv[2];
const file = process.argv[3];
if (!fs.existsSync(file)) {
  process.exit(0);
}
for (const line of fs.readFileSync(file, "utf8").split(/\r?\n/)) {
  const trimmed = line.trim();
  if (!trimmed || trimmed.startsWith("#")) {
    continue;
  }
  const index = trimmed.indexOf("=");
  if (index < 1) {
    continue;
  }
  if (trimmed.slice(0, index).trim() !== key) {
    continue;
  }
  let value = trimmed.slice(index + 1).trim();
  if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
    value = value.slice(1, -1);
  }
  process.stdout.write(value);
  process.exit(0);
}
NODE
}

url_from_addr() {
  local addr="$1"
  local fallback="$2"
  if [[ -z "${addr}" ]]; then
    printf '%s' "$fallback"
    return
  fi
  if [[ "$addr" == http://* || "$addr" == https://* ]]; then
    printf '%s' "$addr"
    return
  fi
  if [[ "$addr" == :* ]]; then
    printf 'http://127.0.0.1%s' "$addr"
    return
  fi
  if [[ "$addr" == 0.0.0.0:* ]]; then
    printf 'http://127.0.0.1:%s' "${addr##*:}"
    return
  fi
  printf 'http://%s' "$addr"
}

json_field() {
  local field="$1"
  node -e '
let input = "";
process.stdin.on("data", chunk => input += chunk);
process.stdin.on("end", () => {
  const value = JSON.parse(input)[process.argv[1]];
  if (value === undefined || value === null || value === "") {
    process.exit(1);
  }
  process.stdout.write(String(value));
});
' "$field"
}

wait_for_url() {
  local label="$1"
  local url="$2"
  local header="${3:-}"
  for _ in $(seq 1 90); do
    if [[ -n "$header" ]]; then
      if curl -fsS -H "$header" "$url" >/dev/null 2>&1; then
        return 0
      fi
    elif curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "Timed out waiting for ${label} at ${url}" >&2
  return 1
}

cleanup() {
  local status=$?
  if [[ "${DENSE_MEM_E2E_KEEP_COMPOSE:-0}" != "1" ]]; then
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null
  fi
  if [[ -n "$TEMP_DOCKER_CONFIG" && -d "$TEMP_DOCKER_CONFIG" ]]; then
    rm -r "$TEMP_DOCKER_CONFIG"
  fi
  exit "$status"
}

if [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "Missing docker-compose.yml at ${COMPOSE_FILE}" >&2
  exit 1
fi

CONTROL_TOKEN="${CONTROL_PORTAL_TOKEN:-$(read_dotenv_value CONTROL_PORTAL_TOKEN)}"
if [[ -z "$CONTROL_TOKEN" ]]; then
  echo "CONTROL_PORTAL_TOKEN must be set in the environment or .env for compose e2e." >&2
  exit 1
fi

CONTROL_ADDR="${CONTROL_HTTP_ADDR:-$(read_dotenv_value CONTROL_HTTP_ADDR)}"
HTTP_ADDR="${HTTP_ADDR:-$(read_dotenv_value HTTP_ADDR)}"
CONTROL_URL="${CONTROL_URL:-$(url_from_addr "$CONTROL_ADDR" "http://127.0.0.1:8090")}"
USER_URL="${USER_URL:-$(url_from_addr "$HTTP_ADDR" "http://127.0.0.1:8080")}"

cd "$ROOT_DIR"

if [[ -z "${DENSE_MEM_E2E_DOCKER_CONFIG:-}" ]]; then
  TEMP_DOCKER_CONFIG="$(mktemp -d)"
  printf '{}\n' > "${TEMP_DOCKER_CONFIG}/config.json"
  export DOCKER_CONFIG="$TEMP_DOCKER_CONFIG"
else
  export DOCKER_CONFIG="$DENSE_MEM_E2E_DOCKER_CONFIG"
fi
export DOCKER_BUILDKIT=1
export COMPOSE_DOCKER_CLI_BUILD=1

echo "Stopping existing compose stack and removing volumes for a clean e2e run."
docker compose -f "$COMPOSE_FILE" down -v --remove-orphans

trap cleanup EXIT

echo "Starting fresh compose stack from docker-compose.yml."
docker compose -f "$COMPOSE_FILE" up -d --build

wait_for_url "main API readiness" "${USER_URL}/ready"
wait_for_url "control portal API" "${CONTROL_URL}/control/api/session" "Authorization: Bearer ${CONTROL_TOKEN}"

echo "Seeding e2e team through the compose server container."
seed_json="$(docker compose -f "$COMPOSE_FILE" exec -T server /app/provision-team --name "E2E Team" --description "compose e2e seed" --rate-limit 300)"
team_id="$(printf '%s' "$seed_json" | json_field team_id)"
api_key="$(printf '%s' "$seed_json" | json_field api_key)"

echo "Running compose-backed Playwright tests."
DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
DENSE_MEM_USER_URL="$USER_URL" \
DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
DENSE_MEM_E2E_TEAM_ID="$team_id" \
DENSE_MEM_E2E_TEAM_NAME="E2E Team" \
DENSE_MEM_E2E_API_KEY="$api_key" \
npm --prefix "$ROOT_DIR/web" run playwright:compose
