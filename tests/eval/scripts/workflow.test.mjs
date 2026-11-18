import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import fs from "node:fs";
import { createServer } from "node:http";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import imagePolicy from "../../../.github/scripts/image-release-policy.cjs";
import { resolveSetting, resolveImportWorkers, requireEvaluationImportSupport, configuration, requireRunner, registryMatrix, aggregate, ownedResource, manualEvaluationRequest, resolveEvaluationImage, sha256 } from "../../../.github/scripts/evaluation-policy.mjs";
import { readWorkflow, workflowJob, runStep } from "../../uat/workflow_helpers.mjs";
import { serverEnvironment, databaseCapacity, startupTimings, control, main, intakeFailureRecords } from "./workflow.mjs";
import { intakeReceiptOutcome, measureIntakeCase, measureIntakeCases } from "./intake-comparison.mjs";

const registry = JSON.parse(fs.readFileSync(new URL("../datasets.json", import.meta.url)));
const valid = { DENSE_MEM_EVAL_MODEL: "configured-model", DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS: "300000", DENSE_MEM_EVAL_IMPORT_WORKERS: "10", DENSE_MEM_EVAL_VERIFIER_URL: "https://provider.example/v1", DENSE_MEM_EVAL_EMBED_URL: "https://api.openai.com/v1", DENSE_MEM_EVAL_API_KEY: "generation-canary", DENSE_MEM_EVAL_EMBED_API_KEY: "embedding-canary", WORKFLOW_EMBEDDING_MODEL: "text-embedding-3-small", WORKFLOW_EMBEDDING_DIMENSIONS: "1536" };

const repository = "markhuangai/dense-mem";
const taggedImage = `ghcr.io/${repository}:candidate`;
const pinnedImage = `ghcr.io/${repository}@sha256:${"a".repeat(64)}`;
const revision = "b".repeat(40);
const manualRequest = { image: taggedImage, repository, ref: "refs/heads/main", runAttempt: 1, actorPermission: "admin", triggeringPermission: "admin" };
const inspectedImage = { Os: "linux", Architecture: "amd64", RepoDigests: [pinnedImage], Config: { Labels: { "org.opencontainers.image.variant": "production", "org.opencontainers.image.source": `https://github.com/${repository}`, "org.opencontainers.image.revision": revision } } };

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
});

