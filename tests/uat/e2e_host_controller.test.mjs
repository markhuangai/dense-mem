import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { chmod, mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { promisify } from "node:util";

const controller = (
  await Promise.all([
    readFile(new URL("../../scripts/e2e-host-controller.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller-stack.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller-runtime.sh", import.meta.url), "utf8"),
  ])
).join("\n");
const compose = await readFile(new URL("../../scripts/e2e-ci-compose.yml", import.meta.url), "utf8");
const adapter = await readFile(new URL("../../scripts/e2e-runtime-adapter.mjs", import.meta.url), "utf8");
const installer = await readFile(new URL("../../scripts/install-e2e-host-controller.sh", import.meta.url), "utf8");
const installerPath = fileURLToPath(new URL("../../scripts/install-e2e-host-controller.sh", import.meta.url));
const redactorPath = fileURLToPath(new URL("../../scripts/e2e-redact-diagnostics.mjs", import.meta.url));
const controllerPath = fileURLToPath(new URL("../../scripts/e2e-host-controller.sh", import.meta.url));
const execFileAsync = promisify(execFile);
const realControllerTest = await readFile(new URL("./e2e_host_controller_real.sh", import.meta.url), "utf8");
const productionWorkflow = await readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8");
const scenarioWorkflow = await readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8");
const scenarioScript = await readFile(new URL("../../scripts/e2e-ci-scenario.sh", import.meta.url), "utf8");

test("controller exposes the versioned lifecycle and lease contract", () => {
  assert.match(controller, /CONTRACT_VERSION="dense-mem-ci-e2e\.v1"/);
  for (const operation of ["doctor", "acquire", "start", "run", "stop", "release", "stale-cleanup", "precheck", "validate"]) {
    assert.match(controller, new RegExp(`e2e-stack[.]sh ${operation}`));
  }
  assert.match(controller, /docker pull/);
  assert.match(controller, /docker image rm/);
  assert.match(controller, /docker "\$\{docker_args\[@\]\}"/);
  assert.match(controller, /stale_failed=0/);
  assert.match(controller, /--mode precheck/);
  assert.match(controller, /redact_diagnostics/);
  assert.match(controller, /e2e-redact-diagnostics\.mjs/);
  assert.match(controller, /cleanup-run/);
  assert.match(controller, /local-all RUN_ID ATTEMPT IMAGE_REF DIGEST SOURCE_REVISION SOURCE_DIR/);
  assert.match(controller, /BASH_VERSINFO\[0\]/);
  assert.match(controller, /manifest\.image_digest/);
  assert.match(controller, /source_revision digest client_volume helpers/);
  assert.match(controller, /validate_runtime_manifest "\$manifest"/);
  assert.match(controller, /runtime manifest scenario does not match/);
  assert.match(controller, /docker image ls -q --no-trunc/);
  const stopStack = controller.slice(controller.indexOf("stop_stack()"), controller.indexOf("release()"));
  assert.match(stopStack, /label=io\.dense-mem\.ci\.compose-project=\$\{project\}/);
  assert.match(productionWorkflow, /e2e-stack\.sh["']?\s+precheck/);
  assert.match(productionWorkflow, /Run directory cleanup/);
  assert.match(scenarioWorkflow, /e2e-stack\.sh["']?\s+validate/);
  assert.match(scenarioWorkflow, /e2e-stack\.sh["']?\s+run[\s\S]*tail -c 262144/);
  assert.match(scenarioWorkflow, /Stop isolated stack[\s\S]*status.*\.status/);
  assert.doesNotMatch(controller, /docker system prune/);
  assert.doesNotMatch(controller, /docker image rm[^\n]*--force/);
});

test("controller rejects broad projects, host ports, and weak environment files", () => {
  assert.match(controller, /densemem-ci-\[a-z0-9\]/);
  assert.match(controller, /mode 0600/);
  assert.match(controller, /assert_no_host_ports/);
  assert.match(compose, /io\.dense-mem\.ci\.contract/);
  assert.match(compose, /command: \["sh", "-c", "exec redis-server/);
  assert.match(compose, /name: \$\{DENSE_MEM_CI_NETWORK_NAME/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
});

test("database helpers receive only their required credentials", () => {
  const postgres = compose.slice(compose.indexOf("  postgres:"), compose.indexOf("\n  redis:"));
  const redis = compose.slice(compose.indexOf("  redis:"), compose.indexOf("\n  server:"));
  assert.doesNotMatch(postgres, /env_file:/);
  assert.match(postgres, /POSTGRES_USER:/);
  assert.match(postgres, /POSTGRES_PASSWORD:/);
  assert.match(postgres, /POSTGRES_DB:/);
  assert.doesNotMatch(redis, /env_file:/);
  assert.match(redis, /REDIS_PASSWORD:/);
});

test("runtime adapter uses stable service DNS and rejects inspected IPs", () => {
  assert.match(adapter, /http:\/\/server:8080/);
  assert.match(adapter, /http:\/\/server:8090/);
  assert.match(adapter, /http:\/\/prometheus:9090/);
  assert.match(adapter, /postgres:5432/);
  assert.doesNotMatch(adapter, /docker inspect/);
  assert.doesNotMatch(adapter, /127\.0\.0\.1/);
});

test("production scenarios preserve Playwright handoff values", () => {
  assert.match(scenarioScript, /DENSE_MEM_E2E_OAUTH_SECOND_TEAM_ID/);
  assert.match(scenarioScript, /DENSE_MEM_E2E_DREAM_STATEMENT/);
  assert.match(scenarioScript, /parse_json_dream_statement/);
  assert.match(scenarioScript, /OAuth scenario result handoff is missing/);
  assert.match(scenarioScript, /--connect-timeout 5 --max-time 10/);
  assert.match(scenarioWorkflow, /^      SCENARIO: \$\{\{ inputs\.scenario \}\}$/m);
  assert.match(scenarioWorkflow, /scenario input must contain only lowercase letters, digits, and underscores/);
  assert.doesNotMatch(scenarioWorkflow, /RUNNER_TEMP[^\n]*inputs\.scenario/);
});

test("host installer never creates or copies a credential file", () => {
  assert.match(installer, /install -m 600/);
  assert.match(installer, /e2e-docker-proxy\.mjs/);
  assert.match(installer, /e2e-runtime-adapter\.mjs/);
  assert.match(installer, /e2e-scenario-registry\.mjs/);
  assert.match(installer, /e2e-host-controller-stack\.sh/);
  assert.match(installer, /e2e-host-controller-runtime\.sh/);
  assert.match(installer, /e2e-redact-diagnostics\.mjs/);
  assert.match(installer, /examples\/prometheus\.yml/);
  assert.match(installer, /chmod 600/);
  assert.match(installer, /telemetry-scrape-token/);
  assert.match(installer, /Create \$\{DESTINATION\}\/telemetry-scrape-token/);
  assert.doesNotMatch(installer, /AI_API_KEY|AI_VERIFIER_API_KEY|PASSWORD=/);
  assert.match(realControllerTest, /DENSE_MEM_E2E_REAL_DOCKER_TESTS/);
  assert.match(realControllerTest, /controller-contract/);
  assert.match(realControllerTest, /rootless Docker daemon/);
  assert.match(realControllerTest, /stale helper image/);
});

test("host installer requires the telemetry token before reporting success", async () => {
  const destination = await mkdtemp(join(tmpdir(), "dense-mem-installer-"));
  try {
    await writeFile(join(destination, ".env"), "AI_API_KEY=test\n");
    await assert.rejects(
      execFileAsync("bash", [installerPath, destination]),
      (error) => {
        assert.equal(error.code, 1);
        assert.match(error.stderr, /Create .*telemetry-scrape-token/);
        return true;
      },
    );

    const tokenPath = join(destination, "telemetry-scrape-token");
    await writeFile(tokenPath, "telemetry-test-token\n");
    const result = await execFileAsync("bash", [installerPath, destination]);
    assert.match(result.stdout, /Installed dense-mem-ci-e2e\.v1 controller/);
    assert.equal((await stat(tokenPath)).mode & 0o777, 0o600);
  } finally {
    await rm(destination, { recursive: true, force: true });
  }
});

test("identity cleanup seed formats IPv6 PostgreSQL authorities safely", async () => {
  const source = await readFile(
    new URL("../../internal/storage/postgres/identity_cleanup_migration_integration_test.go", import.meta.url),
    "utf8",
  );
  assert.match(source, /net\.JoinHostPort\(host,/);
});

test("diagnostic redaction protects secrets split across input chunks", async () => {
  const { redactChunks } = await import("../../scripts/e2e-redact-diagnostics.mjs");
  assert.equal(
    redactChunks(["prefix xxABC", "DEFyy suffix"], ["ABCDEF"]),
    "prefix xx[REDACTED]yy suffix",
  );
  assert.equal(
    redactChunks(["prefix zzabc", "defyy suffix"], ["a", "abcdef"]),
    "prefix zz[REDACTED]yy suffix",
  );
  assert.equal(redactChunks(["ordinary", " text"], ["ABCDEF"]), "ordinary text");
  const output = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [redactorPath], {
      env: {
        ...process.env,
        DENSE_MEM_CI_REDACT_ENV_FILE: "/dev/null",
        DENSE_MEM_CI_REDACT_EXTRA_VALUES: "ABCDEF",
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
    child.stdin.write("prefix xxABC");
    setTimeout(() => {
      child.stdin.end("DEFyy suffix");
    }, 10);
  });
  assert.equal(output, "prefix xx[REDACTED]yy suffix");
});

test("diagnostic redaction follows supported Compose env syntax and fails closed", async () => {
  const { valuesFromEnvFile } = await import("../../scripts/e2e-redact-diagnostics.mjs");
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-redact-env-"));
  const envFile = join(directory, ".env");
  try {
    await writeFile(envFile, [
      "# ignored comment",
      "export PLAIN=plain-secret # inline comment",
      "DOUBLE=\"double\\\"quote\" # trailing comment",
      "SINGLE='single # literal'",
      "HASH=abc#def",
    ].join("\n"));
    assert.deepEqual(valuesFromEnvFile(envFile), [
      "plain-secret",
      'double"quote',
      "single # literal",
      "abc#def",
    ]);

    await writeFile(envFile, 'TOKEN="unterminated\n');
    assert.throws(() => valuesFromEnvFile(envFile), /unsupported Compose env syntax/);
    await writeFile(envFile, 'TOKEN="secret${OTHER}"\n');
    assert.throws(() => valuesFromEnvFile(envFile), /variable interpolation is unsupported/);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

async function runReleaseWithFakeDocker(mode) {
  const directory = await mkdtemp(join(tmpdir(), "dense-mem-release-"));
  const ciHome = join(directory, "dense-mem-ci");
  const leaseDirectory = join(ciHome, "leases");
  const digest = `sha256:${"a".repeat(64)}`;
  const leasePath = join(leaseDirectory, `${digest.slice("sha256:".length)}.1.1.lease`);
  const dockerPath = join(directory, "docker");
  await mkdir(leaseDirectory, { recursive: true });
  await writeFile(leasePath, [
    "contract=dense-mem-ci-e2e.v1",
    "repository=markhuangai/dense-mem",
    "run_id=1",
    "run_attempt=1",
    "image=ghcr.io/markhuangai/dense-mem",
    `digest=${digest}`,
    "created_at=2026-01-01T00:00:00Z",
    "",
  ].join("\n"));
  await writeFile(dockerPath, `#!/usr/bin/env bash
set -euo pipefail
mode=${JSON.stringify(mode)}
if [[ "\${1:-}" == "image" && "\${2:-}" == "inspect" ]]; then
  case "$mode" in
    missing)
      printf '%s\\n' 'Error response from daemon: No such image: candidate' >&2
      exit 1
      ;;
    daemon)
      printf '%s\\n' 'Cannot connect to the Docker daemon' >&2
      exit 1
      ;;
    present)
      if [[ " $* " == *" --format "* ]]; then printf '%s\\n' 'sha256:fixture'; else printf '%s\\n' '{}'; fi
      exit 0
      ;;
  esac
fi
if [[ "\${1:-}" == "image" && "\${2:-}" == "rm" ]]; then
  if [[ "$mode" == "missing" ]]; then
    printf '%s\\n' 'Error response from daemon: No such image: candidate' >&2
    exit 1
  fi
  if [[ "$mode" == "present" ]]; then
    printf '%s\\n' 'Error response from daemon: image is in use' >&2
    exit 1
  fi
fi
exit 0
`);
  await chmod(dockerPath, 0o755);
  try {
    let result;
    try {
      result = await execFileAsync(
        controllerPath,
        ["release", leasePath],
        {
          env: {
            ...process.env,
            PATH: `${directory}:${process.env.PATH}`,
            DENSE_MEM_CI_HOME: ciHome,
            DENSE_MEM_CI_REPOSITORY: "markhuangai/dense-mem",
          },
          maxBuffer: 1024 * 1024,
        },
      );
    } catch (error) {
      result = error;
    }
    return { code: result.code ?? 0, leaseExists: existsSync(leasePath) };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

test("release distinguishes absent images from Docker inspection failures", async () => {
  const missing = await runReleaseWithFakeDocker("missing");
  assert.equal(missing.code, 0);
  assert.equal(missing.leaseExists, false);

  const daemonFailure = await runReleaseWithFakeDocker("daemon");
  assert.notEqual(daemonFailure.code, 0);
  assert.equal(daemonFailure.leaseExists, true);

  const stillPresent = await runReleaseWithFakeDocker("present");
  assert.notEqual(stillPresent.code, 0);
  assert.equal(stillPresent.leaseExists, true);
});
