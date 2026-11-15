import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { resolveSetting, configuration, requireRunner, registryMatrix, aggregate, ownedResource, manualEvaluationRequest, resolveEvaluationImage, validReplaySource } from "../../../.github/scripts/evaluation-policy.mjs";
import { readWorkflow, workflowJob, runStep } from "../../uat/workflow_helpers.mjs";
import { serverEnvironment } from "./workflow.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../datasets.json", import.meta.url)));
const valid = { DENSE_MEM_EVAL_MODEL: "configured-model", DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS: "300000", DENSE_MEM_EVAL_VERIFIER_URL: "https://provider.example/v1", DENSE_MEM_EVAL_EMBED_URL: `https://api.cloudflare.com/client/v4/accounts/${"a".repeat(32)}/ai/v1`, DENSE_MEM_EVAL_API_KEY: "generation-canary", DENSE_MEM_EVAL_EMBED_API_KEY: "embedding-canary", WORKFLOW_EMBEDDING_MODEL: "@cf/baai/bge-m3", WORKFLOW_EMBEDDING_DIMENSIONS: "1024" };

const repository = "markhuangai/dense-mem";
const taggedImage = `ghcr.io/${repository}:candidate`;
const pinnedImage = `ghcr.io/${repository}@sha256:${"a".repeat(64)}`;
const revision = "b".repeat(40);
const manualRequest = { image: taggedImage, repository, ref: "refs/heads/main", runAttempt: 1, actorPermission: "admin", triggeringPermission: "admin" };
const inspectedImage = { Os: "linux", Architecture: "amd64", RepoDigests: [pinnedImage], Config: { Labels: { "org.opencontainers.image.variant": "production", "org.opencontainers.image.source": `https://github.com/${repository}`, "org.opencontainers.image.revision": revision } } };