test("manual evaluation needs only an image and defaults to the full benchmark", () => {
  assert.deepEqual(manualEvaluationRequest(manualRequest), { image: taggedImage, mode: "live" });
  assert.equal(manualEvaluationRequest({ ...manualRequest, mode: "fixtures" }).mode, "fixtures");
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
      const settings = { GITHUB_WORKSPACE: process.cwd(), INPUT_IMAGE: taggedImage, INPUT_MODE: mode, TRIGGERING_ACTOR: "initiator" };
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
  for (const mode of ["fixtures", "live", "intake"]) { env.INPUT_EVALUATION = mode; await run(); assert.equal(outputs.image, pinnedImage); }
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

test("intake comparison requires terminal status and complete exact Unicode partition coverage", () => {
  const pieces = ["Ａ😀. ", "第二句。"];
  const content = pieces.join("");
  const sourceID = "d0cd0251-452e-482b-a323-256ecea94a38";
  const secondID = "40c59bc6-ebd7-4827-ab99-edb944979b72";
  const boundary = Array.from(pieces[0]).length;
  const result = { isError: false, receipt: { processing_state: "completed", search_state: "current",
    evidence: pieces.map((piece, index) => ({
      disposition: "stored", evidence_index: 0, partition_index: index, span_start: index ? boundary : 0,
      span_end: index ? Array.from(content).length : boundary, source_item_id: sourceID,
      evidence_id: index ? secondID : sourceID, occurrence_id: index ? secondID : sourceID, content_hash: "sha256:" + sha256(piece),
    })),
  } };
  assert.equal(intakeReceiptOutcome(result, content), "accepted");
  for (const evidence of [[], result.receipt.evidence.slice(0, 1),
    [result.receipt.evidence[0], { ...result.receipt.evidence[1], span_start: boundary + 1 }],
    [result.receipt.evidence[0], { ...result.receipt.evidence[1], content_hash: "wrong" }],
  ]) {
    assert.equal(intakeReceiptOutcome({ ...result, receipt: { ...result.receipt, evidence } }, content), "evidence_mismatch");
  }
  assert.notEqual(intakeReceiptOutcome({ ...result, isError: true }, content), "accepted");
  const rejected = { processing_state: "failed", errors: [{ code: "submission_policy_rejected", details: { component: "remember.initial_scan" } }] };
  assert.equal(intakeReceiptOutcome({ isError: true, receipt: rejected }, content), "initial_scan");
  assert.notEqual(intakeReceiptOutcome({ isError: false, receipt: rejected }, content), "initial_scan");
  assert.notEqual(intakeReceiptOutcome({ isError: true, receipt: { ...rejected, processing_state: "completed" } }, content), "initial_scan");
});

test("intake work stays at four concurrent cases and retains all results in source order", async () => {
  const cases = Array.from({ length: 9 }, (_, index) => ({ index }));
  const release = new Map();
  let results;
  const work = measureIntakeCases(cases, new AbortController().signal, ({ item, index }, _signal, publishCase) => new Promise((resolve) => {
    release.set(index, () => { publishCase(item); resolve(); });
  }), (update) => { results = update; });
  assert.deepEqual([...release.keys()], [0, 1, 2, 3]);
  release.get(3)();
  await Promise.resolve();
  assert.deepEqual([...release.keys()], [0, 1, 2, 3, 4]);
  for (const index of [0, 4, 1, 2, 8, 7, 6, 5]) {
    release.get(index)();
    await Promise.resolve();
  }
  await work;
  assert.deepEqual(results, cases);
});

test("intake work stops starting cases at the deadline and publishes the in-flight outcomes", async () => {
  const controller = new AbortController();
  const started = [];
  let results;
  const work = measureIntakeCases([0, 1, 2, 3, 4], controller.signal, ({ item }, signal, publishCase) => new Promise((resolve) => {
    started.push(item);
    signal.addEventListener("abort", () => { publishCase({ item, actual: "deadline_expired" }); resolve(); }, { once: true });
  }), (update) => { results = update; });
  controller.abort(new Error("deadline"));
  await assert.rejects(work, /deadline/);
  assert.deepEqual(started, [0, 1, 2, 3]);
  assert.deepEqual(results, started.map((item) => ({ item, actual: "deadline_expired" })));
});

test("intake work publishes a case outcome while its optional failure capture is pending", async () => {
  let finishCapture, results;
  const failure = { case: "fixture-case", actual: "provider_response_invalid", protected_failure_log: "unavailable" };
  const work = measureIntakeCases([failure], new AbortController().signal, async ({ item }, _signal, publishCase) => {
    publishCase(item);
    await new Promise((resolve) => { finishCapture = resolve; });
    publishCase({ ...item, protected_failure_log: "captured" });
  }, (update) => { results = update; });
  assert.deepEqual(results, [failure]);
  finishCapture();
  await work;
  assert.deepEqual(results, [{ ...failure, protected_failure_log: "captured" }]);
});

test("intake timeout retries wait behind cases already queued and honor cancellation", async () => {
  for (const cancelQueued of [false, true]) {
    const controller = new AbortController();
    const starts = [];
    const cases = [0, 1, 2, 3, 4, 5, 6];
    const work = measureIntakeCases(cases, controller.signal, async ({ item, attempt }, signal) => {
      starts.push([item, attempt]);
      if (item === 0 && attempt === 1) return true;
      if (item === 6 && cancelQueued) controller.abort(new Error("deadline"));
      signal.throwIfAborted();
      return false;
    }, () => {});
    if (cancelQueued) await assert.rejects(work, /deadline/);
    else await work;
    const firstAttempts = cases.map((item) => [item, 1]);
    assert.deepEqual(starts, cancelQueued ? firstAttempts : [...firstAttempts, [0, 2]]);
  }
});

test("intake comparison reuses its team and exact request only within the gateway retry budget", async (t) => {
  for (const scenario of [
    { name: "assessment 524 recovers", status: 524, failures: 5, attempts: 6 },
    { name: "assessment 504 exhausts", status: 504, failures: 6, attempts: 6 },
    { name: "direct 504 recovers", status: 504, failures: 1, attempts: 2, direct: true },
    { name: "ordinary 502 stays terminal", status: 502, failures: 6, attempts: 1 },
    { name: "foreign 524 stays terminal", status: 524, failures: 6, attempts: 1, foreign: true },
    { name: "security verdict stays terminal", status: 524, failures: 6, attempts: 1, rejected: true },
  ]) {
    await t.test(scenario.name, async (t) => {
      const item = { id: "fixture", content: "Atlas stores exact text.", source: "fixture", candidate: "accepted" };
      const teamID = randomUUID();
      const readbacks = new Map();
      const requests = [];
      const setupPaths = [];
      const captured = [];
      let lookups = 0;
      const server = createServer(async (request, response) => {
        const correlation = request.headers["x-correlation-id"];
        const attemptID = randomUUID();
        const send = (value) => response.end(JSON.stringify(value));
        if (request.method === "GET") {
          lookups++;
          return send({ data: readbacks.get(request.url) });
        }
        const body = [];
        for await (const chunk of request) body.push(chunk);
        if (request.url !== "/mcp") {
          setupPaths.push(request.url);
          return send({ data: request.url.endsWith("/credentials") ? { api_key: "intake-key-canary" } : { id: teamID } });
        }
        requests.push({ args: JSON.parse(Buffer.concat(body)).params.arguments, authorization: request.headers.authorization, correlation });
        if (requests.length <= scenario.failures) {
          if (scenario.direct) {
            response.statusCode = scenario.status;
            return response.end();
          }
          const [code, reason, component] = scenario.rejected
            ? ["submission_policy_rejected", "security_rejection", "remember.intake_review"]
            : ["provider_unavailable", "provider_server_failure", "remember.assessment"];
          const retryable = !scenario.rejected;
          readbacks.set(`/control/api/teams/${teamID}/remember-attempts/${attemptID}`, {
            team_id: scenario.foreign ? randomUUID() : teamID, attempt_id: attemptID, correlation_id: correlation,
            outcome: "failed", failed_phase: "assessment", error_code: code, retryable,
            diagnostics: { provider_exchanges: [
              { component: "assessor", status_code: 524 },
              { component: "assessor", status_code: scenario.status, response_body: "private-response-canary" },
            ] },
          });
          return send({ result: { isError: true, structuredContent: {
            processing_state: "failed", submission_id: attemptID, correlation_id: correlation,
            errors: [{ code, reason_code: reason, retryable, next_action: retryable ? "retry_same_request" : "resubmit_remember", details: { component } }],
          } } });
        }
        return send({ result: { structuredContent: {
          processing_state: "completed", search_state: "current", submission_id: attemptID,
          evidence: [{ disposition: "stored", evidence_index: 0, partition_index: 0, span_start: 0, span_end: item.content.length,
            source_item_id: randomUUID(), occurrence_id: randomUUID(), evidence_id: randomUUID(), content_hash: "sha256:" + sha256(item.content) }],
        } } });
      });
      await once(server.listen(0, "127.0.0.1"), "listening");
      t.after(() => new Promise((resolve) => server.close(resolve)));
      const url = `http://127.0.0.1:${server.address().port}`;
      const measure = measureIntakeCase.bind(null, {
        arm: "candidate", runtime: { paths: { runID: "fixture" }, state: { environment: {
          EVAL_MCP_URL: url, EVAL_CONTROL_URL: url, EVAL_CONTROL_TOKEN: "control-token-canary",
        } } }, control, mask: () => {},
        diagnostics: async (_, { correlationID }) => { captured.push(correlationID); return true; },
        docker: async () => Buffer.from(item.content).toString("hex"),
      });
      let results;
      await measureIntakeCases([item], AbortSignal.timeout(5_000), measure, (update) => { results = update; });
      assert.deepEqual(setupPaths, ["/control/api/teams", `/control/api/teams/${teamID}/credentials`]);
      assert.equal(requests.length, scenario.attempts);
      assert.equal(lookups, scenario.direct || scenario.rejected ? 0 : Math.min(scenario.failures, scenario.attempts));
      const [result] = results;
      const passed = scenario.attempts > scenario.failures;
      assert.equal(result.actual, passed ? "accepted" : scenario.rejected ? "intake_review" : "provider_unavailable");
      assert.deepEqual(result.attempts.map((attempt) => attempt.attempt), requests.map((_, index) => index + 1));
      assert.deepEqual(result.attempts.map((attempt) => attempt.correlation_id), requests.map((request) => request.correlation));
      assert.equal(new Set(requests.map((request) => request.correlation)).size, scenario.attempts);
      assert.deepEqual(captured, requests.slice(0, Math.min(scenario.failures, scenario.attempts)).map((request) => request.correlation));
      for (const request of requests) {
        assert.deepEqual(request.args, requests[0].args);
        assert.equal(request.authorization, "Bearer intake-key-canary");
      }
      if (scenario.foreign) assert.equal(result.provider_status, "unavailable");
      else if (!scenario.rejected) assert.equal(result.attempts[0][scenario.direct ? "http_status" : "provider_http_status"], scenario.status);
      assert.doesNotMatch(JSON.stringify(results), /intake-key-canary|control-token-canary|private-response-canary|127\.0\.0\.1/);
    });
  }
});

test("intake failure capture selects bounded warnings for a single correlation without payloads", () => {
  const target = { correlation_id: "case-id", level: "WARN", msg: "invalid complete response" };
  const lines = [target, { ...target, correlation_id: "other-case" }, { ...target, level: "INFO" },
    { ...target, payload_chunk: { body: "unrelated-provider-payload" } }];
  assert.equal(intakeFailureRecords(lines.map((entry) => JSON.stringify(entry)).join("\n"), "case-id"), JSON.stringify(target));
  assert.equal(intakeFailureRecords(Array(70).fill(JSON.stringify(target)).join("\n"), "case-id").split("\n").length, 40);
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

test("aggregation accepts frozen seeds prepared with different worker counts", () => {
  const reports = registry.datasets.map((dataset) => ({ identity: { dataset: dataset.id, import_workers: 10 }, expected_answers: dataset.questions.rows * 4, corpus_rows: dataset.corpus.rows, completed_answers: 0 }));
  assert.doesNotThrow(() => aggregate(registry, reports));
  reports[1].identity.import_workers = 4;
  assert.doesNotThrow(() => aggregate(registry, reports));
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
  assert.equal(datasets.env.WORKFLOW_EMBEDDING_MODEL, "text-embedding-3-small");
  assert.equal(datasets.env.WORKFLOW_EMBEDDING_DIMENSIONS, "1536");
  assert.equal(datasets.env.DENSE_MEM_EVAL_IMPORT_WORKERS, undefined);
  assert.equal(Object.hasOwn(workflow.on.workflow_call.inputs, "import_workers"), false);
  for (const entrypoint of ["run-evaluation.yml", "production-image-e2e.yml"]) {
    const caller = await readWorkflow(entrypoint);
    assert.equal(Object.hasOwn(caller.on.workflow_dispatch.inputs, "import_workers"), false);
    assert.equal(Object.hasOwn(workflowJob(caller, "evaluation").with, "import_workers"), false);
  }
  assert.equal(datasets["runs-on"], "ubuntu-latest"); assert.match(datasets["timeout-minutes"], /inputs\.mode == 'intake' && 30 \|\| 360/); assert.equal(datasets.strategy["fail-fast"], false);
  const deadline = datasets.steps.find((step) => step.name === "Freeze model deadline").run;
  assert.match(deadline, /minutes=330/); assert.match(deadline, /if \[\[ "\$EVAL_MODE" == intake \]\]; then minutes=20/);
  assert.ok(datasets.steps.filter((step) => step.uses?.startsWith("actions/upload-artifact@")).every((step) => step.with["retention-days"] === 2));
  assert.equal(runStep(datasets, "node --test tests/eval/scripts/workflow_docker.test.mjs").if, "inputs.mode == 'fixtures'");
  assert.match(workflowJob(workflow, "aggregate").if, /always/);
  const caller = await readWorkflow("production-image-e2e.yml");
  assert.deepEqual(caller.on.workflow_dispatch.inputs.evaluation.options, ["none", "fixtures", "intake", "live"]);
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

test("registry selection accepts only full benchmarks and synthetic system fixtures", async (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "evaluation-matrix-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const output = path.join(directory, "output");
  const env = { GITHUB_ACTIONS: "true", RUNNER_ENVIRONMENT: "github-hosted", GITHUB_RUN_ID: "42", GITHUB_RUN_ATTEMPT: "1", GITHUB_OUTPUT: output };
  for (const [mode, count] of [["live", 5], ["fixtures", 1]]) {
    fs.writeFileSync(output, "");
    await main("matrix", { ...env, EVAL_MODE: mode });
    const matrix = JSON.parse(fs.readFileSync(output, "utf8").trim().slice("matrix=".length));
    assert.equal(matrix.dataset.length, count);
  }
  for (const mode of ["replay", "none", "unknown", ""]) await assert.rejects(main("matrix", { ...env, EVAL_MODE: mode }), /Invalid evaluation mode/);
});
