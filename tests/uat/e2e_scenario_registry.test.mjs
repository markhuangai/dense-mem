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
  const marker = `  ${name}:`;
  const start = workflow.indexOf(marker);
  assert.notEqual(start, -1, `workflow job ${name} is missing`);
  const remainder = workflow.slice(start + marker.length);
  const nextJob = remainder.search(/\n  [a-z0-9-]+:\n/);
  return nextJob === -1 ? remainder : remainder.slice(0, nextJob);
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
    const start = workflow.indexOf(`  ${job}`);
    assert.notEqual(start, -1);
    const remainder = workflow.slice(start + job.length);
    const nextJob = remainder.search(/\n  [a-z0-9-]+:\n/);
    const block = nextJob === -1 ? remainder : remainder.slice(0, nextJob);
    assert.match(block, /runs-on: docker-runner/);
  }
  for (const job of ["prechecks:", "acquire:", "shared-start:", "shared-stop:", "release:"]) {
    const start = workflow.indexOf(`  ${job}`);
    assert.notEqual(start, -1);
    const remainder = workflow.slice(start + job.length);
    const nextJob = remainder.search(/\n  [a-z0-9-]+:\n/);
    const block = nextJob === -1 ? remainder : remainder.slice(0, nextJob);
    assert.match(block, /runs-on:\s*(?:rootless-docker|\[rootless-docker(?:,|\]))/);
  }
  assert.match(reusable, /runs-on:\s*(?:rootless-docker|\[rootless-docker(?:,|\]))/);
  assert.match(workflow, /max-parallel: 4/);
  assert.match(workflowJob(workflow, "exclusive"), /max-parallel: 1/);
  assert.match(workflowJob(workflow, "shared"), /max-parallel: 4/);
  assert.match(workflowJob(workflow, "shared-start"), /runs-on: \[rootless-docker, rootless-docker-shared\]/);
  assert.match(workflowJob(workflow, "shared-stop"), /runs-on: \[rootless-docker, rootless-docker-shared\]/);
  assert.match(reusable, /runs-on: \[rootless-docker, rootless-docker-shared\]/);
  assert.match(workflow, /SHARED_STOP_RESULT/);
  assert.match(workflow, /passed \(cleanup failed\)/);
  assert.match(workflow, /e2e_host_controller_real\.sh/);
  assert.match(workflow, /ref: main[\s\S]*path: \.ci-controller-contract/);
  assert.match(workflow, /source_repository: \$\{\{ needs\.authorize\.outputs\.source_repository \}\}/);
  assert.match(reusable, /repository: \$\{\{ inputs\.source_repository \}\}/);
  assert.match(workflow, /ref: \$\{\{ steps\.resolve\.outputs\.source_revision \}\}/);
  assert.doesNotMatch(workflow, /ref: \$\{\{ inputs\.source_revision \|\| 'main' \}\}/);
  assert.match(controller, /docker compose/);
  assert.match(controller, /run --rm/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
  assert.doesNotMatch(workflow, /runs-on:\s*\[pc|runs-on:.*docker-runner.*e2e/i);
});
