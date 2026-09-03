#!/usr/bin/env bash
# Sourced by e2e-host-controller.sh.

CONFLICT_PROVIDER_EMBEDDING_MODEL="dense-mem-conflict-e2e-embedding"

run_go_source_container() (
  local source_dir="$1" image="$2" project="$3" run_id="$4" attempt="$5" phase="$6" scenario="$7" digest="$8" network="$9" docker_socket="${10}" redact_env_file="${11}"
  shift 11
  local -a redaction_values=()
  while [[ "$1" != "--" ]]; do
    redaction_values+=("$1")
    shift
  done
  shift
  local -a environment_args=()
  while [[ "$1" != "--" ]]; do
    environment_args+=(-e "$1")
    shift
  done
  shift
  local -a command=("$@")
  local container_name="${project}-driver-${BASHPID}"
  local container=""
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local -a docker_args=(
    create --name "$container_name"
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}"
    --label "io.dense-mem.ci.repository=${REPOSITORY}"
    --label "io.dense-mem.ci.run-id=${run_id}"
    --label "io.dense-mem.ci.run-attempt=${attempt}"
    --label "io.dense-mem.ci.phase=${phase}"
    --label "io.dense-mem.ci.scenario=${scenario}"
    --label "io.dense-mem.ci.image-digest=${digest}"
    --label "io.dense-mem.ci.created-at=${created_at}"
    --label "io.dense-mem.ci.compose-project=${project}"
    --network "$network"
    --workdir /workspace
  )
  if [[ -n "$docker_socket" ]]; then
    docker_args+=(
      --mount "type=bind,source=${docker_socket},target=${docker_socket}"
      -e "DOCKER_HOST=unix://${docker_socket}"
    )
  fi
  docker_args+=("${environment_args[@]}" "$image" "${command[@]}")

  cleanup_go_container() {
    local status=$?
    trap - EXIT INT TERM
    if [[ -n "$container" ]]; then
      docker rm -f "$container" >/dev/null 2>&1 || status=1
    fi
    exit "$status"
  }
  trap cleanup_go_container EXIT INT TERM
  container="$(docker "${docker_args[@]}")"
  copy_git_source "$container" "$source_dir"
  set +e
  docker start --attach "$container" 2>&1 |
    redact_diagnostics "$redact_env_file" "${redaction_values[@]}"
  local -a pipeline_status=("${PIPESTATUS[@]}")
  set -e
  ((pipeline_status[1] == 0)) || fail "diagnostic redaction failed for ${scenario}"
  local result_status="${pipeline_status[0]}"
  trap - EXIT INT TERM
  docker rm "$container" >/dev/null
  container=""
  return "$result_status"
)

build_conflict_review_driver() (
  local source_dir="$1" image="$2" project="$3" run_id="$4" attempt="$5" phase="$6" scenario="$7"
  local container_name="${project}-driver-${BASHPID}"
  local container=""
  local output="${DENSE_MEM_CI_HELPER_DIR}/conflict-review-driver"
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local -a docker_args=(
    create --name "$container_name"
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}"
    --label "io.dense-mem.ci.repository=${REPOSITORY}"
    --label "io.dense-mem.ci.run-id=${run_id}"
    --label "io.dense-mem.ci.run-attempt=${attempt}"
    --label "io.dense-mem.ci.phase=${phase}"
    --label "io.dense-mem.ci.scenario=${scenario}"
    --label "io.dense-mem.ci.image-digest=${DENSE_MEM_CI_IMAGE_DIGEST}"
    --label "io.dense-mem.ci.created-at=${created_at}"
    --label "io.dense-mem.ci.compose-project=${project}"
    --workdir /workspace
    "$image"
    bash -euc
    'mkdir -p /tmp/dense-mem-helper && go build -o /tmp/dense-mem-helper/conflict-review-driver ./tests/uat/conflict_review_driver'
  )

  cleanup_conflict_driver() {
    local status=$?
    trap - EXIT INT TERM
    if [[ -n "$container" ]]; then
      docker rm -f "$container" >/dev/null 2>&1 || status=1
    fi
    exit "$status"
  }
  trap cleanup_conflict_driver EXIT INT TERM
  container="$(docker "${docker_args[@]}")"
  copy_git_source "$container" "$source_dir"
  set +e
  docker start --attach "$container" 2>&1 |
    redact_diagnostics "$ENV_FILE"
  local -a pipeline_status=("${PIPESTATUS[@]}")
  set -e
  ((pipeline_status[1] == 0)) || fail "diagnostic redaction failed for conflict review helper"
  ((pipeline_status[0] == 0)) || fail "failed to build the conflict review helper"
  docker cp "$container:/tmp/dense-mem-helper/conflict-review-driver" "$output" >/dev/null ||
    fail "failed to copy the conflict review helper"
  chmod 700 "$output"
  trap - EXIT INT TERM
  docker rm "$container" >/dev/null
  container=""
)

