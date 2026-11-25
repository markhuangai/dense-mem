import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import imagePolicy from "../../../.github/scripts/image-release-policy.cjs";
import { resolveSetting, resolveImportWorkers, requireEvaluationImportSupport, configuration, requireRunner, registryMatrix, aggregate, ownedResource, manualEvaluationRequest, resolveEvaluationImage, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { readWorkflow, workflowJob, runStep } from "../../uat/workflow_helpers.mjs";
import { serverEnvironment, databaseCapacity, startupTimings, control, location, main, setup } from "./workflow.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../datasets.json", import.meta.url)));
const valid = { DENSE_MEM_EVAL_MODEL: "configured-model", DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS: "300000", DENSE_MEM_EVAL_IMPORT_WORKERS: "10", DENSE_MEM_EVAL_VERIFIER_URL: "https://provider.example/v1", DENSE_MEM_EVAL_EMBED_URL: "https://api.openai.com/v1", DENSE_MEM_EVAL_API_KEY: "generation-canary", DENSE_MEM_EVAL_EMBED_API_KEY: "embedding-canary", WORKFLOW_EMBEDDING_MODEL: "text-embedding-3-small", WORKFLOW_EMBEDDING_DIMENSIONS: "1536" };

const repository = "markhuangai/dense-mem";
const taggedImage = `ghcr.io/${repository}:candidate`;
const pinnedImage = `ghcr.io/${repository}@sha256:${"a".repeat(64)}`;
const revision = "b".repeat(40);
const manualRequest = { image: taggedImage, repository, ref: "refs/heads/main", runAttempt: 1, actorPermission: "admin", triggeringPermission: "admin" };
const inspectedImage = { Os: "linux", Architecture: "amd64", RepoDigests: [pinnedImage], Config: { Labels: { "org.opencontainers.image.variant": "production", "org.opencontainers.image.source": `https://github.com/${repository}`, "org.opencontainers.image.revision": revision } } };

test("the local runtime rejects the retired ontology preparation operation", async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "retired-ontology-"));
  t.after(() => fs.rmSync(root, { recursive: true }));
  const env = { ...valid, EVAL_MODE: "live", EVAL_DATASET: "hotpotqa",
    EVAL_LOCAL_SEED_ID: `local-${"a".repeat(32)}`, EVAL_LOCAL_SEED_ROOT: root };
  await assert.rejects(setup(env, { operation: "bake-ontology" }), /only public seed preparation and restore verification/);
  assert.equal(fs.existsSync(location(env).stateFile), false);
});

test("startup measurements separate migration from subsequent readiness and reject unavailable boundaries", () => {
  const start = Date.parse("2026-11-16T16:00:00Z");
  const logs = ["non-JSON startup output", { time: new Date(start).toISOString(), msg: "running postgres migrations" }, { time: new Date(start + 1200).toISOString(), msg: "postgres migrations completed" }].map((event) => typeof event === "string" ? event : JSON.stringify(event)).join("\n");
  assert.deepEqual(startupTimings(logs, start + 2000), { migration: 1200, readiness: 800 });
  for (const invalid of ["", logs.split("\n").slice(0, -1).join("\n"), logs.replace("16:00:01.200", "15:59:59.000")]) assert.throws(() => startupTimings(invalid, start + 2000), /unavailable/);
  assert.throws(() => startupTimings(logs, start + 1000), /unavailable/);
});

test("evaluation requires an image supporting configurable import admission", () => {
  for (const marker of [undefined, "", "unknown", "configurable"]) {
    const inspected = structuredClone(inspectedImage);
    if (marker !== undefined) inspected.Config.Labels["io.dense-mem.postgres-advisory-lock-concurrency"] = marker;
    if (marker === "configurable") assert.doesNotThrow(() => requireEvaluationImportSupport(inspected));
    else assert.throws(() => requireEvaluationImportSupport(inspected), /newer.*image/i);
  }
  const inspected = structuredClone(inspectedImage);
  const labels = inspected.Config.Labels;
  labels["io.dense-mem.postgres-advisory-lock-concurrency"] = "configurable";
  assert.throws(() => requireEvaluationImportSupport(inspected, { skipAISecurityReview: true }), /locked public seed admission/);
  labels["io.dense-mem.public-seed-admission"] = "locked-requests-v1";
  assert.doesNotThrow(() => requireEvaluationImportSupport(inspected, { skipAISecurityReview: true }));
});

test("manual evaluation needs only an image and defaults to the full benchmark", () => {
  assert.deepEqual(manualEvaluationRequest(manualRequest), { image: taggedImage, mode: "live" });
  assert.equal(manualEvaluationRequest({ ...manualRequest, mode: "fixtures" }).mode, "fixtures");
  for (const tag of ["test-777", "v2.8.1-rc.6", "v2.8.1"]) {
    const image = `ghcr.io/${repository}:${tag}`;
    assert.equal(manualEvaluationRequest({ ...manualRequest, image: tag }).image, image);
    assert.equal(manualEvaluationRequest({ ...manualRequest, image }).image, image);
  }
  assert.equal(manualEvaluationRequest({ ...manualRequest, image: pinnedImage }).image, pinnedImage);
  for (const change of [
    { ref: "refs/heads/untrusted" }, { ref: "refs/tags/main" }, { runAttempt: 2 },
    { actorPermission: "write" }, { triggeringPermission: "maintain" },
    { image: "ghcr.io/other/project:latest" }, { image: `${taggedImage}\ncommand` },
    { mode: "none" }, { mode: "unknown" }, { mode: "replay" },
  ]) assert.throws(() => manualEvaluationRequest({ ...manualRequest, ...change }));
});

