E2E_OAUTH_DIR=""
E2E_OAUTH_PORT=""
E2E_OAUTH_FIXTURE_TOKEN=""
E2E_OAUTH_SESSION_TOKEN=""
E2E_OAUTH_CSRF_TOKEN=""
E2E_OAUTH_SECOND_TEAM_ID=""

oauth_e2e_enabled() {
  [[ "$E2E_SCENARIO" == "oauth_provider_compatibility" || "$E2E_SCENARIO" == "mcp_oauth" ]]
}

prepare_oauth_mock_files() {
  if ! oauth_e2e_enabled; then
    return
  fi
  if [[ -e "$E2E_OAUTH_DIR" ]]; then
    echo "Refusing to replace existing OAuth mock directory ${E2E_OAUTH_DIR}." >&2
    return 1
  fi
  mkdir "$E2E_OAUTH_DIR"
  printf '%s\n' "$E2E_MARKER" > "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${E2E_OAUTH_DIR}/server.key" \
    -out "${E2E_OAUTH_DIR}/ca.pem" \
    -days 1 \
    -subj "/CN=oauth-provider-mock" \
    -addext "subjectAltName=DNS:oauth-provider-mock,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
  chmod 600 "${E2E_OAUTH_DIR}/server.key"
  chmod 644 "${E2E_OAUTH_DIR}/ca.pem"
  E2E_OAUTH_FIXTURE_TOKEN="oauth-fixture-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"
  E2E_OAUTH_SESSION_TOKEN="oauth-session-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"
  E2E_OAUTH_CSRF_TOKEN="oauth-csrf-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"
  E2E_COMPOSE_OVERLAY_FILE="${ROOT_DIR}/docker-compose.oauth-e2e-${E2E_FILE_ID}.yml"
  node - "$E2E_COMPOSE_OVERLAY_FILE" "$ROOT_DIR" "$E2E_OAUTH_DIR" "$E2E_OAUTH_PORT" "$E2E_OAUTH_FIXTURE_TOKEN" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");

const [destination, rootDir, oauthDir, oauthPort, fixtureToken, marker] = process.argv.slice(2);
const quote = (value) => JSON.stringify(value);
const mount = (source, target) => quote(`${source}:${target}:ro`);
const mockScript = `${oauthDir}/oauth-provider-mock.mjs`;
fs.copyFileSync(`${rootDir}/web/tests-compose/oauth-provider-mock.mjs`, mockScript);
const contents = `${marker}
services:
  server:
    environment:
      SSL_CERT_FILE: /e2e/oauth-ca.pem
    volumes:
      - ${mount(`${oauthDir}/ca.pem`, "/e2e/oauth-ca.pem")}
    depends_on:
      oauth-provider-mock:
        condition: service_healthy
  oauth-provider-mock:
    image: node:24-alpine
    working_dir: /e2e
    command: ["node", "/e2e/oauth-provider-mock.mjs"]
    environment:
      DENSE_MEM_OAUTH_CERT: /e2e/tls/ca.pem
      DENSE_MEM_OAUTH_KEY: /e2e/tls/server.key
      DENSE_MEM_OAUTH_ISSUER_BASE: https://oauth-provider-mock:9444
      DENSE_MEM_OAUTH_FIXTURE_TOKEN: ${quote(fixtureToken)}
    volumes:
      - ${mount(mockScript, "/e2e/oauth-provider-mock.mjs")}
      - ${mount(oauthDir, "/e2e/tls")}
    ports:
      - "127.0.0.1:${oauthPort}:9444"
    healthcheck:
      test: ["CMD", "node", "-e", "require('node:https').get({hostname:'127.0.0.1',port:9444,path:'/health',rejectUnauthorized:false},r=>process.exit(r.statusCode===200?0:1)).on('error',()=>process.exit(1))"]
      interval: 1s
      timeout: 2s
      retries: 60
`;
fs.writeFileSync(destination, contents);
NODE
}

wait_for_oauth_provider_mock() {
  for _ in $(seq 1 90); do
    if curl --cacert "${E2E_OAUTH_DIR}/ca.pem" -fsS "https://localhost:${E2E_OAUTH_PORT}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "Timed out waiting for the local OAuth provider mock." >&2
  return 1
}

run_oauth_e2e() {
  local team_id="$1"
  local credential_id="$2"
  local api_key="$3"
  local result_file="${TEMP_DIR}/oauth-e2e-result.json"

  wait_for_oauth_provider_mock
  echo "Running compose-backed ${E2E_SCENARIO} e2e."
  NODE_EXTRA_CA_CERTS="${E2E_OAUTH_DIR}/ca.pem" \
  DENSE_MEM_E2E_SCENARIO="$E2E_SCENARIO" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_CREDENTIAL_ID="$credential_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_OAUTH_INTERNAL_URL="https://oauth-provider-mock:9444" \
  DENSE_MEM_E2E_OAUTH_MOCK_URL="https://localhost:${E2E_OAUTH_PORT}" \
  DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN="$E2E_OAUTH_FIXTURE_TOKEN" \
  DENSE_MEM_E2E_SSO_SESSION_TOKEN="$E2E_OAUTH_SESSION_TOKEN" \
  DENSE_MEM_E2E_SSO_CSRF_TOKEN="$E2E_OAUTH_CSRF_TOKEN" \
  DENSE_MEM_E2E_RESULT_FILE="$result_file" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  node "$ROOT_DIR/tests/uat/oauth_mcp_e2e.mjs"

  if [[ "$E2E_SCENARIO" != "mcp_oauth" || "${DENSE_MEM_E2E_SKIP_PLAYWRIGHT:-0}" == "1" ]]; then
    return
  fi
  E2E_OAUTH_SECOND_TEAM_ID="$(json_file_field "$result_file" second_team_id)"
  echo "Running compose-backed OAuth team-resource Playwright e2e."
  run_compose_playwright_tests oauth
}

json_file_field() {
  local path="$1"
  local field="$2"
  node -e '
    const fs = require("node:fs");
    const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"))[process.argv[2]];
    if (typeof value !== "string" || value.length === 0) process.exit(1);
    process.stdout.write(value);
  ' "$path" "$field"
}

cleanup_oauth_e2e_files() {
  if [[ -n "$E2E_OAUTH_DIR" && -f "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker" ]] && is_generated_marker_file "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker"; then
    rm -r "$E2E_OAUTH_DIR"
  fi
}
