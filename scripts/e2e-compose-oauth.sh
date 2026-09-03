E2E_OAUTH_DIR=""
E2E_OAUTH_PROVIDER_PORT=""
E2E_OAUTH_HARNESS_PORT=""
E2E_OAUTH_FIXTURE_TOKEN=""
E2E_OAUTH_HARNESS_IMAGE=""
E2E_OAUTH_SESSION_TOKEN=""
E2E_OAUTH_CSRF_TOKEN=""
E2E_OAUTH_SECOND_TEAM_ID=""

validate_oauth_port_override() {
  local field="$1"
  local value="$2"
  if [[ -z "$value" ]]; then
    return 0
  fi
  if [[ "$value" =~ ^[0-9]{1,5}$ ]] && (( 10#$value >= 1 && 10#$value <= 65535 )); then
    return 0
  fi
  echo "${field} must be a numeric TCP port between 1 and 65535." >&2
  return 1
}

oauth_compatibility_e2e_enabled() {
  [[ "$E2E_SCENARIO" == "oauth_provider_compatibility" ]]
}

mcp_oauth_e2e_enabled() {
  [[ "$E2E_SCENARIO" == "mcp_oauth" ]]
}

prepare_oauth_compatibility_files() {
  if ! oauth_compatibility_e2e_enabled; then
    return
  fi
  if [[ -e "$E2E_OAUTH_DIR" ]]; then
    echo "Refusing to replace existing OAuth compatibility directory ${E2E_OAUTH_DIR}." >&2
    return 1
  fi
  mkdir "$E2E_OAUTH_DIR"
  printf '%s\n' "$E2E_MARKER" > "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${E2E_OAUTH_DIR}/server.key" \
    -out "${E2E_OAUTH_DIR}/ca.pem" \
    -days 1 \
    -subj "/CN=oauth-compatibility-e2e" \
    -addext "subjectAltName=DNS:oauth-provider-mock,DNS:oauth-compat-harness,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
  chmod 600 "${E2E_OAUTH_DIR}/server.key"
  chmod 644 "${E2E_OAUTH_DIR}/ca.pem"
  E2E_OAUTH_FIXTURE_TOKEN="oauth-fixture-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"

  node - "${E2E_OAUTH_DIR}/config.json" <<'NODE'
const fs = require("node:fs");
const destination = process.argv[2];
const issuerBase = "https://oauth-provider-mock:9444";
const profiles = [
  {
    name: "entra",
    issuer: `${issuerBase}/entra`,
    protected_resource: {
      audiences: ["dense-mem-entra", "api://dense-mem-entra"],
      jwks_source: "discovery",
      jwks_uri: "",
      algorithms: ["RS256"],
      scope_claim: "scp",
      scope_mappings: [
        { external_scope: "memory.read", internal_scopes: ["read"] },
        { external_scope: "memory.write", internal_scopes: ["write"] },
      ],
      team_claim: "tenant",
    },
  },
  {
    name: "pingone",
    issuer: `${issuerBase}/pingone`,
    protected_resource: {
      audiences: ["urn:dense-mem:pingone"],
      jwks_source: "static",
      jwks_uri: `${issuerBase}/pingone/jwks`,
      algorithms: ["PS256"],
      scope_claim: "scope",
      scope_mappings: [
        { external_scope: "ping.read", internal_scopes: ["read"] },
        { external_scope: "ping.write", internal_scopes: ["write"] },
      ],
      team_claim: "tenant",
    },
  },
  {
    name: "generic",
    issuer: `${issuerBase}/generic/`,
    protected_resource: {
      audiences: ["https://dense-mem.example.test/mcp"],
      jwks_source: "discovery",
      jwks_uri: "",
      algorithms: ["ES256"],
      scope_claim: "permissions",
      scope_mappings: [
        { external_scope: "generic.read", internal_scopes: ["read"] },
        { external_scope: "generic.write", internal_scopes: ["write"] },
      ],
      team_claim: "tenant",
    },
  },
];
fs.writeFileSync(destination, `${JSON.stringify({ profiles }, null, 2)}\n`, { mode: 0o644 });
NODE

  E2E_COMPOSE_OVERLAY_FILE="${ROOT_DIR}/docker-compose.oauth-e2e-${E2E_FILE_ID}.yml"
  E2E_OAUTH_HARNESS_IMAGE="densemem-e2e-${E2E_FILE_ID}-oauth-compat-harness:latest"
  node - "$E2E_COMPOSE_OVERLAY_FILE" "$ROOT_DIR" "$E2E_OAUTH_DIR" "$E2E_OAUTH_PROVIDER_PORT" "$E2E_OAUTH_HARNESS_PORT" "$E2E_OAUTH_FIXTURE_TOKEN" "$E2E_OAUTH_HARNESS_IMAGE" "$E2E_MARKER" "$(id -u)" "$(id -g)" <<'NODE'
const fs = require("node:fs");

const [destination, rootDir, oauthDir, providerPort, harnessPort, fixtureToken, harnessImage, marker, hostUID, hostGID] = process.argv.slice(2);
const quote = (value) => JSON.stringify(value);
const mockScript = `${oauthDir}/oauth-provider-mock.mjs`;
fs.copyFileSync(`${rootDir}/tests/uat/oauth_provider_mock.mjs`, mockScript);
const contents = `${marker}
services:
  oauth-provider-mock:
    image: node:26-alpine
    working_dir: /e2e
    command: ["node", "/e2e/oauth-provider-mock.mjs"]
    environment:
      DENSE_MEM_OAUTH_CERT: /e2e/ca.pem
      DENSE_MEM_OAUTH_KEY: /e2e/server.key
      DENSE_MEM_OAUTH_ISSUER_BASE: https://oauth-provider-mock:9444
      DENSE_MEM_OAUTH_FIXTURE_TOKEN: ${quote(fixtureToken)}
    volumes:
      - e2e-oauth-files:/e2e
    ports:
      - "127.0.0.1:${providerPort}:9444"
    healthcheck:
      test: ["CMD", "node", "-e", "require('node:https').get({hostname:'127.0.0.1',port:9444,path:'/health',rejectUnauthorized:false},r=>process.exit(r.statusCode===200?0:1)).on('error',()=>process.exit(1))"]
      interval: 1s
      timeout: 2s
      retries: 60
  oauth-compat-harness:
    build:
      context: ${quote(rootDir)}
      dockerfile: cmd/oauth-compat-harness/Dockerfile
    image: ${quote(harnessImage)}
    user: ${quote(`${hostUID}:${hostGID}`)}
    command:
      - --listen=:9445
      - --public-base-url=https://localhost:${harnessPort}
      - --config=/e2e/config.json
      - --tls-cert=/e2e/ca.pem
      - --tls-key=/e2e/server.key
    environment:
      SSL_CERT_FILE: /e2e/ca.pem
    volumes:
      - e2e-oauth-files:/e2e
    depends_on:
      oauth-provider-mock:
        condition: service_healthy
    ports:
      - "127.0.0.1:${harnessPort}:9445"
    healthcheck:
      test: ["CMD-SHELL", "wget --no-check-certificate -q -O /dev/null https://127.0.0.1:9445/health"]
      interval: 1s
      timeout: 2s
      retries: 60
volumes:
  e2e-oauth-files:
`;
fs.writeFileSync(destination, contents);
NODE
}

wait_for_oauth_compatibility_services() {
  for _ in $(seq 1 90); do
    if curl --cacert "${E2E_OAUTH_DIR}/ca.pem" -fsS "https://localhost:${E2E_OAUTH_PROVIDER_PORT}/health" >/dev/null 2>&1 && \
      curl --cacert "${E2E_OAUTH_DIR}/ca.pem" -fsS "https://localhost:${E2E_OAUTH_HARNESS_PORT}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "Timed out waiting for the OAuth compatibility services." >&2
  return 1
}

run_oauth_compatibility_e2e() {
  local api_key="$1"
  wait_for_oauth_compatibility_services
  echo "Running compose-backed OAuth protected-resource compatibility e2e."
  NODE_EXTRA_CA_CERTS="${E2E_OAUTH_DIR}/ca.pem" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_OAUTH_HARNESS_URL="https://localhost:${E2E_OAUTH_HARNESS_PORT}" \
  DENSE_MEM_E2E_OAUTH_MOCK_URL="https://localhost:${E2E_OAUTH_PROVIDER_PORT}" \
  DENSE_MEM_E2E_OAUTH_ISSUER_BASE="https://oauth-provider-mock:9444" \
  DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN="$E2E_OAUTH_FIXTURE_TOKEN" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  DENSE_MEM_E2E_OAUTH_COMPOSE_FILE="$E2E_COMPOSE_OVERLAY_FILE" \
  node "$ROOT_DIR/tests/uat/oauth_provider_compatibility_e2e.mjs"
}

prepare_mcp_oauth_files() {
  if ! mcp_oauth_e2e_enabled; then
    return
  fi
  if [[ -e "$E2E_OAUTH_DIR" ]]; then
    echo "Refusing to replace existing MCP OAuth directory ${E2E_OAUTH_DIR}." >&2
    return 1
  fi
  mkdir "$E2E_OAUTH_DIR"
  printf '%s\n' "$E2E_MARKER" > "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${E2E_OAUTH_DIR}/server.key" \
    -out "${E2E_OAUTH_DIR}/ca.pem" \
    -days 1 \
    -subj "/CN=mcp-oauth-e2e" \
    -addext "subjectAltName=DNS:oauth-provider-mock,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
  chmod 600 "${E2E_OAUTH_DIR}/server.key"
  chmod 644 "${E2E_OAUTH_DIR}/ca.pem"
  E2E_OAUTH_FIXTURE_TOKEN="oauth-fixture-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"
  E2E_OAUTH_SESSION_TOKEN="oauth-session-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"
  E2E_OAUTH_CSRF_TOKEN="oauth-csrf-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"

  E2E_COMPOSE_OVERLAY_FILE="${ROOT_DIR}/docker-compose.mcp-oauth-e2e-${E2E_FILE_ID}.yml"
  node - "$E2E_COMPOSE_OVERLAY_FILE" "$ROOT_DIR" "$E2E_OAUTH_DIR" "$E2E_OAUTH_PROVIDER_PORT" "$E2E_OAUTH_FIXTURE_TOKEN" "$E2E_MARKER" <<'NODE'
const fs = require("node:fs");

const [destination, rootDir, oauthDir, providerPort, fixtureToken, marker] = process.argv.slice(2);
const quote = (value) => JSON.stringify(value);
const mockScript = `${oauthDir}/oauth-provider-mock.mjs`;
fs.copyFileSync(`${rootDir}/tests/uat/oauth_provider_mock.mjs`, mockScript);
const contents = `${marker}
services:
  server:
    environment:
      SSL_CERT_FILE: /e2e/oauth-files/ca.pem
    volumes:
      - e2e-oauth-files:/e2e/oauth-files:ro
    depends_on:
      oauth-provider-mock:
        condition: service_healthy
  oauth-provider-mock:
    image: node:26-alpine
    working_dir: /e2e
    command: ["node", "/e2e/oauth-provider-mock.mjs"]
    environment:
      DENSE_MEM_OAUTH_CERT: /e2e/ca.pem
      DENSE_MEM_OAUTH_KEY: /e2e/server.key
      DENSE_MEM_OAUTH_ISSUER_BASE: https://oauth-provider-mock:9444
      DENSE_MEM_OAUTH_FIXTURE_TOKEN: ${quote(fixtureToken)}
    volumes:
      - e2e-oauth-files:/e2e
    ports:
      - "127.0.0.1:${providerPort}:9444"
    healthcheck:
      test: ["CMD", "node", "-e", "require('node:https').get({hostname:'127.0.0.1',port:9444,path:'/health',rejectUnauthorized:false},r=>process.exit(r.statusCode===200?0:1)).on('error',()=>process.exit(1))"]
      interval: 1s
      timeout: 2s
      retries: 60
volumes:
  e2e-oauth-files:
`;
fs.writeFileSync(destination, contents);
NODE
}

prepare_oauth_fixture_volume() {
  if ! oauth_compatibility_e2e_enabled && ! mcp_oauth_e2e_enabled; then
    return
  fi

  local container_id
  local volume_name
  compose create oauth-provider-mock >/dev/null
  container_id="$(compose ps -aq oauth-provider-mock)"
  if [[ -z "$container_id" ]]; then
    echo "Failed to create the E2E OAuth provider fixture volume." >&2
    return 1
  fi
  volume_name="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/e2e"}}{{.Name}}{{end}}{{end}}' "$container_id")"
  if [[ -z "$volume_name" ]]; then
    echo "Failed to resolve the E2E OAuth provider fixture volume." >&2
    return 1
  fi
  docker cp "${E2E_OAUTH_DIR}/." "${container_id}:/e2e"
  docker run --rm -v "${volume_name}:/e2e" alpine:3.24 sh -ec \
    'for path in /e2e/oauth-provider-mock.mjs /e2e/ca.pem /e2e/server.key /e2e/config.json; do
       if [ -e "$path" ]; then chmod 644 "$path"; fi
     done'
}