test("image resolution freezes a production digest and rejects changed or unrelated metadata", () => {
  for (const reference of [taggedImage, pinnedImage, `${taggedImage}@${pinnedImage.split("@")[1]}`]) {
    assert.deepEqual(resolveEvaluationImage(reference, repository, inspectedImage), { image: pinnedImage, revision });
  }
  for (const change of [
    { Os: "windows" }, { Architecture: "arm64" }, { Config: {} },
    { RepoDigests: [] }, { RepoDigests: ["ghcr.io/other/project@sha256:" + "a".repeat(64)] },
    { RepoDigests: [pinnedImage, `ghcr.io/${repository}@sha256:${"c".repeat(64)}`] },
  ]) assert.throws(() => resolveEvaluationImage(taggedImage, repository, { ...inspectedImage, ...change }));
  for (const [key, value] of [["variant", "development"], ["source", "https://github.com/other/project"], ["revision", "main"]]) {
    const labels = { ...inspectedImage.Config.Labels, [`org.opencontainers.image.${key}`]: value };
    assert.throws(() => resolveEvaluationImage(taggedImage, repository, { ...inspectedImage, Config: { Labels: labels } }));
  }
  assert.throws(() => resolveEvaluationImage(`ghcr.io/${repository}@sha256:${"c".repeat(64)}`, repository, inspectedImage), /digest/);
});

test("manual workflow accepts an image without PR inputs and keeps credentials in the reusable job", async () => {
  const workflow = await readWorkflow("run-evaluation.yml");
  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.equal(inputs.image.required, true); assert.equal(inputs.pr_number, undefined);
  assert.equal(inputs.mode.default, "live"); assert.deepEqual(inputs.mode.options, ["live", "fixtures"]);
  const authorize = workflowJob(workflow, "authorize");
  const request = authorize.steps.find((step) => step.id === "request");
  assert.match(request.with.script, /manualEvaluationRequest/);
  assert.match(request.with.script, /actorPermission: await permission\(context.actor\)/);
  assert.match(request.with.script, /triggeringPermission: await permission\(process.env.TRIGGERING_ACTOR\)/);
  assert.match(request.with.script, /core.setOutput\("source_revision", context.sha\)/);
  for (const step of authorize.steps) {
    assert.doesNotMatch(JSON.stringify(step.env || {}), /secrets\./);
    if (step.uses?.startsWith("docker/login-action@") || step.id === "image") assert.equal(step.if, undefined);
  }
  const resolve = runStep(authorize, "docker pull");
  assert.equal(resolve.env.REQUESTED_IMAGE, "${{ steps.request.outputs.image }}");
  assert.match(resolve.run, /resolveEvaluationImage/); assert.match(resolve.run, /"\$\{REQUESTED_IMAGE\}"/);
  assert.doesNotMatch(resolve.run, /\$\{\{ inputs\./);
  const evaluation = workflowJob(workflow, "evaluation");
  assert.equal(evaluation.uses, "./.github/workflows/evaluation.yml");
  assert.equal(evaluation.with.image, "${{ needs.authorize.outputs.image }}");
  assert.equal(evaluation.with.source_revision, "${{ needs.authorize.outputs.source_revision }}");
  assert.equal(evaluation.with.model, "dense-mem-testing"); assert.equal(evaluation.with.context_window_tokens, "500000");
  const existing = workflowJob(await readWorkflow("production-image-e2e.yml"), "evaluation");
  assert.deepEqual(evaluation.secrets, existing.secrets);
});

test("manual authorization executes both modes without PR or historical-run lookups", async (t) => {
  const workflow = await readWorkflow("run-evaluation.yml");
  const script = workflowJob(workflow, "authorize").steps.find((step) => step.id === "request").with.script;
  const execute = new (Object.getPrototypeOf(async () => {}).constructor)("github", "context", "core", script);
  for (const mode of ["live", "fixtures"]) {
    await t.test(mode, async (t) => {
      const settings = { GITHUB_WORKSPACE: process.cwd(), INPUT_IMAGE: "candidate", INPUT_MODE: mode, TRIGGERING_ACTOR: "initiator" };
      const previous = Object.fromEntries(Object.keys(settings).map((key) => [key, process.env[key]]));
      Object.assign(process.env, settings);
      t.after(() => {
        for (const [key, value] of Object.entries(previous)) {
          if (value === undefined) delete process.env[key];
          else process.env[key] = value;
        }
      });
      const permissions = []; const outputs = {};
      const github = { rest: {
        repos: { getCollaboratorPermissionLevel: async ({ username }) => { permissions.push(username); return { data: { permission: "admin" } }; } },
      } };
      const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, actor: "initiator", ref: "refs/heads/main", runAttempt: 1, sha: "c".repeat(40) };
      await execute(github, context, { setOutput: (key, value) => { outputs[key] = value; } });
      assert.deepEqual(permissions, ["initiator", "initiator"]);
      assert.equal(outputs.source_revision, context.sha);
      assert.equal(outputs.image, taggedImage);
      for (const change of [{ ref: "refs/heads/feature" }, { runAttempt: 2 }]) {
        await assert.rejects(execute(github, { ...context, ...change }, { setOutput: () => assert.fail("Rejected requests must not publish outputs") }), /fresh workflow/);
      }
      github.rest.repos.getCollaboratorPermissionLevel = async () => ({ data: { permission: "write" } });
      await assert.rejects(execute(github, context, { setOutput: () => assert.fail("Unauthorized request published outputs") }), /admins/);
    });
  }
});

test("own-PR evaluation executes authorization against the publishing attempt after validation retries", async () => {
  const workflow = await readWorkflow("production-image-e2e.yml");
  const script = workflowJob(workflow, "authorize").steps.find((step) => step.id === "resolve").with.script;
  const execute = new (Object.getPrototypeOf(async () => {}).constructor)("github", "context", "core", "require", "process", script);
  const target = `https://github.com/${repository}/actions/runs/100`;
  const pull = { number: 42, state: "open", draft: false, user: { login: "owner" }, base: { ref: "main", sha: "c".repeat(40) }, head: { ref: "feature", sha: revision, repo: { full_name: repository } } };
  const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, actor: "owner", serverUrl: "https://github.com", eventName: "workflow_dispatch", ref: "refs/heads/feature", sha: revision, runAttempt: 1 };
  const statuses = [{ id: 2, context: "PR test image policy", state: "success", description: "Published test-42 (run 100, attempt 1).", target_url: target }, { id: 3, context: "Production image E2E", state: "success", creator: { login: "github-actions[bot]" }, target_url: target }];
  const latest = { id: 100, run_attempt: 2, event: "pull_request_target", path: ".github/workflows/pr-test-image.yml", display_title: "PR test image: PR #42", conclusion: "success" };
  const published = { ...latest, run_attempt: 1, conclusion: "failure" };
  const jobs = [{ name: "Publish trusted preview", conclusion: "success" }];
  const receipt = { version: 1, pr_number: 42, source_sha: revision, image: pinnedImage, run_id: "100", run_attempt: "1", trusted_revision: "c".repeat(40) };
  const comments = [{ user: { login: "github-actions[bot]" }, body: imagePolicy.formatPreviewReceipt(receipt, target) }];
  const github = { paginate: (method, args) => method(args), rest: {
    repos: { getCollaboratorPermissionLevel: async () => ({ data: { permission: "admin" } }), listCommitStatusesForRef: async () => statuses }, pulls: { get: async () => ({ data: pull }) }, issues: { listComments: async () => comments },
    actions: { getWorkflowRun: async () => ({ data: latest }), getWorkflowRunAttempt: async ({ run_id, attempt_number }) => { assert.equal(run_id, 100); assert.equal(attempt_number, 1); return { data: published }; }, listJobsForWorkflowRunAttempt: async ({ run_id, attempt_number }) => { assert.equal(run_id, 100); assert.equal(attempt_number, 1); return jobs; } },
  } };
  let outputs;
  const env = { INPUT_PR_NUMBER: "42", INPUT_EVALUATION: "fixtures", TRIGGERING_ACTOR: "owner" };
  const run = () => {
    outputs = {};
    return execute(github, context, { setOutput: (key, value) => { outputs[key] = value; } }, (file) => { assert.equal(file, "./.ci-policy/.github/scripts/image-release-policy.cjs"); return imagePolicy; }, { env });
  };
  for (const mode of ["fixtures", "live"]) { env.INPUT_EVALUATION = mode; await run(); assert.equal(outputs.image, pinnedImage); }
  env.INPUT_EVALUATION = "intake";
  await assert.rejects(run(), /invalid evaluation mode/);
  env.INPUT_EVALUATION = "live";
  latest.run_attempt = 1; latest.conclusion = "success"; await run();
  latest.run_attempt = 2;
  for (const change of [{ conclusion: "failure" }, { conclusion: null }]) {
    const original = { ...latest }; Object.assign(latest, change);
    await assert.rejects(run(), /current-head production-image receipt/); Object.assign(latest, original);
  }
  for (const change of [{ run_attempt: 0 }, { path: ".github/workflows/other.yml" }, { display_title: "PR test image: PR #43" }]) {
    const original = { ...latest }; Object.assign(latest, change);
    await assert.rejects(run(), /workflow receipt is invalid/); Object.assign(latest, original);
  }
  published.run_attempt = 2;
  await assert.rejects(run(), /workflow receipt is invalid/); published.run_attempt = 1;
  jobs[0].conclusion = "failure";
  await assert.rejects(run(), /publish job did not pass/); jobs[0].conclusion = "success";
  comments[0].body = imagePolicy.formatPreviewReceipt({ ...receipt, run_attempt: "2" }, target);
  await assert.rejects(run(), /preview receipt is missing/); comments[0].body = imagePolicy.formatPreviewReceipt(receipt, target);
  env.INPUT_EVALUATION = "none"; latest.conclusion = "failure";
  await run(); assert.equal(outputs.image, pinnedImage);
});

