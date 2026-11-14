import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import "./development_e2e.test.mjs";
import { actionStep, actionSteps, jobScripts, parseWorkflow, readWorkflow, runStep, stringifyWorkflow, workflowJob } from "./workflow_helpers.mjs";

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

test("LangChain retrieval is included in the full production matrix with auth fixtures", async () => {
  const scenario = classifyScenario(registry, "langchain_retriever");
  assert.equal(scenario.audited, true);
  assert.equal(scenario.runtime, "production");
  assert.equal(scenario.isolation, "exclusive");
  assert.deepEqual(scenario.helper_profiles, ["oauth", "synchronous_write"]);
  assert.equal(scenario.timeout_minutes, 60);
  assert.equal(scenario.playwright, false);
  assert.ok(matrixFor(registry, "all").include.some(({ name }) => name === "langchain_retriever"));
  const runner = await readFile(new URL("../../scripts/e2e-scenario.sh", import.meta.url), "utf8");
  assert.match(runner, /langchain_retriever\) bash .*packages\/python\/scripts\/e2e\.sh" langchain_retriever/);
});

test("ADK memory is an exclusive production scenario with the real intake fixture", () => {
  const scenario = classifyScenario(registry, "google_adk_memory");
  assert.equal(scenario.audited, true);
  assert.equal(scenario.runtime, "production");
  assert.equal(scenario.isolation, "exclusive");
  assert.deepEqual(scenario.helper_profiles, ["synchronous_write"]);
  assert.ok(matrixFor(registry, "all").include.some(({ name }) => name === "google_adk_memory"));
  assert.equal(scenario.timeout_minutes, 60);
});

function assertNodeSetup(job) {
  const node = actionStep(job, "actions/setup-node");
  assert.ok(String(node.with?.["node-version"] ?? "").trim(), "Node version must be configured");
  assert.equal(String(node.with["package-manager-cache"]), "false");
}

function assertDockerSetup(job) {
  const docker = actionStep(job, "docker/setup-docker-action");
  assert.equal(String(docker.with?.rootless), "true");
  assert.equal(String(docker.with?.["set-host"]), "true");
  assert.deepEqual(JSON.parse(docker.with["daemon-config"]), { "exec-opts": ["native.cgroupdriver=cgroupfs"] });
  runStep(job, "scripts/e2e-ci-bootstrap.sh");
  assert.ok(!job.steps.some((step) => step.env?.DOCKER_HOST?.includes("steps.docker.outputs.sock")));
}

function assertPreviewBuildPolicy(workflow) {
  const build = workflowJob(workflow, "build");
  assert.equal(build["runs-on"], "ubuntu-latest");
  const qemu = actionStep(build, "docker/setup-qemu-action");
  const buildx = actionStep(build, "docker/setup-buildx-action");
  const runtime = actionStep(build, "crazy-max/ghaction-github-runtime");
  const command = runStep(build, "docker buildx build");
  assert.equal(qemu.with.platforms, "arm64");
  assert.ok(build.steps.indexOf(qemu) < build.steps.indexOf(buildx), "ARM emulation must precede Buildx");
  assert.ok(build.steps.indexOf(buildx) < build.steps.indexOf(command));
  assert.ok(build.steps.indexOf(runtime) < build.steps.indexOf(command), "GitHub Actions runtime must precede the build");
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
    assert.ok(command.run.includes(line), `preview build is missing ${line}`);
  }
}

function assertWorkflowOrchestration(workflow) {
  const database = workflowJob(workflow, "database-prechecks");
  const scenarios = workflowJob(workflow, "scenarios");
  const report = workflowJob(workflow, "report");
  assert.deepEqual(database.strategy.matrix.shard, [0, 1, 2, 3, 4, 5, 6]);
  assert.equal(database.strategy["max-parallel"], 7);
  assert.equal(database["timeout-minutes"], 30);
  assert.equal(database["runs-on"], "ubuntu-latest");
  assert.deepEqual(new Set(scenarios.needs), new Set(["authorize", "prechecks"]));
  assert.doesNotMatch(JSON.stringify(scenarios), /needs\.database-prechecks/);
  assert.equal(scenarios.strategy["max-parallel"], 8);
  assert.equal(scenarios.strategy.matrix, "${{ fromJSON(needs.authorize.outputs.scenario_matrix) }}");
  assert.equal(scenarios.with.source_revision, "${{ needs.authorize.outputs.source_revision }}");
  assert.deepEqual(new Set(report.needs), new Set(["authorize", "prechecks", "database-prechecks", "scenarios", "ci-trial"]));
  assert.equal(runStep(report, "SCENARIO_RESULT").env.SCENARIO_RESULT, "${{ needs.scenarios.result }}");
  assert.equal(report.if, "always()");
}