prepare_stack_helpers() {
  local project="$1" source_dir="$2" helpers="$3" run_id="$4" attempt="$5" phase="$6" scenario="$7"
  DENSE_MEM_CI_HELPER_DIR="${JOB_DIR}/${run_id}-${attempt}/${phase}-${scenario}-helpers"
  DENSE_MEM_CI_PRIVATE_DIR="${JOB_DIR}/${run_id}-${attempt}/${phase}-${scenario}-private"
  DENSE_MEM_CI_COMPOSE_OVERLAY_FILE=""
  mkdir -p "$DENSE_MEM_CI_HELPER_DIR" "$DENSE_MEM_CI_PRIVATE_DIR"
  chmod 700 "$DENSE_MEM_CI_HELPER_DIR" "$DENSE_MEM_CI_PRIVATE_DIR"
  [[ -d "$source_dir" && "$source_dir" == /* ]] || fail "helper source directory must be an absolute directory"

  local oauth_token=""
  local harness_image=""
  local provider_dimensions
  provider_dimensions="$(env_value AI_API_EMBEDDING_DIMENSIONS 2>/dev/null || printf '%s' 1536)"
  if has_helper "$helpers" oauth || has_helper "$helpers" oauth_compatibility; then
    require_command openssl
    [[ -f "${source_dir}/tests/uat/oauth_provider_mock.mjs" ]] || fail "missing OAuth provider fixture"
    openssl req -x509 -newkey rsa:2048 -nodes \
      -keyout "${DENSE_MEM_CI_PRIVATE_DIR}/server.key" \
      -out "${DENSE_MEM_CI_HELPER_DIR}/ca.pem" \
      -days 1 \
      -subj "/CN=dense-mem-ci-oauth" \
      -addext "subjectAltName=DNS:oauth-provider-mock,DNS:oauth-compat-harness,DNS:entra-mock,DNS:server,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1 ||
      fail "failed to create the OAuth fixture certificate"
    chmod 600 "${DENSE_MEM_CI_PRIVATE_DIR}/server.key"
    chmod 644 "${DENSE_MEM_CI_HELPER_DIR}/ca.pem"
    oauth_token="oauth-fixture-$(node -e 'process.stdout.write(require("node:crypto").randomBytes(24).toString("base64url"))')"
    node - "${DENSE_MEM_CI_HELPER_DIR}/config.json" <<'NODE'
const fs = require("node:fs");
const destination = process.argv[2];
const issuer = "https://oauth-provider-mock:9444";
const profiles = [
  {
    name: "entra",
    issuer: `${issuer}/entra`,
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
    issuer: `${issuer}/pingone`,
    protected_resource: {
      audiences: ["urn:dense-mem:pingone"],
      jwks_source: "static",
      jwks_uri: `${issuer}/pingone/jwks`,
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
    issuer: `${issuer}/generic/`,
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
    printf 'DENSE_MEM_E2E_OAUTH_FIXTURE_TOKEN=%s\nDENSE_MEM_E2E_OAUTH_INTERNAL_URL=https://oauth-provider-mock:9444\nDENSE_MEM_E2E_OAUTH_MOCK_URL=https://oauth-provider-mock:9444\nDENSE_MEM_E2E_OAUTH_ISSUER_BASE=https://oauth-provider-mock:9444\n' \
      "$oauth_token" > "${DENSE_MEM_CI_HELPER_DIR}/runtime.env"
    if has_helper "$helpers" oauth_compatibility; then
      require_command docker
      harness_image="${project}-oauth-compat-harness:latest"
      docker build --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}" \
        --label "io.dense-mem.ci.repository=${REPOSITORY}" \
        --label "io.dense-mem.ci.run-id=${run_id}" \
        --label "io.dense-mem.ci.run-attempt=${attempt}" \
        --label "io.dense-mem.ci.phase=${phase}" \
        --label "io.dense-mem.ci.scenario=${scenario}" \
        --label "io.dense-mem.ci.image-digest=${DENSE_MEM_CI_IMAGE_DIGEST}" \
        --label "io.dense-mem.ci.created-at=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        --label "com.docker.compose.project=${project}" \
        --tag "$harness_image" \
        --file "${source_dir}/cmd/oauth-compat-harness/Dockerfile" \
        "$source_dir" >/dev/null || fail "failed to build the OAuth compatibility helper"
      printf 'DENSE_MEM_E2E_OAUTH_HARNESS_URL=https://oauth-compat-harness:9445\n' >> "${DENSE_MEM_CI_HELPER_DIR}/runtime.env"
    fi
    chmod 600 "${DENSE_MEM_CI_HELPER_DIR}/runtime.env"
  fi

  if has_helper "$helpers" conflict_provider || has_helper "$helpers" conflict_review; then
    local go_image
    go_image="$(env_value DENSE_MEM_CI_GO_TEST_IMAGE 2>/dev/null || printf '%s' golang:1.26.6-bookworm)"
    [[ "$go_image" =~ ^[A-Za-z0-9._/:@-]+$ ]] || fail "invalid conflict review helper image"
    build_conflict_review_driver "$source_dir" "$go_image" "$project" "$run_id" "$attempt" "$phase" "$scenario"
  fi

  if [[ -n "$helpers" ]]; then
    DENSE_MEM_CI_COMPOSE_OVERLAY_FILE="${DENSE_MEM_CI_HELPER_DIR}/compose.yml"
    node - "$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE" "$helpers" "$oauth_token" "$harness_image" "$provider_dimensions" "$CONFLICT_PROVIDER_EMBEDDING_MODEL" <<'NODE'
const fs = require("node:fs");
const [destination, helpers, oauthToken, harnessImage, providerDimensions, conflictProviderEmbeddingModel] = process.argv.slice(2);
const has = (name) => new Set(helpers.split(",").filter(Boolean)).has(name);
const conflictProviderDimensions = has("synchronous_write") ? (providerDimensions || "1536") : "1536";
const lines = ["# dense-mem-ci-e2e.v1 generated helper overlay", "services:"];
const serverEnvironment = new Map();
const serverVolumes = [];
const helperServices = [];
if (has("verifier")) {
  for (const [key, value] of Object.entries({
    AI_VERIFIER_API_URL: "http://synchronous-write-provider:8787/v1",
    AI_VERIFIER_API_KEY: "dense-mem-e2e-verifier-key",
    AI_VERIFIER_MODEL: "dense-mem-e2e-verifier",
    AI_VERIFIER_DISABLE_TEMPERATURE: "true",
  })) serverEnvironment.set(key, value);
}
if (has("conflict_provider")) {
  for (const [key, value] of Object.entries({
    AI_API_URL: "http://conflict-provider:8081/v1",
    AI_API_KEY: "dense-mem-conflict-e2e-key",
    AI_API_EMBEDDING_MODEL: conflictProviderEmbeddingModel,
    AI_API_EMBEDDING_DIMENSIONS: conflictProviderDimensions,
    AI_VERIFIER_API_URL: "http://conflict-provider:8081/v1",
    AI_VERIFIER_API_KEY: "dense-mem-conflict-e2e-key",
    AI_VERIFIER_MODEL: "dense-mem-conflict-e2e-verifier",
    AI_VERIFIER_DISABLE_TEMPERATURE: "true",
  })) serverEnvironment.set(key, value);
  helperServices.push(["conflict-provider", ["    command: [\"sh\", \"-c\", \"sleep infinity\"]"]]);
}
if (has("synchronous_write")) {
  for (const [key, value] of Object.entries({
    AI_API_URL: "http://synchronous-write-provider:8787/v1",
    AI_API_KEY: "dense-mem-synchronous-write-e2e-key",
    AI_VERIFIER_API_URL: "http://synchronous-write-provider:8787/v1",
    AI_VERIFIER_API_KEY: "dense-mem-synchronous-write-e2e-key",
    AI_VERIFIER_MODEL: "dense-mem-synchronous-write-e2e-verifier",
    AI_API_EMBEDDING_TIMEOUT_SECONDS: "2",
    AI_VERIFIER_TIMEOUT_SECONDS: "2",
    AI_VERIFIER_DISABLE_TEMPERATURE: "true",
  })) serverEnvironment.set(key, value);
  helperServices.push(["synchronous-write-provider", [
    "    command: [\"sh\", \"-c\", \"sleep infinity\"]",
    "    environment:",
    `      DENSE_MEM_E2E_PROVIDER_DIMENSIONS: ${JSON.stringify(providerDimensions || "1536")}`,
    "      DENSE_MEM_E2E_PROVIDER_TIMEOUT_DELAY_MS: \"5000\"",
  ]]);
}
if (has("oauth") || has("oauth_compatibility")) {
  serverEnvironment.set("SSL_CERT_FILE", "/e2e/oauth-files/ca.pem");
  serverVolumes.push("oauth-provider-files:/e2e/oauth-files:ro");
  helperServices.push(["oauth-provider-mock", [
    "    command: [\"sh\", \"-c\", \"sleep infinity\"]",
    "    environment:",
    "      DENSE_MEM_OAUTH_CERT: /e2e/ca.pem",
    "      DENSE_MEM_OAUTH_KEY: /e2e/server.key",
    "      DENSE_MEM_OAUTH_ISSUER_BASE: https://oauth-provider-mock:9444",
    `      DENSE_MEM_OAUTH_FIXTURE_TOKEN: ${JSON.stringify(oauthToken)}`,
  ]]);
}
if (has("oauth_compatibility")) {
  helperServices.push(["oauth-compat-harness", [
    `    image: ${JSON.stringify(harnessImage)}`,
    "    command: [\"sh\", \"-c\", \"sleep infinity\"]",
    "    environment:",
    "      SSL_CERT_FILE: \"/e2e/ca.pem\"",
  ]]);
  helperServices.push(["entra-mock", [
    "    command: [\"sh\", \"-c\", \"sleep infinity\"]",
    "    environment:",
    "      DENSE_MEM_ENTRA_CERT: /e2e/ca.pem",
    "      DENSE_MEM_ENTRA_KEY: /e2e/server.key",
    "      DENSE_MEM_ENTRA_ISSUER: https://entra-mock:9443",
  ]]);
}
if (serverEnvironment.size > 0 || serverVolumes.length > 0) {
  lines.push("  server:");
  if (serverEnvironment.size > 0) {
    lines.push("    environment:");
    for (const [key, value] of serverEnvironment) lines.push(`      ${key}: ${JSON.stringify(value)}`);
  }
  if (serverVolumes.length > 0) {
    lines.push("    volumes:");
    for (const volume of serverVolumes) lines.push(`      - ${volume}`);
  }
}
for (const [name, serviceLines] of helperServices) lines.push(`  ${name}:`, ...serviceLines);
if (lines.length === 2) lines[1] = "services: {}";
lines.push("");
fs.writeFileSync(destination, `${lines.join("\n")}\n`, { mode: 0o600 });
NODE
  fi
  printf '%s\n' "$DENSE_MEM_CI_HELPER_DIR"
}

start_stack_helpers() {
  local project="$1" source_dir="$2" helpers="$3"
  if has_helper "$helpers" oauth || has_helper "$helpers" oauth_compatibility; then
    local provider
    provider="$(ci_compose ps -q oauth-provider-mock)"
    [[ -n "$provider" ]] || fail "OAuth provider helper was not created"
    docker cp "${DENSE_MEM_CI_HELPER_DIR}/ca.pem" "${provider}:/e2e/ca.pem" >/dev/null
    docker cp "${DENSE_MEM_CI_PRIVATE_DIR}/server.key" "${provider}:/e2e/server.key" >/dev/null
    docker cp "${DENSE_MEM_CI_HELPER_DIR}/config.json" "${provider}:/e2e/config.json" >/dev/null
    docker cp "${source_dir}/tests/uat/oauth_provider_mock.mjs" "${provider}:/e2e/oauth-provider-mock.mjs" >/dev/null
    docker exec "$provider" chmod 644 /e2e/ca.pem /e2e/server.key /e2e/config.json /e2e/oauth-provider-mock.mjs >/dev/null
    docker exec -d "$provider" node /e2e/oauth-provider-mock.mjs >/dev/null
    if has_helper "$helpers" oauth_compatibility; then
      local harness
      harness="$(ci_compose ps -q oauth-compat-harness)"
      [[ -n "$harness" ]] || fail "OAuth compatibility harness was not created"
      docker exec -d "$harness" /app/oauth-compat-harness \
        --listen=:9445 \
        --public-base-url=https://oauth-compat-harness:9445 \
        --config=/e2e/config.json \
        --tls-cert=/e2e/ca.pem \
        --tls-key=/e2e/server.key >/dev/null
      local entra
      entra="$(ci_compose ps -q entra-mock)"
      [[ -n "$entra" ]] || fail "Entra mock helper was not created"
      docker cp "${source_dir}/web/tests-compose/entra-mock.mjs" "${entra}:/e2e/entra-mock.mjs" >/dev/null
      docker exec "$entra" chmod 644 /e2e/entra-mock.mjs >/dev/null
      docker exec -d "$entra" node /e2e/entra-mock.mjs >/dev/null
    fi
  fi
  if has_helper "$helpers" conflict_provider; then
    local conflict
    conflict="$(ci_compose ps -q conflict-provider)"
    [[ -n "$conflict" ]] || fail "conflict provider helper was not created"
    docker cp "${source_dir}/tests/uat/conflict_openai_stub.mjs" "${conflict}:/e2e/conflict_openai_stub.mjs" >/dev/null
    docker exec -d "$conflict" node /e2e/conflict_openai_stub.mjs >/dev/null
  fi
  if has_helper "$helpers" verifier || has_helper "$helpers" synchronous_write; then
    local provider
    provider="$(ci_compose ps -q synchronous-write-provider)"
    [[ -n "$provider" ]] || fail "deterministic provider helper was not created"
    docker cp "${source_dir}/tests/uat/synchronous_write/provider-fixture.mjs" "${provider}:/e2e/provider-fixture.mjs" >/dev/null
    docker exec -d "$provider" node /e2e/provider-fixture.mjs >/dev/null
  fi
}

seed_stack_inputs() {
  local project="$1" run_id="$2" attempt="$3" phase="$4" scenario="$5"
  local created_at
  created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  local -a labels=(
    --label "io.dense-mem.ci.contract=${CONTRACT_VERSION}"
    --label "io.dense-mem.ci.repository=${REPOSITORY}"
    --label "io.dense-mem.ci.run-id=${run_id}"
    --label "io.dense-mem.ci.run-attempt=${attempt}"
    --label "io.dense-mem.ci.phase=${phase}"
    --label "io.dense-mem.ci.scenario=${scenario}"
    --label "io.dense-mem.ci.image-digest=${DENSE_MEM_CI_IMAGE_DIGEST}"
    --label "io.dense-mem.ci.created-at=${created_at}"
  )
  labels+=(--label "io.dense-mem.ci.compose-project=${project}")
  docker volume create "${labels[@]}" "$DENSE_MEM_CI_PROMETHEUS_CONFIG_VOLUME_NAME" >/dev/null
  docker volume create "${labels[@]}" "$DENSE_MEM_CI_TELEMETRY_TOKEN_VOLUME_NAME" >/dev/null

  local seed_container="${project}-inputs"
  seed_container="$(docker run -d --name "$seed_container" "${labels[@]}" \
    --mount "type=volume,source=${DENSE_MEM_CI_PROMETHEUS_CONFIG_VOLUME_NAME},target=/config" \
    --mount "type=volume,source=${DENSE_MEM_CI_TELEMETRY_TOKEN_VOLUME_NAME},target=/token" \
    alpine:3.24 sh -ec 'sleep infinity')"
  docker cp "$PROMETHEUS_FILE" "$seed_container:/config/prometheus.yml" >/dev/null
  docker cp "$TELEMETRY_TOKEN_FILE" "$seed_container:/token/telemetry-scrape-token" >/dev/null
  docker exec "$seed_container" chmod 0444 /config/prometheus.yml /token/telemetry-scrape-token >/dev/null
  docker rm -f "$seed_container" >/dev/null
}

write_runtime_compose() {
  local path="$1" project="$2" image="$3"
  node - "$path" "$project" "$image" <<'NODE'
const fs = require("node:fs");
const [path, project, image] = process.argv.slice(2);
const lines = [
  "# dense-mem-ci-e2e.v1 non-secret runtime view",
  "services:",
  "  postgres:", "    image: pgvector/pgvector:0.8.2-pg18-trixie", "    networks: [ci]",
  "  redis:", "    image: redis:7-alpine", "    networks: [ci]",
  "    healthcheck:", "      test: [\"CMD-SHELL\", \"REDISCLI_AUTH= redis-cli ping | grep -q PONG\"]",
  "  server:", `    image: ${JSON.stringify(image)}`, "    networks: [ci]",
  "  prometheus:", "    image: prom/prometheus:v3.12.0", "    networks: [ci]",
  "  conflict-provider:", "    image: node:24-alpine", "    networks: [ci]",
  "  oauth-provider-mock:", "    image: node:26-alpine", "    networks: [ci]",
  "  entra-mock:", "    image: node:26-alpine", "    networks: [ci]",
  "  oauth-compat-harness:", "    image: alpine:3.24", "    networks: [ci]",
  "  synchronous-write-provider:", "    image: node:22-alpine", "    networks: [ci]",
  "volumes:", "  oauth-provider-files:", `    name: ${project}_oauth-provider-files`, "    external: true",
  "networks:", "  ci:", "    external: true", `    name: ${project}_ci`, "",
];
fs.writeFileSync(path, `${lines.join("\n")}\n`, { mode: 0o600 });
NODE
}

run_synchronous_primitives_driver() {
  local source_dir="$1" project="$2" postgres_user="$3" postgres_password="$4" postgres_db="$5" run_id="$6" attempt="$7" phase="$8" scenario="$9" digest="${10}"
  local database_url
  database_url="$(node - "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER" "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_PASSWORD" "$postgres_db" <<'NODE'
const [user, password, database] = process.argv.slice(2);
const url = new URL("postgresql://postgres:5432");
url.username = user;
url.password = password;
url.pathname = `/${database}`;
url.searchParams.set("sslmode", "disable");
process.stdout.write(url.toString());
NODE
)"
  local go_image
  go_image="$(env_value DENSE_MEM_CI_GO_TEST_IMAGE 2>/dev/null || printf '%s' golang:1.26.6-bookworm)"
  run_go_source_container \
    "$source_dir" "$go_image" "$project" "$run_id" "$attempt" "$phase" "$scenario" "$digest" "${project}_ci" "" "$ENV_FILE" \
    "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_PASSWORD" "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER" "$postgres_db" -- \
    "DATABASE_URL=${database_url}" \
    "DENSE_MEM_E2E_PRIMITIVES_PROVIDER_URL=http://synchronous-write-provider:8787/v1" \
    "DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1" \
    "DENSE_MEM_REQUIRE_POSTGRES_TESTS=1" -- \
    go test -tags=compose_e2e ./internal/service/memoryservice \
      -run '^TestComposeSynchronousEvidenceOnlyAssessorBatch$' -count=1 || return $?

  run_go_source_container \
    "$source_dir" "$go_image" "$project" "$run_id" "$attempt" "$phase" "$scenario" "$digest" "${project}_ci" "" "$ENV_FILE" \
    "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_PASSWORD" "$DENSE_MEM_CI_BOOTSTRAP_POSTGRES_USER" "$postgres_db" -- \
    "DATABASE_URL=${database_url}" \
    "DENSE_MEM_E2E_PRIMITIVES_PROVIDER_URL=http://synchronous-write-provider:8787/v1" \
    "DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1" \
    "DENSE_MEM_REQUIRE_POSTGRES_TESTS=1" -- \
    go test -tags=compose_e2e ./internal/repository \
      -run '^TestComposeRememberPrimitives$' -count=1
}

run_mcp_sdk_parity_driver() {
  local source_dir="$1" project="$2" run_id="$3" attempt="$4" phase="$5" scenario="$6" digest="$7"
  local go_image
  go_image="$(env_value DENSE_MEM_CI_GO_TEST_IMAGE 2>/dev/null || printf '%s' golang:1.26.6-bookworm)"
  run_go_source_container \
    "$source_dir" "$go_image" "$project" "$run_id" "$attempt" "$phase" "$scenario" "$digest" "${project}_ci" "" "$ENV_FILE" -- \
    -- \
    go test ./internal/mcp -run '^TestConformanceHarness$' -count=1
}