test("isolated server admits configured import workers while preserving other limits and private routing", () => {
  const environment = serverEnvironment({ ...configuration(valid), importWorkers: 10 }, { EVAL_CONTROL_TOKEN: "control-canary", EVAL_DATABASE_PASSWORD: "database-canary", ...valid });
  const maximumCalls = Math.max(...registry.datasets.map((dataset) => dataset.corpus.rows * 6 + dataset.questions.rows * 2 * 8 + 1));
  assert.ok(Number(environment.RATE_LIMIT_PER_MINUTE) >= maximumCalls);
  assert.equal(environment.AI_VERIFIER_MAX_CONCURRENCY, "10");
  assert.equal(environment.AI_API_URL, "https://api.openai.com/v1");
  assert.equal(environment.AI_API_EMBEDDING_MODEL, "text-embedding-3-small");
  assert.equal(environment.AI_API_EMBEDDING_DIMENSIONS, "1536");
  assert.equal(environment.AI_API_EMBEDDING_MAX_CONCURRENCY, "2");
  assert.equal(environment.AI_VERIFIER_API_KEY, valid.DENSE_MEM_EVAL_API_KEY);
  assert.equal(environment.AI_API_KEY, valid.DENSE_MEM_EVAL_EMBED_API_KEY);
  assert.equal(environment.CONTROL_PORTAL_TOKEN, "control-canary");
  assert.equal(environment.POSTGRES_PASSWORD, "database-canary");
  const fixture = serverEnvironment({ model: "fixture", context: 300000, importWorkers: 10, verifier: "http://fixture-provider:8787/v1", embedding: "http://fixture-provider:8787/v1" }, {});
  assert.equal(fixture.AI_VERIFIER_API_URL, "http://fixture-provider:8787/v1");
  assert.equal(fixture.AI_API_URL, "http://fixture-provider:8787/v1");
  assert.equal(fixture.AI_API_EMBEDDING_MODEL, "text-embedding-3-small");
  assert.equal(fixture.AI_API_EMBEDDING_DIMENSIONS, "1536");
});

test("import worker settings are explicit, bounded and shared with the server generation limit", () => {
  for (const workers of [1, 4, 10, 20, 100]) {
    assert.equal(resolveImportWorkers(String(workers)), workers);
    const configured = { ...configuration(valid), importWorkers: resolveImportWorkers(String(workers)) };
    assert.equal(configured.importWorkers, workers);
    assert.equal(serverEnvironment(configured, {}).AI_VERIFIER_MAX_CONCURRENCY, String(Math.max(4, workers)));
    assert.equal(serverEnvironment(configured, {}).POSTGRES_ADVISORY_LOCK_CONCURRENCY, String(workers));
    const capacity = databaseCapacity(workers);
    assert.equal(serverEnvironment(configured, {}).POSTGRES_MAX_OPEN_CONNS, String(capacity.application));
    assert.ok(capacity.application - workers >= workers + 10, "each held advisory connection leaves room for a foreground transaction and background activity");
    assert.ok(capacity.postgres >= capacity.application + 2 + 2 + 10, "operation logs, observer and administrative headroom must fit");
  }
  assert.equal(databaseCapacity(20).application, 50);
  assert.equal(databaseCapacity().application, 25);
  for (const value of [undefined, "", "0", "-1", "101", "1.5", "20.0", " 20", "20\n", "invalid-canary"]) {
    assert.throws(() => resolveImportWorkers(value), (error) => /DENSE_MEM_EVAL_IMPORT_WORKERS/.test(error.message) && !error.message.includes("invalid-canary"));
  }
});

