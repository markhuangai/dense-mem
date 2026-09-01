import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

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
  assert.match(controller, /--mode precheck/);
  assert.match(controller, /redact_diagnostics/);
  assert.match(controller, /cleanup-run/);
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
  assert.match(compose, /name: \$\{DENSE_MEM_CI_NETWORK_NAME/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
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
});

test("host installer never creates or copies a credential file", () => {
  assert.match(installer, /install -m 600/);
  assert.match(installer, /e2e-docker-proxy\.mjs/);
  assert.match(installer, /e2e-runtime-adapter\.mjs/);
  assert.match(installer, /e2e-scenario-registry\.mjs/);
  assert.match(installer, /e2e-host-controller-stack\.sh/);
  assert.match(installer, /e2e-host-controller-runtime\.sh/);
  assert.match(installer, /chmod 600/);
  assert.doesNotMatch(installer, /AI_API_KEY|AI_VERIFIER_API_KEY|PASSWORD=/);
  assert.match(realControllerTest, /DENSE_MEM_E2E_REAL_DOCKER_TESTS/);
  assert.match(realControllerTest, /controller-contract/);
  assert.match(realControllerTest, /rootless Docker daemon/);
  assert.match(realControllerTest, /stale helper image/);
});
