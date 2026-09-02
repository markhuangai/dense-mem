import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import {
  EXPECTED_SCENARIOS,
  assertValidRegistry,
  classifyScenario,
  helperProfilesFor,
  matrixFor,
  readRegistry,
  validateRegistry,
} from "../../scripts/e2e-scenario-registry.mjs";

const registry = readRegistry();

function workflowJob(workflow, name) {
  const marker = new RegExp(`^  ${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}:\\n`, "m");
  const match = workflow.match(marker);
  assert.ok(match, `workflow job ${name} is missing`);
  const start = match.index + match[0].length;
  const remainder = workflow.slice(start);
  const nextJob = remainder.search(/\n  [a-z0-9-]+:\n/);
  return nextJob === -1 ? remainder : remainder.slice(0, nextJob);
}

function assertWorkflowOrchestration(workflow) {
  const exclusive = workflowJob(workflow, "exclusive");
  const sharedStart = workflowJob(workflow, "shared-start");
  const shared = workflowJob(workflow, "shared");
  const sharedStop = workflowJob(workflow, "shared-stop");
  const release = workflowJob(workflow, "release");
  const cleanup = workflowJob(workflow, "cleanup-run");
  const report = workflowJob(workflow, "report");

  assert.match(exclusive, /^    needs: \[authorize, acquire\]$/m);
  assert.match(exclusive, /^    strategy:\n      fail-fast: true\n      max-parallel: 1$/m);
  assert.match(sharedStart, /^    needs: \[authorize, acquire, exclusive\]$/m);
  assert.match(sharedStart, /^    if: needs\.exclusive\.result == 'success'$/m);
  assert.match(shared, /^    needs: \[authorize, acquire, shared-start\]$/m);
  assert.match(shared, /^    strategy:\n      fail-fast: true\n      max-parallel: 4$/m);
  assert.match(sharedStop, /^    needs: \[shared-start, shared\]$/m);
  assert.match(sharedStop, /^    if: always\(\) && needs\.shared-start\.result == 'success'$/m);
  assert.match(release, /^    needs: \[authorize, acquire, exclusive, shared-start, shared, shared-stop\]$/m);
  assert.match(release, /^    if: always\(\) && needs\.acquire\.result == 'success' && needs\.acquire\.outputs\.lease != ''$/m);
  assert.match(cleanup, /^    needs: \[authorize, prechecks, stale-cleanup, acquire, exclusive, shared-start, shared, shared-stop, release\]$/m);
  assert.match(cleanup, /^    if: always\(\)$/m);
  assert.match(report, /^    needs: \[authorize, prechecks, stale-cleanup, acquire, exclusive, shared-start, shared, shared-stop, release, cleanup-run\]$/m);
  assert.match(report, /^    if: always\(\)$/m);
}

test("production E2E registry is complete and valid", () => {
  assert.deepEqual(validateRegistry(registry), []);
  assert.deepEqual(registry.scenarios.map(({ name }) => name), EXPECTED_SCENARIOS);
  assert.doesNotThrow(() => assertValidRegistry(registry));
});

test("registry partitions eleven isolated and ten team-scoped scenarios", () => {
  assert.equal(matrixFor(registry, "exclusive").include.length, 11);
  assert.equal(matrixFor(registry, "shared_team").include.length, 10);
  assert.equal(helperProfilesFor(registry, "shared_team").includes("playwright"), false);
});

test("registry validation fails closed for duplicate, unknown, and non-production rows", () => {
  const invalid = structuredClone(registry);
  invalid.scenarios[0].name = invalid.scenarios[1].name;
  invalid.scenarios[1].runtime = "evaluation";
  invalid.scenarios.push({
    name: "future_scenario",
    isolation: "unknown",
    runtime: "production",
    helper_profiles: ["unknown"],
    timeout_minutes: 1,
    playwright: false,
  });
  const errors = validateRegistry(invalid);
  assert.ok(errors.some((error) => error.includes("duplicate scenario")));
  assert.ok(errors.some((error) => error.includes("must use runtime=production")));
  assert.ok(errors.some((error) => error.includes("unknown scenario")));
  assert.ok(errors.some((error) => error.includes("unknown isolation")));
  assert.ok(errors.some((error) => error.includes("unknown helper profile")));
});

test("unregistered scenarios default to an isolated production stack", () => {
  const classified = classifyScenario(registry, "future_scenario");
  assert.equal(classified.isolation, "exclusive");
  assert.equal(classified.runtime, "production");
  assert.equal(classified.audited, false);
});