test("evaluation accepts only the pinned OpenAI embedding endpoint, model and dimensions", () => {
  assert.equal(configuration(valid).embedding, "https://api.openai.com/v1");
  assert.equal(configuration({ ...valid, DENSE_MEM_EVAL_EMBED_URL: "https://api.openai.com/v1/" }).embedding, "https://api.openai.com/v1/");
  for (const url of [
    `https://api.cloudflare.com/client/v4/accounts/${"a".repeat(32)}/ai/v1`,
    "https://api.openai.com.attacker.example/v1", "http://api.openai.com/v1",
    "https://credential-canary@api.openai.com/v1", "https://api.openai.com/v1?key=credential-canary",
    "https://api.openai.com/v1/embeddings",
  ]) assert.throws(() => configuration({ ...valid, DENSE_MEM_EVAL_EMBED_URL: url }), { message: "Invalid Actions setting: DENSE_MEM_EVAL_EMBED_URL" });
  for (const change of [
    { WORKFLOW_EMBEDDING_MODEL: "@cf/baai/bge-m3" },
    { WORKFLOW_EMBEDDING_DIMENSIONS: "1024" },
    { WORKFLOW_EMBEDDING_DIMENSIONS: "1536.0" },
  ]) assert.throws(() => configuration({ ...valid, ...change }), /Workflow embedding pins are invalid/);
});

test("only absence permits configuration fallback; invalid values never escape errors", () => {
  assert.equal(resolveSetting("model", ["workflow", "variable", "secret"], Boolean), "workflow");
  assert.equal(resolveSetting("model", ["", "variable", "secret"], Boolean), "variable");
  assert.equal(resolveSetting("model", [undefined, "", "secret"], Boolean), "secret");
  assert.throws(() => resolveSetting("model", ["", ""], Boolean), /Missing required/);
  assert.throws(() => resolveSetting("context", ["invalid-canary", "300000"], (v) => /^[0-9]+$/.test(v)), (error) => !error.message.includes("invalid-canary"));
  assert.equal(configuration(valid).context, 300000);
  for (const key of ["DENSE_MEM_EVAL_MODEL", "DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS", "DENSE_MEM_EVAL_VERIFIER_URL", "DENSE_MEM_EVAL_EMBED_URL", "DENSE_MEM_EVAL_API_KEY", "DENSE_MEM_EVAL_EMBED_API_KEY"]) {
    assert.throws(() => configuration({ ...valid, [key]: "" }), new RegExp(key));
  }
  for (const value of [" ", "0", "16383", "300000.5", "-1", "9007199254740992"]) assert.throws(() => configuration({ ...valid, DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS: value }));
  assert.throws(() => configuration({ ...valid, DENSE_MEM_EVAL_VERIFIER_URL: "https://credential-canary@provider.example/v1" }), (error) => !error.message.includes("credential-canary"));
});

test("local seed preparation accepts an HTTP verifier and retains credential and embedding validation", () => {
  const verifier = "http://verifier.example.test/v1";
  const env = { ...valid, DENSE_MEM_EVAL_VERIFIER_URL: verifier };
  const local = { allowHTTPVerifier: true };
  assert.equal(configuration(env, local).verifier, verifier);
  assert.throws(() => configuration(env), /DENSE_MEM_EVAL_VERIFIER_URL/);
  for (const value of ["http://credential-canary@verifier.example.test/v1", verifier + "?key=credential-canary", verifier + "#credential-canary", "ftp://verifier.example.test/v1"]) {
    assert.throws(() => configuration({ ...env, DENSE_MEM_EVAL_VERIFIER_URL: value }, local), { message: "Invalid Actions setting: DENSE_MEM_EVAL_VERIFIER_URL" });
  }
  assert.throws(() => configuration({ ...env, DENSE_MEM_EVAL_EMBED_URL: "http://api.openai.com/v1" }, local), /DENSE_MEM_EVAL_EMBED_URL/);
});

test("registry additions and removals change the matrix and all public denominators", () => {
  assert.equal(registry.datasets.reduce((sum, d) => sum + d.corpus.rows, 0), 45895);
  assert.equal(registry.datasets.reduce((sum, d) => sum + d.questions.rows, 0), 5000);
  assert.deepEqual(registryMatrix(registry).dataset, registry.datasets.map((d) => d.id));
  assert.equal(registryMatrix({ ...registry, datasets: registry.datasets.slice(1) }).dataset.length, 4);
  const extension = { ...registry.datasets[0], id: "new_pack" };
  assert.equal(registryMatrix({ ...registry, datasets: [...registry.datasets, extension] }).dataset.length, 6);
  assert.throws(() => registryMatrix({ ...registry, datasets: [...registry.datasets, registry.datasets[0]] }), /duplicate/);
  const empty = aggregate(registry, [], {});
  assert.equal(empty.expected_answers, 10000);
  assert.equal(empty.completed_answers, 0);
  assert.equal(empty.complete, false);
  assert.equal(empty.datasets.length, 5);
});

