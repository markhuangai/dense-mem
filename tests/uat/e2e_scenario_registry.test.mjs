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
  assert.match(build, /^    runs-on: ubuntu-latest$/m);
  assert.match(
    build,
    /- name: Set up QEMU\n\s+uses: docker\/setup-qemu-action@v4\n\s+with:\n\s+platforms: arm64\n\n\s+- name: Set up Docker Buildx/,
    "preview build must configure ARM emulation before Buildx",
  );
  assert.match(
    build,
    /- name: Expose GitHub Actions runtime\n\s+uses: crazy-max\/ghaction-github-runtime@v4\n\n\s+- name: Build production OCI layout/,
    "preview build must expose the GitHub Actions runtime before Buildx",
  );
  for (const line of [
    "docker buildx build \\",
    "--target preview",
    "--platform linux/amd64,linux/arm64/v8",
    "--provenance=false",
    '--cache-from "type=gha,scope=dense-mem-preview-${PR_NUMBER}"',
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
  const databasePrechecks = workflowJob(workflow, "database-prechecks");
  const scenarios = workflowJob(workflow, "scenarios");
  const report = workflowJob(workflow, "report");
  assert.ok(databasePrechecks.includes("shard: [0, 1, 2]"));
  assert.ok(databasePrechecks.includes("max-parallel: 3"));
  assert.ok(databasePrechecks.includes("timeout-minutes: 45"));
  assert.ok(databasePrechecks.includes("runs-on: ubuntu-latest"));
  assert.ok(scenarios.includes("needs: [authorize, prechecks, database-prechecks]"));
  assert.ok(scenarios.includes("needs.database-prechecks.result == 'success'"));
  assert.ok(scenarios.includes("max-parallel: 4"));
  assert.ok(scenarios.includes("matrix: ${{ fromJSON(needs.authorize.outputs.scenario_matrix) }}"));
  assert.ok(scenarios.includes("source_revision: ${{ needs.authorize.outputs.source_revision }}"));
  assert.ok(report.includes("needs: [authorize, prechecks, database-prechecks, scenarios]"));
  assert.ok(report.includes("SCENARIO_RESULT: ${{ needs.scenarios.result }}"));
  assert.ok(report.includes("if: always()"));
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
  const withoutQemu = workflow.replace(
    /\n      - name: Set up QEMU\n        uses: docker\/setup-qemu-action@v4\n        with:\n          platforms: arm64\n/,
    "",
  );
  assert.notEqual(withoutQemu, workflow);
  assert.throws(() => assertPreviewBuildPolicy(withoutQemu), /ARM emulation/);
  const withoutRuntime = workflow.replace(
    /\n      - name: Expose GitHub Actions runtime\n        uses: crazy-max\/ghaction-github-runtime@v4\n/,
    "",
  );
  assert.notEqual(withoutRuntime, workflow);
  assert.throws(() => assertPreviewBuildPolicy(withoutRuntime), /GitHub Actions runtime/);
  const mutated = workflow.replace("--provenance=false", "--provenance=true");
  assert.notEqual(mutated, workflow);
  assert.throws(() => assertPreviewBuildPolicy(mutated), /preview build is missing --provenance=false/);
});

test("production E2E runs the complete exact-source registry on hosted isolated jobs", async () => {
  const [workflow, reusable, caller, bootstrap] = await Promise.all([
    readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/production-e2e-scenario.yml", import.meta.url), "utf8"),
    readFile(new URL("../../.github/workflows/pr-test-image.yml", import.meta.url), "utf8"),
    readFile(new URL("../../scripts/e2e-ci-bootstrap.sh", import.meta.url), "utf8"),
  ]);
  assert.equal(matrixFor(registry, "all").include.length, 24);
  assertWorkflowOrchestration(workflow);
  const authorize = workflowJob(workflow, "authorize");
  const prechecks = workflowJob(workflow, "prechecks");
  const database = workflowJob(workflow, "database-prechecks");
  const scenario = workflowJob(reusable, "scenario");
  assertNode24Setup(authorize);
  for (const job of [prechecks, database, scenario]) {
    assert.ok(job.includes("runs-on: ubuntu-latest"));
    assertNode24Setup(job);
    assert.ok(job.includes("docker/setup-docker-action@v5"));
    assert.ok(job.includes("rootless: true"));
    assert.ok(job.includes("set-host: true"));
    assert.match(job, /daemon-config: \|\s*\{"exec-opts":\["native\.cgroupdriver=cgroupfs"\]\}/);
    assert.ok(job.includes("scripts/e2e-ci-bootstrap.sh"));
    assert.doesNotMatch(job, /DOCKER_HOST: unix:\/\/\$\{\{ steps\.docker\.outputs\.sock \}\}/);
  }
  assert.ok(authorize.includes("--matrix all"));
  assert.ok(authorize.includes("--validate-compatible"));
  assert.ok(authorize.includes("source_revision"));
  assert.ok(authorize.includes("parseSuccessfulPolicyStatus"));
  assert.ok(authorize.includes("listJobsForWorkflowRun"));
  assert.ok(authorize.includes("Publish trusted preview"));
  assert.ok(authorize.includes("manual E2E trials require a repository admin"));
  assert.ok(workflow.includes("workflow_dispatch:"));
  assert.ok(workflow.includes("CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}"));
  assert.ok(caller.includes("source_revision: ${{ needs.resolve.outputs.head_sha }}"));
  assert.ok(caller.includes("cloudflare_account_id: ${{ vars.CLOUDFLARE_ACCOUNT_ID }}"));
  assert.ok(caller.includes("CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}"));
  assert.ok(bootstrap.includes("RUNNER_TEMP"));
  assert.ok(bootstrap.includes("::add-mask::"));
  assert.ok(bootstrap.includes("AI_API_EMBEDDING_MAX_BATCH_ITEMS: \"100\""));
  assert.ok(scenario.includes("timeout-minutes: ${{ inputs.timeout_minutes }}"));
  assert.ok(scenario.includes("scripts/e2e-host-controller.sh start"));
  assert.ok(scenario.includes("scripts/e2e-host-controller.sh run"));
  assert.ok(scenario.includes("scripts/e2e-host-controller.sh stop"));
  assert.ok(scenario.includes("if: always() && steps.stack.outputs.project != ''"));
  assert.ok(scenario.includes("::stop-commands::"));
  assert.doesNotMatch(workflow, /runs-on: rootless-docker|shared-start:|shared-stop:|exclusive-cleanup:/);
  assert.doesNotMatch(workflow, /secrets:\s*inherit/);
  assert.doesNotThrow(() => assertPreviewBuildPolicy(caller));
});

test("production orchestration assertions detect a missing precheck dependency", async () => {
  const workflow = await readFile(new URL("../../.github/workflows/production-image-e2e.yml", import.meta.url), "utf8");
  assert.doesNotThrow(() => assertWorkflowOrchestration(workflow));
  const mutated = workflow.replace(
    "needs: [authorize, prechecks, database-prechecks]",
    "needs: [authorize, prechecks]",
  );
  assert.notEqual(mutated, workflow);
  assert.throws(() => assertWorkflowOrchestration(mutated));
});