test("production E2E registry is complete and valid", () => {
  assert.deepEqual(validateRegistry(registry), []);
  assert.equal(new Set(registry.scenarios.map(({ name }) => name)).size, registry.scenarios.length);
  assert.doesNotThrow(() => assertValidRegistry(registry));
  assert.equal(matrixFor(registry, "all").include[0].name, "ontology_organization");
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

test("preview Buildx policy permits action upgrades but rejects weakened output settings", async () => {
  const workflow = await readWorkflow("pr-test-image.yml");
  assertPreviewBuildPolicy(workflow);
  const upgraded = structuredClone(workflow);
  for (const step of upgraded.jobs.build.steps) {
    step.name = "Renamed build step";
    if (step.uses) step.uses = step.uses.split("@")[0] + "@v99";
  }
  assertPreviewBuildPolicy(parseWorkflow(stringifyWorkflow(upgraded, { indent: 4, sortKeys: true, forceQuotes: true })));
  for (const action of ["docker/setup-qemu-action", "docker/setup-buildx-action", "crazy-max/ghaction-github-runtime"]) {
    const invalid = structuredClone(workflow);
    invalid.jobs.build.steps = invalid.jobs.build.steps.filter((step) => !step.uses?.startsWith(action + "@"));
    assert.throws(() => assertPreviewBuildPolicy(invalid), /missing/);
  }
  for (const mutate of [
    (job) => { job.steps.reverse(); },
    (job) => { actionStep(job, "docker/setup-qemu-action").with.platforms = "amd64"; },
    (job) => { const step = runStep(job, "docker buildx build"); step.run = step.run.replace("--provenance=false", "--provenance=true"); },
  ]) {
    const invalid = structuredClone(workflow);
    mutate(invalid.jobs.build);
    assert.throws(() => assertPreviewBuildPolicy(invalid));
  }
});

test("hosted setup permits upgrades but rejects missing configuration and weaker isolation", async () => {
  const workflow = await readWorkflow("production-image-e2e.yml");
  const job = workflowJob(workflow, "prechecks");
  assertNodeSetup(job);
  assertDockerSetup(job);
  const upgraded = structuredClone(job);
  for (const step of upgraded.steps) {
    step.name = "Renamed setup step";
    if (step.uses) step.uses = step.uses.split("@")[0] + "@" + "a".repeat(40);
  }
  actionStep(upgraded, "actions/setup-node").with["node-version"] = "26";
  actionStep(upgraded, "actions/setup-node").with["package-manager-cache"] = "false";
  actionStep(upgraded, "docker/setup-docker-action").with.rootless = "true";
  const reformatted = parseWorkflow(stringifyWorkflow(upgraded, { indent: 4, sortKeys: true, forceQuotes: true }));
  assertNodeSetup(reformatted);
  assertDockerSetup(reformatted);
  for (const mutate of [
    (j) => { delete actionStep(j, "actions/setup-node").with["node-version"]; },
    (j) => { actionStep(j, "actions/setup-node").with["package-manager-cache"] = true; },
    (j) => { actionStep(j, "docker/setup-docker-action").with.rootless = false; },
    (j) => { delete actionStep(j, "docker/setup-docker-action").with["set-host"]; },
    (j) => { actionStep(j, "docker/setup-docker-action").uses = "untrusted/setup-docker-action@v99"; },
  ]) {
    const invalid = structuredClone(job);
    mutate(invalid);
    assert.throws(() => { assertNodeSetup(invalid); assertDockerSetup(invalid); });
  }
});

test("production E2E runs the complete exact-source registry on hosted isolated jobs", async () => {
  const [workflow, reusable, caller, bootstrap] = await Promise.all([
    readWorkflow("production-image-e2e.yml"),
    readWorkflow("production-e2e-scenario.yml"),
    readWorkflow("pr-test-image.yml"),
    readFile(new URL("../../scripts/e2e-ci-bootstrap.sh", import.meta.url), "utf8"),
  ]);
  assert.equal(matrixFor(registry, "all").include.length, 30);
  assert.equal(classifyScenario(registry, "ontology_upgrade").isolation, "exclusive");
  assertWorkflowOrchestration(workflow);
  const authorize = workflowJob(workflow, "authorize");
  const prechecks = workflowJob(workflow, "prechecks");
  const database = workflowJob(workflow, "database-prechecks");
  const scenario = workflowJob(reusable, "scenario");
  assertNodeSetup(authorize);
  for (const job of [prechecks, database, scenario]) {
    assert.equal(job["runs-on"], "ubuntu-latest");
    assertNodeSetup(job);
    assertDockerSetup(job);
  }
  for (const required of ["--matrix all", "--validate-compatible", "source_revision", "parseSuccessfulPolicyStatus",
    "listJobsForWorkflowRun", "Publish trusted preview", "manual E2E trials require a repository admin"]) {
    assert.ok(jobScripts(authorize).includes(required));
  }
  assert.ok(Object.hasOwn(workflow.on, "workflow_dispatch"));
  assert.equal(prechecks.steps.find((step) => step.env?.CLOUDFLARE_API_TOKEN)?.env.CLOUDFLARE_API_TOKEN, "${{ secrets.CLOUDFLARE_API_TOKEN }}");
  const e2e = workflowJob(caller, "production-e2e");
  assert.equal(e2e.with.source_revision, "${{ needs.resolve.outputs.head_sha }}");
  assert.equal(e2e.with.cloudflare_account_id, "${{ vars.CLOUDFLARE_ACCOUNT_ID }}");
  assert.equal(e2e.secrets.CLOUDFLARE_API_TOKEN, "${{ secrets.CLOUDFLARE_API_TOKEN }}");
  assert.ok(bootstrap.includes("RUNNER_TEMP"));
  assert.ok(bootstrap.includes("::add-mask::"));
  assert.ok(bootstrap.includes('AI_API_EMBEDDING_MAX_BATCH_ITEMS: "100"'));
  assert.equal(scenario["timeout-minutes"], "${{ inputs.timeout_minutes > 30 && 30 || inputs.timeout_minutes }}");
  runStep(scenario, "scripts/e2e-host-controller.sh start");
  runStep(scenario, "scripts/e2e-host-controller.sh run");
  assert.equal(runStep(scenario, "scripts/e2e-host-controller.sh stop").if, "always() && steps.stack.outputs.project != ''");
  assert.match(jobScripts(scenario), /::stop-commands::/);
  for (const [id, job] of Object.entries(workflow.jobs)) {
    assert.ok(!["shared-start", "shared-stop", "exclusive-cleanup"].includes(id));
    assert.notEqual(job["runs-on"], "rootless-docker");
    assert.notEqual(job.secrets, "inherit");
  }
  assert.doesNotThrow(() => assertPreviewBuildPolicy(caller));
});

test("production orchestration assertions detect a missing precheck dependency", async () => {
  const workflow = await readWorkflow("production-image-e2e.yml");
  assert.doesNotThrow(() => assertWorkflowOrchestration(workflow));
  assertWorkflowOrchestration(parseWorkflow(stringifyWorkflow(workflow, { indent: 4, sortKeys: true })));
  const mutated = structuredClone(workflow);
  mutated.jobs.scenarios.needs = ["authorize"];
  assert.throws(() => assertWorkflowOrchestration(mutated));
});

test("the production aggregate requires every independent phase and manual CI trial", async () => {
  const workflow = await readWorkflow("production-image-e2e.yml");
  const report = workflowJob(workflow, "report");
  const command = runStep(report, "SCENARIO_RESULT").run;
  const passing = { AUTH_RESULT: "success", PRECHECK_RESULT: "success", DATABASE_PRECHECK_RESULT: "success", SCENARIO_RESULT: "success",
    CI_TRIAL_RESULT: "success", EVENT_NAME: "workflow_dispatch", IMAGE: "fixture-image", SOURCE_REVISION: "a".repeat(40), GITHUB_STEP_SUMMARY: "/dev/null" };
  const execute = (env) => spawnSync("bash", ["-c", command], { encoding: "utf8", env: { ...process.env, ...env } });
  assert.equal(execute(passing).status, 0);
  for (const field of ["AUTH_RESULT", "PRECHECK_RESULT", "DATABASE_PRECHECK_RESULT", "SCENARIO_RESULT", "CI_TRIAL_RESULT"]) {
    for (const result of ["failure", "cancelled", "skipped", ""]) {
      assert.notEqual(execute({ ...passing, [field]: result }).status, 0, `${field}=${result}`);
    }
  }
  assert.equal(execute({ ...passing, EVENT_NAME: "pull_request_target", CI_TRIAL_RESULT: "skipped" }).status, 0);
  const trial = workflowJob(workflow, "ci-trial");
  assert.deepEqual([trial.needs].flat(), ["authorize"]);
  assert.equal(trial.if, "github.event_name == 'workflow_dispatch'");
  assert.equal(trial.uses, "./.github/workflows/ci-shared.yml");
  assert.equal(trial.with["source-revision"], "${{ needs.authorize.outputs.source_revision }}");
});

test("PostgreSQL deadlines include setup and leave cleanup time in every workflow", async () => {
  for (const [file, job] of [["production-image-e2e.yml", "database-prechecks"], ["development-e2e.yml", "postgres"], ["fork-e2e.yml", "database"]]) {
    const workflow = await readWorkflow(file);
    const definition = workflowJob(workflow, job);
    const deadline = runStep(definition, "27 * 60");
    for (const checkout of actionSteps(definition, "actions/checkout")) {
      assert.ok(definition.steps.indexOf(deadline) < definition.steps.indexOf(checkout));
    }
    assert.equal(definition["timeout-minutes"], 30);
    const execution = runStep(definition, "scripts/e2e-host-controller.sh precheck").run;
    assert.match(execution, /timeout --signal=TERM --kill-after=30s "\$\{remaining\}s" scripts\/e2e-host-controller\.sh precheck/);
    const prefix = execution.slice(0, execution.indexOf("timeout --signal=TERM"));
    const invoke = (seconds) => spawnSync("bash", ["-c", prefix], { env: { ...process.env, POSTGRES_DEADLINE: String(Math.floor(Date.now() / 1000) + seconds) } });
    assert.equal(invoke(60).status, 0, file);
    assert.equal(invoke(-1).status, 124, file);
  }
});