test("registry CLI classifies unknown scenarios without sharing them", async () => {
  const { execFile } = await import("node:child_process");
  const { promisify } = await import("node:util");
  const run = promisify(execFile);
  const result = await run(process.execPath, ["scripts/e2e-scenario-registry.mjs", "--scenario", "future_scenario"]);
  const classified = JSON.parse(result.stdout);
  assert.equal(classified.isolation, "exclusive");
  assert.equal(classified.audited, false);
});

test("scenario classification fails closed for invalid registry metadata", () => {
  const invalid = structuredClone(registry);
  invalid.scenarios[0].runtime = "development";
  assert.throws(
    () => classifyScenario(invalid, invalid.scenarios[0].name),
    /invalid E2E scenario registry:.*runtime=production/s,
  );
});

test("production E2E jobs use the runner that matches their capability", async () => {
  const [workflow, reusable, controllerMain, controllerStack, controllerRuntime, compose] = await Promise.all([
    readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller-stack.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller-runtime.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-ci-compose.yml", import.meta.url), "utf8"),
  ]);
  const controller = [controllerMain, controllerStack, controllerRuntime].join("\n");
  for (const job of ["authorize:", "report:"]) {
    assert.match(workflowJob(workflow, job.slice(0, -1)), /^    runs-on: docker-runner$/m);
  }
  for (const job of ["prechecks:", "acquire:", "shared-start:", "shared-stop:", "release:"]) {
    assert.match(workflowJob(workflow, job.slice(0, -1)), /^    runs-on: rootless-docker$/m);
  }
  assert.match(reusable, /^    runs-on: rootless-docker$/m);
  assert.doesNotMatch(workflow, /rootless-docker-shared/);
  assert.doesNotMatch(reusable, /rootless-docker-shared/);
  assert.doesNotMatch(workflow, /workflow_dispatch/);
  const workflowCall = workflow.slice(workflow.indexOf("on:\n  workflow_call:"), workflow.indexOf("\npermissions:"));
  const scenarioCall = reusable.slice(reusable.indexOf("on:\n  workflow_call:"), reusable.indexOf("\npermissions:"));
  for (const obsolete of [
    "source_revision",
    "source_repository",
    "main_revision",
    "pull_request_author",
    "preview_run_id",
    "preview_run_attempt",
    "caller_workflow",
  ]) {
    assert.doesNotMatch(workflow, new RegExp(`inputs\\.${obsolete}|outputs\\.${obsolete}`));
  }
  assert.match(workflow, /validatePinnedProductionImageReference/);
  assert.match(workflowCall, /^      image:$/m);
  for (const input of ["test_repository", "test_revision"]) {
    assert.doesNotMatch(workflowCall, new RegExp(`^      ${input}:$`, "m"));
  }
  for (const input of ["image", "scenario", "timeout_minutes", "manifest"]) {
    assert.match(scenarioCall, new RegExp(`^      ${input}:$`, "m"));
  }
  for (const input of ["digest", "test_repository", "test_revision", "phase", "helper_profiles", "playwright"]) {
    assert.doesNotMatch(scenarioCall, new RegExp(`^      ${input}:$`, "m"));
  }
  assert.match(workflow, /^      max-parallel: 4$/m);
  assert.doesNotMatch(workflowJob(workflow, "stale-cleanup"), /^    if:/m);
  assertWorkflowOrchestration(workflow);
  assert.match(workflow, /SHARED_STOP_RESULT/);
  assert.match(workflow, /passed \(cleanup failed\)/);
  assert.match(workflow, /e2e_host_controller_real\.sh/);
  assert.match(workflow, /ref: main[\s\S]*path: \.ci-controller-contract/);
  assert.match(workflow, /repository: \$\{\{ github\.event\.pull_request\.head\.repo\.full_name \}\}/);
  assert.match(workflow, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.match(reusable, /repository: \$\{\{ github\.event\.pull_request\.head\.repo\.full_name \}\}/);
  assert.match(reusable, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.match(reusable, /DENSE_MEM_E2E_SCENARIO_REGISTRY: \$\{\{ github\.workspace \}\}\/scripts\/e2e-scenarios\.json/);
  assert.match(reusable, /PHASE: \$\{\{ inputs\.manifest != '' && 'shared' \|\| 'exclusive' \}\}/);
  assert.match(controller, /docker compose/);
  assert.match(controller, /run --rm/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
  assert.doesNotMatch(workflow, /runs-on:\s*\[pc|runs-on:.*docker-runner.*e2e/i);
});

test("production E2E orchestration assertions reject a missing matrix dependency", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8");
  assert.doesNotThrow(() => assertWorkflowOrchestration(workflow));
  const mutated = workflow.replace(
    /^    needs: \[authorize, acquire, shared-start\]$/m,
    "    needs: [authorize, acquire]",
  );
  assert.notEqual(mutated, workflow);
  assert.throws(() => assertWorkflowOrchestration(mutated));
});