test("execution and cleanup fail closed outside the owning hosted run", () => {
  assert.throws(() => requireRunner({}), /GitHub-hosted/);
  const runner = { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted", GITHUB_RUN_ID: "42", GITHUB_RUN_ATTEMPT: "1" };
  assert.doesNotThrow(() => requireRunner(runner));
  assert.throws(() => requireRunner({ ...runner, GITHUB_RUN_ATTEMPT: "2" }), /reruns/);
  assert.throws(() => requireRunner({ ...runner, RUNNER_ENVIRONMENT: "self-hosted" }));
  assert.equal(ownedResource({ Labels: { "io.dense-mem.eval.run": "42-hotpotqa" } }, "42-hotpotqa"), true);
  assert.throws(() => ownedResource({ Labels: { "io.dense-mem.eval.run": "41-hotpotqa" } }, "42-hotpotqa"), /ownership/);
});

test("runner workflow reserves shutdown time, retains numeric artifacts and aggregates failures", async () => {
  const workflow = await readWorkflow("evaluation.yml");
  const datasets = workflowJob(workflow, "datasets");
  assert.equal(datasets.env.WORKFLOW_EMBEDDING_MODEL, "text-embedding-3-small");
  assert.equal(datasets.env.WORKFLOW_EMBEDDING_DIMENSIONS, "1536");
  assert.equal(datasets.env.DENSE_MEM_EVAL_IMPORT_WORKERS, undefined);
  assert.equal(Object.hasOwn(workflow.on.workflow_call.inputs, "import_workers"), false);
  assert.equal(Object.hasOwn(workflow.on.workflow_call.inputs, "ontology_seed_version"), false);
  assert.equal(Object.hasOwn(datasets.env, "EVAL_CONDITION"), false);
  const workerInput = workflow.on.workflow_call.inputs.answer_workers;
  assert.deepEqual(workerInput, { type: "string", default: "4" });
  for (const entrypoint of ["run-evaluation.yml", "production-image-e2e.yml"]) {
    const caller = await readWorkflow(entrypoint);
    const inputs = caller.on.workflow_dispatch.inputs;
    const call = workflowJob(caller, "evaluation").with;
    assert.equal(Object.hasOwn(inputs, "import_workers"), false);
    assert.equal(Object.hasOwn(call, "import_workers"), false);
    assert.equal(Object.hasOwn(inputs, "ontology_seed_version"), false);
    assert.equal(Object.hasOwn(call, "ontology_seed_version"), false);
    assert.equal(inputs.answer_workers.type, workerInput.type);
    assert.equal(inputs.answer_workers.default, workerInput.default);
    assert.equal(call.answer_workers, "${{ inputs.answer_workers }}");
    const chosen = {};
    for (const { id } of registry.datasets) {
      assert.equal(inputs[`run_${id}`]?.type, "boolean");
      assert.equal(inputs[`run_${id}`].default, true);
      chosen[id] = id === "hotpotqa";
    }
    const selection = call.dataset_selection.replace(/\$\{\{ inputs\.run_([a-z0-9_]+) \}\}/g, (_, id) => JSON.stringify(chosen[id]));
    assert.deepEqual(JSON.parse(selection), chosen);
  }
  assert.equal(workflow.env.EVAL_DATASET_SELECTION, "${{ inputs.dataset_selection }}");
  assert.equal(datasets["runs-on"], "ubuntu-latest"); assert.equal(datasets["timeout-minutes"], 360); assert.equal(datasets.strategy["fail-fast"], false);
  assert.equal(datasets.strategy["max-parallel"], "${{ fromJSON(needs.registry.outputs.max_parallel) }}");
  assert.equal(datasets.env.EVAL_ANSWER_WORKERS, "${{ needs.registry.outputs.answer_workers }}");
  const deadline = datasets.steps.find((step) => step.name === "Freeze model deadline").run;
  assert.match(deadline, /minutes=330/);
  assert.ok(datasets.steps.filter((step) => step.uses?.startsWith("actions/upload-artifact@")).every((step) => step.with["retention-days"] === 2));
  const recovery = runStep(datasets, "node tests/eval/scripts/workflow.mjs recover");
  assert.equal(recovery.if, "always()");
  assert.ok(datasets.steps.indexOf(recovery) < datasets.steps.findIndex((step) => step.name === "Remove this job's runtime"));
  assert.ok(datasets.steps.indexOf(recovery) < datasets.steps.findIndex((step) => step.uses?.startsWith("actions/upload-artifact@")));
  assert.equal(runStep(datasets, "node --test tests/eval/scripts/workflow_docker.test.mjs").if, "inputs.mode == 'fixtures'");
  assert.match(workflowJob(workflow, "aggregate").if, /always/);
  const caller = await readWorkflow("production-image-e2e.yml");
  assert.deepEqual(caller.on.workflow_dispatch.inputs.evaluation.options, ["none", "fixtures", "live"]);
  assert.doesNotMatch((await readWorkflow("run-evaluation.yml")).on.workflow_dispatch.inputs.mode.options.join(" "), /intake/);
  const secrets = workflowJob(caller, "evaluation").secrets;
  const inputs = workflowJob(caller, "evaluation").with;
  assert.equal(inputs.model, "dense-mem-testing");
  assert.equal(inputs.context_window_tokens, "500000");
  assert.match(datasets.env.DENSE_MEM_EVAL_MODEL, /inputs\.model \|\| vars\.DENSE_MEM_EVAL_MODEL \|\| secrets\.eval-model/);
  assert.match(datasets.env.DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS, /inputs\.context_window_tokens \|\| vars\.DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS \|\| secrets\.eval-context-window/);
  assert.match(secrets["eval-verifier-url"], /'' \|\| vars\.[A-Z_]+ \|\| secrets\.[A-Z_]+/);
  assert.equal(secrets["eval-embedding-url"], "${{ 'https://api.openai.com/v1' || vars.DENSE_MEM_EVAL_EMBED_URL || secrets.DENSE_MEM_EVAL_EMBED_URL }}");
  assert.equal(secrets["eval-api-key"], "${{ secrets.DENSE_MEM_EVAL_API_KEY }}");
  assert.equal(secrets["eval-embedding-api-key"], "${{ secrets.DENSE_MEM_EVAL_EMBED_API_KEY }}");
  const resolve = workflowJob(caller, "authorize").steps.find((step) => step.id === "resolve").with.script;
  assert.match(resolve, /triggeringPermission.permission !== "admin"/); assert.match(resolve, /current-head production-image receipt/);
  const ci = await readWorkflow("ci-shared.yml"); runStep(workflowJob(ci, "quality"), "bash scripts/test-evaluation.sh");
});

const hostedTest = { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted", GITHUB_RUN_ID: "42", GITHUB_RUN_ATTEMPT: "1" };

test("the matrix supports selected public datasets and requires at least one", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "evaluation-matrix-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const output = path.join(directory, "output");
  const env = { ...hostedTest, GITHUB_OUTPUT: output };
  const liveSelection = { hotpotqa: true, musique: false, "2wikimultihopqa": true, nq_rear: false, popqa: false };
  for (const [mode, selection, expected, workers, maxParallel] of [
    ["live", undefined, registry.datasets.map(({ id }) => id), undefined, 5],
    ["live", undefined, registry.datasets.map(({ id }) => id), "10", 2],
    ["live", JSON.stringify(liveSelection), ["hotpotqa", "2wikimultihopqa"], "6", 3],
    ["fixtures", '{"hotpotqa":false}', ["hotpotqa"], "4", 5],
  ]) {
    fs.writeFileSync(output, "");
    await main("matrix", { ...env, EVAL_MODE: mode, EVAL_DATASET_SELECTION: selection, EVAL_ANSWER_WORKERS: workers });
    const [matrix, answerWorkers, parallelism] = fs.readFileSync(output, "utf8").trim().split("\n");
    assert.deepEqual(JSON.parse(matrix.slice("matrix=".length)).include, expected.map((dataset) => ({ dataset })));
    assert.equal(answerWorkers, `answer_workers=${workers ?? 4}`);
    assert.equal(parallelism, `max_parallel=${maxParallel}`);
  }
  fs.writeFileSync(output, "");
  for (const selection of ['{"hotpotqa":false}', '{"hotpotqa":"true"}', '{"qasper":true}', "null", "not-json"]) {
    await assert.rejects(main("matrix", { ...env, EVAL_MODE: "live", EVAL_DATASET_SELECTION: selection }), /Dataset selection/);
    assert.equal(fs.readFileSync(output, "utf8"), "");
  }
  for (const workers of ["0", "3", "11", "4.5", "invalid"]) {
    await assert.rejects(main("matrix", { ...env, EVAL_MODE: "live", EVAL_ANSWER_WORKERS: workers }), /EVAL_ANSWER_WORKERS/);
    assert.equal(fs.readFileSync(output, "utf8"), "");
  }
  for (const mode of ["replay", "none", "unknown", ""]) await assert.rejects(main("matrix", { ...env, EVAL_MODE: mode }), /Invalid evaluation mode/);
});

test("pinning reports a missing selected seed without looking up unselected tags", async (t) => {
  const manifests = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    const pathname = new URL(url).pathname;
    if (pathname === "/token") return Response.json({ token: "fixture" });
    manifests.push(pathname);
    return new Response(null, { status: 404 });
  });
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "evaluation-pins-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  await main("pin-seeds", { ...hostedTest, EVAL_MODE: "live", RUNNER_TEMP: directory, GITHUB_OUTPUT: path.join(directory, "output"),
    EVAL_DATASET_SELECTION: '{"hotpotqa":false,"popqa":true}' });
  const pins = JSON.parse(fs.readFileSync(path.join(directory, "output"), "utf8").trim().slice("images=".length));
  assert.deepEqual(pins, { popqa: { failure: "published_seed_missing" } });
  assert.deepEqual(manifests, ["/v2/markhuangai/dense-mem-eval-postgres/manifests/popqa-latest"]);
});

