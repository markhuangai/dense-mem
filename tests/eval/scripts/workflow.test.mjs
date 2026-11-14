import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { resolveSetting, configuration, requireRunner, registryMatrix, aggregate, ownedResource } from "../../../.github/scripts/evaluation-policy.mjs";
import { readWorkflow, workflowJob, runStep } from "../../uat/workflow_helpers.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../datasets.json", import.meta.url)));
const valid = { DENSE_MEM_EVAL_MODEL: "configured-model", DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS: "300000", DENSE_MEM_EVAL_VERIFIER_URL: "https://provider.example/v1", DENSE_MEM_EVAL_EMBED_URL: `https://api.cloudflare.com/client/v4/accounts/${"a".repeat(32)}/ai/v1`, DENSE_MEM_EVAL_API_KEY: "generation-canary", DENSE_MEM_EVAL_EMBED_API_KEY: "embedding-canary", WORKFLOW_EMBEDDING_MODEL: "@cf/baai/bge-m3", WORKFLOW_EMBEDDING_DIMENSIONS: "1024" };

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
