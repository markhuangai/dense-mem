SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE=""

prepare_synchronous_write_files() {
  if [[ "$E2E_SCENARIO" != "synchronous_write" ]]; then
    return
  fi

  local slice="${DENSE_MEM_E2E_WRITE_SLICE:-legacy}"
  case "$slice" in
    legacy|remember|correction|conflict|dream|reconciliation|diagnostics|contract)
      ;;
    *)
      echo "DENSE_MEM_E2E_WRITE_SLICE must be one of legacy, remember, correction, conflict, dream, reconciliation, diagnostics, or contract." >&2
      return 1
      ;;
  esac

  SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE="${ROOT_DIR}/docker-compose.synchronous-write-e2e-${E2E_FILE_ID}.yml"
  node - "$SYNCHRONOUS_WRITE_COMPOSE_OVERLAY_FILE" "$ROOT_DIR" "$slice" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");

const [destination, rootDir, slice, marker] = process.argv.slice(2);
const quote = (value) => JSON.stringify(value);
const fixture = `${rootDir}/tests/uat/synchronous_write/provider-fixture.mjs`;
const fixtureMount = `${fixture}:/e2e/provider-fixture.mjs:ro`;
const contents = `${marker}
services:
  server:
    build:
      target: e2e
    environment:
      DENSE_MEM_E2E_WRITE_SLICE: ${quote(slice)}
      AI_API_EMBEDDING_TIMEOUT_SECONDS: "2"
      AI_VERIFIER_TIMEOUT_SECONDS: "2"
      AI_API_URL: http://synchronous-write-provider:8787/v1
      AI_VERIFIER_API_URL: http://synchronous-write-provider:8787/v1
  synchronous-write-provider:
    image: node:22-alpine
    working_dir: /e2e
    command: ["node", "/e2e/provider-fixture.mjs"]
    environment:
      DENSE_MEM_E2E_PROVIDER_FAULT: "\${DENSE_MEM_E2E_PROVIDER_FAULT:-none}"
      DENSE_MEM_E2E_PROVIDER_DIMENSIONS: "\${AI_API_EMBEDDING_DIMENSIONS:-1536}"
      DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS: "5000"
    volumes:
      - ${quote(fixtureMount)}
`;
fs.writeFileSync(destination, contents);
NODE
}

run_synchronous_write_e2e() {
  local team_id="$1"
  local api_key="$2"
  local slice="${DENSE_MEM_E2E_WRITE_SLICE:-legacy}"
  echo "Running compose-backed synchronous-write contract cases with slice ${slice}."
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_WRITE_CASE="$slice" \
  node "$ROOT_DIR/tests/uat/synchronous_write/runner.mjs"
}