const campaign = { runID: "42", source: revision, image: pinnedImage,
  registryHash: sha256(fs.readFileSync(new URL("../datasets.json", import.meta.url))), answerWorkers: 10 };
const seedPin = `ghcr.io/markhuangai/dense-mem-eval-postgres@sha256:${"d".repeat(64)}`;

function frozenReport({ id, questions, corpus }) {
  return {
    identity: { dataset: id, condition: "off", run_id: `42-${id}-off`, workflow_run_id: "42", source_revision: campaign.source,
      image: campaign.image, registry_sha256: campaign.registryHash, answer_workers: 10, seed_image: seedPin,
      canonical_sha256: "c".repeat(64), model: "fixture", embedding_model: "text-embedding-3-small",
      embedding_dimensions: 1536, context_window_tokens: 500000, scoring_policy: "fixture" },
    corpus_rows: corpus.rows, restored_rows: corpus.rows, expected_answers: questions.rows * 2, completed_answers: questions.rows * 2,
    complete: true, capture_complete: true, owned_cleanup: true, model_verified_before_cutoff: true,
    timings_ms: { ontology_off_scoring: 7000, capture_sink_ms: 80, runtime_log_capture: 2700 },
    comparison_matrix: ["single", "multi_turn"].map((policy) => ({
      policy, condition: "off", expected: questions.rows, completed: questions.rows, failed: 0, missing: 0,
      provider_calls: questions.rows, usage_unavailable: 0, usage_unreconciled: 0,
      mean: { recall_at_1: 0.5, recall_at_5: 0.6, recall_at_10: 0.7, exact_match: 0.3, f1: 0.4, cited_gold_source_coverage: 0.2 },
      all_support_recovery_rate: 0.25, reported_input_tokens: 3000, reported_output_tokens: 2000, reported_total_tokens: 5000,
    })),
  };
}

test("a complete single-dataset run keeps its actual denominator and proves cleanup", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "evaluation-aggregate-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const artifact = path.join(directory, "evaluation-reports", "evaluation-hotpotqa-off");
  fs.mkdirSync(artifact, { recursive: true });
  const fixture = frozenReport(registry.datasets[0]);
  fs.writeFileSync(path.join(artifact, "scores.json"), JSON.stringify(fixture));
  const cleanupReceipt = path.join(artifact, "cleanup.json");
  fs.writeFileSync(cleanupReceipt, JSON.stringify({ run_id: fixture.identity.run_id, complete: true }));
  const env = { ...hostedTest, EVAL_MODE: "live", EVAL_DATASET_SELECTION: '{"hotpotqa":true}', RUNNER_TEMP: directory,
    EVAL_SOURCE_REVISION: campaign.source, EVAL_IMAGE: campaign.image, EVAL_ANSWER_WORKERS: "10",
    EVAL_PINNED_SEEDS: JSON.stringify({ hotpotqa: { image: seedPin } }), GITHUB_STEP_SUMMARY: path.join(directory, "summary") };
  const resultPath = path.join(directory, "evaluation-reports", "aggregate", "comparison.json");
  const originalCode = process.exitCode;
  t.after(() => { process.exitCode = originalCode; });
  await main("aggregate", env);
  const result = JSON.parse(fs.readFileSync(resultPath));
  assert.equal(result.complete, true);
  assert.equal(result.full_public_campaign, false);
  assert.equal(result.expected_answers, 2000);
  assert.equal(result.completed_answers, 2000);
  assert.deepEqual(result.datasets.map(({ dataset }) => dataset), ["hotpotqa"]);
  assert.match(fs.readFileSync(env.GITHUB_STEP_SUMMARY, "utf8"), /hotpotqa \| single \| Complete \| 1,000 \/ 1,000/);
  fs.writeFileSync(cleanupReceipt, "{");
  await main("aggregate", env);
  const incomplete = JSON.parse(fs.readFileSync(resultPath));
  assert.equal(incomplete.complete, false);
  assert.equal(incomplete.completed_answers, 2000);
  assert.equal(incomplete.datasets[0].owned_cleanup, false);
  assert.equal(process.exitCode, 1);
});

