import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import {
  assertCompatibleRegistry,
  assertValidRegistry,
  classifyScenario,
  helperProfilesFor,
  matrixFor,
  readRegistry,
  validateRegistryExtension,
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

function assertNode24Setup(job) {
  assert.match(
    job,
    /- name: Set up Node\.js\n\s+uses: actions\/setup-node@v7\n\s+with:\n\s+node-version: 24\n\s+package-manager-cache: false/,
  );
}

function assertPreviewBuildPolicy(workflow) {
  const build = workflowJob(workflow, "build");
  for (const line of [
    "docker buildx build \\",
    "--target preview",
    "--platform linux/amd64,linux/arm64/v8",
    "--provenance=false",
    "--output \"type=oci,dest=${RUNNER_TEMP}/preview-oci,tar=false,name=dense-mem:test-${PR_NUMBER}\"",
    '--build-arg "IMAGE_VERSION=test-${PR_NUMBER}"',
    '--build-arg "IMAGE_REVISION=${HEAD_SHA}"',
    '--build-arg "IMAGE_CREATED=${HEAD_CREATED}"',
    '--build-arg "PREVIEW_PR=${PR_NUMBER}"',
    '--build-arg "PREVIEW_HEAD=${HEAD_SHA}"',
    '--build-arg "PREVIEW_MAIN=${MAIN_SHA}"',
    '--build-arg "PREVIEW_RUN_ID=${RUN_ID}"',
    '--build-arg "PREVIEW_RUN_ATTEMPT=${RUN_ATTEMPT}"',
  ]) {
    assert.ok(build.includes(line), `preview build is missing ${line}`);
  }
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
  assert.equal(new Set(registry.scenarios.map(({ name }) => name)).size, registry.scenarios.length);
  assert.doesNotThrow(() => assertValidRegistry(registry));
});

test("registry partitions exclusive and team-scoped scenarios", () => {
  assert.ok(matrixFor(registry, "exclusive").include.length > 0);
  assert.ok(matrixFor(registry, "shared_team").include.length > 0);
  assert.ok(helperProfilesFor(registry, "shared_team").includes("verifier"));
});

test("registry validation fails closed for duplicate, unknown, and non-production rows", () => {
  const invalid = structuredClone(registry);
  invalid.scenarios[0].name = invalid.scenarios[1].name;
  invalid.scenarios[1].runtime = "evaluation";
  invalid.scenarios.push({ name: "future_scenario", isolation: "unknown", runtime: "production", helper_profiles: ["unknown"], timeout_minutes: 1, playwright: false });
  const errors = validateRegistry(invalid);
  assert.ok(errors.some((error) => error.includes("duplicate scenario")));
  assert.ok(errors.some((error) => error.includes("must use runtime=production")));
  assert.ok(errors.some((error) => error.includes("unknown helper profile")));
  assert.ok(errors.some((error) => error.includes("unknown isolation")));
});

test("unregistered scenarios default to an isolated production stack", () => {
  const classified = classifyScenario(registry, "future_scenario");
  assert.equal(classified.isolation, "exclusive");
  assert.equal(classified.runtime, "production");
  assert.equal(classified.audited, false);
});

test("registry compatibility permits additions but preserves baseline definitions", () => {
  const extended = structuredClone(registry);
  extended.scenarios.push({
    name: "future_scenario",
    isolation: "exclusive",
    runtime: "production",
    helper_profiles: [],
    timeout_minutes: 30,
    playwright: false,
  });
  assert.deepEqual(validateRegistryExtension(extended, registry), []);
  assert.doesNotThrow(() => assertCompatibleRegistry(extended, registry));

  const missing = structuredClone(registry);
  missing.scenarios = missing.scenarios.filter(({ name }) => name !== "mcp_oauth");
  assert.ok(validateRegistryExtension(missing, registry).includes("candidate is missing baseline scenario: mcp_oauth"));

  for (const mutate of [
    (scenario) => { scenario.isolation = "shared_team"; },
    (scenario) => { scenario.helper_profiles = ["playwright"]; scenario.playwright = true; },
    (scenario) => { scenario.timeout_minutes += 1; },
    (scenario) => { scenario.playwright = !scenario.playwright; },
  ]) {
    const changed = structuredClone(registry);
    mutate(changed.scenarios.find(({ name }) => name === "mcp_oauth"));
    const errors = validateRegistryExtension(changed, registry);
    assert.ok(errors.includes("candidate changed baseline scenario: mcp_oauth"));
    assert.throws(() => assertCompatibleRegistry(changed, registry), /candidate changed baseline scenario: mcp_oauth/);
  }
});

test("scenario classification fails closed for invalid registry metadata", () => {
  const invalid = structuredClone(registry);
  invalid.scenarios[0].runtime = "development";
  assert.throws(() => classifyScenario(invalid, invalid.scenarios[0].name), /invalid E2E scenario registry:.*runtime=production/s);
});

