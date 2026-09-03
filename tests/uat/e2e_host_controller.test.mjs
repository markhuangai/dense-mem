import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const [controllerMain, controllerStack, controllerRuntime] = await Promise.all([
  readFile(new URL("../../scripts/e2e-host-controller.sh", import.meta.url), "utf8"),
  readFile(new URL("../../scripts/e2e-host-controller-stack.sh", import.meta.url), "utf8"),
  readFile(new URL("../../scripts/e2e-host-controller-runtime.sh", import.meta.url), "utf8"),
]);
const controllerPath = fileURLToPath(new URL("../../scripts/e2e-host-controller.sh", import.meta.url));
const controller = [controllerMain, controllerStack, controllerRuntime].join("\n");
const compose = await readFile(new URL("../../scripts/e2e-ci-compose.yml", import.meta.url), "utf8");
const redactorPath = fileURLToPath(new URL("../../scripts/e2e-redact-diagnostics.mjs", import.meta.url));
const realControllerTest = await readFile(new URL("./e2e_host_controller_real.sh", import.meta.url), "utf8");
const productionWorkflow = await readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8");
const scenarioWorkflow = await readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8");
const scenarioScript = await readFile(new URL("../../scripts/e2e-ci-scenario.sh", import.meta.url), "utf8");

async function runDoctor({ omit = "" } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-doctor-"));
  const configDirectory = join(directory, "dense-mem-ci");
  const binDirectory = join(directory, "bin");
  const envFile = join(configDirectory, ".env");
  const telemetryTokenFile = join(configDirectory, "telemetry-scrape-token");
  const dockerPath = join(binDirectory, "docker");
  try {
    await mkdir(configDirectory, { recursive: true });
    await mkdir(binDirectory, { recursive: true });
    const envLines = [
      "POSTGRES_USER=densemem",
      "POSTGRES_PASSWORD=postgres-secret",
      "POSTGRES_DB=densemem",
      "AI_API_KEY=provider-secret",
      "CONTROL_PORTAL_TOKEN=control-secret",
    ].filter((line) => !omit || !line.startsWith(`${omit}=`));
    await writeFile(envFile, `${envLines.join("\n")}\n`);
    await chmod(envFile, 0o600);
    await writeFile(telemetryTokenFile, "telemetry-secret\n");
    await chmod(telemetryTokenFile, 0o600);
    await writeFile(dockerPath, `#!/usr/bin/env bash
set -euo pipefail
case "\${1:-}" in
  info)
    if [[ "\${2:-}" == "--format" ]]; then
      printf '%s\\n' '["rootless"]'
    fi
    ;;
  compose)
    if [[ "\$*" == *"--format json"* ]]; then
      printf '%s\\n' '{"services":{}}'
    fi
    ;;
esac
`);
    await chmod(dockerPath, 0o700);
    return spawnSync("bash", [controllerPath, "doctor"], {
      encoding: "utf8",
      env: {
        ...process.env,
        HOME: directory,
        PATH: `${binDirectory}:${process.env.PATH}`,
        DENSE_MEM_CI_ENV_FILE: envFile,
        DENSE_MEM_CI_TELEMETRY_TOKEN_FILE: telemetryTokenFile,
        DENSE_MEM_CI_COMPOSE_FILE: fileURLToPath(new URL("../../scripts/e2e-ci-compose.yml", import.meta.url)),
        DENSE_MEM_CI_PROMETHEUS_FILE: fileURLToPath(new URL("../../examples/prometheus.yml", import.meta.url)),
        DENSE_MEM_CI_REGISTRY_SCRIPT: fileURLToPath(new URL("../../scripts/e2e-scenario-registry.mjs", import.meta.url)),
      },
    });
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

test("controller is PR-owned and has no persistent lease or manifest contract", () => {
  assert.match(controller, /CONTRACT_VERSION="dense-mem-ci-e2e\.v1"/);
  for (const operation of ["doctor", "start", "run", "stop", "stale-cleanup", "precheck"]) {
    assert.match(controller, new RegExp(`e2e-host-controller[.]sh ${operation}`));
  }
  assert.match(controller, /stale-cleanup \[MAX_AGE_SECONDS\] \[RUN_ID ATTEMPT PHASE\]/);
  assert.match(controller, /JOB_DIR=.*RUNNER_TEMP/);
  assert.match(controller, /DENSE_MEM_CI_PROMETHEUS_FILE/);
  assert.match(controller, /DENSE_MEM_CI_TELEMETRY_TOKEN_FILE/);
  assert.match(controller, /validate_cleanup_phase\(\)/);
  assert.match(controller, /validate_cleanup_phase "\$target_phase"/);
  assert.match(controller, /validate_cleanup_phase[\s\S]*precheck/);
  assert.match(controller, /for database_field in POSTGRES_USER POSTGRES_DB/);
  assert.match(controller, /redact_diagnostics/);
  assert.match(controller, /must contain at least two characters/);
  assert.match(controller, /\[\[ \$\{#secret_value\} -ge 2 \]\]/);
  assert.doesNotMatch(controller, /\$\{#secret_value\} -ne 1/);
  assert.match(controller, /docker "\$\{docker_args\[@\]\}"/);
  assert.match(controller, /git -C \"\$source_dir\" archive --format=tar --prefix=workspace\//);
  assert.match(controller, /docker cp - \"\$container:\/\"/);
  assert.match(controller, /SCENARIO_TEST_IMAGE=\"mcr\.microsoft\.com\/playwright:v1\.62\.1-noble\"/);
  assert.doesNotMatch(controller, /DENSE_MEM_CI_TEST_IMAGE/);
  assert.doesNotMatch(controller, /DENSE_MEM_CI_DAEMON_ID|DENSE_MEM_CI_DOCKER_SOCKET|LEASE_DIR|RUN_DIR|DENSE_MEM_E2E_SOURCE_REVISION|e2e-docker-proxy|e2e-runtime-adapter/);
  assert.doesNotMatch(controller, /\$\{source_dir\}:\/workspace|\$\{runtime_compose_host\}:|\$\{helper_overlay\}:|\$\{run_root\}\/results/);
  assert.doesNotMatch(controller, /docker system prune|docker image rm[^\n]*--force/);
});

test("validate_bundle rejects missing database identity and accepts a valid runner bundle", async () => {
  const valid = await runDoctor();
  assert.equal(valid.status, 0, valid.stderr);
  assert.match(valid.stdout, /dense-mem-ci-e2e\.v1/);
  for (const field of ["POSTGRES_USER", "POSTGRES_DB"]) {
    const invalid = await runDoctor({ omit: field });
    assert.notEqual(invalid.status, 0, `${field} unexpectedly passed validation`);
    assert.match(invalid.stderr, new RegExp(`${field} must be configured`));
  }
});

test("Compose consumes fixed runner config and controller-seeded inputs without host ports", () => {
  assert.match(compose, /env_file:\n\s+- \$\{DENSE_MEM_CI_ENV_FILE/);
  assert.doesNotMatch(compose, /DENSE_MEM_CI_PROMETHEUS_FILE/);
  assert.doesNotMatch(compose, /DENSE_MEM_CI_TELEMETRY_TOKEN_FILE/);
  assert.match(compose, /prometheus-config:/);
  assert.match(compose, /external: true/);
  assert.match(compose, /TELEMETRY_SCRAPE_TOKEN:/);
  assert.match(compose, /io\.dense-mem\.ci\.contract/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
  const postgres = compose.slice(compose.indexOf("  postgres:"), compose.indexOf("\n  redis:"));
  const redis = compose.slice(compose.indexOf("  redis:"), compose.indexOf("\n  server:"));
  assert.doesNotMatch(postgres, /env_file:/);
  assert.doesNotMatch(redis, /env_file:/);
});

test("production workflow uses PR E2E assets, four-way matrices, and one shared-project output", () => {
  assert.match(productionWorkflow, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.match(productionWorkflow, /scripts\/e2e-host-controller\.sh/);
  assert.match(productionWorkflow, /max-parallel: 4/);
  assert.match(productionWorkflow, /shared_project: \$\{\{ steps\.start\.outputs\.shared_project \}\}/);
  assert.match(productionWorkflow, /printf 'shared_project=%s\\n'/);
  assert.doesNotMatch(productionWorkflow, /actions\/download-artifact@v8/);
  assert.doesNotMatch(productionWorkflow, /acquire:|release:|cleanup-run:|e2e-stack\.sh|dense-mem-ci\/e2e-stack|manifest/);
  assert.doesNotMatch(productionWorkflow, /rootless-docker-shared|runs-on:\s*pc/);
});

test("scenario workflow derives the stack from shared_project and executes PR scripts", () => {
  assert.match(scenarioWorkflow, /shared_project:/);
  assert.match(scenarioWorkflow, /PHASE: \$\{\{ inputs\.shared_project != ''/);
  assert.match(scenarioWorkflow, /node scripts\/e2e-scenario-registry\.mjs/);
  assert.match(scenarioWorkflow, /scripts\/e2e-host-controller\.sh start/);
  assert.match(scenarioWorkflow, /scripts\/e2e-host-controller\.sh run/);
  assert.match(scenarioWorkflow, /scripts\/e2e-host-controller\.sh stop/);
  assert.match(scenarioWorkflow, /tail -c 262144/);
  assert.doesNotMatch(scenarioWorkflow, /manifest|e2e-stack\.sh|dense-mem-ci\/e2e-stack|e2e-runtime-adapter/);
});

test("nested Docker clients use the runner socket at its original path", () => {
  assert.match(controllerStack, /run_go_source_container\(\) \(/);
  assert.match(controllerStack, /build_conflict_review_driver\(\) \(/);
  assert.match(controllerRuntime, /--mount \"type=bind,source=\$\{docker_socket\},target=\$\{docker_socket\}\"/);
  assert.match(controllerRuntime, /-e \"DOCKER_HOST=unix:\/\/\$\{docker_socket\}\"/);
  assert.match(controllerStack, /--mount \"type=bind,source=\$\{docker_socket\},target=\$\{docker_socket\}\"/);
  assert.match(controllerRuntime, /copy_file_into_container \"\$container\" \"\$runtime_compose_host\"/);
  assert.match(controllerRuntime, /copy_file_into_container \"\$container\" \"\$DENSE_MEM_CI_DOCKER_BIN\"/);
  assert.match(controllerRuntime, /copy_file_into_container \"\$container\" \"\$DENSE_MEM_CI_COMPOSE_PLUGIN\"/);
  assert.match(controllerStack, /docker cp \"\$PROMETHEUS_FILE\" \"\$seed_container:\/config\/prometheus\.yml\"/);
  assert.match(controllerStack, /docker cp \"\$TELEMETRY_TOKEN_FILE\" \"\$seed_container:\/token\/telemetry-scrape-token\"/);
});

test("runtime Compose view declares the OAuth helper volume", () => {
  assert.match(controllerStack, /"volumes:", "  oauth-provider-files:", `    name: \$\{project\}_oauth-provider-files`, "    external: true"/);
});

test("conflict-provider helpers align the effective embedding settings", async () => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-helper-dimensions-"));
  try {
    const result = spawnSync("bash", ["-e", "-u", "-o", "pipefail", "-c", `
      set -euo pipefail
      CONTRACT_VERSION=dense-mem-ci-e2e.v1
      REPOSITORY=markhuangai/dense-mem
      DENSE_MEM_CI_IMAGE_DIGEST=sha256:0000000000000000000000000000000000000000000000000000000000000000
      JOB_DIR="$DENSE_MEM_TEST_JOB_DIR"
      fail() { printf '%s\\n' "$*" >&2; exit 1; }
      require_command() { :; }
      has_helper() {
        local helpers=",$1,"
        [[ "$helpers" == *",$2,"* ]]
      }
      env_value() {
        [[ "$1" == "AI_API_EMBEDDING_DIMENSIONS" ]] || return 1
        printf '%s' "$DENSE_MEM_TEST_DIMENSIONS"
      }
      go() {
        if [[ "$1" == "build" && "$2" == "-o" ]]; then
          : > "$3"
        fi
      }
      source "$DENSE_MEM_TEST_STACK"
      build_conflict_review_driver() {
        : > "$DENSE_MEM_CI_HELPER_DIR/conflict-review-driver"
      }
      prepare_stack_helpers densemem-ci-test "$DENSE_MEM_TEST_SOURCE_DIR" conflict_provider,synchronous_write 1 1 exclusive synchronous_write >/dev/null
      printf '%s\\n' combined
      cat "$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE"
      prepare_stack_helpers densemem-ci-test "$DENSE_MEM_TEST_SOURCE_DIR" conflict_provider 1 1 exclusive conflict >/dev/null
      printf '%s\\n' standalone
      cat "$DENSE_MEM_CI_COMPOSE_OVERLAY_FILE"
    `], {
      encoding: "utf8",
      env: {
        ...process.env,
        DENSE_MEM_TEST_JOB_DIR: join(directory, "job"),
        DENSE_MEM_TEST_SOURCE_DIR: directory,
        DENSE_MEM_TEST_STACK: fileURLToPath(new URL("../../scripts/e2e-host-controller-stack.sh", import.meta.url)),
        DENSE_MEM_TEST_DIMENSIONS: "3072",
      },
    });
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /AI_API_EMBEDDING_MODEL: "dense-mem-conflict-e2e-embedding"/);
    assert.match(result.stdout, /AI_API_EMBEDDING_DIMENSIONS: "3072"/);
    assert.match(result.stdout, /DENSE_MEM_E2E_PROVIDER_DIMENSIONS: "3072"/);
    assert.match(result.stdout, /standalone[\s\S]*AI_API_EMBEDDING_DIMENSIONS: "1536"/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("conflict review drivers receive the provider settings used by their stack", () => {
  assert.match(controllerRuntime, /CONFLICT_PROVIDER_EMBEDDING_MODEL/);
  assert.match(controllerRuntime, /if has_helper "\$helpers" conflict_provider; then[\s\S]*embedding_model="\$CONFLICT_PROVIDER_EMBEDDING_MODEL"/);
  assert.match(controllerRuntime, /AI_API_EMBEDDING_MODEL=\$\{embedding_model\}/);
  assert.match(controllerRuntime, /AI_API_EMBEDDING_DIMENSIONS=\$\{embedding_dimensions\}/);
  assert.match(controllerRuntime, /provider_field in[\s\S]*AI_API_EMBEDDING_MODEL AI_API_EMBEDDING_DIMENSIONS/);
  assert.match(controllerRuntime, /AI_API_URL=http:\/\/conflict-provider:8081\/v1/);
  assert.match(controllerRuntime, /AI_VERIFIER_API_URL=http:\/\/conflict-provider:8081\/v1/);
  assert.match(controllerRuntime, /for provider_field in[\s\S]*AI_API_URL[\s\S]*AI_VERIFIER_TIMEOUT_SECONDS/);
  assert.match(controllerRuntime, /if \[\[ -n \"\$provider_value\" \]\]; then[\s\S]*conflict_driver_env\+=/);
  assert.match(controllerRuntime, /docker_args\+=\(-e \"\$provider_env\"\)/);
});

test("Go driver wrappers copy source into project-scoped containers", async () => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-go-driver-transfer-"));
  try {
    const binDirectory = join(directory, "bin");
    const dockerPath = join(binDirectory, "docker");
    await mkdir(binDirectory, { recursive: true });
    await writeFile(dockerPath, `#!/usr/bin/env bash
set -euo pipefail
next=""
if [[ "\${1:-}" == "create" ]]; then
  next="$(grep -c '^create ' "\${DENSE_MEM_TEST_DOCKER_LOG}" || true)"
fi
printf '%s\\n' "$(printf '%q ' "$@")" >> "\${DENSE_MEM_TEST_DOCKER_LOG}"
case "\${1:-}" in
  create)
    printf 'container-%s\\n' "$((next + 1))"
    ;;
  cp)
    if [[ "\${2:-}" == "-" ]]; then cat >/dev/null; fi
    if [[ "\${2:-}" != "-" ]]; then : > "\${3:?}"; fi
    ;;
  start) printf '%s\\n' 'driver passed' ;;
  rm) ;;
esac
`);
    await chmod(dockerPath, 0o700);
    const result = spawnSync("bash", ["-e", "-u", "-o", "pipefail", "-c", `
      set -euo pipefail
      CONTRACT_VERSION=dense-mem-ci-e2e.v1
      REPOSITORY=markhuangai/dense-mem
      ENV_FILE="$DENSE_MEM_TEST_ENV"
      CONTROLLER_DIR="$DENSE_MEM_TEST_ROOT/scripts"
      DENSE_MEM_CI_HELPER_DIR="$DENSE_MEM_TEST_ROOT/helpers"
      DENSE_MEM_CI_RUN_ID=11
      DENSE_MEM_CI_RUN_ATTEMPT=2
      DENSE_MEM_CI_PHASE=exclusive
      DENSE_MEM_CI_SCENARIO=identity_cleanup
      DENSE_MEM_CI_IMAGE_DIGEST=sha256:0000000000000000000000000000000000000000000000000000000000000000
      fail() { printf '%s\\n' "$*" >&2; exit 1; }
      env_value() { return 1; }
      redact_diagnostics() { cat; }
      copy_git_source() {
        printf '%s\\n' archive | docker cp - "$1:/"
      }
      source "$DENSE_MEM_TEST_STACK"
      mkdir -p "$DENSE_MEM_CI_HELPER_DIR"
      caller_cleanup() { :; }
      trap caller_cleanup EXIT
      assert_caller_cleanup() { [[ "$(trap -p EXIT)" == *caller_cleanup* ]]; }
      build_conflict_review_driver source-dir golang:1.26.6-bookworm densemem-ci-test 11 2 exclusive conflict
      assert_caller_cleanup
      run_synchronous_primitives_driver source-dir densemem-ci-test densemem postgres densemem 11 2 exclusive synchronous_write sha256:0000000000000000000000000000000000000000000000000000000000000000
      assert_caller_cleanup
      run_mcp_sdk_parity_driver source-dir densemem-ci-test 11 2 shared mcp_sdk_parity sha256:0000000000000000000000000000000000000000000000000000000000000000
      assert_caller_cleanup
      run_identity_cleanup_seed source-dir densemem-ci-test densemem postgres densemem
      assert_caller_cleanup
    `], {
      encoding: "utf8",
      env: {
        ...process.env,
        PATH: `${directory}/bin:${process.env.PATH}`,
        DENSE_MEM_TEST_ROOT: directory,
        DENSE_MEM_TEST_STACK: fileURLToPath(new URL("../../scripts/e2e-host-controller-stack.sh", import.meta.url)),
        DENSE_MEM_TEST_ENV: join(directory, ".env"),
        DENSE_MEM_TEST_DOCKER_LOG: join(directory, "docker.log"),
      },
    });
    assert.equal(result.status, 0, result.stderr);
    const log = await readFile(join(directory, "docker.log"), "utf8");
    assert.equal((log.match(/^create /gm) || []).length, 5);
    assert.equal((log.match(/^cp /gm) || []).length, 6);
    assert.match(log, /--network densemem-ci-test_ci/);
    assert.doesNotMatch(log, /--mount|DENSE_MEM_TEST_ENV|\.env/);
    assert.match(log, /start --attach container-1/);
    assert.match(log, /start --attach container-5/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("production scenarios preserve Playwright handoff values", () => {
  assert.match(scenarioScript, /DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID/);
  assert.match(scenarioScript, /DENSE_MEM_E2E_DREAM_STATEMENT/);
  assert.match(scenarioScript, /parse_json_dream_statement/);
  assert.match(scenarioScript, /OAuth scenario result handoff is missing/);
  assert.match(scenarioScript, /--connect-timeout 5 --max-time 10/);
});

test("real controller fixtures use workflow-scoped names and leave no local state", () => {
  assert.match(realControllerTest, /GITHUB_RUN_ID/);
  assert.match(realControllerTest, /GITHUB_RUN_ATTEMPT/);
  assert.match(realControllerTest, /fixture_prefix/);
  assert.match(realControllerTest, /DENSE_MEM_CI_JOB_DIR/);
  assert.match(realControllerTest, /controller created persistent lease\/run state/);
  assert.match(realControllerTest, /for state_dir in "\$JOB_DIR" "\$CONFIG_DIR"/);
  assert.match(realControllerTest, /stale-cleanup 1 "\$run_stale" "\$fixture_attempt" shared/);
  assert.match(realControllerTest, /io\.dense-mem\.ci\.phase=exclusive/);
  assert.doesNotMatch(realControllerTest, /DENSE_MEM_CI_DAEMON_ID|e2e-docker-proxy|e2e-runtime-adapter|release.*lease/);
  assert.match(realControllerTest, /\"io\.dense-mem\.ci\.run-attempt\": attempt/);
});

test("diagnostic redaction protects secrets split across input chunks", async () => {
  const { redactChunks } = await import("../../scripts/e2e-redact-diagnostics.mjs");
  assert.equal(redactChunks(["prefix xxABC", "DEFyy suffix"], ["ABCDEF"]), "prefix xx[REDACTED]yy suffix");
  assert.equal(redactChunks(["prefix zzabc", "defyy suffix"], ["a", "abcdef"]), "prefix zz[REDACTED]yy suffix");
  assert.equal(redactChunks(["ordinary", " text"], ["ABCDEF"]), "ordinary text");
  const output = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [redactorPath], {
      env: { ...process.env, DENSE_MEM_CI_REDACT_ENV_FILE: "/dev/null", DENSE_MEM_CI_REDACT_EXTRA_VALUES: "ABCDEF" },
    });
    let text = "";
    let error = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => { text += chunk; });
    child.stderr.on("data", (chunk) => { error += chunk; });
    child.on("error", reject);
    child.on("close", (code) => code === 0 ? resolve(text) : reject(new Error(`${code}: ${error}`)));
    child.stdin.write("prefix xxABC");
    setTimeout(() => child.stdin.end("DEFyy suffix"), 10);
  });
  assert.equal(output, "prefix xx[REDACTED]yy suffix");
});

test("controller diagnostics redact short credentials", async () => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-redact-short-"));
  const envFile = join(directory, ".env");
  try {
    await writeFile(envFile, "POSTGRES_PASSWORD=abc\n");
    const output = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [redactorPath], {
        env: {
          ...process.env,
          DENSE_MEM_CI_REDACT_ALLOW_SHORT: "1",
          DENSE_MEM_CI_REDACT_ENV_FILE: envFile,
        },
      });
      let text = "";
      let error = "";
      child.stdout.setEncoding("utf8");
      child.stderr.setEncoding("utf8");
      child.stdout.on("data", (chunk) => { text += chunk; });
      child.stderr.on("data", (chunk) => { error += chunk; });
      child.on("error", reject);
      child.on("close", (code) => code === 0 ? resolve(text) : reject(new Error(`${code}: ${error}`)));
      child.stdin.end("prefix abc suffix");
    });
    assert.equal(output, "prefix [REDACTED] suffix");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("controller diagnostics leave one-character text intact", async () => {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-redact-one-char-"));
  const envFile = join(directory, ".env");
  try {
    await writeFile(envFile, "POSTGRES_PASSWORD=a\n");
    const output = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, [redactorPath], {
        env: {
          ...process.env,
          DENSE_MEM_CI_REDACT_ALLOW_SHORT: "1",
          DENSE_MEM_CI_REDACT_ENV_FILE: envFile,
        },
      });
      let text = "";
      let error = "";
      child.stdout.setEncoding("utf8");
      child.stderr.setEncoding("utf8");
      child.stdout.on("data", (chunk) => { text += chunk; });
      child.stderr.on("data", (chunk) => { error += chunk; });
      child.on("error", reject);
      child.on("close", (code) => code === 0 ? resolve(text) : reject(new Error(`${code}: ${error}`)));
      child.stdin.end("prefix a suffix");
    });
    assert.equal(output, "prefix a suffix");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("diagnostic redaction parses supported Compose env syntax and rejects interpolation", async () => {
  const { valueFromEnvFile, valuesFromEnvFile } = await import("../../scripts/e2e-redact-diagnostics.mjs");
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-redact-env-"));
  const envFile = join(directory, ".env");
  try {
    await writeFile(envFile, [
      "# ignored comment",
      "export PLAIN=plain-secret # inline comment",
      "COLON: colon-secret # inline comment",
      "DOUBLE=\"double\\\"quote\" # trailing comment",
      "SINGLE='single # literal'",
      "HASH=abc#def",
    ].join("\n"));
    assert.deepEqual(valuesFromEnvFile(envFile), ["plain-secret", "colon-secret", 'double"quote', "single # literal", "abc#def"]);
    assert.equal(valueFromEnvFile(envFile, "PLAIN"), "plain-secret");
    assert.equal(valueFromEnvFile(envFile, "COLON"), "colon-secret");
    await writeFile(envFile, 'TOKEN="unterminated\n');
    assert.throws(() => valuesFromEnvFile(envFile), /unsupported Compose env syntax/);
    await writeFile(envFile, 'TOKEN="secret${OTHER}"\n');
    assert.throws(() => valuesFromEnvFile(envFile), /variable interpolation is unsupported/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