test("aggregate renders and publishes output only for a verified complete public campaign", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "evaluation-full-summary-"));
  const originalCode = process.exitCode;
  t.after(() => { fs.rmSync(directory, { recursive: true, force: true }); process.exitCode = originalCode; });
  const inputs = { ...hostedTest, EVAL_MODE: "live", RUNNER_TEMP: directory,
    EVAL_SOURCE_REVISION: campaign.source, EVAL_IMAGE: campaign.image, EVAL_ANSWER_WORKERS: "10",
    EVAL_PINNED_SEEDS: JSON.stringify(Object.fromEntries(registry.datasets.map(({ id }) => [id, { image: seedPin }]))),
    GITHUB_STEP_SUMMARY: path.join(directory, "summary"), GITHUB_OUTPUT: path.join(directory, "output"),
  };
  const work = path.join(directory, "evaluation-reports");
  for (const [index, row] of registry.datasets.entries()) {
    const folder = path.join(work, `evaluation-${row.id}-off`);
    fs.mkdirSync(folder, { recursive: true });
    const result = frozenReport(row);
    fs.writeFileSync(path.join(folder, "cleanup.json"), JSON.stringify({ run_id: result.identity.run_id, complete: true }));
    if (index === 0) {
      result.capture_complete = false;
      result.capture_failure = { phase: "log capture", kind: "runtime_log", sequence: 37, reason: "enqueue_interrupted" };
      const [single, multiTurn] = result.comparison_matrix;
      Object.assign(single, { provider_calls: 1_000, usage_unavailable: 700, usage_unreconciled: 300,
        reported_input_tokens: 0, reported_output_tokens: 0, reported_total_tokens: 0 });
      Object.assign(multiTurn, { provider_calls: 1_200, usage_unreconciled: 200 });
    }
    if (index !== 1) fs.writeFileSync(path.join(folder, "scores.json"), JSON.stringify(result));
  }
  await main("aggregate", inputs);
  const summary = fs.readFileSync(inputs.GITHUB_STEP_SUMMARY, "utf8");
  assert.match(summary, /completed 8000\/10000 answers/);
  assert.match(summary, /hotpotqa \| single \| Incomplete \| 1,000 \/ 1,000/);
  assert.match(summary, /musique \| multi_turn \| Unavailable \| 0 \/ 1,000/);
  assert.match(summary, /verification, capture or cleanup incomplete/);
  assert.match(summary, /phase log capture, record runtime_log, sequence 37, reason enqueue_interrupted/);
  assert.match(summary, /hotpotqa \| single .*50\.00%.*60\.00%.*70\.00%.*\| Unavailable \|/);
  assert.match(summary, /hotpotqa \| multi_turn .*3,000 \/ 2,000 \/ 5,000 \(1,000 of 1,200 calls\)/);
  assert.match(summary, /hotpotqa \| 7\.00 \| 0\.08 \| 2\.70/);
  assert.equal(fs.existsSync(inputs.GITHUB_OUTPUT), false);
  for (const row of registry.datasets.slice(0, 2)) {
    fs.writeFileSync(path.join(work, `evaluation-${row.id}-off`, "scores.json"), JSON.stringify(frozenReport(row)));
  }
  fs.writeFileSync(inputs.GITHUB_STEP_SUMMARY, "");
  await main("aggregate", inputs);
  const receipt = JSON.parse(fs.readFileSync(inputs.GITHUB_OUTPUT, "utf8").trim().slice("complete_report=".length));
  assert.equal(receipt.source, campaign.source);
  assert.equal(receipt.image, campaign.image);
  assert.equal(receipt.datasets, 5);
  assert.equal(receipt.answers, 10000);
  assert.equal(receipt.markdown, fs.readFileSync(inputs.GITHUB_STEP_SUMMARY, "utf8"));
  assert.match(receipt.markdown, /completed 10000\/10000 answers\. The run passed/);
});

test("production report summary shows evaluation status and propagates failure", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "production-summary-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const caller = await readWorkflow("production-image-e2e.yml");
  const script = workflowJob(caller, "report").steps[0].run;
  const summary = path.join(directory, "summary");
  for (const [mode, result] of [["live", "failure"], ["fixtures", "success"], ["none", "success"]]) {
    fs.writeFileSync(summary, "");
    const execution = spawnSync("bash", ["-c", script], { encoding: "utf8", env: {
      ...process.env, GITHUB_STEP_SUMMARY: summary, AUTH_RESULT: "success", EVENT_NAME: "workflow_dispatch",
      PRECHECK_RESULT: "success", DATABASE_PRECHECK_RESULT: "success", SCENARIO_RESULT: "success", CI_TRIAL_RESULT: "success",
      EVALUATION_MODE: mode, EVALUATION_RESULT: result, IMAGE: pinnedImage, SOURCE_REVISION: revision,
    } });
    assert.equal(execution.status, result === "success" ? 0 : 1, execution.stderr);
    const rendered = fs.readFileSync(summary, "utf8");
    if (mode === "none") assert.match(rendered, /Scenario matrix \| success/);
    else assert.ok(rendered.includes(`| Public RAG evaluation | ${result} |`));
  }
});

