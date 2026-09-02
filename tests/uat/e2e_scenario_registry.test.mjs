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
  const marker = new RegExp(`^  ${name.replace(/[.*+?^${}()|[\\]\\\\]/g, "\\\\$&")}:\\n`, "m");
  const match = workflow.match(marker);
  assert.ok(match, `workflow job ${name} is missing`);
  const start = match.index + match[0].length;
  const remainder = workflow.slice(start);
  const nextJob = remainder.search(/\n  [a-z0-9-]+:\n/);
  return nextJob === -1 ? remainder : remainder.slice(0, nextJob);
}

function assertWorkflowOrchestration(workflow) {
  const exclusive = workflowJob(workflow, "exclusive");
  const exclusiveCleanup = workflowJob(workflow, "exclusive-cleanup");
  const sharedStart = workflowJob(workflow, "shared-start");
  const shared = workflowJob(workflow, "shared");
  const sharedStop = workflowJob(workflow, "shared-stop");
  const report = workflowJob(workflow, "report");

  assert.match(exclusive, /^    needs: \[authorize, prechecks, stale-cleanup\]$/m);
  assert.match(exclusive, /^    strategy:\n      fail-fast: true\n      max-parallel: 4$/m);
  assert.match(exclusiveCleanup, /^    needs: \[authorize, prechecks, stale-cleanup, exclusive\]$/m);
  assert.match(exclusiveCleanup, /^    if: always\(\) && needs\.authorize\.result == 'success'$/m);
  assert.match(exclusiveCleanup, /^    runs-on: rootless-docker$/m);
  assert.match(exclusiveCleanup, /scripts\/e2e-host-controller\.sh stale-cleanup 1 \\\n\s+"\$\{GITHUB_RUN_ID\}" "\$\{GITHUB_RUN_ATTEMPT\}" exclusive/);
  assert.match(sharedStart, /^    needs: \[authorize, prechecks, stale-cleanup, exclusive, exclusive-cleanup\]$/m);
  assert.match(sharedStart, /^    if: needs\.exclusive\.result == 'success' && needs\.exclusive-cleanup\.result == 'success'$/m);
  assert.match(shared, /^    needs: \[authorize, shared-start\]$/m);
  assert.match(shared, /^    strategy:\n      fail-fast: true\n      max-parallel: 4$/m);
  assert.match(sharedStop, /^    needs: \[shared-start, shared\]$/m);
  assert.match(sharedStop, /^    if: always\(\) && needs\.shared-start\.result == 'success'$/m);
  assert.match(report, /^    needs: \[authorize, prechecks, stale-cleanup, exclusive, exclusive-cleanup, shared-start, shared, shared-stop\]$/m);
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
  assert.deepEqual(helperProfilesFor(registry, "shared_team"), ["verifier"]);
});

test("registry validation fails closed for duplicate, unknown, and non-production rows", () => {
  const invalid = structuredClone(registry);
  invalid.scenarios[0].name = invalid.scenarios[1].name;
  invalid.scenarios[1].runtime = "evaluation";
  invalid.scenarios.push({ name: "future_scenario", isolation: "unknown", runtime: "production", helper_profiles: ["unknown"], timeout_minutes: 1, playwright: false });
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

test("scenario classification fails closed for invalid registry metadata", () => {
  const invalid = structuredClone(registry);
  invalid.scenarios[0].runtime = "development";
  assert.throws(() => classifyScenario(invalid, invalid.scenarios[0].name), /invalid E2E scenario registry:.*runtime=production/s);
});

test("production jobs use capability-matched runners and PR-owned assets", async () => {
  const [workflow, reusable, controller, compose] = await Promise.all([
    readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-ci-compose.yml", import.meta.url), "utf8"),
  ]);
  for (const job of ["authorize", "report"]) assert.match(workflowJob(workflow, job), /^    runs-on: docker-runner$/m);
  for (const job of ["prechecks", "stale-cleanup", "exclusive-cleanup", "shared-start", "shared-stop"]) assert.match(workflowJob(workflow, job), /^    runs-on: rootless-docker$/m);
  assert.match(workflowJob(reusable, "scenario"), /^    runs-on: rootless-docker$/m);
  assert.doesNotMatch(workflow, /rootless-docker-shared|runs-on:\s*pc|workflow_dispatch/);
  const workflowCall = workflow.slice(workflow.indexOf("on:\n  workflow_call:"), workflow.indexOf("\npermissions:"));
  const scenarioCall = reusable.slice(reusable.indexOf("on:\n  workflow_call:"), reusable.indexOf("\npermissions:"));
  assert.match(workflowCall, /^      image:$/m);
  assert.doesNotMatch(workflowCall, /test_repository|test_revision|main_revision|source_revision/);
  for (const input of ["image", "scenario", "timeout_minutes", "shared_project"]) assert.match(scenarioCall, new RegExp(`^      ${input}:$`, "m"));
  assert.doesNotMatch(scenarioCall, /^      (manifest|digest|test_repository|test_revision|phase|helper_profiles|playwright):$/m);
  assert.match(workflow, /shared_project: \$\{\{ steps\.start\.outputs\.shared_project \}\}/);
  assert.match(workflow, /max-parallel: 4/);
  assert.match(workflow, /repository: \$\{\{ github\.event\.pull_request\.head\.repo\.full_name \}\}/);
  assert.match(workflow, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.match(workflow, /path: \.ci-policy[\s\S]*?sparse-checkout:\s*\|\n\s+\.github\/scripts\n\s+scripts\/e2e-scenario-registry\.mjs/);
  assert.match(workflow, /node \.ci-policy\/scripts\/e2e-scenario-registry\.mjs --matrix exclusive/);
  assert.doesNotMatch(workflow, /node \.ci-source\/scripts\/e2e-scenario-registry\.mjs --matrix/);
  assert.match(reusable, /repository: \$\{\{ github\.event\.pull_request\.head\.repo\.full_name \}\}/);
  assert.match(reusable, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.match(controller, /DENSE_MEM_CI_PROMETHEUS_FILE/);
  assert.match(controller, /DENSE_MEM_CI_TELEMETRY_TOKEN_FILE/);
  assert.doesNotMatch(controller, /DENSE_MEM_CI_DAEMON_ID|LEASE_DIR|RUN_DIR|DENSE_MEM_E2E_SOURCE_REVISION|e2e-docker-proxy|e2e-runtime-adapter/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
  assertWorkflowOrchestration(workflow);
});

test("production orchestration assertions detect a missing shared dependency", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8");
  assert.doesNotThrow(() => assertWorkflowOrchestration(workflow));
  const mutated = workflow.replace(/^    needs: \[authorize, prechecks, stale-cleanup, exclusive, exclusive-cleanup\]$/m, "    needs: [authorize, prechecks]");
  assert.notEqual(mutated, workflow);
  assert.throws(() => assertWorkflowOrchestration(mutated));
});