test("preview Buildx policy rejects weakened output settings", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/pr-test-image.yml", import.meta.url), "utf8");
  assert.doesNotThrow(() => assertPreviewBuildPolicy(workflow));
  const mutated = workflow.replace("--provenance=false", "--provenance=true");
  assert.notEqual(mutated, workflow);
  assert.throws(() => assertPreviewBuildPolicy(mutated), /preview build is missing --provenance=false/);
});

test("production jobs use capability-matched runners and PR-owned assets", async () => {
  const [workflow, reusable, caller, controller, compose, envExample] = await Promise.all([
    readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/pr-test-image.yml", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-host-controller.sh", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-stack.yml", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-ci.env.example", import.meta.url), "utf8"),
  ]);
  const authorize = workflowJob(workflow, "authorize");
  assert.match(authorize, /^    runs-on: docker-runner$/m);
  assertNode24Setup(authorize);
  const report = workflowJob(workflow, "report");
  assert.match(report, /^    runs-on: docker-runner$/m);
  assert.doesNotMatch(report, /actions\/setup-node@v7|actions\/download-artifact@v8/);
  for (const job of ["prechecks", "stale-cleanup", "exclusive-cleanup", "shared-start", "shared-stop"]) {
    const definition = workflowJob(workflow, job);
    assert.match(definition, /^    runs-on: rootless-docker$/m);
    assertNode24Setup(definition);
    assert.match(definition, /repository: \$\{\{ github\.event\.pull_request\.head\.repo\.full_name \}\}/);
    assert.match(definition, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  }
  const scenario = workflowJob(reusable, "scenario");
  assert.match(scenario, /^    runs-on: rootless-docker$/m);
  assertNode24Setup(scenario);
  assert.doesNotMatch(workflow, /rootless-docker-shared|runs-on:\s*pc|workflow_dispatch/);
  assert.doesNotMatch(workflow, /secrets:\s*inherit/);
  assert.doesNotMatch(caller, /secrets:\s*inherit/);
  assert.doesNotThrow(() => assertPreviewBuildPolicy(caller));
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
  assert.match(workflow, /path: \.ci-policy[\s\S]*?sparse-checkout:\s*\|\n\s+\.github\/scripts\n\s+scripts\/e2e-scenarios\.json\n\s+scripts\/e2e-scenario-registry\.mjs/);
  assert.match(workflow, /node \.ci-policy\/scripts\/e2e-scenario-registry\.mjs --matrix exclusive/);
  assert.doesNotMatch(workflow, /node \.ci-source\/scripts\/e2e-scenario-registry\.mjs --matrix/);
  assert.match(reusable, /repository: \$\{\{ github\.event\.pull_request\.head\.repo\.full_name \}\}/);
  assert.match(reusable, /ref: \$\{\{ github\.event\.pull_request\.head\.sha \}\}/);
  assert.doesNotMatch(envExample, /^DENSE_MEM_CI_TEST_IMAGE=/m);
  assert.match(controller, /DENSE_MEM_CI_PROMETHEUS_FILE/);
  assert.match(controller, /DENSE_MEM_CI_TELEMETRY_TOKEN_FILE/);
  assert.match(controller, /git -C "\$source_dir" archive --format=tar --prefix=workspace\//);
  assert.doesNotMatch(controller, /DENSE_MEM_CI_DAEMON_ID|DENSE_MEM_CI_DOCKER_SOCKET|LEASE_DIR|RUN_DIR|DENSE_MEM_E2E_SOURCE_REVISION|e2e-docker-proxy|e2e-runtime-adapter/);
  assert.doesNotMatch(controller, /\$\{source_dir\}:\/workspace|\$\{runtime_compose_host\}:|\$\{helper_overlay\}:|\$\{run_root\}\/results/);
  assert.doesNotMatch(compose, /^\s+ports:/m);
  assert.doesNotMatch(compose, /DENSE_MEM_CI_PROMETHEUS_FILE|DENSE_MEM_CI_TELEMETRY_TOKEN_FILE/);
  assert.match(compose, /external: true/);
  assertWorkflowOrchestration(workflow);
  assert.match(authorize, /path: \.ci-policy[\s\S]*?sparse-checkout:\s*\|\n\s+\.github\/scripts\n\s+scripts\/e2e-scenarios\.json\n\s+scripts\/e2e-scenario-registry\.mjs/);
  assert.match(authorize, /node \.ci-policy\/scripts\/e2e-scenario-registry\.mjs --matrix exclusive/);
});

test("production orchestration assertions detect a missing shared dependency", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8");
  assert.doesNotThrow(() => assertWorkflowOrchestration(workflow));
  const mutated = workflow.replace(/^    needs: \[authorize, prechecks, stale-cleanup, exclusive, exclusive-cleanup\]$/m, "    needs: [authorize, prechecks]");
  assert.notEqual(mutated, workflow);
  assert.throws(() => assertWorkflowOrchestration(mutated));
});