test("only the complete own-PR workflow path can write and reconcile the evaluation comment", async () => {
  const caller = await readWorkflow("production-image-e2e.yml");
  const reusable = await readWorkflow("evaluation.yml");
  const controller = await readWorkflow("pr-test-image.yml");
  const job = workflowJob(caller, "evaluation-comment");
  assert.deepEqual(job.permissions, { actions: "read", "pull-requests": "write" });
  assert.deepEqual(job.needs, ["authorize", "evaluation"]);
  assert.match(job.if, /github.event_name == 'workflow_dispatch'.*inputs.evaluation == 'live'/s);
  for (const { id } of registry.datasets) assert.match(job.if, new RegExp(`inputs\\.run_${id}`));
  assert.match(job.if, /needs\.evaluation\.result == 'success'/);
  assert.ok(job.steps.every((step) => step.uses?.startsWith("actions/github-script@")));
  for (const [definition, except] of [[caller, job], [reusable, null]]) {
    for (const executable of Object.values(definition.jobs)) {
      if (executable === except) continue;
      assert.ok(Object.values(executable.permissions ?? definition.permissions).every((permission) => permission === "read"));
    }
  }
  assert.equal(workflowJob(controller, "production-e2e").permissions["pull-requests"], "write");
  const script = Object.getPrototypeOf(async function () {}).constructor("github", "context", "core", "process", job.steps[0].with.script);
  const marker = "<!-- dense-mem-evaluation-v1:42 -->";
  const comments = [{ id: 6, user: { login: "contributor" }, body: `${marker}\nCopied marker` }];
  const artifacts = [{ id: 63, name: "evaluation-comparison", expired: false }];
  const author = "authorized-admin";
  const branch = "fix/evaluation";
  const context = { eventName: "workflow_dispatch", runAttempt: 1, runId: 42, actor: author, sha: revision,
    ref: `refs/heads/${branch}`, repo: { owner: "markhuangai", repo: "dense-mem" }, serverUrl: "https://github.com" };
  const pull = { state: "open", draft: false, user: { login: author }, base: { ref: "main" }, head: { ref: branch, sha: revision, repo: { full_name: repository } } };
  const receipt = { run_id: "42", source: revision, image: pinnedImage, datasets: 5, answers: 10_000, markdown: "| Scores | 10,000 |\n" };
  const env = { INPUT_PR: "832", EVAL_SOURCE: revision, EVAL_IMAGE: pinnedImage, EVAL_ARTIFACT_ID: "63", EVAL_REPORT: JSON.stringify(receipt) };
  const github = { paginate: (method, input) => method(input), rest: {
    actions: { listWorkflowRunArtifacts: ({ run_id }) => { assert.equal(run_id, 42); return artifacts; } },
    pulls: { get: () => ({ data: pull }) },
    issues: {
      listComments: ({ issue_number }) => { assert.equal(issue_number, 832); return comments; },
      createComment: ({ issue_number, body }) => {
        assert.equal(issue_number, 832);
        const created = { id: 9, body, user: { login: "github-actions[bot]" }, html_url: "comment-link" };
        comments.push(created);
        return { data: created };
      },
      updateComment: ({ comment_id, body }) => { comments.find((item) => item.id === comment_id).body = body; },
      getComment: ({ comment_id }) => ({ data: comments.find((item) => item.id === comment_id) }),
    },
  } };
  const run = (overrides = {}) => script(github, context, { notice: (url) => assert.equal(url, "comment-link") }, { env: { ...env, ...overrides } });
  await assert.rejects(run({ EVAL_REPORT: "" }), /invalid completed evaluation receipt/);
  await assert.rejects(run({ EVAL_REPORT: JSON.stringify({ ...receipt, answers: 9999 }) }), /authorized full campaign/);
  await assert.rejects(run({ EVAL_ARTIFACT_ID: "7" }), /artifact is unavailable/);
  pull.head.sha = "f".repeat(40);
  await assert.rejects(run(), /no longer the authorized head/);
  assert.equal(comments.length, 1);
  pull.head.sha = revision;
  await run();
  await run();
  assert.equal(comments.length, 2);
  assert.match(comments[1].body, /Scores.*10,000/);
  assert.match(comments[1].body, /actions\/runs\/42\/artifacts\/63/);
  await run({ EVAL_REPORT: JSON.stringify({ ...receipt, markdown: "Reconciled metric table" }) });
  assert.equal(comments.length, 2);
  assert.match(comments[1].body, /Reconciled metric table/);
});

test("a failed or missing dataset preserves a frozen denominator and other scores", () => {
  const reports = registry.datasets.map(frozenReport);
  for (const [index, report] of reports.entries()) report.identity.seed_preparation_workers = index + 4;
  const pins = Object.fromEntries(registry.datasets.map(({ id }) => [id, { image: seedPin }]));
  const all = (rows) => aggregate(registry, rows, { ...campaign, pins });
  const complete = all(reports);
  assert.equal(complete.complete, true);
  assert.equal(complete.expected_answers, 10000);
  assert.equal(complete.completed_answers, 10000);

  const shortened = structuredClone(reports);
  shortened[0].comparison_matrix[0].completed--;
  shortened[0].completed_answers--;
  shortened[0].capture_complete = false;
  shortened[0].capture_failure = { reason: "matching_work_exceeded" };
  const partial = all(shortened);
  assert.equal(partial.complete, false);
  assert.equal(partial.expected_answers, 10000);
  assert.equal(partial.completed_answers, 9999);
  assert.equal(partial.datasets[0].capture_failure.reason, "matching_work_exceeded");
  assert.equal(partial.datasets[1].complete, true);

  assert.equal(all(reports.slice(1)).completed_answers, 8000);
  assert.equal(all([...reports, reports[0]]).datasets[0].unavailable, "duplicate reports");

  for (const change of [
    (row) => { row.identity.condition = "on"; },
    (row) => { row.identity.answer_workers = 4; },
    (row) => { row.identity.seed_image = "other"; },
    (row) => { row.identity.run_id = "foreign-off"; },
    (row) => { row.expected_answers = 1999; },
    (row) => { row.comparison_matrix[1] = null; },
  ]) {
    const invalid = structuredClone(reports);
    change(invalid[0]);
    const result = all(invalid);
    assert.equal(result.complete, false);
    assert.equal(result.expected_answers, 10000);
    assert.equal(result.completed_answers, 8000);
    assert.equal(result.datasets[1].complete, true);
  }

  const mixed = structuredClone(reports);
  mixed[0].identity.model = "other";
  assert.equal(all(mixed).complete, false);
  assert.match(all(mixed).failure, /different scoring settings/);
});