wait_for_mcp_oauth_provider() {
  for _ in $(seq 1 90); do
    if curl --cacert "${E2E_OAUTH_DIR}/ca.pem" -fsS "https://localhost:${E2E_OAUTH_PROVIDER_PORT}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "Timed out waiting for the MCP OAuth provider mock." >&2
  return 1
}

run_mcp_oauth_e2e() {
  local team_id="$1"
  local api_key="$2"
  local result_file="${TEMP_DIR}/mcp-oauth-result.json"

  wait_for_mcp_oauth_provider
  echo "Running compose-backed MCP OAuth protected-resource e2e."
  NODE_EXTRA_CA_CERTS="${E2E_OAUTH_DIR}/ca.pem" \
  DENSE_MEM_CONTROL_URL="$CONTROL_URL" \
  DENSE_MEM_USER_URL="$USER_URL" \
  DENSE_MEM_E2E_MCP_PUBLIC_BASE_URL="$USER_URL" \
  DENSE_MEM_CONTROL_TOKEN="$CONTROL_TOKEN" \
  DENSE_MEM_E2E_TEAM_ID="$team_id" \
  DENSE_MEM_E2E_API_KEY="$api_key" \
  DENSE_MEM_E2E_OAUTH_INTERNAL_URL="https://oauth-provider-mock:9444" \
  DENSE_MEM_E2E_OAUTH_MOCK_URL="https://localhost:${E2E_OAUTH_PROVIDER_PORT}" \
  DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN="$E2E_OAUTH_FIXTURE_TOKEN" \
  DENSE_MEM_E2E_SSO_SESSION_TOKEN="$E2E_OAUTH_SESSION_TOKEN" \
  DENSE_MEM_E2E_SSO_CSRF_TOKEN="$E2E_OAUTH_CSRF_TOKEN" \
  DENSE_MEM_E2E_RESULT_FILE="$result_file" \
  DENSE_MEM_E2E_COMPOSE_PROJECT="$COMPOSE_PROJECT_NAME" \
  DENSE_MEM_E2E_COMPOSE_FILE="$COMPOSE_FILE" \
  node "$ROOT_DIR/tests/uat/oauth_mcp_e2e.mjs"

  if [[ "${DENSE_MEM_E2E_SKIP_PLAYWRIGHT:-0}" == "1" ]]; then
    return
  fi
  E2E_OAUTH_SECOND_TEAM_ID="$(json_field second_team_id < "$result_file")"
  echo "Running compose-backed OAuth team-resource Playwright e2e."
  run_compose_playwright_tests oauth
}

cleanup_oauth_compatibility_files() {
  if [[ -n "$E2E_OAUTH_DIR" && -f "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker" ]] && is_generated_marker_file "${E2E_OAUTH_DIR}/.dense-mem-e2e-marker"; then
    rm -r "$E2E_OAUTH_DIR"
  fi
}