test("manual evaluation needs only an image and defaults to the full benchmark", () => {
  assert.deepEqual(manualEvaluationRequest(manualRequest), { image: taggedImage, mode: "live", replayRunID: "" });
  assert.equal(manualEvaluationRequest({ ...manualRequest, mode: "fixtures" }).mode, "fixtures");
  assert.equal(manualEvaluationRequest({ ...manualRequest, image: pinnedImage, mode: "replay", replayRunID: "42" }).mode, "replay");
  for (const change of [
    { ref: "refs/heads/untrusted" }, { ref: "refs/tags/main" }, { runAttempt: 2 },
    { actorPermission: "write" }, { triggeringPermission: "maintain" },
    { image: "ghcr.io/other/project:latest" }, { image: `${taggedImage}\ncommand` },
    { mode: "none" }, { mode: "unknown" }, { replayRunID: "42" },
    { image: pinnedImage, mode: "replay" }, { mode: "replay", replayRunID: "42" },
    { image: pinnedImage, mode: "replay", replayRunID: "0" },
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

test("both evaluation entrypoints retain the original completed source for replay", () => {
  const original = { path: ".github/workflows/run-evaluation.yml", event: "workflow_dispatch", run_attempt: 1, status: "completed", head_sha: revision };
  assert.equal(validReplaySource(original, revision), true);
  assert.equal(validReplaySource({ ...original, path: ".github/workflows/production-image-e2e.yml" }, revision), true);
  assert.equal(validReplaySource(original, "c".repeat(40)), false);
  for (const change of [{ path: ".github/workflows/other.yml" }, { event: "push" }, { run_attempt: 2 }, { status: "in_progress" }, { head_sha: "main" }]) {
    assert.equal(validReplaySource({ ...original, ...change }, revision), false);
  }
});

test("manual workflow accepts an image without PR inputs and keeps credentials in the reusable job", async () => {
  const workflow = await readWorkflow("run-evaluation.yml");
  assert.deepEqual(Object.keys(workflow.on), ["workflow_dispatch"]);
  const inputs = workflow.on.workflow_dispatch.inputs;
  assert.equal(inputs.image.required, true); assert.equal(inputs.pr_number, undefined);
  assert.equal(inputs.mode.default, "live"); assert.deepEqual(inputs.mode.options, ["live", "fixtures", "replay"]);
  const authorize = workflowJob(workflow, "authorize");
  const request = authorize.steps.find((step) => step.id === "request");
  assert.match(request.with.script, /manualEvaluationRequest/);
  assert.match(request.with.script, /actorPermission: await permission\(context.actor\)/);
  assert.match(request.with.script, /triggeringPermission: await permission\(process.env.TRIGGERING_ACTOR\)/);
  assert.match(request.with.script, /validReplaySource\(original, original.head_sha\)/);
  assert.match(request.with.script, /sourceRevision = original.head_sha/);
  for (const step of authorize.steps) {
    assert.doesNotMatch(JSON.stringify(step.env || {}), /secrets\./);
    if (step.uses?.startsWith("docker/login-action@") || step.id === "image") assert.equal(step.if, "inputs.mode != 'replay'");
  }
  const resolve = runStep(authorize, "docker pull");
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

test("manual authorization executes without PR lookups and pins replay to the original runner", async (t) => {
  const workflow = await readWorkflow("run-evaluation.yml");
  const script = workflowJob(workflow, "authorize").steps.find((step) => step.id === "request").with.script;
  const execute = new (Object.getPrototypeOf(async () => {}).constructor)("github", "context", "core", script);
  for (const mode of ["live", "replay"]) {
    await t.test(mode, async (t) => {
      const settings = { GITHUB_WORKSPACE: process.cwd(), INPUT_IMAGE: mode === "replay" ? pinnedImage : taggedImage, INPUT_MODE: mode, INPUT_REPLAY_RUN_ID: mode === "replay" ? "42" : "", TRIGGERING_ACTOR: "initiator" };
      const previous = Object.fromEntries(Object.keys(settings).map((key) => [key, process.env[key]]));
      Object.assign(process.env, settings);
      t.after(() => {
        for (const [key, value] of Object.entries(previous)) {
          if (value === undefined) delete process.env[key];
          else process.env[key] = value;
        }
      });
      const permissions = []; const outputs = {}; const runs = [];
      const github = { rest: {
        repos: { getCollaboratorPermissionLevel: async ({ username }) => { permissions.push(username); return { data: { permission: "admin" } }; } },
        actions: { getWorkflowRun: async ({ run_id }) => {
          runs.push(run_id);
          return { data: { path: ".github/workflows/run-evaluation.yml", head_sha: revision, event: "workflow_dispatch", run_attempt: 1, status: "completed" } };
        } },
      } };
      const context = { repo: { owner: "markhuangai", repo: "dense-mem" }, actor: "initiator", ref: "refs/heads/main", runAttempt: 1, sha: "c".repeat(40) };
      await execute(github, context, { setOutput: (key, value) => { outputs[key] = value; } });
      assert.deepEqual(permissions, ["initiator", "initiator"]);
      assert.deepEqual(runs, mode === "replay" ? ["42"] : []);
      assert.equal(outputs.source_revision, mode === "replay" ? revision : context.sha);
      assert.equal(outputs.image, mode === "replay" ? pinnedImage : undefined);
      for (const change of [{ ref: "refs/heads/feature" }, { runAttempt: 2 }]) {
        await assert.rejects(execute(github, { ...context, ...change }, { setOutput: () => assert.fail("Rejected requests must not publish outputs") }), /fresh workflow/);
      }
      github.rest.repos.getCollaboratorPermissionLevel = async () => ({ data: { permission: "write" } });
      await assert.rejects(execute(github, context, { setOutput: () => assert.fail("Unauthorized request published outputs") }), /admins/);
    });
  }
});

test("isolated server admits the complete bounded campaign without changing concurrency or private routing", () => {
  const environment = serverEnvironment(configuration(valid), { EVAL_CONTROL_TOKEN: "control-canary", EVAL_DATABASE_PASSWORD: "database-canary", ...valid });
  const maximumCalls = Math.max(...registry.datasets.map((dataset) => dataset.corpus.rows * 2 + dataset.questions.rows * 2 * 8 + 1));
  assert.ok(Number(environment.RATE_LIMIT_PER_MINUTE) >= maximumCalls);
  assert.equal(environment.AI_VERIFIER_MAX_CONCURRENCY, "4");
  assert.equal(environment.AI_API_EMBEDDING_MAX_CONCURRENCY, "2");
  assert.equal(environment.AI_VERIFIER_API_KEY, valid.DENSE_MEM_EVAL_API_KEY);
  assert.equal(environment.AI_API_KEY, valid.DENSE_MEM_EVAL_EMBED_API_KEY);
  assert.equal(environment.CONTROL_PORTAL_TOKEN, "control-canary");
  assert.equal(environment.POSTGRES_PASSWORD, "database-canary");
  const fixture = serverEnvironment({ model: "fixture", context: 300000, verifier: "http://fixture-provider:8787/v1", embedding: "http://fixture-provider:8787/v1" }, {});
  assert.equal(fixture.AI_VERIFIER_API_URL, "http://fixture-provider:8787/v1");
  assert.equal(fixture.AI_API_URL, "http://fixture-provider:8787/v1");
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

test("registry additions and removals change the matrix and all public denominators", () => {
  assert.equal(registry.datasets.reduce((sum, d) => sum + d.corpus.rows, 0), 45895);
  assert.equal(registry.datasets.reduce((sum, d) => sum + d.questions.rows, 0), 5000);
  assert.deepEqual(registryMatrix(registry).dataset, registry.datasets.map((d) => d.id));
  assert.equal(registryMatrix({ ...registry, datasets: registry.datasets.slice(1) }).dataset.length, 4);
  const extension = { ...registry.datasets[0], id: "new_pack" };
  assert.equal(registryMatrix({ ...registry, datasets: [...registry.datasets, extension] }).dataset.length, 6);
  assert.throws(() => registryMatrix({ ...registry, datasets: [...registry.datasets, registry.datasets[0]] }), /duplicate/);
  const result = aggregate(registry, []);
  assert.equal(result.expected_answers, 20000); assert.equal(result.completed_answers, 0); assert.equal(result.complete, false); assert.equal(result.datasets.length, 5);
  const dataset = registry.datasets[0];
  const partial = { identity: { dataset: dataset.id }, expected_answers: 4000, corpus_rows: dataset.corpus.rows, completed_answers: 4, complete: false, capture_complete: true };
  const failed = aggregate(registry, [partial]); assert.equal(failed.expected_answers, 20000); assert.equal(failed.completed_answers, 4); assert.equal(failed.complete, false);
  assert.throws(() => aggregate(registry, [{ ...partial, expected_answers: 4 }]), /denominator/);
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
  assert.equal(datasets["runs-on"], "ubuntu-latest"); assert.equal(datasets["timeout-minutes"], 360); assert.equal(datasets.strategy["fail-fast"], false);
  assert.match(datasets.steps.find((step) => step.name === "Freeze model deadline").run, /330 \* 60/);
  assert.ok(datasets.steps.filter((step) => step.uses?.startsWith("actions/upload-artifact@")).every((step) => step.with["retention-days"] === 2));
  assert.equal(runStep(datasets, "node --test tests/eval/scripts/workflow_docker.test.mjs").if, "inputs.mode == 'fixtures'");
  assert.match(workflowJob(workflow, "aggregate").if, /always/);
  const caller = await readWorkflow("production-image-e2e.yml");
  const secrets = workflowJob(caller, "evaluation").secrets;
  const inputs = workflowJob(caller, "evaluation").with;
  assert.equal(inputs.model, "dense-mem-testing");
  assert.equal(inputs.context_window_tokens, "500000");
  assert.match(datasets.env.DENSE_MEM_EVAL_MODEL, /inputs\.model \|\| vars\.DENSE_MEM_EVAL_MODEL \|\| secrets\.eval-model/);
  assert.match(datasets.env.DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS, /inputs\.context_window_tokens \|\| vars\.DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS \|\| secrets\.eval-context-window/);
  assert.match(secrets["eval-verifier-url"], /'' \|\| vars\.[A-Z_]+ \|\| secrets\.[A-Z_]+/);
  assert.equal(secrets["eval-api-key"], "${{ secrets.DENSE_MEM_EVAL_API_KEY }}");
  assert.equal(secrets["eval-embedding-api-key"], "${{ secrets.DENSE_MEM_EVAL_EMBED_API_KEY }}");
  const resolve = workflowJob(caller, "authorize").steps.find((step) => step.id === "resolve").with.script;
  assert.match(resolve, /triggeringPermission.permission !== "admin"/); assert.match(resolve, /current-head production-image receipt/);
  const ci = await readWorkflow("ci-shared.yml"); runStep(workflowJob(ci, "quality"), "bash scripts/test-evaluation.sh");
});

test("replay's executable path contains no provisioning or model transport", () => {
  const replay = fs.readFileSync(new URL("replay.mjs", import.meta.url), "utf8");
  assert.doesNotMatch(replay, /docker|chat\/completions|embeddings|\["run"/);
  assert.match(replay, /\["report", manifestPath\]/); assert.match(replay, /artifact.digest/); assert.match(replay, /\/logs/);
});
